package contest

import (
	"encoding/json"
	"log"
	"time"

	"github.com/gorilla/websocket"
)

const (
	// Time allowed to write a message to the peer
	writeWait = 10 * time.Second

	// Time allowed to read the next pong message from the peer
	pongWait = 60 * time.Second

	// Send pings to peer with this period (must be less than pongWait)
	pingPeriod = (pongWait * 9) / 10

	// Maximum message size allowed from peer
	maxMessageSize = 512 * 1024 // 512 KB
)

// Client represents a single WebSocket connection to a contest
type Client struct {
	// The WebSocket connection
	conn *websocket.Conn

	// The hub this client belongs to
	hub *Hub

	// Buffered channel of outbound messages
	send chan []byte

	// User information
	UserID   string
	Username string
	IsHost   bool
}

// NewClient creates a new Client instance
func NewClient(conn *websocket.Conn, hub *Hub, userID, username string, isHost bool) *Client {
	return &Client{
		conn:     conn,
		hub:      hub,
		send:     make(chan []byte, 256),
		UserID:   userID,
		Username: username,
		IsHost:   isHost,
	}
}

// ReadPump pumps messages from the WebSocket connection to the hub
// The application runs ReadPump in a per-connection goroutine
func (c *Client) ReadPump() {
	defer func() {
		// Unregister the client when the connection closes
		c.hub.unregister <- c
		c.conn.Close()
	}()

	// Configure the WebSocket connection
	c.conn.SetReadLimit(maxMessageSize)
	c.conn.SetReadDeadline(time.Now().Add(pongWait))
	c.conn.SetPongHandler(func(string) error {
		c.conn.SetReadDeadline(time.Now().Add(pongWait))
		return nil
	})

	// Read messages from the WebSocket connection
	for {
		_, message, err := c.conn.ReadMessage()
		if err != nil {
			if websocket.IsUnexpectedCloseError(err, websocket.CloseGoingAway, websocket.CloseAbnormalClosure) {
				log.Printf("WebSocket error for user %s: %v", c.Username, err)
			}
			break
		}

		// Parse the message type first
		var baseMsg struct {
			Type MessageType `json:"type"`
		}
		if err := json.Unmarshal(message, &baseMsg); err != nil {
			log.Printf("Failed to parse message type from user %s: %v", c.Username, err)
			c.SendError("Invalid message format")
			continue
		}

		log.Printf("Received message from user %s: type=%s, raw=%s", c.Username, baseMsg.Type, string(message))

		// Parse the full message into a map to preserve all fields
		var fullMsg map[string]interface{}
		if err := json.Unmarshal(message, &fullMsg); err != nil {
			log.Printf("Failed to parse full message from user %s: %v", c.Username, err)
			c.SendError("Invalid message format")
			continue
		}

		// Create internal message with the full data
		internalMsg := InternalMessage{
			Type:   baseMsg.Type,
			Client: c,
			Data:   fullMsg,
		}

		// Forward the message to the hub for processing
		log.Printf("Forwarding message type %s from user %s to hub for processing", baseMsg.Type, c.Username)
		c.hub.processMessage <- internalMsg
	}
}

// WritePump pumps messages from the hub to the WebSocket connection
// The application runs WritePump in a per-connection goroutine
func (c *Client) WritePump() {
	ticker := time.NewTicker(pingPeriod)
	defer func() {
		ticker.Stop()
		c.conn.Close()
	}()

	for {
		select {
		case message, ok := <-c.send:
			c.conn.SetWriteDeadline(time.Now().Add(writeWait))
			if !ok {
				// The hub closed the channel
				c.conn.WriteMessage(websocket.CloseMessage, []byte{})
				return
			}

			// Write the message to the WebSocket connection
			w, err := c.conn.NextWriter(websocket.TextMessage)
			if err != nil {
				return
			}
			w.Write(message)

			// Add queued messages to the current WebSocket message (batching)
			n := len(c.send)
			for i := 0; i < n; i++ {
				w.Write([]byte{'\n'})
				w.Write(<-c.send)
			}

			if err := w.Close(); err != nil {
				return
			}

		case <-ticker.C:
			c.conn.SetWriteDeadline(time.Now().Add(writeWait))
			if err := c.conn.WriteMessage(websocket.PingMessage, nil); err != nil {
				return
			}
		}
	}
}

// SendMessage sends a message to the client
func (c *Client) SendMessage(messageType MessageType, payload interface{}) {
	message := Message{
		Type:    messageType,
		Payload: payload,
	}

	data, err := json.Marshal(message)
	if err != nil {
		log.Printf("Failed to marshal message for user %s: %v", c.Username, err)
		return
	}

	select {
	case c.send <- data:
	default:
		// The send buffer is full, close the connection
		close(c.send)
	}
}

// SendError sends an error message to the client
func (c *Client) SendError(errorMessage string) {
	c.SendMessage(MessageTypeError, ErrorPayload{
		Message: errorMessage,
	})
}

// Close closes the client connection
func (c *Client) Close() {
	close(c.send)
}
