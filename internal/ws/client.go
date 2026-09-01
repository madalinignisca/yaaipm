package ws

import (
	"log"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"github.com/madalin/forgedesk/internal/models"
)

const (
	writeWait      = 10 * time.Second
	pongWait       = 60 * time.Second
	pingPeriod     = 54 * time.Second
	maxMessageSize = 8192
)

// MessageHandler is called when the client sends a message.
type MessageHandler func(client *Client, data []byte)

// Client represents a single WebSocket connection in a project room.
type Client struct {
	Hub       *Hub
	Conn      *websocket.Conn
	send      chan []byte
	done      chan struct{}
	closeOnce sync.Once
	User      *models.User
	OnMessage MessageHandler
	ProjectID string
	UserID    string
	UserName  string
}

// NewClient creates a new WebSocket client.
func NewClient(hub *Hub, conn *websocket.Conn, projectID, userID, userName string, user *models.User, handler MessageHandler) *Client {
	return &Client{
		Hub:       hub,
		Conn:      conn,
		send:      make(chan []byte, 256),
		done:      make(chan struct{}),
		ProjectID: projectID,
		UserID:    userID,
		UserName:  userName,
		User:      user,
		OnMessage: handler,
	}
}

// Close signals the client to shut down. Safe to call multiple times.
func (c *Client) Close() {
	c.closeOnce.Do(func() {
		close(c.done)
	})
}

// Done returns a channel that is closed when the client shuts down.
func (c *Client) Done() <-chan struct{} {
	return c.done
}

// Send queues a message for sending to this client.
// Returns false if the client is closed or the buffer is full.
func (c *Client) Send(data []byte) bool {
	select {
	case <-c.done:
		return false
	default:
	}
	select {
	case c.send <- data:
		return true
	case <-c.done:
		return false
	default:
		log.Printf("ws: client send buffer full, dropping message for user %s", c.UserID)
		return false
	}
}

// ReadPump reads messages from the WebSocket connection.
// Must be called as a goroutine. Unregisters on close.
func (c *Client) ReadPump() {
	defer func() {
		c.Hub.Unregister(c)
		c.Conn.Close()
	}()

	c.Conn.SetReadLimit(maxMessageSize)
	_ = c.Conn.SetReadDeadline(time.Now().Add(pongWait))
	c.Conn.SetPongHandler(func(string) error {
		_ = c.Conn.SetReadDeadline(time.Now().Add(pongWait))
		return nil
	})

	for {
		_, message, err := c.Conn.ReadMessage()
		if err != nil {
			if websocket.IsUnexpectedCloseError(err, websocket.CloseGoingAway, websocket.CloseNormalClosure) {
				log.Printf("ws: read error: %v", err)
			}
			break
		}
		if c.OnMessage != nil {
			c.OnMessage(c, message)
		}
	}
}

// WritePump sends messages from the send channel to the WebSocket connection.
// Must be called as a goroutine.
func (c *Client) WritePump() {
	ticker := time.NewTicker(pingPeriod)
	defer func() {
		ticker.Stop()
		c.Conn.Close()
	}()

	for {
		select {
		case message := <-c.send:
			_ = c.Conn.SetWriteDeadline(time.Now().Add(writeWait))
			if err := c.Conn.WriteMessage(websocket.TextMessage, message); err != nil {
				return
			}

		case <-c.done:
			_ = c.Conn.WriteMessage(websocket.CloseMessage, []byte{})
			return

		case <-ticker.C:
			_ = c.Conn.SetWriteDeadline(time.Now().Add(writeWait))
			if err := c.Conn.WriteMessage(websocket.PingMessage, nil); err != nil {
				return
			}
		}
	}
}
