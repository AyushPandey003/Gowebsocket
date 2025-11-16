package contest

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"math"
	"net/http"
	"os"
	"sort"
	"sync"
	"time"

	"momentum-contest/internal/storage"
)

// GameState represents the current state of a contest
type GameState string

const (
	GameStateWaiting    GameState = "WAITING"
	GameStateInProgress GameState = "IN_PROGRESS"
	GameStateFinished   GameState = "FINISHED"
)

// Hub manages a single contest room
type Hub struct {
	// Contest configuration
	Config ContestConfig

	// Registered clients
	clients map[*Client]bool
	mu      sync.RWMutex

	// Game state
	gameState            GameState
	currentQuestionIndex int
	questionStartTime    time.Time

	// Player scores and answers
	playerScores  map[string]int // userID -> score
	playerAnswers []storage.PlayerAnswer

	// Channels for hub coordination
	Register       chan *Client
	unregister     chan *Client
	processMessage chan InternalMessage
	broadcast      chan Message

	// Question timer
	ticker       *time.Ticker
	timerCancel  chan bool
	timerRunning bool

	// Redis pub/sub for cross-server communication
	pubsub *PubSub

	// Storage for database operations
	storage *storage.Storage

	// Context for cancellation
	ctx    context.Context
	cancel context.CancelFunc
}

// NewHub creates a new Hub instance
func NewHub(config ContestConfig, pubsub *PubSub, store *storage.Storage) *Hub {
	ctx, cancel := context.WithCancel(context.Background())

	return &Hub{
		Config:               config,
		clients:              make(map[*Client]bool),
		gameState:            GameStateWaiting,
		currentQuestionIndex: -1,
		playerScores:         make(map[string]int),
		playerAnswers:        make([]storage.PlayerAnswer, 0),
		Register:             make(chan *Client),
		unregister:           make(chan *Client),
		processMessage:       make(chan InternalMessage, 256),
		broadcast:            make(chan Message, 256),
		timerCancel:          make(chan bool, 1),
		timerRunning:         false,
		pubsub:               pubsub,
		storage:              store,
		ctx:                  ctx,
		cancel:               cancel,
	}
}

// Run starts the hub's main event loop
func (h *Hub) Run() {
	log.Printf("Starting hub for contest %s", h.Config.ContestID)

	for {
		select {
		case <-h.ctx.Done():
			log.Printf("Hub for contest %s is shutting down", h.Config.ContestID)
			h.closeAllConnections()
			return

		case client := <-h.Register:
			h.handleRegister(client)

		case client := <-h.unregister:
			h.handleUnregister(client)

		case msg := <-h.processMessage:
			h.handleMessage(msg)

		case msg := <-h.broadcast:
			h.BroadcastToClients(msg)
		}
	}
}

// handleRegister registers a new client to the hub
func (h *Hub) handleRegister(client *Client) {
	h.mu.Lock()
	defer h.mu.Unlock()

	// Check if the contest has already finished
	if h.gameState == GameStateFinished {
		log.Printf("User %s attempted to join finished contest %s", client.Username, h.Config.ContestID)
		client.SendError("Contest has already finished")
		client.Close()
		return
	}

	// Check if user is already connected (prevent duplicate connections)
	for existingClient := range h.clients {
		if existingClient.UserID == client.UserID {
			log.Printf("User %s already connected to contest %s, rejecting duplicate", client.Username, h.Config.ContestID)
			client.SendError("You are already connected to this contest")
			client.Close()
			return
		}
	}

	// Check if the contest is already full (after checking for duplicates)
	if len(h.clients) >= h.Config.MaxPlayers {
		log.Printf("User %s attempted to join full contest %s (%d/%d players)", client.Username, h.Config.ContestID, len(h.clients), h.Config.MaxPlayers)
		client.SendError("Contest is full")
		client.Close()
		return
	}

	// Register the client
	h.clients[client] = true
	// Initialize player score only if not already exists (in case of reconnection edge cases)
	if _, exists := h.playerScores[client.UserID]; !exists {
		h.playerScores[client.UserID] = 0
	}

	log.Printf("User %s joined contest %s (total players: %d/%d)", client.Username, h.Config.ContestID, len(h.clients), h.Config.MaxPlayers)

	// Get current player states
	playerStates := h.getPlayerStates()

	// Send current player list to the newly joined client
	client.SendMessage(MessageTypePlayerList, PlayerListPayload{
		Players: playerStates,
	})

	// Broadcast player joined message to all clients via Redis
	payload := PlayerJoinedPayload{
		UserID:      client.UserID,
		Username:    client.Username,
		IsHost:      client.IsHost,
		PlayerCount: len(h.clients),
		Players:     playerStates,
	}

	h.pubsub.PublishBroadcast(h.Config.ContestID, MessageTypePlayerJoined, payload)
}

