package contest

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"time"

	"github.com/go-redis/redis/v8"
)

// PubSub handles Redis publish/subscribe operations for cross-server message broadcasting
type PubSub struct {
	client *redis.Client
	ctx    context.Context
}

// NewPubSub creates a new PubSub instance
func NewPubSub(ctx context.Context, addr, password string, db int) (*PubSub, error) {
	client := redis.NewClient(&redis.Options{
		Addr:         addr,
		Password:     password,
		DB:           db,
		PoolSize:     20,  // Increased pool size for better concurrency
		MinIdleConns: 5,   // Keep minimum idle connections
		MaxRetries:   3,   // Retry failed operations
		DialTimeout:  5 * time.Second,
		ReadTimeout:  3 * time.Second,
		WriteTimeout: 3 * time.Second,
	})

	// Test the connection
	if err := client.Ping(ctx).Err(); err != nil {
		return nil, fmt.Errorf("failed to connect to Redis: %w", err)
	}

	log.Println("Successfully connected to Redis")

	return &PubSub{
		client: client,
		ctx:    ctx,
	}, nil
}

// Close closes the Redis client connection
func (ps *PubSub) Close() error {
	return ps.client.Close()
}

// PublishMessage publishes a message to a contest-specific Redis channel
// This allows multiple server instances to stay synchronized
func (ps *PubSub) PublishMessage(contestID string, message interface{}) error {
	channel := fmt.Sprintf("contest:%s", contestID)

	// Serialize the message to JSON
	data, err := json.Marshal(message)
	if err != nil {
		return fmt.Errorf("failed to marshal message: %w", err)
	}

	// Publish to Redis
	if err := ps.client.Publish(ps.ctx, channel, data).Err(); err != nil {
		return fmt.Errorf("failed to publish message to Redis: %w", err)
	}

	return nil
}

// SubscribeToContests subscribes to all contest channels and forwards messages to the appropriate hubs
// This runs in a background goroutine and is the key to horizontal scaling
func (ps *PubSub) SubscribeToContests(manager *Manager) {
	// Subscribe to the pattern "contest:*" to receive messages from all contests
	pubsub := ps.client.PSubscribe(ps.ctx, "contest:*")
	defer pubsub.Close()

	log.Println("Started Redis pub/sub subscription for contest:*")

	// Channel for receiving messages
	ch := pubsub.Channel()

	for {
		select {
		case <-ps.ctx.Done():
			log.Println("Stopping Redis subscription due to context cancellation")
			return
		case msg, ok := <-ch:
			if !ok {
				log.Println("Redis subscription channel closed")
				return
			}

			// Extract contest ID from channel name (format: "contest:contestID")
			contestID := msg.Channel[8:] // Remove "contest:" prefix

			// Parse the message
			var message Message
			if err := json.Unmarshal([]byte(msg.Payload), &message); err != nil {
				log.Printf("Failed to unmarshal Redis message for contest %s: %v", contestID, err)
				continue
			}

			// Get the hub for this contest
			hub := manager.GetHub(contestID)
			if hub == nil {
				// Hub doesn't exist on this server instance, which is fine
				// This message was probably for a different server instance
				continue
			}

			// Broadcast the message to all clients in this hub (on this server instance)
			hub.BroadcastToClients(message)
		}
	}
}

// PublishBroadcast is a helper method that publishes a message to be broadcast to all clients in a contest
func (ps *PubSub) PublishBroadcast(contestID string, messageType MessageType, payload interface{}) error {
	log.Printf("Publishing broadcast message type %s to contest %s via Redis", messageType, contestID)
	message := Message{
		Type:    messageType,
		Payload: payload,
	}
	err := ps.PublishMessage(contestID, message)
	if err != nil {
		log.Printf("Failed to publish broadcast message type %s to contest %s: %v", messageType, contestID, err)
	}
	return err
}

// PublishToClient is a helper that creates a message intended for a specific client
// Note: In the actual implementation, the hub will handle routing to the specific client
func (ps *PubSub) PublishToClient(contestID, userID string, messageType MessageType, payload interface{}) error {
	message := struct {
		Type         MessageType `json:"type"`
		Payload      interface{} `json:"payload"`
		TargetUserID string      `json:"target_user_id,omitempty"`
	}{
		Type:         messageType,
		Payload:      payload,
		TargetUserID: userID,
	}
	return ps.PublishMessage(contestID, message)
}
