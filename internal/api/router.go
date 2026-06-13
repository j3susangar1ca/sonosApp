package api

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"time"

	"github.com/gorilla/websocket"
	"github.com/jesuslangarica/sonosApp/internal/eventbus"
	"github.com/jesuslangarica/sonosApp/internal/models"
	"github.com/jesuslangarica/sonosApp/internal/player"
	"github.com/jesuslangarica/sonosApp/internal/streaming"
	"github.com/jesuslangarica/sonosApp/internal/telemetry"
	"github.com/jesuslangarica/sonosApp/internal/ws"
)

var upgrader = websocket.Upgrader{
	ReadBufferSize:  1024,
	WriteBufferSize: 1024,
	CheckOrigin: func(r *http.Request) bool {
		return true // Allow development environments
	},
}

// NewRouter configures the perimeter HTTP endpoints using Go 1.22+ native ServeMux routing capabilities.
func NewRouter(
	fsm *player.JukeboxFSM,
	eb *eventbus.EventBus,
	hub *ws.WebSocketHub,
	proxy *streaming.StreamingProxy,
) http.Handler {
	mux := http.NewServeMux()

	// API endpoints
	mux.HandleFunc("POST /api/tracks", handleAddTrack(eb))
	mux.HandleFunc("POST /api/skip", handleSkip(eb))
	mux.HandleFunc("POST /api/volume", handleSetVolume(eb))
	mux.HandleFunc("POST /api/clear", handleClearQueue(eb))
	mux.HandleFunc("POST /api/pause", handlePause(eb))
	mux.HandleFunc("POST /api/resume", handleResume(eb))
	mux.HandleFunc("GET /api/queue", handleGetQueue(fsm))
	mux.HandleFunc("GET /api/users", handleGetUsers(fsm))
	mux.HandleFunc("GET /api/ws", handleWebSocket(hub))
	mux.Handle("GET /metrics", telemetry.Handler())

	// Local file streaming proxy mount
	mux.HandleFunc("GET /stream", func(w http.ResponseWriter, r *http.Request) {
		proxy.ServeHTTP(w, r)
	})

	return mux
}

func handleAddTrack(eb *eventbus.EventBus) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			URL    string `json:"url"`
			UserID string `json:"user_id"`
		}

		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid request body", http.StatusBadRequest)
			return
		}

		if req.URL == "" || req.UserID == "" {
			http.Error(w, "missing required fields: url and user_id", http.StatusBadRequest)
			return
		}

		cmd := models.Command{
			Type: models.ActionAddTrack,
			Payload: models.AddTrackPayload{
				URL:       req.URL,
				UserID:    req.UserID,
				Timestamp: time.Now(),
			},
		}

		eb.Inject(cmd)

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusAccepted)
		_ = json.NewEncoder(w).Encode(map[string]string{"status": "accepted"})
	}
}

func handleSkip(eb *eventbus.EventBus) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			UserID string `json:"user_id"`
		}

		// Body is optional for skips
		_ = json.NewDecoder(r.Body).Decode(&req)

		cmd := models.Command{
			Type:    models.ActionSkip,
			Payload: req.UserID,
		}

		eb.Inject(cmd)

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusAccepted)
		_ = json.NewEncoder(w).Encode(map[string]string{"status": "accepted"})
	}
}

func handleSetVolume(eb *eventbus.EventBus) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Level  int    `json:"level"`
			UserID string `json:"user_id"`
		}

		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid request body", http.StatusBadRequest)
			return
		}

		if req.Level < 0 || req.Level > 100 {
			http.Error(w, "volume level must be between 0 and 100", http.StatusBadRequest)
			return
		}

		cmd := models.Command{
			Type: models.ActionSetVolume,
			Payload: models.SetVolumePayload{
				Level:  req.Level,
				UserID: req.UserID,
			},
		}

		eb.Inject(cmd)

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusAccepted)
		_ = json.NewEncoder(w).Encode(map[string]string{"status": "accepted"})
	}
}

func handleClearQueue(eb *eventbus.EventBus) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		cmd := models.Command{
			Type:    models.ActionClearQueue,
			Payload: nil,
		}

		eb.Inject(cmd)

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusAccepted)
		_ = json.NewEncoder(w).Encode(map[string]string{"status": "accepted"})
	}
}

func handlePause(eb *eventbus.EventBus) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		cmd := models.Command{
			Type:    models.ActionPause,
			Payload: nil,
		}

		eb.Inject(cmd)

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusAccepted)
		_ = json.NewEncoder(w).Encode(map[string]string{"status": "accepted"})
	}
}

func handleResume(eb *eventbus.EventBus) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		cmd := models.Command{
			Type:    models.ActionResume,
			Payload: nil,
		}

		eb.Inject(cmd)

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusAccepted)
		_ = json.NewEncoder(w).Encode(map[string]string{"status": "accepted"})
	}
}

func handleGetQueue(fsm *player.JukeboxFSM) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		queue := fsm.GetQueue()
		if queue == nil {
			queue = make([]models.Track, 0)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(queue)
	}
}

func handleGetUsers(fsm *player.JukeboxFSM) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		users := fsm.GetActiveUsers()
		votes := fsm.GetVotes()
		karma := fsm.GetKarma()

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"users": users,
			"votes": votes,
			"karma": karma,
		})
	}
}

func handleWebSocket(hub *ws.WebSocketHub) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		userID := r.URL.Query().Get("user_id")
		if userID == "" {
			http.Error(w, "missing required user_id parameter", http.StatusBadRequest)
			return
		}

		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			slog.Error("Failed to upgrade HTTP connection to WebSocket", "error", err)
			return
		}

		// Register client in the hub
		client := ws.NewClient(userID, conn)
		hub.Register(client)
	}
}
