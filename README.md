# Momentum Contest - Real-Time Quiz Competition Platform

A production-ready, horizontally-scalable real-time quiz contest application built with Go, WebSockets, Redis Pub/Sub, and PostgreSQL.

## 🚀 Features

- **Real-time Multiplayer Contests**: Up to 6 players can compete simultaneously
- **Synchronized Gameplay**: All players see questions at the same time
- **Instant Question Progression**: When one player answers correctly, everyone moves to the next question
- **Speed-Based Scoring**: Faster correct answers earn more points (up to 1000 points per question)
- **Horizontal Scalability**: Redis Pub/Sub enables multiple server instances
- **JWT Authentication**: Secure authentication for all API endpoints
- **Graceful Shutdown**: Properly closes all connections and saves data
- **WebSocket Communication**: Low-latency, bidirectional communication

## 🏗️ Architecture

```
momentum-contest/
├── cmd/
│   └── server/
│       └── main.go              # Application entry point
├── internal/
│   ├── api/
│   │   ├── auth.go              # JWT authentication & middleware
│   │   └── router.go            # HTTP routes & handlers
│   ├── config/
│   │   └── config.go            # Configuration management
│   ├── contest/
│   │   ├── client.go            # WebSocket client management
│   │   ├── hub.go               # Contest room logic
│   │   ├── manager.go           # Multi-room management
│   │   ├── message.go           # Message protocol definitions
│   │   └── pubsub.go            # Redis Pub/Sub integration
│   └── storage/
│       └── postgres.go          # Database operations
├── go.mod
├── go.sum
├── README.md
└── schema.sql                    # Database schema
```

## 📋 Prerequisites