// handleUnregister removes a client from the hub
func (h *Hub) handleUnregister(client *Client) {
	h.mu.Lock()
	defer h.mu.Unlock()

	if _, ok := h.clients[client]; ok {
		delete(h.clients, client)
		client.Close()

		log.Printf("User %s left contest %s (remaining players: %d)", client.Username, h.Config.ContestID, len(h.clients))

		// If the game is in progress and no players remain, finish the game
		if len(h.clients) == 0 && h.gameState == GameStateInProgress {
			h.gameState = GameStateFinished
			// Stop timer inline
			if h.timerRunning {
				if h.ticker != nil {
					h.ticker.Stop()
					h.ticker = nil
				}
				h.timerRunning = false
				if h.timerCancel != nil {
					select {
					case h.timerCancel <- true:
					default:
					}
				}
			}
			return
		}

		// Broadcast player left message
		payload := PlayerLeftPayload{
			UserID:      client.UserID,
			Username:    client.Username,
			PlayerCount: len(h.clients),
		}

		h.pubsub.PublishBroadcast(h.Config.ContestID, MessageTypePlayerLeft, payload)
	}
}

// handleMessage processes messages from clients
func (h *Hub) handleMessage(msg InternalMessage) {
	log.Printf("Processing message type %s from user %s in contest %s", msg.Type, msg.Client.Username, h.Config.ContestID)

	switch msg.Type {
	case MessageTypeStartGame:
		h.handleStartGame(msg.Client)
	case MessageTypeSubmitAnswer:
		h.handleSubmitAnswer(msg.Client, msg.Data)
	default:
		log.Printf("Unknown message type: %s from user %s", msg.Type, msg.Client.Username)
		msg.Client.SendError("Unknown message type")
	}
}

// handleStartGame starts the contest
func (h *Hub) handleStartGame(client *Client) {
	h.mu.Lock()
	defer h.mu.Unlock()

	log.Printf("START_GAME received from user %s (isHost: %v) for contest %s (state: %s, players: %d)",
		client.Username, client.IsHost, h.Config.ContestID, h.gameState, len(h.clients))

	// Only the host can start the game
	if !client.IsHost {
		log.Printf("User %s is not the host, cannot start game for contest %s", client.Username, h.Config.ContestID)
		client.SendError("Only the host can start the game")
		return
	}

	// Check if the game is already in progress
	if h.gameState != GameStateWaiting {
		log.Printf("Contest %s is already in state %s, cannot start", h.Config.ContestID, h.gameState)
		client.SendError("Game has already started")
		return
	}

	// Check if there are enough players (at least the host)
	if len(h.clients) < 1 {
		log.Printf("Not enough players to start contest %s (current: %d)", h.Config.ContestID, len(h.clients))
		client.SendError("Not enough players to start")
		return
	}

	log.Printf("Starting contest %s with %d players", h.Config.ContestID, len(h.clients))

	// Update game state
	h.gameState = GameStateInProgress

	// Update contest status and actual start time in database
	go func() {
		ctx := context.Background()
		now := time.Now()

		// Get contest info to get duration_minutes
		contestInfo, err := h.storage.GetContestInfo(ctx, h.Config.ContestID)
		var durationMinutes int
		// Helper to compute duration from question timer (in seconds) -> minutes
		perQuestionMinutes := int(math.Ceil(float64(h.Config.QuestionTimer) / 60.0))
		if perQuestionMinutes == 0 {
			perQuestionMinutes = 1
		}
		if err != nil || contestInfo == nil {
			// Fallback: calculate from question count and timer
			durationMinutes = h.Config.QuestionCount * perQuestionMinutes
			if durationMinutes == 0 {
				durationMinutes = 30 // Default fallback
			}
		} else {
			// Use duration from contest record (more accurate)
			// Get duration from contest table
			var duration int
			query := `SELECT duration_minutes FROM contest WHERE id = $1`
			if err := h.storage.Pool().QueryRow(ctx, query, h.Config.ContestID).Scan(&duration); err == nil {
				durationMinutes = duration
			} else {
				// Fallback calculation
				durationMinutes = h.Config.QuestionCount * perQuestionMinutes
				if durationMinutes == 0 {
					durationMinutes = 30
				}
			}
		}

		endTime := now.Add(time.Duration(durationMinutes) * time.Minute)

		// Update contest with actual start/end times and status
		if err := h.storage.UpdateContestStartTime(ctx, h.Config.ContestID, now, endTime); err != nil {
			log.Printf("Failed to update contest start time: %v", err)
		}
	}()

	// Broadcast contest started message
	payload := ContestStartedPayload{
		Message:        "Contest has started! Good luck!",
		TotalQuestions: h.Config.QuestionCount,
		QuestionTimer:  h.Config.QuestionTimer,
		Players:        h.getPlayerStates(),
	}

	log.Printf("Broadcasting CONTEST_STARTED message for contest %s to %d players", h.Config.ContestID, len(h.clients))
	h.pubsub.PublishBroadcast(h.Config.ContestID, MessageTypeContestStarted, payload)

	// Start the first question after a brief delay
	go func() {
		time.Sleep(2 * time.Second)
		log.Printf("Sending first question for contest %s", h.Config.ContestID)
		h.sendNextQuestion()
	}()
}

