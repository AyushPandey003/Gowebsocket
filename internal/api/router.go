package api

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"

	"momentum-contest/internal/config"
	"momentum-contest/internal/contest"
	"momentum-contest/internal/storage"

	"github.com/google/uuid"
	"github.com/gorilla/mux"
	"github.com/gorilla/websocket"
)

// Router holds all dependencies for API handlers
type Router struct {
	authService *AuthService
	manager     *contest.Manager
	pubsub      *contest.PubSub
	storage     *storage.Storage
	config      *config.Config
	upgrader    websocket.Upgrader
}

// NewRouter creates a new Router instance
func NewRouter(
	authService *AuthService,
	manager *contest.Manager,
	pubsub *contest.PubSub,
	store *storage.Storage,
	cfg *config.Config,
) *Router {
	return &Router{
		authService: authService,
		manager:     manager,
		pubsub:      pubsub,
		storage:     store,
		config:      cfg,
		upgrader: websocket.Upgrader{
			ReadBufferSize:  1024,
			WriteBufferSize: 1024,
			CheckOrigin: func(r *http.Request) bool {
				// In production, implement proper CORS checking
				return true
			},
		},
	}
}

// CORSMiddleware adds CORS headers to all responses
func (rt *Router) CORSMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Set CORS headers
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization")
		w.Header().Set("Access-Control-Allow-Credentials", "true")

		// Handle preflight requests
		if r.Method == "OPTIONS" {
			w.WriteHeader(http.StatusOK)
			return
		}

		next.ServeHTTP(w, r)
	})
}

// SetupRoutes configures all HTTP routes
func (rt *Router) SetupRoutes() *mux.Router {
	router := mux.NewRouter()

	// Apply CORS middleware to all routes
	router.Use(rt.CORSMiddleware)

	// Public routes
	router.HandleFunc("/login", rt.authService.LoginHandler).Methods("POST")
	router.HandleFunc("/health", rt.HealthHandler).Methods("GET")

	// Protected routes (require JWT authentication)
	api := router.PathPrefix("/api").Subrouter()
	api.Use(rt.authService.JWTMiddleware)
	api.HandleFunc("/contests", rt.CreateContestHandler).Methods("POST")
	api.HandleFunc("/contests/{contestID}", rt.DeleteContestHandler).Methods("DELETE")

	// WebSocket endpoint (protected)
	wsRouter := router.PathPrefix("/ws").Subrouter()
	wsRouter.Use(rt.authService.JWTMiddleware)
	wsRouter.HandleFunc("/contests/{contestID}", rt.WebSocketHandler).Methods("GET")

	return router
}

