package ws

import (
	"sync"
	"testing"
	"time"
)

const settle = 20 * time.Millisecond

func newTestClient(hub *Hub, projectID string) *Client {
	return NewClient(hub, nil, projectID, "user-1", "Test User", nil, nil)
}

func TestRegisterAddsClientToRoom(t *testing.T) {
	hub := NewHub()
	go hub.Run()

	c := newTestClient(hub, "proj-1")
	hub.Register(c)
	time.Sleep(settle)

	hub.mu.RLock()
	room, ok := hub.rooms["proj-1"]
	hub.mu.RUnlock()

	if !ok {
		t.Fatal("room was not created")
	}
	if _, exists := room[c]; !exists {
		t.Fatal("client not found in room")
	}
}

func TestUnregisterRemovesClientAndSignalsDone(t *testing.T) {
	hub := NewHub()
	go hub.Run()

	c := newTestClient(hub, "proj-1")
	hub.Register(c)
	time.Sleep(settle)

	hub.Unregister(c)
	time.Sleep(settle)

	// done channel should be closed
	select {
	case <-c.Done():
		// expected
	default:
		t.Fatal("done channel was not closed after unregister")
	}
}

func TestUnregisterLastClientCleansUpRoom(t *testing.T) {
	hub := NewHub()
	go hub.Run()

	c := newTestClient(hub, "proj-1")
	hub.Register(c)
	time.Sleep(settle)

	hub.Unregister(c)
	time.Sleep(settle)

	hub.mu.RLock()
	_, exists := hub.rooms["proj-1"]
	hub.mu.RUnlock()

	if exists {
		t.Fatal("room should be deleted when last client leaves")
	}
}

func TestUnregisterOneOfTwoKeepsRoom(t *testing.T) {
	hub := NewHub()
	go hub.Run()

	c1 := newTestClient(hub, "proj-1")
	c2 := NewClient(hub, nil, "proj-1", "user-2", "User Two", nil, nil)
	hub.Register(c1)
	hub.Register(c2)
	time.Sleep(settle)

	hub.Unregister(c1)
	time.Sleep(settle)

	hub.mu.RLock()
	room := hub.rooms["proj-1"]
	hub.mu.RUnlock()

	if room == nil {
		t.Fatal("room should still exist with one client remaining")
	}
	if _, exists := room[c2]; !exists {
		t.Fatal("remaining client should still be in room")
	}
}

func TestBroadcastSendsToAllClients(t *testing.T) {
	hub := NewHub()
	go hub.Run()

	c1 := NewClient(hub, nil, "proj-1", "user-1", "U1", nil, nil)
	c2 := NewClient(hub, nil, "proj-1", "user-2", "U2", nil, nil)
	hub.Register(c1)
	hub.Register(c2)
	time.Sleep(settle)

	msg := []byte("hello")
	hub.Broadcast("proj-1", msg, nil)

	got1 := readWithTimeout(t, c1.send)
	got2 := readWithTimeout(t, c2.send)

	if string(got1) != "hello" {
		t.Fatalf("c1 got %q, want %q", got1, "hello")
	}
	if string(got2) != "hello" {
		t.Fatalf("c2 got %q, want %q", got2, "hello")
	}
}

func TestBroadcastWithExcludeSkipsClient(t *testing.T) {
	hub := NewHub()
	go hub.Run()

	c1 := NewClient(hub, nil, "proj-1", "user-1", "U1", nil, nil)
	c2 := NewClient(hub, nil, "proj-1", "user-2", "U2", nil, nil)
	hub.Register(c1)
	hub.Register(c2)
	time.Sleep(settle)

	hub.Broadcast("proj-1", []byte("ping"), c1)

	// c2 should receive
	got := readWithTimeout(t, c2.send)
	if string(got) != "ping" {
		t.Fatalf("c2 got %q, want %q", got, "ping")
	}

	// c1 should NOT receive
	select {
	case msg := <-c1.send:
		t.Fatalf("excluded client received message: %q", msg)
	case <-time.After(50 * time.Millisecond):
		// expected
	}
}

