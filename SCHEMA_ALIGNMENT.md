# Schema Alignment Documentation

This document ensures the Go backend database queries match the frontend schema definitions in `db/schema.ts`.

## Table Names

All table names use snake_case in PostgreSQL (as defined in schema.ts):

- `user` - User accounts
- `contest` - Contest definitions
- `contest_participant` - Participants in contests
- `contest_question` - Questions assigned to contests
- `problem_set` - Question/problem definitions
- `player_answer` - Individual player answers
- `contest_result` - Final contest results
- `contest_invitation` - Contest invitations
- `contest_submission` - Contest submissions

## Column Mappings

### problem_set table
- `id` → `text` (primary key)
- `question_text` → `text` (used in queries, maps to `questionText` in schema)
- `options` → `json` array of strings (handled via JSON unmarshaling)
- `correct_answer` → `text`
- `difficulty` → enum: "easy" | "medium" | "hard"
- `is_active` → `boolean`

### contest table
- `id` → `text` (primary key)
- `name` → `text`
- `created_by` → `text` (references user.id)
- `difficulty` → enum
- `question_count` → `integer`
- `duration_minutes` → `integer`
- `max_participants` → `integer`
- `status` → enum: "draft" | "waiting" | "in_progress" | "finished" | "cancelled"

### player_answer table
- `contest_id` → `text`
- `question_id` → `text`
- `user_id` → `text`
- `answer_given` → `text`
- `is_correct` → `boolean`
- `time_taken` → `integer` (milliseconds) - **FIXED: Changed from float64 to int**
- `points_awarded` → `integer`
- `answered_at` → `timestamp`

### contest_result table
- `contest_id` → `text`
- `user_id` → `text`
- `username` → `text`
- `score` → `integer`
- `rank` → `integer`
- `completed_at` → `timestamp`

## Data Type Fixes Applied

1. **time_taken field**: Changed from `float64` (seconds) to `int` (milliseconds) to match schema
   - Calculation: `time.Since(startTime).Milliseconds()` → `int`
   - Frontend still receives seconds for display: `timeTakenSeconds`

2. **JSON fields**: Added explicit JSON unmarshaling for `options` array
   - Scans as `[]byte` then unmarshals to `[]string`

## Notes

- All enum values match between frontend and backend
- Column names use snake_case consistently
- Timestamps are handled as `time.Time` in Go, `timestamp` in PostgreSQL
- JSON arrays are properly unmarshaled from PostgreSQL JSONB/JSON columns

