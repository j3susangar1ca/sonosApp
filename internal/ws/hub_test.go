package ws

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/jesuslangarica/sonosApp/internal/player"
)

var upgrader = websocket.Upgrader{
	CheckOrigin: func(r *http.Request) bool { return true },
}

// TestHubRegisterAndBroadcast checks connection, FSM registration, and parallel broadcast.
func TestHubRegisterAndBroadcast(t *testing.T) {
	fsm := player.NewJukeboxFSM(10, nil)
	hub := NewWebSocketHub(fsm)
	hub.Start()
	defer hub.Stop()

	// Mock server
	var wsConn *websocket.Conn
	var wsMutex sync.Mutex
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		
		wsMutex.Lock()
		wsConn = conn
		wsMutex.Unlock()

		// Build client
		client := &Client{
			ID:            "userA",
			Conn:          conn,
			Send:          make(chan []byte, 10),
			LastHeartbeat: time.Now(),
		}
		hub.register <- client
	}))
	defer server.Close()

	// Dial WebSocket client
	dialURL := "ws" + strings.TrimPrefix(server.URL, "http")
	clientConn, _, err := websocket.DefaultDialer.Dial(dialURL, nil)
	if err != nil {
		t.Fatalf("failed to dial: %v", err)
	}
	defer clientConn.Close()

	// Wait for registration
	time.Sleep(50 * time.Millisecond)

	active := fsm.GetActiveUsers()
	if !active["userA"] {
		t.Error("expected userA to be active in FSM")
	}

	// Broadcast
	hub.Broadcast([]byte("hello client"))

	// Verify client receives
	_, msg, err := clientConn.ReadMessage()
	if err != nil {
		t.Fatalf("failed to read message: %v", err)
	}
	if string(msg) != "hello client" {
		t.Errorf("expected 'hello client', got '%s'", string(msg))
	}
}

// TestHubHeartbeatPruning verifies that clients without heartbeats are pruned.
func TestHubHeartbeatPruning(t *testing.T) {
	fsm := player.NewJukeboxFSM(10, nil)
	hub := NewWebSocketHub(fsm)
	// Set very small timeouts for testing speed
	hub.heartbeatTimeout = 100 * time.Millisecond
	hub.Start()
	defer hub.Stop()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		client := &Client{
			ID:            "userDead",
			Conn:          conn,
			Send:          make(chan []byte, 10),
			LastHeartbeat: time.Now(),
		}
		hub.register <- client
	}))
	defer server.Close()

	dialURL := "ws" + strings.TrimPrefix(server.URL, "http")
	clientConn, _, err := websocket.DefaultDialer.Dial(dialURL, nil)
	if err != nil {
		t.Fatalf("failed to dial: %v", err)
	}
	defer clientConn.Close()

	// Wait for registration
	time.Sleep(50 * time.Millisecond)

	active := fsm.GetActiveUsers()
	if !active["userDead"] {
		t.Fatal("expected userDead to be registered in FSM")
	}

	// Wait for heartbeat sweep (interval is 2s, but we can call sweepInactiveClients manually or wait)
	// Since we want fast tests, we call sweepInactiveClients directly!
	time.Sleep(120 * time.Millisecond) // Let it expire
	hub.sweepInactiveClients()

	activeAfter := fsm.GetActiveUsers()
	if activeAfter["userDead"] {
		t.Error("expected userDead to be pruned from active users")
	}
}

// TestHubHeartbeatUpdate validates heartbeat receipt.
func TestHubHeartbeatUpdate(t *testing.T) {
	fsm := player.NewJukeboxFSM(10, nil)
	hub := NewWebSocketHub(fsm)
	hub.heartbeatTimeout = 150 * time.Millisecond
	hub.Start()
	defer hub.Stop()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		client := &Client{
			ID:            "userLive",
			Conn:          conn,
			Send:          make(chan []byte, 10),
			LastHeartbeat: time.Now(),
		}
		hub.register <- client
	}))
	defer server.Close()

	dialURL := "ws" + strings.TrimPrefix(server.URL, "http")
	clientConn, _, err := websocket.DefaultDialer.Dial(dialURL, nil)
	if err != nil {
		t.Fatalf("failed to dial: %v", err)
	}
	defer clientConn.Close()

	time.Sleep(50 * time.Millisecond)

	// Send heartbeat "ping"
	err = clientConn.WriteMessage(websocket.TextMessage, []byte("ping"))
	if err != nil {
		t.Fatalf("failed to send ping: %v", err)
	}

	// Wait and verify client wasn't pruned because of heartbeat update
	time.Sleep(100 * time.Millisecond)
	hub.sweepInactiveClients()

	active := fsm.GetActiveUsers()
	if !active["userLive"] {
		t.Error("expected userLive to remain active after sending ping heartbeat")
	}
}

// TestBroadcastTimeout verifies that a slow receiver doesn't block broadcast to others.
func TestBroadcastTimeout(t *testing.T) {
	fsm := player.NewJukeboxFSM(10, nil)
	hub := NewWebSocketHub(fsm)
	hub.sendTimeout = 10 * time.Millisecond // fast timeout
	hub.Start()
	defer hub.Stop()

	fastCh := make(chan []byte, 1)
	slowCh := make(chan []byte, 0) // blocks on send immediately if not reading

	// Register fake clients directly to bypass network overhead
	fastClient := &Client{ID: "fast", Send: fastCh}
	slowClient := &Client{ID: "slow", Send: slowCh}

	hub.mu.Lock()
	hub.clients["fast"] = fastClient
	hub.clients["slow"] = slowClient
	hub.mu.Unlock()

	// Broadcast
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()

	done := make(chan struct{})
	go func() {
		hub.Broadcast([]byte("data"))
		close(done)
	}()

	select {
	case <-done:
		// Broadcast finished successfully without blocking indefinitely!
	case <-ctx.Done():
		t.Fatal("Broadcast blocked on slow client")
	}

	// Fast client should receive
	select {
	case msg := <-fastCh:
		if string(msg) != "data" {
			t.Errorf("unexpected msg: %s", string(msg))
		}
	default:
		t.Error("fast client did not receive broadcast")
	}
}
