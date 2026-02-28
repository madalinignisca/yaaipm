package ws

import (
	"log"
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
	ProjectID string
	UserID    string
	UserName  string
	User      *models.User
	OnMessage MessageHandler
}

// NewClient creates a new WebSocket client.
func NewClient(hub *Hub, conn *websocket.Conn, projectID, userID, userName string, user *models.User, handler MessageHandler) *Client {
	return &Client{
		Hub:       hub,
		Conn:      conn,
		send:      make(chan []byte, 256),
		ProjectID: projectID,
		UserID:    userID,
		UserName:  userName,
		User:      user,
		OnMessage: handler,
	}
}

// Send queues a message for sending to this client.
func (c *Client) Send(data []byte) {
	select {
	case c.send <- data:
	default:
		log.Printf("ws: client send buffer full, dropping message for user %s", c.UserID)
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
	c.Conn.SetReadDeadline(time.Now().Add(pongWait))
	c.Conn.SetPongHandler(func(string) error {
		c.Conn.SetReadDeadline(time.Now().Add(pongWait))
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
		case message, ok := <-c.send:
			c.Conn.SetWriteDeadline(time.Now().Add(writeWait))
			if !ok {
				// Hub closed the channel
				c.Conn.WriteMessage(websocket.CloseMessage, []byte{})
				return
			}
			if err := c.Conn.WriteMessage(websocket.TextMessage, message); err != nil {
				return
			}

		case <-ticker.C:
			c.Conn.SetWriteDeadline(time.Now().Add(writeWait))
			if err := c.Conn.WriteMessage(websocket.PingMessage, nil); err != nil {
				return
			}
		}
	}
}
