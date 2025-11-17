package main

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"momentum-contest/internal/api"
	"momentum-contest/internal/config"
	"momentum-contest/internal/contest"
	"momentum-contest/internal/storage"
)

func main() {
	// Set up logging
	log.SetFlags(log.LstdFlags | log.Lshortfile)
	log.Println("Starting Momentum Contest Server...")

	// Load configuration
	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("Failed to load configuration: %v", err)
	}
	log.Printf("Configuration loaded successfully")

	// Create context for initialization
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// Initialize database connection
	store, err := storage.NewStorage(ctx, cfg.DatabaseURL)
	if err != nil {
		log.Fatalf("Failed to connect to database: %v", err)
	}
	defer store.Close()
	log.Println("Database connection established")

	// Initialize Redis client and Pub/Sub
	pubsubCtx := context.Background()
	pubsub, err := contest.NewPubSub(pubsubCtx, cfg.RedisAddr, cfg.RedisPassword, cfg.RedisDB)
	if err != nil {
		log.Fatalf("Failed to connect to Redis: %v", err)
	}
	defer pubsub.Close()
	log.Println("Redis connection established")

	// Initialize contest manager
	manager := contest.NewManager()
	// Set dependencies for auto-loading hubs from database
	manager.SetDependencies(pubsub, store, cfg)
	log.Println("Contest manager initialized")

	// Start Redis pub/sub subscription in background
	go pubsub.SubscribeToContests(manager)
	log.Println("Redis pub/sub subscription started")

	// Initialize authentication service
	authService := api.NewAuthService(cfg.JWTSecret)
	log.Println("Authentication service initialized")

	// Set up HTTP router
	router := api.NewRouter(authService, manager, pubsub, store, cfg)
	httpRouter := router.SetupRoutes()
	log.Println("HTTP routes configured")

	// Create HTTP server
	server := &http.Server{
		Addr:         fmt.Sprintf(":%s", cfg.Port),
		Handler:      httpRouter,
		ReadTimeout:  15 * time.Second,
		WriteTimeout: 15 * time.Second,
		IdleTimeout:  60 * time.Second,
	}

	// Channel to listen for errors from the HTTP server
	serverErrors := make(chan error, 1)

	// Start HTTP server in a goroutine
	go func() {
		log.Printf("HTTP server listening on port %s", cfg.Port)
		serverErrors <- server.ListenAndServe()
	}()

	// Channel to listen for interrupt signals
	shutdown := make(chan os.Signal, 1)
	signal.Notify(shutdown, os.Interrupt, syscall.SIGTERM, syscall.SIGINT)

	// Block until we receive a signal or an error
	select {
	case err := <-serverErrors:
		log.Fatalf("Error starting server: %v", err)

	case sig := <-shutdown:
		log.Printf("Received shutdown signal: %v", sig)

		// Initiate graceful shutdown
		log.Println("Starting graceful shutdown...")

		// Give outstanding requests a deadline for completion
		shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer shutdownCancel()

		// Shutdown the contest manager first (closes all hubs and connections)
		log.Println("Shutting down contest manager...")
		manager.Shutdown()
		log.Println("Contest manager shut down successfully")

		// Shutdown the HTTP server
		log.Println("Shutting down HTTP server...")
		if err := server.Shutdown(shutdownCtx); err != nil {
			log.Printf("Error during server shutdown: %v", err)
			// Force close if graceful shutdown fails
			server.Close()
		}
		log.Println("HTTP server shut down successfully")

		log.Println("Graceful shutdown completed")
	}
}