// sendNextQuestion sends the next question to all players
func (h *Hub) sendNextQuestion() {
	h.mu.Lock()

	// Increment question index
	h.currentQuestionIndex++

	// Check if the game is over
	if h.currentQuestionIndex >= len(h.Config.Questions) {
		h.mu.Unlock()
		h.endGame()
		return
	}

	// Get the current question
	question := h.Config.Questions[h.currentQuestionIndex]

	log.Printf("Sending question %d/%d for contest %s", h.currentQuestionIndex+1, len(h.Config.Questions), h.Config.ContestID)

	// Record the start time for score calculation
	h.questionStartTime = time.Now()

	// Prepare the question payload (without the correct answer)
	payload := NewQuestionPayload{
		QuestionNumber: h.currentQuestionIndex + 1,
		TotalQuestions: len(h.Config.Questions),
		QuestionID:     question.ID,
		QuestionText:   question.QuestionText,
		Options:        question.Options,
		Timer:          h.Config.QuestionTimer,
	}

	// Unlock before broadcasting and starting timer (to avoid blocking)
	h.mu.Unlock()

	// Broadcast the question via Redis
	h.pubsub.PublishBroadcast(h.Config.ContestID, MessageTypeNewQuestion, payload)

	// Start the question timer (will acquire lock internally)
	h.startTimer()
}

