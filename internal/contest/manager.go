package contest

import (
	"context"
	"log"
	"sync"

	"momentum-contest/internal/config"
	"momentum-contest/internal/storage"
)

// Manager manages all active contest hubs
type Manager struct {
	hubs    map[string]*Hub
	mu      sync.RWMutex
	pubsub  *PubSub
	storage *storage.Storage
	config  *config.Config
}

// NewManager creates a new Manager instance
func NewManager() *Manager {
	return &Manager{
		hubs: make(map[string]*Hub),
	}
}

// SetDependencies sets the dependencies needed for auto-creating hubs
func (m *Manager) SetDependencies(pubsub *PubSub, store *storage.Storage, cfg *config.Config) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.pubsub = pubsub
	m.storage = store
	m.config = cfg
}

// CreateHub creates a new hub for a contest and starts it
func (m *Manager) CreateHub(config ContestConfig, pubsub *PubSub, store *storage.Storage) *Hub {
	m.mu.Lock()
	defer m.mu.Unlock()

	// Check if hub already exists
	if existingHub, exists := m.hubs[config.ContestID]; exists {
		// If the existing hub is finished, remove it and create a new one
		// This prevents reusing finished hubs for new contests
		if existingHub.GetGameState() == GameStateFinished {
			log.Printf("Hub for contest %s exists but is finished, removing and creating new one", config.ContestID)
			existingHub.Shutdown()
			delete(m.hubs, config.ContestID)
		} else {
			log.Printf("Hub for contest %s already exists and is active", config.ContestID)
			return existingHub
		}
	}

	// Create new hub
	hub := NewHub(config, pubsub, store, m)
	m.hubs[config.ContestID] = hub

	// Start the hub in a goroutine
	go hub.Run()

	log.Printf("Created and started hub for contest %s", config.ContestID)

	return hub
}

// GetHub retrieves a hub by contest ID, creating it from database if it doesn't exist
func (m *Manager) GetHub(contestID string) *Hub {
	m.mu.RLock()
	hub, exists := m.hubs[contestID]
	m.mu.RUnlock()

	if exists {
		return hub
	}

	// Hub doesn't exist, try to load it from database
	return m.loadHubFromDatabase(contestID)
}

// loadHubFromDatabase loads a contest from the database and creates a hub for it
func (m *Manager) loadHubFromDatabase(contestID string) *Hub {
	if m.storage == nil || m.pubsub == nil || m.config == nil {
		log.Printf("Cannot load hub from database: dependencies not set")
		return nil
	}

	ctx := context.Background()

	// Get contest info from database
	contestInfo, err := m.storage.GetContestInfo(ctx, contestID)
	if err != nil {
		log.Printf("Failed to load contest info for %s: %v", contestID, err)
		return nil
	}

	// Get questions for this contest
	questions, err := m.storage.GetContestQuestions(ctx, contestID)
	if err != nil {
		log.Printf("Failed to load questions for contest %s: %v", contestID, err)
		return nil
	}

	// Get creator username
	_, creatorUsername, err := m.storage.GetUserInfo(ctx, contestInfo.CreatedBy)
	if err != nil {
		log.Printf("Failed to get creator info for contest %s: %v", contestID, err)
		creatorUsername = "Unknown"
	}

	// Create contest configuration
	contestConfig := ContestConfig{
		ContestID:       contestID,
		Difficulty:      contestInfo.Difficulty,
		QuestionCount:   contestInfo.QuestionCount,
		QuestionTimer:   m.config.QuestionTimer,
		MaxPlayers:      contestInfo.MaxParticipants,
		CreatorID:       contestInfo.CreatedBy,
		CreatorUsername: creatorUsername,
		Questions:       questions,
	}

	// Check contest status from database - don't load finished contests
	var contestStatus string
	query := `SELECT status FROM contest WHERE id = $1`
	if err := m.storage.Pool().QueryRow(ctx, query, contestID).Scan(&contestStatus); err == nil {
		if contestStatus == "finished" {
			log.Printf("Contest %s is finished in database, not loading hub", contestID)
			return nil
		}
	}

	// Create the hub
	m.mu.Lock()
	defer m.mu.Unlock()

	// Double-check it wasn't created by another goroutine
	if existingHub, exists := m.hubs[contestID]; exists {
		// If existing hub is finished, remove it and create new one
		if existingHub.GetGameState() == GameStateFinished {
			log.Printf("Existing hub for contest %s is finished, removing and creating new one", contestID)
			existingHub.Shutdown()
			delete(m.hubs, contestID)
		} else {
			return existingHub
		}
	}

	hub := NewHub(contestConfig, m.pubsub, m.storage, m)
	m.hubs[contestID] = hub

	// Start the hub in a goroutine
	go hub.Run()

	log.Printf("Loaded and created hub for contest %s from database", contestID)

	return hub
}

// RemoveHub removes a hub from the manager
func (m *Manager) RemoveHub(contestID string) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if hub, exists := m.hubs[contestID]; exists {
		hub.Shutdown()
		delete(m.hubs, contestID)
		log.Printf("Removed hub for contest %s", contestID)
	}
}

// GetActiveContestCount returns the number of active contests
func (m *Manager) GetActiveContestCount() int {
	m.mu.RLock()
	defer m.mu.RUnlock()

	return len(m.hubs)
}

// Shutdown gracefully shuts down all hubs
func (m *Manager) Shutdown() {
	m.mu.Lock()
	defer m.mu.Unlock()

	log.Printf("Shutting down all hubs (total: %d)", len(m.hubs))

	// Create a wait group to wait for all hubs to shut down
	var wg sync.WaitGroup

	for contestID, hub := range m.hubs {
		wg.Add(1)
		go func(id string, h *Hub) {
			defer wg.Done()
			h.Shutdown()
			log.Printf("Hub %s shut down successfully", id)
		}(contestID, hub)
	}

	// Wait for all hubs to shut down
	wg.Wait()

	// Clear the hubs map
	m.hubs = make(map[string]*Hub)

	log.Println("All hubs have been shut down")
}
