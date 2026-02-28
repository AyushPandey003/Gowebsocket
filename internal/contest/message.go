package contest

import (
	"momentum-contest/internal/storage"
)

// MessageType represents the type of WebSocket message
type MessageType string

// Client-to-Server message types
const (
	// START_GAME is sent by the host to begin the contest
	MessageTypeStartGame MessageType = "START_GAME"

	// SUBMIT_ANSWER is sent by a player when they answer a question
	MessageTypeSubmitAnswer MessageType = "SUBMIT_ANSWER"
)

// Server-to-Client message types
const (
	// PLAYER_JOINED is broadcast when a new player joins the contest
	MessageTypePlayerJoined MessageType = "PLAYER_JOINED"

	// CONTEST_STARTED is broadcast when the host starts the contest
	MessageTypeContestStarted MessageType = "CONTEST_STARTED"

	// NEW_QUESTION is sent to all players when a new question is presented
	MessageTypeNewQuestion MessageType = "NEW_QUESTION"

	// ANSWER_RESULT is sent to a specific player after they submit an answer
	MessageTypeAnswerResult MessageType = "ANSWER_RESULT"

	// SCORE_UPDATE is broadcast to all players when someone scores
	MessageTypeScoreUpdate MessageType = "SCORE_UPDATE"

	// GAME_OVER is sent when the contest ends with final results
	MessageTypeGameOver MessageType = "GAME_OVER"

	// ERROR is sent when an error occurs
	MessageTypeError MessageType = "ERROR"

	// PLAYER_LEFT is broadcast when a player disconnects
	MessageTypePlayerLeft MessageType = "PLAYER_LEFT"

	// PLAYER_LIST is sent to a client when they join to show current players
	MessageTypePlayerList MessageType = "PLAYER_LIST"

	// QUESTION_TIMEOUT is broadcast when a question timer expires
	MessageTypeQuestionTimeout MessageType = "QUESTION_TIMEOUT"
)

// Message represents the base structure for all WebSocket messages
type Message struct {
	Type    MessageType `json:"type"`
	Payload interface{} `json:"payload,omitempty"`
}

// StartGameMessage is sent by the host to start the contest
type StartGameMessage struct {
	Type MessageType `json:"type"`
}

// SubmitAnswerMessage is sent by a player when they submit an answer
type SubmitAnswerMessage struct {
	Type       MessageType `json:"type"`
	QuestionID string      `json:"question_id"`
	Answer     string      `json:"answer"`
}

// PlayerJoinedPayload is sent when a player joins the contest
type PlayerJoinedPayload struct {
	UserID      string        `json:"user_id"`
	Username    string        `json:"username"`
	IsHost      bool          `json:"is_host"`
	PlayerCount int           `json:"player_count"`
	Players     []PlayerState `json:"players"`
}

// ContestStartedPayload is sent when the contest begins
type ContestStartedPayload struct {
	Message        string        `json:"message"`
	TotalQuestions int           `json:"total_questions"`
	QuestionTimer  int           `json:"question_timer"`
	Players        []PlayerState `json:"players"`
}

// NewQuestionPayload is sent when a new question is presented
type NewQuestionPayload struct {
	QuestionNumber int      `json:"question_number"`
	TotalQuestions int      `json:"total_questions"`
	QuestionID     string   `json:"question_id"`
	QuestionText   string   `json:"question_text"`
	Options        []string `json:"options"`
	Timer          int      `json:"timer"` // Time limit in seconds
}

// AnswerResultPayload is sent to a player after they submit an answer
type AnswerResultPayload struct {
	QuestionID    string  `json:"question_id"`
	IsCorrect     bool    `json:"is_correct"`
	CorrectAnswer string  `json:"correct_answer"`
	PointsAwarded int     `json:"points_awarded"`
	TimeTaken     float64 `json:"time_taken"`
	NewScore      int     `json:"new_score"`
}

// ScoreUpdatePayload is broadcast when a player scores
type ScoreUpdatePayload struct {
	UserID       string `json:"user_id"`
	Username     string `json:"username"`
	Score        int    `json:"score"`
	PointsEarned int    `json:"points_earned"`
}

// GameOverPayload is sent when the contest ends
type GameOverPayload struct {
	Message         string            `json:"message"`
	FinalScoreboard []PlayerState     `json:"final_scoreboard"`
	Questions       []QuestionSummary `json:"questions"`
}

// ErrorPayload is sent when an error occurs
type ErrorPayload struct {
	Message string `json:"message"`
	Code    string `json:"code,omitempty"`
}

// PlayerLeftPayload is sent when a player disconnects
type PlayerLeftPayload struct {
	UserID      string `json:"user_id"`
	Username    string `json:"username"`
	PlayerCount int    `json:"player_count"`
}

// PlayerListPayload is sent to a client when they join
type PlayerListPayload struct {
	Players []PlayerState `json:"players"`
}

// QuestionTimeoutPayload is sent when a question timer expires
type QuestionTimeoutPayload struct {
	QuestionID    string `json:"question_id"`
	CorrectAnswer string `json:"correct_answer"`
}

// PlayerState represents the current state of a player in the contest
type PlayerState struct {
	UserID   string `json:"user_id"`
	Username string `json:"username"`
	Score    int    `json:"score"`
	IsHost   bool   `json:"is_host"`
	Rank     int    `json:"rank,omitempty"`
}

// QuestionSummary provides a summary of a question and its correct answer
type QuestionSummary struct {
	QuestionNumber int      `json:"question_number"`
	QuestionID     string   `json:"question_id"`
	QuestionText   string   `json:"question_text"`
	Options        []string `json:"options"`
	CorrectAnswer  string   `json:"correct_answer"`
}

// InternalMessage represents messages passed internally within the hub
type InternalMessage struct {
	Type   MessageType
	Client *Client
	Data   interface{}
}

// ContestConfig holds the configuration for a contest
type ContestConfig struct {
	ContestID       string
	Difficulty      string
	QuestionCount   int
	QuestionTimer   int
	MaxPlayers      int
	CreatorID       string
	CreatorUsername string
	Questions       []storage.Question
}