- **Go** 1.23+ ([Download](https://golang.org/dl/))
- **PostgreSQL** 12+ ([Download](https://www.postgresql.org/download/))
- **Redis** 6+ ([Download](https://redis.io/download))

## 🔧 Installation

### 1. Clone the Repository

```bash
git clone <repository-url>
cd momentum-contest
```

### 2. Install Dependencies

```bash
go mod download
```

### 3. Set Up PostgreSQL

Create a database and run the schema:

```bash
# Connect to PostgreSQL
psql -U postgres

# Create database
CREATE DATABASE contest_db;

# Exit psql
\q

# Run the schema
psql -U postgres -d contest_db -f schema.sql
```

### 4. Set Up Redis

Start Redis server:

```bash
# Using Docker (recommended)
docker run -d -p 6379:6379 redis:latest

# Or install locally and run
redis-server
```

### 5. Configure Environment Variables

Create a `.env` file or set environment variables:

```bash
# Server Configuration
PORT=8080

# Database Configuration
DATABASE_URL=postgres://postgres:postgres@localhost:5432/contest_db?sslmode=disable

# Redis Configuration
REDIS_ADDR=localhost:6379
REDIS_PASSWORD=
REDIS_DB=0

# JWT Configuration (CHANGE IN PRODUCTION!)
JWT_SECRET=your-super-secret-jwt-key-change-this-in-production

# Game Configuration
QUESTION_TIMER=15    # Seconds per question
MAX_PLAYERS=6        # Maximum players per contest
```

### 6. Build and Run

```bash
# Build
go build -o server ./cmd/server

# Run
./server

# Or run directly
go run ./cmd/server/main.go
```

The server will start on `http://localhost:8080`

## 📡 API Endpoints

### 1. Login (Get JWT Token)

```http
POST /login
Content-Type: application/json

{
  "username": "player1",
  "password": "any-password"
}
```

**Response:**
```json
{
  "token": "eyJhbGciOiJIUzI1NiIs...",
  "user_id": "user_player1_1234567890",
  "username": "player1",
  "expires_at": "2025-11-03T12:00:00Z"
}
```

### 2. Create Contest

```http
POST /api/contests
Authorization: Bearer <token>
Content-Type: application/json

{
  "difficulty": "medium",
  "question_count": 10
}
```

**Response:**
```json
{
  "contest_id": "550e8400-e29b-41d4-a716-446655440000",
  "difficulty": "medium",
  "question_count": 10,
  "websocket_url": "/ws/contests/550e8400-e29b-41d4-a716-446655440000",
  "message": "Contest created successfully. Connect to the WebSocket to join."
}
```

### 3. Health Check

```http
GET /health
```

**Response:**
```json
{
  "status": "healthy",
  "active_contests": 5
}
```

## 🔌 WebSocket Protocol

### Connection

```
ws://localhost:8080/ws/contests/{contestID}?token={jwt_token}
```

### Client → Server Messages

#### Start Game (Host Only)
```json
{
  "type": "START_GAME"
}
```

#### Submit Answer
```json
{
  "type": "SUBMIT_ANSWER",
  "question_id": "q123",
  "answer": "Option A"
}
```

### Server → Client Messages

#### Player Joined
```json
{
  "type": "PLAYER_JOINED",
  "payload": {
    "user_id": "user123",
    "username": "player1",
    "is_host": false,
    "player_count": 3,
    "players": [...]
  }
}
```

#### Contest Started
```json
{
  "type": "CONTEST_STARTED",
  "payload": {
    "message": "Contest has started! Good luck!",
    "total_questions": 10,
    "question_timer": 15,
    "players": [...]
  }
}
```

#### New Question
```json
{
  "type": "NEW_QUESTION",
  "payload": {
    "question_number": 1,
    "total_questions": 10,
    "question_id": "q123",
    "question_text": "What is the capital of France?",
    "options": ["London", "Paris", "Berlin", "Madrid"],
    "timer": 15
  }
}
```

#### Answer Result (Personal)
```json
{
  "type": "ANSWER_RESULT",
  "payload": {
    "question_id": "q123",
    "is_correct": true,
    "correct_answer": "Paris",
    "points_awarded": 850,
    "time_taken": 2.5,
    "new_score": 1700
  }
}
```

#### Score Update (Broadcast)
```json
{
  "type": "SCORE_UPDATE",
  "payload": {
    "user_id": "user123",
    "username": "player1",
    "score": 1700,
    "points_earned": 850
  }
}
```

#### Game Over
```json
{
  "type": "GAME_OVER",
  "payload": {
    "message": "Contest finished! Here are the final results.",
    "final_scoreboard": [
      {
        "user_id": "user123",
        "username": "player1",
        "score": 8500,
        "is_host": true,
        "rank": 1
      }
    ],
    "questions": [...]
  }
}
```

## 🎮 Game Flow

1. **Contest Creation**: Creator calls `/api/contests` to create a room
2. **Players Join**: All players (including creator) connect via WebSocket
3. **Start Game**: Host sends `START_GAME` message
4. **Question Loop**:
   - Server sends question to all players
   - 15-second timer starts
   - Players submit answers
   - First correct answer: Award points, broadcast update, next question
   - Timer expires: No points, next question
5. **Game End**: After final question, server sends results and saves to DB

## 🔐 Security Features

- **JWT Authentication**: All endpoints require valid JWT tokens
- **Token Validation**: Tokens validated via middleware
- **WebSocket Security**: Token required in query parameter or header
- **CORS Protection**: Configurable origin checking (set properly in production)

## 🚦 Scaling

### Horizontal Scaling with Redis

The application uses Redis Pub/Sub to synchronize state across multiple server instances:

1. Each server instance subscribes to `contest:*` pattern
2. When an event occurs, it's published to Redis
3. All server instances receive the event and update their local clients
4. This allows contests to span multiple servers

### Running Multiple Instances

```bash
# Terminal 1
PORT=8080 go run ./cmd/server/main.go

# Terminal 2
PORT=8081 go run ./cmd/server/main.go

# Use a load balancer (nginx, HAProxy) to distribute traffic
```

## 📊 Database Schema

The application requires three main tables:

- **questions**: Stores quiz questions with options and correct answers
- **contests**: Stores contest metadata
- **contest_results**: Stores final player scores and rankings
- **player_answers**: Stores individual answer submissions (bulk inserted)

See `schema.sql` for the complete schema.

## 🧪 Testing

### Quick Start - Automated Testing

We provide three automated test scripts for easy testing:

#### Option 1: Node.js Test Script (Recommended)

```bash
# Install dependencies
npm install -g wscat ws

# Run interactive mode
node test_contest.js

# Or run auto-play mode directly
node test_contest.js --auto

# Or join existing contest
node test_contest.js <contest-id>
```

Features:
- ✅ Interactive menu for test scenarios
- ✅ Auto-play mode with AI players
- ✅ Colored console output
- ✅ Automatic answer submission
- ✅ Real-time score tracking

#### Option 2: Python Test Script

```bash
# Install dependencies
pip install websockets requests

# Run interactive mode
python test_contest.py

# Or run auto-play mode
python test_contest.py --auto

# Or join existing contest
python test_contest.py <contest-id>
```

Features:
- ✅ Async/await support
- ✅ Clean output with emojis
- ✅ Multiple AI players
- ✅ Easy to extend

#### Option 3: Bash Script (Simple)

```bash
# Make executable
chmod +x test_contest.sh

# Run full test setup
./test_contest.sh

# Create a contest
./test_contest.sh create TestHost medium 5

# Connect to existing contest
./test_contest.sh connect <contest-id> TestUser
```

Features:
- ✅ No dependencies (except curl and wscat)
- ✅ Creates multiple players automatically
- ✅ Saves tokens for manual testing
- ✅ Provides connection commands

### Manual Testing with cURL

```bash
# 1. Login
TOKEN=$(curl -s -X POST http://localhost:8080/login \
  -H "Content-Type: application/json" \
  -d '{"username":"testuser","password":"test"}' \
  | jq -r '.token')

# 2. Create Contest
CONTEST=$(curl -s -X POST http://localhost:8080/api/contests \
  -H "Authorization: Bearer $TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"difficulty":"medium","question_count":5}')

echo $CONTEST

# 3. Extract Contest ID
CONTEST_ID=$(echo $CONTEST | grep -o '"contest_id":"[^"]*"' | cut -d'"' -f4)

# 4. Connect via WebSocket
wscat -c "ws://localhost:8080/ws/contests/$CONTEST_ID?token=$TOKEN"
```

### WebSocket Testing

Use a WebSocket client like [wscat](https://github.com/websockets/wscat):

```bash
# Install wscat
npm install -g wscat

# Connect (use actual values, not placeholders!)
wscat -c "ws://localhost:8080/ws/contests/$CONTEST_ID?token=$TOKEN"

# Send start game (if you're the host)
{"type":"START_GAME"}

# Submit answer
{"type":"SUBMIT_ANSWER","question_id":"q123","answer":"Paris"}
```

### Test Scenarios

The automated scripts support several test scenarios:

1. **Basic Flow** (3 players, manual control)
   - Creates host and 2 additional players
   - Manual answer submission
   - Good for debugging and understanding flow

2. **Auto-Play** (4 AI players, automatic)
   - All players auto-answer questions
   - Simulates real game conditions
   - Great for performance testing

3. **Join Existing** (single player)
   - Join an already created contest
   - Good for testing late-joining behavior

## 🐛 Troubleshooting

### Database Connection Issues
- Ensure PostgreSQL is running
- Verify DATABASE_URL is correct
- Check firewall/network settings

### Redis Connection Issues
- Ensure Redis is running: `redis-cli ping` should return `PONG`
- Verify REDIS_ADDR is correct

### WebSocket Connection Issues
- Ensure JWT token is valid and not expired
- Check that token is passed in query parameter
- Verify contest exists before connecting

## 📝 Production Deployment Checklist

- [ ] Change `JWT_SECRET` to a strong random value
- [ ] Configure proper CORS origins in `router.go`
- [ ] Use environment variables for all sensitive data
- [ ] Enable PostgreSQL SSL mode
- [ ] Set up Redis authentication
- [ ] Configure load balancer for multiple instances
- [ ] Set up monitoring and logging
- [ ] Implement rate limiting
- [ ] Add database migrations tool
- [ ] Configure proper user authentication (replace dummy login)
- [ ] Set up HTTPS/TLS certificates
- [ ] Implement database connection pooling tuning

## 🤝 Contributing

1. Fork the repository
2. Create a feature branch
3. Commit your changes
4. Push to the branch
5. Create a Pull Request

## 📄 License

This project is licensed under the MIT License.

## 👨‍💻 Author

Built with ❤️ using Go, WebSockets, Redis, and PostgreSQL

---

**Happy Coding! 🎉**