// handleSubmitAnswer processes a player's answer
func (h *Hub) handleSubmitAnswer(client *Client, data interface{}) {
	h.mu.Lock()
	defer h.mu.Unlock()

	// Check if the game is in progress
	if h.gameState != GameStateInProgress {
		client.SendError("Game is not in progress")
		return
	}

	// Parse the answer data
	var answerMsg SubmitAnswerMessage
	jsonData, _ := json.Marshal(data)
	if err := json.Unmarshal(jsonData, &answerMsg); err != nil {
		client.SendError("Invalid answer format")
		return
	}

	// Verify the question ID matches the current question
	if h.currentQuestionIndex < 0 || h.currentQuestionIndex >= len(h.Config.Questions) {
		client.SendError("No active question")
		return
	}

	currentQuestion := h.Config.Questions[h.currentQuestionIndex]
	if answerMsg.QuestionID != currentQuestion.ID {
		// Debug logging to help diagnose mismatched question IDs from clients
		log.Printf("Question ID mismatch in contest %s: client=%s sent=%s current=%s index=%d",
			h.Config.ContestID, client.Username, answerMsg.QuestionID, currentQuestion.ID, h.currentQuestionIndex)
		client.SendError("Question ID mismatch")
		return
	}

	// Check if the player already answered this question
	for _, answer := range h.playerAnswers {
		if answer.QuestionID == currentQuestion.ID && answer.UserID == client.UserID {
			client.SendError("You have already answered this question")
			return
		}
	}

	// Calculate time taken in milliseconds (to match schema)
	timeTakenMs := int(time.Since(h.questionStartTime).Milliseconds())
	timeTakenSeconds := time.Since(h.questionStartTime).Seconds()
	isCorrect := answerMsg.Answer == currentQuestion.CorrectAnswer

	// Calculate points awarded
	pointsAwarded := 0
	if isCorrect {
		// Formula: score = (1 - (time_taken / timer)) * 1000
		// Ensure minimum score of 100 points for correct answers
		scoreMultiplier := 1.0 - (timeTakenSeconds / float64(h.Config.QuestionTimer))
		if scoreMultiplier < 0 {
			scoreMultiplier = 0
		}
		pointsAwarded = int(scoreMultiplier * 1000)
		if pointsAwarded < 100 {
			pointsAwarded = 100
		}

		h.playerScores[client.UserID] += pointsAwarded
	}

	// Record the answer
	playerAnswer := storage.PlayerAnswer{
		ContestID:     h.Config.ContestID,
		QuestionID:    currentQuestion.ID,
		UserID:        client.UserID,
		AnswerGiven:   answerMsg.Answer,
		IsCorrect:     isCorrect,
		TimeTaken:     float64(timeTakenMs), // Store in milliseconds (as float64 for storage struct, will be converted to int in API)
		PointsAwarded: pointsAwarded,
		AnsweredAt:    time.Now(),
	}
	h.playerAnswers = append(h.playerAnswers, playerAnswer)

	log.Printf("User %s answered question %d: correct=%v, points=%d", client.Username, h.currentQuestionIndex+1, isCorrect, pointsAwarded)

	// Send answer result to the player
	resultPayload := AnswerResultPayload{
		QuestionID:    currentQuestion.ID,
		IsCorrect:     isCorrect,
		CorrectAnswer: currentQuestion.CorrectAnswer,
		PointsAwarded: pointsAwarded,
		TimeTaken:     timeTakenSeconds, // Send in seconds for frontend display
		NewScore:      h.playerScores[client.UserID],
	}
	client.SendMessage(MessageTypeAnswerResult, resultPayload)

	// If the answer is correct, broadcast score update and move to next question
	if isCorrect {
		// Broadcast score update
		scorePayload := ScoreUpdatePayload{
			UserID:       client.UserID,
			Username:     client.Username,
			Score:        h.playerScores[client.UserID],
			PointsEarned: pointsAwarded,
		}
		h.pubsub.PublishBroadcast(h.Config.ContestID, MessageTypeScoreUpdate, scorePayload)

		// Stop the timer inline (we already have the lock)
		if h.timerRunning {
			if h.ticker != nil {
				h.ticker.Stop()
				h.ticker = nil
			}
			h.timerRunning = false
			if h.timerCancel != nil {
				select {
				case h.timerCancel <- true:
				default:
				}
			}
		}

		// Move to the next question after a brief delay
		// Use a goroutine to avoid deadlock (sendNextQuestion will try to acquire lock)
		go func() {
			time.Sleep(2 * time.Second)
			h.sendNextQuestion()
		}()
	}
}

// startTimer starts the question timer
func (h *Hub) startTimer() {
	h.mu.Lock()

	// Stop any existing timer first (inline to avoid deadlock)
	if h.timerRunning {
		if h.ticker != nil {
			h.ticker.Stop()
			h.ticker = nil
		}
		h.timerRunning = false
		if h.timerCancel != nil {
			select {
			case h.timerCancel <- true:
			default:
			}
		}
	}

	// Capture current question index for the timer goroutine
	questionIndex := h.currentQuestionIndex
	contestID := h.Config.ContestID

	// Create a new ticker
	h.ticker = time.NewTicker(time.Duration(h.Config.QuestionTimer) * time.Second)
	h.timerRunning = true

	// Create a new cancel channel for this timer
	h.timerCancel = make(chan bool, 1)
	timerCancel := h.timerCancel
	ticker := h.ticker

	// Unlock before starting goroutine
	h.mu.Unlock()

	go func() {
		select {
		case <-ticker.C:
			// Timer expired, move to next question
			log.Printf("Timer expired for question %d in contest %s", questionIndex+1, contestID)

			// Get question info safely
			h.mu.RLock()
			var timeoutPayload QuestionTimeoutPayload
			if questionIndex >= 0 && questionIndex < len(h.Config.Questions) {
				currentQuestion := h.Config.Questions[questionIndex]
				timeoutPayload = QuestionTimeoutPayload{
					QuestionID:    currentQuestion.ID,
					CorrectAnswer: currentQuestion.CorrectAnswer,
				}
			}
			h.mu.RUnlock()

			// Broadcast timeout message
			if timeoutPayload.QuestionID != "" {
				h.pubsub.PublishBroadcast(contestID, MessageTypeQuestionTimeout, timeoutPayload)
			}

			// Move to next question
			h.sendNextQuestion()
		case <-timerCancel:
			// Timer was cancelled (correct answer was submitted)
			log.Printf("Timer cancelled for question %d in contest %s", questionIndex+1, contestID)
			return
		}
	}()
}

