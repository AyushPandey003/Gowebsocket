package config

import (
	"bufio"
	"fmt"
	"os"
	"strconv"
	"strings"
)

// Config holds all application configuration
type Config struct {
	Port          string
	DatabaseURL   string
	RedisAddr     string
	RedisPassword string
	RedisDB       int
	JWTSecret     string
	QuestionTimer int // Timer duration in seconds for each question
	MaxPlayers    int // Maximum number of players allowed in a contest
}

// Load reads configuration from environment variables
// It returns a Config struct populated with values from env vars or defaults
func Load() (*Config, error) {
	loadDotEnv(".env")

	config := &Config{
		Port:          getEnv("PORT", "8080"),
		DatabaseURL:   getEnv("DATABASE_URL", "postgres://postgres:postgres@localhost:5432/contest_db?sslmode=disable"),
		RedisAddr:     getEnv("REDIS_ADDR", "localhost:6379"),
		RedisPassword: getEnv("REDIS_PASSWORD", ""),
		RedisDB:       getEnvAsInt("REDIS_DB", 0),
		JWTSecret:     getEnv("JWT_SECRET", "your-256-bit-secret-change-this-in-production"),
		QuestionTimer: getEnvAsInt("QUESTION_TIMER", 15),
		MaxPlayers:    getEnvAsInt("MAX_PLAYERS", 6),
	}

	// Validate required fields
	if config.JWTSecret == "your-256-bit-secret-change-this-in-production" {
		fmt.Println("WARNING: Using default JWT secret. Please set JWT_SECRET environment variable in production!")
	}

	if config.DatabaseURL == "" {
		return nil, fmt.Errorf("DATABASE_URL is required")
	}

	return config, nil
}

func loadDotEnv(filePath string) {
	file, err := os.Open(filePath)
	if err != nil {
		return
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}

		parts := strings.SplitN(line, "=", 2)
		if len(parts) != 2 {
			continue
		}

		key := strings.TrimSpace(parts[0])
		value := strings.TrimSpace(parts[1])
		value = strings.Trim(value, `"'`)

		if key == "" {
			continue
		}

		if _, exists := os.LookupEnv(key); !exists {
			_ = os.Setenv(key, value)
		}
	}
}

// getEnv reads an environment variable or returns a default value
func getEnv(key, defaultValue string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return defaultValue
}

// getEnvAsInt reads an environment variable as integer or returns a default value
func getEnvAsInt(key string, defaultValue int) int {
	valueStr := os.Getenv(key)
	if valueStr == "" {
		return defaultValue
	}

	value, err := strconv.Atoi(valueStr)
	if err != nil {
		return defaultValue
	}

	return value
}
