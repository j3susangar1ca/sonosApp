package ws

import (
	"log/slog"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"github.com/jesuslangarica/sonosApp/internal/player"
)

// Client represents a connected user.
type Client struct {
	ID            string
	Conn          *websocket.Conn
	Send          chan []byte
	LastHeartbeat time.Time
	done          chan struct{} // Closed when the client is evicted or stopped to prevent goroutine leaks.
}

// NewClient initializes a Client with the required channel and metadata.
func NewClient(id string, conn *websocket.Conn) *Client {
	return &Client{
		ID:            id,
		Conn:          conn,
		Send:          make(chan []byte, 256),
		LastHeartbeat: time.Now(),
		done:          make(chan struct{}),
	}
}

// WebSocketHub handles the active connections, heartbeats, and parallel broadcasts.
type WebSocketHub struct {
	mu               sync.RWMutex
	clients          map[string]*Client
	register         chan *Client
	unregister       chan *Client
	fsm              *player.JukeboxFSM
	stopChan         chan struct{}
	wg               sync.WaitGroup
	heartbeatTimeout time.Duration
	sendTimeout      time.Duration
}

// NewWebSocketHub instantiates a WebSocketHub.
func NewWebSocketHub(fsm *player.JukeboxFSM) *WebSocketHub {
	return &WebSocketHub{
		clients:          make(map[string]*Client),
		register:         make(chan *Client),
		unregister:       make(chan *Client),
		fsm:              fsm,
		stopChan:         make(chan struct{}),
		heartbeatTimeout: 10 * time.Second,
		sendTimeout:      100 * time.Millisecond,
	}
}

// Register adds a client to the hub's registry.
func (h *WebSocketHub) Register(c *Client) {
	select {
	case h.register <- c:
	case <-h.stopChan:
	}
}

// Unregister removes a client from the hub's registry.
func (h *WebSocketHub) Unregister(c *Client) {
	select {
	case h.unregister <- c:
	case <-h.stopChan:
	}
}

// Start runs the hub registration and heartbeat sweep workers.
func (h *WebSocketHub) Start() {
	h.wg.Add(2)
	go h.run()
	go h.heartbeatSweepLoop()
}

// Stop stops the hub and evicts all clients.
func (h *WebSocketHub) Stop() {
	close(h.stopChan)
	h.wg.Wait()
}

// HandleHeartbeat updates the LastHeartbeat timestamp for the given user.
func (h *WebSocketHub) HandleHeartbeat(userID string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if c, exists := h.clients[userID]; exists {
		c.LastHeartbeat = time.Now()
		slog.Debug("Received heartbeat", "user_id", userID)
	}
}

// Broadcast dispatches a message to all active clients concurrently with a timeout.
func (h *WebSocketHub) Broadcast(msg []byte) {
	h.mu.RLock()
	defer h.mu.RUnlock()

	for _, client := range h.clients {
		go func(c *Client) {
			doneChan := c.done
			if doneChan == nil {
				// Prevent blocking on nil channel if client was created without done
				doneChan = make(chan struct{})
			}

			select {
			case c.Send <- msg:
			case <-doneChan:
				// Client is disconnected/closed, ignore send.
			case <-time.After(h.sendTimeout):
				slog.Error("Failed to broadcast message to client (timeout / buffer full)", "user_id", c.ID)
			}
		}(client)
	}
}

func (h *WebSocketHub) run() {
	defer h.wg.Done()
	for {
		select {
		case <-h.stopChan:
			h.mu.Lock()
			for _, client := range h.clients {
				h.closeClient(client)
			}
			h.mu.Unlock()
			return

		case client := <-h.register:
			h.mu.Lock()
			if oldClient, exists := h.clients[client.ID]; exists {
				h.closeClient(oldClient)
			}
			h.clients[client.ID] = client
			h.fsm.AddUser(client.ID)
			h.mu.Unlock()

			// Launch pumps
			go client.writePump(h.sendTimeout)
			go client.readPump(h)
			slog.Info("Client registered successfully", "user_id", client.ID)

		case client := <-h.unregister:
			h.mu.Lock()
			if _, exists := h.clients[client.ID]; exists {
				h.closeClient(client)
				h.fsm.RemoveUser(client.ID)
			}
			h.mu.Unlock()
			slog.Info("Client unregistered successfully", "user_id", client.ID)
		}
	}
}

func (h *WebSocketHub) heartbeatSweepLoop() {
	defer h.wg.Done()
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-h.stopChan:
			return
		case <-ticker.C:
			h.sweepInactiveClients()
		}
	}
}

func (h *WebSocketHub) sweepInactiveClients() {
	h.mu.Lock()
	defer h.mu.Unlock()

	now := time.Now()
	var toUnregister []*Client

	for _, client := range h.clients {
		if now.Sub(client.LastHeartbeat) > h.heartbeatTimeout {
			slog.Warn("Heartbeat sweep timeout, scheduling client eviction", "user_id", client.ID, "last_heartbeat", client.LastHeartbeat)
			toUnregister = append(toUnregister, client)
		}
	}

	for _, client := range toUnregister {
		h.closeClient(client)
		h.fsm.RemoveUser(client.ID)
	}
}

func (h *WebSocketHub) closeClient(c *Client) {
	if _, ok := h.clients[c.ID]; ok {
		delete(h.clients, c.ID)
	}

	if c.done != nil {
		select {
		case <-c.done:
			// already closed
		default:
			close(c.done)
		}
	}

	if c.Conn != nil {
		_ = c.Conn.Close()
	}
}

// writePump handles serialization and sends from the channel out to the WebSocket.
func (c *Client) writePump(sendTimeout time.Duration) {
	defer func() {
		if c.Conn != nil {
			_ = c.Conn.Close()
		}
	}()

	if c.Conn == nil {
		return
	}

	for {
		select {
		case msg, ok := <-c.Send:
			if !ok {
				_ = c.Conn.WriteMessage(websocket.CloseMessage, []byte{})
				return
			}

			_ = c.Conn.SetWriteDeadline(time.Now().Add(sendTimeout))
			err := c.Conn.WriteMessage(websocket.TextMessage, msg)
			if err != nil {
				return
			}
		case <-c.done:
			return
		}
	}
}

// readPump listens for user incoming heartbeats (TextMessage "ping").
func (c *Client) readPump(hub *WebSocketHub) {
	defer func() {
		select {
		case hub.unregister <- c:
		case <-time.After(5 * time.Second):
			slog.Warn("Failed to unregister client, forcing close", "user_id", c.ID)
		}
		if c.Conn != nil {
			_ = c.Conn.Close()
		}
	}()

	if c.Conn == nil {
		return
	}

	c.Conn.SetReadLimit(512)
	c.Conn.SetReadDeadline(time.Now().Add(60 * time.Second))

	for {
		msgType, msg, err := c.Conn.ReadMessage()
		if err != nil {
			break
		}

		// Renew read deadline after successful message
		c.Conn.SetReadDeadline(time.Now().Add(60 * time.Second))

		if msgType == websocket.TextMessage && string(msg) == "ping" {
			hub.HandleHeartbeat(c.ID)
		}
	}
}