// stopTimer stops the question timer
// NOTE: This function should be called with the lock held, or it will acquire it
func (h *Hub) stopTimer() {
	h.mu.Lock()
	defer h.mu.Unlock()

	if !h.timerRunning {
		return
	}

	if h.ticker != nil {
		h.ticker.Stop()
		h.ticker = nil
	}

	h.timerRunning = false

	// Signal cancellation (non-blocking)
	if h.timerCancel != nil {
		select {
		case h.timerCancel <- true:
		default:
		}
	}
}

// endGame ends the contest and broadcasts final results
func (h *Hub) endGame() {
	log.Printf("Ending contest %s", h.Config.ContestID)

	h.gameState = GameStateFinished

	// Stop timer inline
	if h.timerRunning {
		if h.ticker != nil {
			h.ticker.Stop()
			h.ticker = nil
		}
		h.timerRunning = false
		if h.timerCancel != nil {
			select {
			case h.timerCancel <- true:
			default:
			}
		}
	}

	// Calculate final rankings
	playerStates := h.getPlayerStates()
	sort.Slice(playerStates, func(i, j int) bool {
		return playerStates[i].Score > playerStates[j].Score
	})

	// Assign ranks
	for i := range playerStates {
		playerStates[i].Rank = i + 1
	}

	// Prepare question summaries
	questionSummaries := make([]QuestionSummary, len(h.Config.Questions))
	for i, q := range h.Config.Questions {
		questionSummaries[i] = QuestionSummary{
			QuestionNumber: i + 1,
			QuestionID:     q.ID,
			QuestionText:   q.QuestionText,
			Options:        q.Options,
			CorrectAnswer:  q.CorrectAnswer,
		}
	}

	// Broadcast game over message
	payload := GameOverPayload{
		Message:         "Contest finished! Here are the final results.",
		FinalScoreboard: playerStates,
		Questions:       questionSummaries,
	}

	h.pubsub.PublishBroadcast(h.Config.ContestID, MessageTypeGameOver, payload)

	// Update contest status to finished
	go func() {
		ctx := context.Background()
		if err := h.storage.UpdateContestStatus(ctx, h.Config.ContestID, "finished"); err != nil {
			log.Printf("Failed to update contest status to finished: %v", err)
		} else {
			log.Printf("Updated contest %s status to finished", h.Config.ContestID)
		}
	}()

	// Save results to database asynchronously
	go h.saveResultsToDatabase(playerStates)

	// Schedule hub cleanup after 5 minutes to allow final result viewing
	go func() {
		time.Sleep(5 * time.Minute)
		log.Printf("Cleaning up hub for finished contest %s", h.Config.ContestID)
		h.Shutdown()
	}()
}

