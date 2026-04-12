package ws

import (
	"sync"
)

// Hub manages WebSocket rooms keyed by project ID.
type Hub struct {
	rooms      map[string]map[*Client]struct{}
	aiLocks    map[string]chan struct{}
	register   chan *Client
	unregister chan *Client
	mu         sync.RWMutex
}

// NewHub creates a new WebSocket hub.
func NewHub() *Hub {
	return &Hub{
		rooms:      make(map[string]map[*Client]struct{}),
		aiLocks:    make(map[string]chan struct{}),
		register:   make(chan *Client),
		unregister: make(chan *Client),
	}
}

// Run processes register/unregister events. Must be called as a goroutine.
func (h *Hub) Run() {
	for {
		select {
		case client := <-h.register:
			h.mu.Lock()
			room := h.rooms[client.ProjectID]
			if room == nil {
				room = make(map[*Client]struct{})
				h.rooms[client.ProjectID] = room
			}
			room[client] = struct{}{}
			h.mu.Unlock()

		case client := <-h.unregister:
			h.mu.Lock()
			if room, ok := h.rooms[client.ProjectID]; ok {
				if _, exists := room[client]; exists {
					delete(room, client)
					client.Close()
					if len(room) == 0 {
						delete(h.rooms, client.ProjectID)
					}
				}
			}
			h.mu.Unlock()
		}
	}
}

// Register adds a client to its project room.
func (h *Hub) Register(client *Client) {
	h.register <- client
}

// Unregister removes a client from its project room.
func (h *Hub) Unregister(client *Client) {
	h.unregister <- client
}

// Broadcast sends data to all clients in a project room, optionally excluding one.
func (h *Hub) Broadcast(projectID string, data []byte, exclude *Client) {
	h.mu.RLock()
	room := h.rooms[projectID]
	targets := make([]*Client, 0, len(room))
	for client := range room {
		if client != exclude {
			targets = append(targets, client)
		}
	}
	h.mu.RUnlock()

	for _, client := range targets {
		client.Send(data)
	}
}

// BroadcastAll sends data to all clients in a project room including the sender.
func (h *Hub) BroadcastAll(projectID string, data []byte) {
	h.Broadcast(projectID, data, nil)
}

// AcquireAILock acquires the AI processing lock for a project.
// Returns a release function. Blocks if another AI call is in progress.
func (h *Hub) AcquireAILock(projectID string) func() {
	h.mu.Lock()
	lock, ok := h.aiLocks[projectID]
	if !ok {
		lock = make(chan struct{}, 1)
		h.aiLocks[projectID] = lock
	}
	h.mu.Unlock()

	lock <- struct{}{} // blocks if another goroutine holds the lock
	return func() { <-lock }
}
