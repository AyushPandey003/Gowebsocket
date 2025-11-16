package config

import (
	"fmt"
	"os"
	"strconv"
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
	config := &Config{
		Port:          getEnv("PORT", "8080"),
		DatabaseURL:   getEnv("DATABASE_URL", "postgresql://neondb_owner:npg_sh38WrDjfkPL@ep-muddy-cake-adfkz0w5-pooler.c-2.us-east-1.aws.neon.tech/neondb?sslmode=require&channel_binding=require"),
		RedisAddr:     getEnv("REDIS_ADDR", "redis-17050.c212.ap-south-1-1.ec2.redns.redis-cloud.com:17050"),
		RedisPassword: getEnv("REDIS_PASSWORD", "W3V3qqtLVKnUWLPdiVgEVJZYv7b0oENR"),
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