// saveResultsToDatabase saves all contest data via a single API call to Next.js backend
func (h *Hub) saveResultsToDatabase(playerStates []PlayerState) {
	ctx := context.Background()

	// Prepare comprehensive contest data payload
	type ContestResultData struct {
		UserID    string `json:"userId"`
		Username  string `json:"username"`
		Score     int    `json:"score"`
		Rank      int    `json:"rank"`
		TimeSpent int    `json:"timeSpent"` // Total time spent in seconds
	}

	type PlayerAnswerData struct {
		UserID        string    `json:"userId"`
		QuestionID    string    `json:"questionId"`
		Answer        string    `json:"answer"`
		IsCorrect     bool      `json:"isCorrect"`
		TimeTaken     int       `json:"timeTaken"` // milliseconds as int
		PointsAwarded int       `json:"pointsAwarded"`
		AnsweredAt    time.Time `json:"answeredAt"`
	}

	type ContestSubmissionData struct {
		UserID           string    `json:"userId"`
		QuestionID       string    `json:"questionId"`
		ProblemSetID     string    `json:"problemSetId"` // Required by schema
		Answer           string    `json:"answer"`
		SelectedAnswer   string    `json:"selectedAnswer"` // Alias for answer
		IsCorrect        bool      `json:"isCorrect"`
		PointsEarned     int       `json:"pointsEarned"`     // Points earned for this submission
		TimeSpentSeconds int       `json:"timeSpentSeconds"` // Required by schema
		SubmittedAt      time.Time `json:"submittedAt"`
	}

	type ContestQuestionData struct {
		ProblemSetID string `json:"problemSetId"`
		OrderIndex   int    `json:"orderIndex"`
	}

	type BulkSavePayload struct {
		ContestID     string                  `json:"contestId"`
		Questions     []ContestQuestionData   `json:"questions"` // Contest questions to ensure they're in DB
		Results       []ContestResultData     `json:"results"`
		Answers       []PlayerAnswerData      `json:"answers"`
		Submissions   []ContestSubmissionData `json:"submissions"`
		ContestStatus string                  `json:"contestStatus"`
	}

	// Prepare contest questions (ensure they're tracked in the database)
	questions := make([]ContestQuestionData, len(h.Config.Questions))
	for i, q := range h.Config.Questions {
		questions[i] = ContestQuestionData{
			ProblemSetID: q.ID, // Question ID is the problem_set_id
			OrderIndex:   i,
		}
	}

	// Calculate total time spent per player (sum of all answer times)
	playerTimeSpent := make(map[string]int) // userID -> total milliseconds
	for _, ans := range h.playerAnswers {
		timeMs := int(ans.TimeTaken)
		playerTimeSpent[ans.UserID] += timeMs
	}

	// Prepare results
	results := make([]ContestResultData, len(playerStates))
	for i, ps := range playerStates {
		totalTimeMs := playerTimeSpent[ps.UserID]
		totalTimeSeconds := totalTimeMs / 1000 // Convert to seconds
		results[i] = ContestResultData{
			UserID:    ps.UserID,
			Username:  ps.Username,
			Score:     ps.Score,
			Rank:      ps.Rank,
			TimeSpent: totalTimeSeconds,
		}
	}

	// Prepare player answers (convert timeTaken to int milliseconds)
	answers := make([]PlayerAnswerData, len(h.playerAnswers))
	for i, ans := range h.playerAnswers {
		timeMs := int(ans.TimeTaken) // Convert float64 to int (milliseconds)
		answers[i] = PlayerAnswerData{
			UserID:        ans.UserID,
			QuestionID:    ans.QuestionID,
			Answer:        ans.AnswerGiven,
			IsCorrect:     ans.IsCorrect,
			TimeTaken:     timeMs,
			PointsAwarded: ans.PointsAwarded,
			AnsweredAt:    ans.AnsweredAt,
		}
	}

	// Prepare submissions (convert from answers with all required fields)
	submissions := make([]ContestSubmissionData, len(h.playerAnswers))
	for i, ans := range h.playerAnswers {
		timeMs := int(ans.TimeTaken)
		timeSeconds := timeMs / 1000 // Convert to seconds for timeSpentSeconds
		if timeSeconds == 0 && timeMs > 0 {
			timeSeconds = 1 // Ensure at least 1 second if there's any time
		}
		submissions[i] = ContestSubmissionData{
			UserID:           ans.UserID,
			QuestionID:       ans.QuestionID,
			ProblemSetID:     ans.QuestionID, // QuestionID is the problem_set_id
			Answer:           ans.AnswerGiven,
			SelectedAnswer:   ans.AnswerGiven, // Same as answer
			IsCorrect:        ans.IsCorrect,
			PointsEarned:     ans.PointsAwarded,
			TimeSpentSeconds: timeSeconds,
			SubmittedAt:      ans.AnsweredAt,
		}
	}

	payload := BulkSavePayload{
		ContestID:     h.Config.ContestID,
		Questions:     questions,
		Results:       results,
		Answers:       answers,
		Submissions:   submissions,
		ContestStatus: "finished",
	}

	// Make single API call to Next.js backend
	if err := h.sendBulkContestData(ctx, payload); err != nil {
		log.Printf("Failed to send bulk contest data via API for contest %s: %v", h.Config.ContestID, err)

		// Fallback to direct database save if API call fails
		log.Printf("Attempting fallback database save for contest %s", h.Config.ContestID)

		contestResults := make([]storage.ContestResult, len(playerStates))
		for i, ps := range playerStates {
			contestResults[i] = storage.ContestResult{
				ContestID: h.Config.ContestID,
				UserID:    ps.UserID,
				Username:  ps.Username,
				Score:     ps.Score,
				Rank:      ps.Rank,
			}
		}

		if err := h.storage.SaveContestResults(ctx, contestResults); err != nil {
			log.Printf("Fallback: Failed to save contest results: %v", err)
		}

		if err := h.storage.SavePlayerAnswers(ctx, h.playerAnswers); err != nil {
			log.Printf("Fallback: Failed to save player answers: %v", err)
		}

		if err := h.storage.UpdateContestStatus(ctx, h.Config.ContestID, "finished"); err != nil {
			log.Printf("Fallback: Failed to update contest status: %v", err)
		}
	} else {
		log.Printf("Successfully sent bulk contest data via API for contest %s", h.Config.ContestID)
	}
}

