package storage

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v4"
	"github.com/jackc/pgx/v4/pgxpool"
)

// Question represents a quiz question from the database
type Question struct {
	ID            string   `json:"id"`
	QuestionText  string   `json:"question_text"`
	Options       []string `json:"options"`
	CorrectAnswer string   `json:"correct_answer"`
	Difficulty    string   `json:"difficulty"`
}

// ContestResult represents the final results of a contest
type ContestResult struct {
	ContestID string
	UserID    string
	Username  string
	Score     int
	Rank      int
}

// PlayerAnswer represents a single player's answer to a question
type PlayerAnswer struct {
	ContestID     string
	QuestionID    string
	UserID        string
	AnswerGiven   string
	IsCorrect     bool
	TimeTaken     float64
	PointsAwarded int
	AnsweredAt    time.Time
}

// Storage handles all database operations
type Storage struct {
	pool *pgxpool.Pool
}

// Pool returns the underlying connection pool (for advanced operations)
func (s *Storage) Pool() *pgxpool.Pool {
	return s.pool
}

// NewStorage creates a new Storage instance and connects to the database
func NewStorage(ctx context.Context, databaseURL string) (*Storage, error) {
	config, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		return nil, fmt.Errorf("unable to parse database URL: %w", err)
	}

	// Configure connection pool settings
	config.MaxConns = 25
	config.MinConns = 5
	config.MaxConnLifetime = time.Hour
	config.MaxConnIdleTime = time.Minute * 30
	config.HealthCheckPeriod = time.Minute

	pool, err := pgxpool.ConnectConfig(ctx, config)
	if err != nil {
		return nil, fmt.Errorf("unable to connect to database: %w", err)
	}

	// Test the connection
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("unable to ping database: %w", err)
	}

	return &Storage{pool: pool}, nil
}

// Close closes the database connection pool
func (s *Storage) Close() {
	s.pool.Close()
}

// GetRandomQuestions fetches random questions from the database based on difficulty and count
func (s *Storage) GetRandomQuestions(ctx context.Context, difficulty string, count int) ([]Question, error) {
	query := `
		SELECT id, question_text, options, correct_answer, difficulty
		FROM problem_set
		WHERE difficulty = $1 AND is_active = true
		ORDER BY RANDOM()
		LIMIT $2
	`

	rows, err := s.pool.Query(ctx, query, difficulty, count)
	if err != nil {
		return nil, fmt.Errorf("failed to query questions: %w", err)
	}
	defer rows.Close()

	var questions []Question
	for rows.Next() {
		var q Question
		if err := rows.Scan(&q.ID, &q.QuestionText, &q.Options, &q.CorrectAnswer, &q.Difficulty); err != nil {
			return nil, fmt.Errorf("failed to scan question: %w", err)
		}
		questions = append(questions, q)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("error iterating questions: %w", err)
	}

	if len(questions) == 0 {
		return nil, fmt.Errorf("no questions found for difficulty: %s", difficulty)
	}

	return questions, nil
}