// HealthHandler returns the health status of the server
func (rt *Router) HealthHandler(w http.ResponseWriter, r *http.Request) {
	response := map[string]interface{}{
		"status":          "healthy",
		"active_contests": rt.manager.GetActiveContestCount(),
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(response)
}

// CreateContestRequest represents a request to create a new contest
type CreateContestRequest struct {
	Name            string `json:"name"`
	Difficulty      string `json:"difficulty"`
	QuestionCount   int    `json:"question_count"`
	DurationMinutes int    `json:"duration_minutes"`
}

// CreateContestResponse represents the response when creating a contest
type CreateContestResponse struct {
	ContestID     string `json:"contest_id"`
	Difficulty    string `json:"difficulty"`
	QuestionCount int    `json:"question_count"`
	WebSocketURL  string `json:"websocket_url"`
	Message       string `json:"message"`
}

// CreateContestHandler handles the creation of a new contest
func (rt *Router) CreateContestHandler(w http.ResponseWriter, r *http.Request) {
	// Extract user info from context
	userID, err := GetUserIDFromContext(r.Context())
	if err != nil {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	username, err := GetUsernameFromContext(r.Context())
	if err != nil {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	// Parse request body
	var req CreateContestRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		log.Printf("Failed to decode request body: %v", err)
		http.Error(w, "Invalid request body", http.StatusBadRequest)
		return
	}

	log.Printf("Creating contest: name=%s, difficulty=%s, questionCount=%d, duration=%d",
		req.Name, req.Difficulty, req.QuestionCount, req.DurationMinutes)

	// Validate and set defaults
	if req.Name == "" {
		req.Name = fmt.Sprintf("%s's Contest", username)
	}

	if req.Difficulty == "" {
		req.Difficulty = "medium" // Default difficulty
	}

	if req.QuestionCount <= 0 || req.QuestionCount > 50 {
		log.Printf("Invalid question count: %d", req.QuestionCount)
		http.Error(w, "Question count must be between 1 and 50", http.StatusBadRequest)
		return
	}

	if req.DurationMinutes <= 0 {
		req.DurationMinutes = 30 // Default duration
	}

	// Validate difficulty
	validDifficulties := map[string]bool{
		"easy":   true,
		"medium": true,
		"hard":   true,
	}
	if !validDifficulties[req.Difficulty] {
		http.Error(w, "Invalid difficulty. Must be 'easy', 'medium', or 'hard'", http.StatusBadRequest)
		return
	}

	// Generate contest ID
	contestID := uuid.New().String()

	// Fetch questions from database
	questions, err := rt.storage.GetRandomQuestions(r.Context(), req.Difficulty, req.QuestionCount)
	if err != nil {
		log.Printf("Failed to fetch questions: %v", err)
		http.Error(w, "Failed to create contest: unable to fetch questions", http.StatusInternalServerError)
		return
	}

	// Create contest record in database (store metadata used by app)
	if err := rt.storage.CreateContest(r.Context(), contestID, userID, req.Name, "", req.Difficulty, req.QuestionCount, req.DurationMinutes); err != nil {
		log.Printf("Failed to create contest in database: %v", err)
		http.Error(w, "Failed to create contest", http.StatusInternalServerError)
		return
	}

	// Create contest configuration
	contestConfig := contest.ContestConfig{
		ContestID:       contestID,
		Difficulty:      req.Difficulty,
		QuestionCount:   req.QuestionCount,
		QuestionTimer:   rt.config.QuestionTimer,
		MaxPlayers:      rt.config.MaxPlayers,
		CreatorID:       userID,
		CreatorUsername: username,
		Questions:       questions,
	}

	// Create the hub
	hub := rt.manager.CreateHub(contestConfig, rt.pubsub, rt.storage)
	if hub == nil {
		http.Error(w, "Failed to create contest hub", http.StatusInternalServerError)
		return
	}

	log.Printf("Contest %s created by user %s (%s)", contestID, username, userID)

	// Return the contest details
	wsURL := fmt.Sprintf("/ws/contests/%s", contestID)
	response := CreateContestResponse{
		ContestID:     contestID,
		Difficulty:    req.Difficulty,
		QuestionCount: req.QuestionCount,
		WebSocketURL:  wsURL,
		Message:       "Contest created successfully. Connect to the WebSocket to join.",
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(response)
}

// WebSocketHandler handles WebSocket connections for a contest
func (rt *Router) WebSocketHandler(w http.ResponseWriter, r *http.Request) {
	// Extract contest ID from URL
	vars := mux.Vars(r)
	contestID := vars["contestID"]

	if contestID == "" {
		http.Error(w, "Contest ID is required", http.StatusBadRequest)
		return
	}

	// Extract user info from context
	userID, err := GetUserIDFromContext(r.Context())
	if err != nil {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	username, err := GetUsernameFromContext(r.Context())
	if err != nil {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	// Get the hub for this contest
	hub := rt.manager.GetHub(contestID)
	if hub == nil {
		log.Printf("Contest %s not found for user %s", contestID, username)
		http.Error(w, "Contest not found", http.StatusNotFound)
		return
	}

	// Log incoming WebSocket upgrade request
	log.Printf("WebSocket upgrade request from user %s (%s) for contest %s", username, userID, contestID)
	log.Printf("Request headers: Origin=%s, Upgrade=%s, Connection=%s",
		r.Header.Get("Origin"),
		r.Header.Get("Upgrade"),
		r.Header.Get("Connection"))

	// Set response headers before upgrade
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Credentials", "true")

	// Upgrade HTTP connection to WebSocket
	conn, err := rt.upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Printf("Failed to upgrade WebSocket connection for user %s (%s): %v", username, userID, err)
		log.Printf("Request method: %s, Protocol: %s", r.Method, r.Proto)
		return
	}

	// Determine if this user is the host
	isHost := hub.Config.CreatorID == userID

	// Create a new client
	client := contest.NewClient(conn, hub, userID, username, isHost)

	// Register the client with the hub
	hub.Register <- client

	// Start the client's read and write pumps
	go client.WritePump()
	go client.ReadPump()

	log.Printf("User %s connected to contest %s via WebSocket", username, contestID)
}

// DeleteContestHandler handles the deletion of a contest
func (rt *Router) DeleteContestHandler(w http.ResponseWriter, r *http.Request) {
	// Extract contest ID from URL
	vars := mux.Vars(r)
	contestID := vars["contestID"]

	if contestID == "" {
		http.Error(w, "Contest ID is required", http.StatusBadRequest)
		return
	}

	// Extract user info from context
	userID, err := GetUserIDFromContext(r.Context())
	if err != nil {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	log.Printf("Delete request for contest %s by user %s", contestID, userID)

	// Get the hub to check if contest exists and if user is the creator
	hub := rt.manager.GetHub(contestID)
	if hub != nil {
		// Contest is active, check if user is the creator
		if hub.Config.CreatorID != userID {
			log.Printf("User %s attempted to delete contest %s but is not the creator (creator: %s)",
				userID, contestID, hub.Config.CreatorID)
			http.Error(w, "Only the contest creator can delete the contest", http.StatusForbidden)
			return
		}

		// Don't allow deleting contests that are in progress
		gameState := hub.GetGameState()
		if gameState != contest.GameStateWaiting {
			log.Printf("Cannot delete contest %s - game state is %s", contestID, gameState)
			http.Error(w, "Cannot delete a contest that is in progress or finished", http.StatusBadRequest)
			return
		}

		// Remove the hub from the manager (this will shut it down gracefully)
		log.Printf("Removing hub for contest %s", contestID)
		rt.manager.RemoveHub(contestID)
	}

	// Delete from database (even if hub doesn't exist, clean up the DB record)
	// Note: The database deletion should be handled by the Next.js API since it owns the DB schema
	// We just need to shut down the Go service's hub

	log.Printf("Contest %s deleted successfully by user %s", contestID, userID)

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(map[string]interface{}{
		"message":    "Contest deleted successfully",
		"contest_id": contestID,
	})
}