func TestBroadcastAllSendsToEveryone(t *testing.T) {
	hub := NewHub()
	go hub.Run()

	c1 := NewClient(hub, nil, "proj-1", "user-1", "U1", nil, nil)
	c2 := NewClient(hub, nil, "proj-1", "user-2", "U2", nil, nil)
	hub.Register(c1)
	hub.Register(c2)
	time.Sleep(settle)

	hub.BroadcastAll("proj-1", []byte("all"))

	got1 := readWithTimeout(t, c1.send)
	got2 := readWithTimeout(t, c2.send)

	if string(got1) != "all" || string(got2) != "all" {
		t.Fatalf("expected both clients to receive 'all', got %q and %q", got1, got2)
	}
}

func TestBroadcastToNonexistentRoomDoesNotPanic(t *testing.T) {
	hub := NewHub()
	go hub.Run()

	// Should not panic
	hub.Broadcast("no-such-room", []byte("ghost"), nil)
	hub.BroadcastAll("no-such-room", []byte("ghost"))
}

func TestAcquireAILockBlocksSecondCaller(t *testing.T) {
	hub := NewHub()
	// No need to run the hub loop for AI lock tests

	release := hub.AcquireAILock("proj-1")

	acquired := make(chan struct{})
	go func() {
		r2 := hub.AcquireAILock("proj-1")
		close(acquired)
		r2()
	}()

	// Second acquire should be blocked
	select {
	case <-acquired:
		t.Fatal("second AcquireAILock should block while first is held")
	case <-time.After(100 * time.Millisecond):
		// expected
	}

	// Release first lock
	release()

	// Now second should proceed
	select {
	case <-acquired:
		// expected
	case <-time.After(time.Second):
		t.Fatal("second AcquireAILock should proceed after release")
	}
}

func TestAcquireAILockDifferentProjectsIndependent(t *testing.T) {
	hub := NewHub()

	var wg sync.WaitGroup
	wg.Add(2)

	for _, pid := range []string{"proj-a", "proj-b"} {
		go func(id string) {
			defer wg.Done()
			release := hub.AcquireAILock(id)
			time.Sleep(10 * time.Millisecond)
			release()
		}(pid)
	}

	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()

	select {
	case <-done:
		// both completed without deadlock
	case <-time.After(time.Second):
		t.Fatal("AI locks on different projects should not block each other")
	}
}

func TestMultipleRoomsAreIndependent(t *testing.T) {
	hub := NewHub()
	go hub.Run()

	c1 := NewClient(hub, nil, "proj-a", "user-1", "U1", nil, nil)
	c2 := NewClient(hub, nil, "proj-b", "user-2", "U2", nil, nil)
	hub.Register(c1)
	hub.Register(c2)
	time.Sleep(settle)

	hub.Broadcast("proj-a", []byte("only-a"), nil)

	got := readWithTimeout(t, c1.send)
	if string(got) != "only-a" {
		t.Fatalf("c1 got %q, want %q", got, "only-a")
	}

	// c2 should NOT receive proj-a's broadcast
	select {
	case msg := <-c2.send:
		t.Fatalf("client in proj-b received proj-a message: %q", msg)
	case <-time.After(50 * time.Millisecond):
		// expected
	}
}

func TestConcurrentBroadcastAndUnregister(t *testing.T) {
	hub := NewHub()
	go hub.Run()

	const numClients = 50
	clients := make([]*Client, numClients)
	for i := range clients {
		clients[i] = NewClient(hub, nil, "proj-race", "user-"+string(rune('A'+i)), "U", nil, nil)
		hub.Register(clients[i])
	}
	time.Sleep(settle)

	var wg sync.WaitGroup

	// Broadcast concurrently while unregistering clients
	wg.Add(2)
	go func() {
		defer wg.Done()
		for i := 0; i < 200; i++ {
			hub.Broadcast("proj-race", []byte("msg"), nil)
		}
	}()
	go func() {
		defer wg.Done()
		for _, c := range clients {
			hub.Unregister(c)
			time.Sleep(time.Millisecond)
		}
	}()

	wg.Wait()
	// If we get here without a panic, the race is fixed
}

func TestSendAfterCloseReturnsFalse(t *testing.T) {
	hub := NewHub()
	c := newTestClient(hub, "proj-1")
	c.Close()

	if c.Send([]byte("late")) {
		t.Fatal("Send should return false after Close")
	}
}

// readWithTimeout reads from a channel with a timeout, failing the test on timeout.
func readWithTimeout(t *testing.T, ch <-chan []byte) []byte {
	t.Helper()
	select {
	case msg := <-ch:
		return msg
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for message on send channel")
		return nil
	}
}