// SaveContestResults performs a bulk insert of all contest results
// This is called once at the end of a contest for efficiency
func (s *Storage) SaveContestResults(ctx context.Context, results []ContestResult) error {
	if len(results) == 0 {
		return nil
	}

	query := `
		INSERT INTO contest_result (contest_id, user_id, username, score, rank, completed_at)
		VALUES ($1, $2, $3, $4, $5, $6)
	`

	batch := &pgx.Batch{}
	now := time.Now()

	for _, result := range results {
		batch.Queue(query, result.ContestID, result.UserID, result.Username, result.Score, result.Rank, now)
	}

	br := s.pool.SendBatch(ctx, batch)
	defer br.Close()

	// Execute all queries in the batch
	for i := 0; i < len(results); i++ {
		if _, err := br.Exec(); err != nil {
			return fmt.Errorf("failed to insert contest result %d: %w", i, err)
		}
	}

	// After inserting results, update user stats and award achievements
	// (This code runs only if insertion succeeded)
	// NOTE: We do updates per-user to keep logic simple and idempotent.
	for _, result := range results {
		// Upsert user_stats: add score to total_points
		// Ensure all fields are properly initialized
		upsertQuery := `
			INSERT INTO user_stats (id, user_id, total_points, level, streak, current_streak, longest_streak, tasks_completed, pomodoros_completed, total_focus_time)
			VALUES ($1, $2, $3, 1, 0, 0, 0, 0, 0, 0)
			ON CONFLICT (user_id) DO UPDATE SET 
				total_points = user_stats.total_points + EXCLUDED.total_points,
				updated_at = NOW()
		`
		// use generated id if inserting
		_, err := s.pool.Exec(ctx, upsertQuery, fmt.Sprintf("us_%d_%s", time.Now().UnixNano(), result.UserID), result.UserID, result.Score)
		if err != nil {
			// Log and continue
			fmt.Printf("failed to upsert user_stats for %s: %v\n", result.UserID, err)
			continue
		}

		// Fetch updated total_points
		var totalPoints int
		err = s.pool.QueryRow(ctx, `SELECT total_points FROM user_stats WHERE user_id = $1`, result.UserID).Scan(&totalPoints)
		if err != nil {
			fmt.Printf("failed to query user_stats for %s: %v\n", result.UserID, err)
			continue
		}

		// Count wins (rank == 1) for this user
		var winsCount int
		err = s.pool.QueryRow(ctx, `SELECT COUNT(*) FROM contest_result WHERE user_id = $1 AND rank = 1`, result.UserID).Scan(&winsCount)
		if err != nil {
			fmt.Printf("failed to count wins for %s: %v\n", result.UserID, err)
			continue
		}

		// Helper to insert achievement if not exists
		insertAchievement := func(userID, achievementID string) error {
			// check existence
			var exists bool
			err := s.pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM user_achievements WHERE user_id=$1 AND achievement_id=$2)`, userID, achievementID).Scan(&exists)
			if err != nil {
				return err
			}
			if exists {
				return nil
			}
			// insert
		_, err = s.pool.Exec(ctx, `
			INSERT INTO user_achievements (id, user_id, achievement_id, unlocked_at)
			VALUES ($1, $2, $3, $4)
		`, fmt.Sprintf("ua_%d_%s", time.Now().UnixNano(), userID), userID, achievementID, time.Now().Format("2006-01-02T15:04:05Z07:00"))
			return err
		}

		// Achievement conditions
		// 1) contest_points_10k: totalPoints >= 10000
		if totalPoints >= 10000 {
			if err := insertAchievement(result.UserID, "contest_points_10k"); err != nil {
				fmt.Printf("failed to insert achievement contest_points_10k for %s: %v\n", result.UserID, err)
			}
		}

		// 2) contest_points_20k_and_2wins: totalPoints >= 20000 && wins >= 2
		if totalPoints >= 20000 && winsCount >= 2 {
			if err := insertAchievement(result.UserID, "contest_points_20k_2wins"); err != nil {
				fmt.Printf("failed to insert achievement contest_points_20k_2wins for %s: %v\n", result.UserID, err)
			}
		}

		// 3) contest_wins_5: winsCount >= 5
		if winsCount >= 5 {
			if err := insertAchievement(result.UserID, "contest_wins_5"); err != nil {
				fmt.Printf("failed to insert achievement contest_wins_5 for %s: %v\n", result.UserID, err)
			}
		}
	}

	return nil
}

// SavePlayerAnswers performs a bulk insert of all player answers for a contest
// Uses pgx.CopyFrom for maximum efficiency when inserting many rows
func (s *Storage) SavePlayerAnswers(ctx context.Context, answers []PlayerAnswer) error {
	if len(answers) == 0 {
		return nil
	}

	// CopyFrom is the most efficient way to insert bulk data in PostgreSQL
	rows := make([][]interface{}, len(answers))
	for i, answer := range answers {
		rows[i] = []interface{}{
			answer.ContestID,
			answer.QuestionID,
			answer.UserID,
			answer.AnswerGiven,
			answer.IsCorrect,
			answer.TimeTaken,
			answer.PointsAwarded,
			answer.AnsweredAt,
		}
	}

	copyCount, err := s.pool.CopyFrom(
		ctx,
		pgx.Identifier{"player_answer"},
		[]string{"contest_id", "question_id", "user_id", "answer_given", "is_correct", "time_taken", "points_awarded", "answered_at"},
		pgx.CopyFromRows(rows),
	)

	if err != nil {
		return fmt.Errorf("failed to copy player answers: %w", err)
	}

	if copyCount != int64(len(answers)) {
		return fmt.Errorf("expected to insert %d answers, but inserted %d", len(answers), copyCount)
	}

	return nil
}

// CreateContest inserts a new contest record and returns the contest ID
func (s *Storage) CreateContest(ctx context.Context, contestID, creatorID, name, description, difficulty string, questionCount, durationMinutes int) error {
	// Set start date to now, end date based on duration
	now := time.Now()
	endDate := now.Add(time.Duration(durationMinutes) * time.Minute)
	
	query := `
		INSERT INTO contest (id, name, description, created_by, difficulty, question_count, duration_minutes, start_date, end_date, created_at, updated_at, status)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, 'waiting')
	`

	_, err := s.pool.Exec(ctx, query, contestID, name, description, creatorID, difficulty, questionCount, durationMinutes, now, endDate, now, now)
	if err != nil {
		return fmt.Errorf("failed to create contest: %w", err)
	}

	return nil
}

// UpdateContestStatus updates the status of a contest (e.g., 'in_progress', 'finished')
func (s *Storage) UpdateContestStatus(ctx context.Context, contestID, status string) error {
	query := `
		UPDATE contest
		SET status = $1, updated_at = $2
		WHERE id = $3
	`

	_, err := s.pool.Exec(ctx, query, status, time.Now(), contestID)
	if err != nil {
		return fmt.Errorf("failed to update contest status: %w", err)
	}

	return nil
}

// UpdateContestStartTime updates the actual start and end times when contest begins
func (s *Storage) UpdateContestStartTime(ctx context.Context, contestID string, startTime, endTime time.Time) error {
	query := `
		UPDATE contest
		SET actual_start_time = $1, actual_end_time = $2, status = $3, updated_at = $4
		WHERE id = $5
	`

	_, err := s.pool.Exec(ctx, query, startTime, endTime, "in_progress", time.Now(), contestID)
	if err != nil {
		return fmt.Errorf("failed to update contest start time: %w", err)
	}

	return nil
}

// ContestInfo holds basic contest metadata used to create hubs
type ContestInfo struct {
	ID              string
	Difficulty      string
	QuestionCount   int
	MaxParticipants int
	CreatedBy       string
}

// GetContestInfo loads contest metadata from the database
func (s *Storage) GetContestInfo(ctx context.Context, contestID string) (*ContestInfo, error) {
	query := `
		SELECT id, difficulty, question_count, max_participants, created_by
		FROM contest
		WHERE id = $1
	`

	row := s.pool.QueryRow(ctx, query, contestID)
	var ci ContestInfo
	if err := row.Scan(&ci.ID, &ci.Difficulty, &ci.QuestionCount, &ci.MaxParticipants, &ci.CreatedBy); err != nil {
		return nil, fmt.Errorf("failed to load contest info: %w", err)
	}
	return &ci, nil
}

// GetContestQuestions returns the ordered list of questions for a contest
func (s *Storage) GetContestQuestions(ctx context.Context, contestID string) ([]Question, error) {
	query := `
		SELECT ps.id, ps.question_text, ps.options, ps.correct_answer, ps.difficulty
		FROM contest_question cq
		JOIN problem_set ps ON cq.problem_set_id = ps.id
		WHERE cq.contest_id = $1
		ORDER BY cq.order_index ASC
	`

	rows, err := s.pool.Query(ctx, query, contestID)
	if err != nil {
		return nil, fmt.Errorf("failed to query contest questions: %w", err)
	}
	defer rows.Close()

	var questions []Question
	for rows.Next() {
		var q Question
		if err := rows.Scan(&q.ID, &q.QuestionText, &q.Options, &q.CorrectAnswer, &q.Difficulty); err != nil {
			return nil, fmt.Errorf("failed to scan contest question: %w", err)
		}
		questions = append(questions, q)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("error iterating contest questions: %w", err)
	}

	return questions, nil
}

// GetUserInfo returns the user ID and username for a given user
func (s *Storage) GetUserInfo(ctx context.Context, userID string) (string, string, error) {
	query := `
		SELECT id, name
		FROM "user"
		WHERE id = $1
	`

	row := s.pool.QueryRow(ctx, query, userID)
	var id, name string
	if err := row.Scan(&id, &name); err != nil {
		return "", "", fmt.Errorf("failed to get user info: %w", err)
	}
	return id, name, nil
}