// getPlayerStates returns the current state of all players
func (h *Hub) getPlayerStates() []PlayerState {
	states := make([]PlayerState, 0, len(h.clients))
	for client := range h.clients {
		states = append(states, PlayerState{
			UserID:   client.UserID,
			Username: client.Username,
			Score:    h.playerScores[client.UserID],
			IsHost:   client.IsHost,
		})
	}
	return states
}

// sendBulkContestData sends all contest data to Next.js API in a single request
func (h *Hub) sendBulkContestData(ctx context.Context, payload interface{}) error {
	// Get API URL from environment or use default
	apiURL := os.Getenv("NEXTJS_API_URL")
	if apiURL == "" {
		apiURL = "https://momentum003.vercel.app/" // Default for local development
	}

	endpoint := fmt.Sprintf("%s/api/contest/save-results", apiURL)

	// Marshal payload to JSON
	jsonData, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("failed to marshal payload: %w", err)
	}

	// Create HTTP request
	req, err := http.NewRequestWithContext(ctx, "POST", endpoint, bytes.NewBuffer(jsonData))
	if err != nil {
		return fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")

	// Send request
	client := &http.Client{
		Timeout: 30 * time.Second,
	}

	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("failed to send request: %w", err)
	}
	defer resp.Body.Close()

	// Check response status
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("API returned status %d: %s", resp.StatusCode, string(body))
	}

	// Log success
	log.Printf("Successfully sent bulk data to Next.js API for contest %s", h.Config.ContestID)
	return nil
}

func (h *Hub) BroadcastToClients(message Message) {
	h.mu.RLock()
	defer h.mu.RUnlock()

	log.Printf("Broadcasting message type %s to %d clients in contest %s", message.Type, len(h.clients), h.Config.ContestID)

	data, err := json.Marshal(message)
	if err != nil {
		log.Printf("Failed to marshal broadcast message: %v", err)
		return
	}

	sentCount := 0
	for client := range h.clients {
		select {
		case client.send <- data:
			sentCount++
		default:
			// Client's send buffer is full, close the connection
			log.Printf("Client %s send buffer full, closing connection", client.Username)
			close(client.send)
			delete(h.clients, client)
		}
	}

	log.Printf("Successfully sent message type %s to %d/%d clients in contest %s", message.Type, sentCount, len(h.clients), h.Config.ContestID)
}

// closeAllConnections closes all client connections
func (h *Hub) closeAllConnections() {
	h.mu.Lock()
	defer h.mu.Unlock()

	for client := range h.clients {
		client.Close()
	}
	h.clients = make(map[*Client]bool)
}

// Shutdown gracefully shuts down the hub
func (h *Hub) Shutdown() {
	log.Printf("Shutting down hub for contest %s", h.Config.ContestID)
	h.cancel()
}

// GetGameState returns the current game state (for external access)
func (h *Hub) GetGameState() GameState {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.gameState
}
