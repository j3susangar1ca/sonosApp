package api

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"time"

	"github.com/gorilla/websocket"
	"github.com/jesuslangarica/sonosApp/internal/eventbus"
	"github.com/jesuslangarica/sonosApp/internal/models"
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

// maxWebhookBodySize defines the maximum allowed webhook body size (4KB) to mitigate DoS.
const maxWebhookBodySize = 4096

// NewRouter configures the perimeter HTTP endpoints using Go 1.22+ native ServeMux routing capabilities.
// §20: Accepts a ZoneRegistry instead of a single FSM to support multiple independent audio zones.
func NewRouter(
	registry *ZoneRegistry,
	eb *eventbus.EventBus,
	hub *ws.WebSocketHub,
	proxy *streaming.StreamingProxy,
	webhookSecret string,
) http.Handler {
	mux := http.NewServeMux()

	// API endpoints — all zone-aware with fallback to "default"
	mux.HandleFunc("POST /api/tracks", handleAddTrack(eb))
	mux.HandleFunc("POST /api/skip", handleSkip(eb))
	mux.HandleFunc("POST /api/volume", handleSetVolume(eb))
	mux.HandleFunc("POST /api/clear", handleClearQueue(eb))
	mux.HandleFunc("POST /api/pause", handlePause(eb))
	mux.HandleFunc("POST /api/resume", handleResume(eb))
	mux.HandleFunc("GET /api/queue", handleGetQueue(registry))
	mux.HandleFunc("GET /api/users", handleGetUsers(registry))
	mux.HandleFunc("GET /api/zones", handleGetZones(registry))
	mux.HandleFunc("GET /api/ws", handleWebSocket(hub))
	mux.Handle("GET /metrics", telemetry.Handler())

	// Calendar Webhook endpoint (§15) — HMAC-SHA256 secured
	mux.HandleFunc("POST /api/webhooks/calendar", handleCalendarWebhook(eb, webhookSecret))

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
			ZoneID string `json:"zone_id,omitempty"`
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
				ZoneID:    ResolveZoneID(req.ZoneID),
			},
			ZoneID: ResolveZoneID(req.ZoneID),
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
			ZoneID string `json:"zone_id,omitempty"`
		}

		// Body is optional for skips
		_ = json.NewDecoder(r.Body).Decode(&req)

		cmd := models.Command{
			Type:    models.ActionSkip,
			Payload: req.UserID,
			ZoneID:  ResolveZoneID(req.ZoneID),
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
			ZoneID string `json:"zone_id,omitempty"`
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
				ZoneID: ResolveZoneID(req.ZoneID),
			},
			ZoneID: ResolveZoneID(req.ZoneID),
		}

		eb.Inject(cmd)

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusAccepted)
		_ = json.NewEncoder(w).Encode(map[string]string{"status": "accepted"})
	}
}

func handleClearQueue(eb *eventbus.EventBus) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			ZoneID string `json:"zone_id,omitempty"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)

		cmd := models.Command{
			Type:    models.ActionClearQueue,
			Payload: nil,
			ZoneID:  ResolveZoneID(req.ZoneID),
		}

		eb.Inject(cmd)

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusAccepted)
		_ = json.NewEncoder(w).Encode(map[string]string{"status": "accepted"})
	}
}

func handlePause(eb *eventbus.EventBus) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			ZoneID string `json:"zone_id,omitempty"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)

		cmd := models.Command{
			Type:    models.ActionPause,
			Payload: nil,
			ZoneID:  ResolveZoneID(req.ZoneID),
		}

		eb.Inject(cmd)

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusAccepted)
		_ = json.NewEncoder(w).Encode(map[string]string{"status": "accepted"})
	}
}

func handleResume(eb *eventbus.EventBus) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			ZoneID string `json:"zone_id,omitempty"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)

		cmd := models.Command{
			Type:    models.ActionResume,
			Payload: nil,
			ZoneID:  ResolveZoneID(req.ZoneID),
		}

		eb.Inject(cmd)

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusAccepted)
		_ = json.NewEncoder(w).Encode(map[string]string{"status": "accepted"})
	}
}

// handleGetQueue returns the queue for a specific zone (query param: zone_id).
func handleGetQueue(registry *ZoneRegistry) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		zoneID := ResolveZoneID(r.URL.Query().Get("zone_id"))
		zone, ok := registry.Get(zoneID)
		if !ok {
			http.Error(w, "zone not found", http.StatusNotFound)
			return
		}

		queue := zone.FSM.GetQueue()
		if queue == nil {
			queue = make([]models.Track, 0)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(queue)
	}
}

// handleGetUsers returns the active users and votes for a specific zone (query param: zone_id).
func handleGetUsers(registry *ZoneRegistry) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		zoneID := ResolveZoneID(r.URL.Query().Get("zone_id"))
		zone, ok := registry.Get(zoneID)
		if !ok {
			http.Error(w, "zone not found", http.StatusNotFound)
			return
		}

		users := zone.FSM.GetActiveUsers()
		votes := zone.FSM.GetVotes()
		karma := zone.FSM.GetKarma()

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"users": users,
			"votes": votes,
			"karma": karma,
		})
	}
}

// handleGetZones returns all registered zones with their current state (§20).
func handleGetZones(registry *ZoneRegistry) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		zones := registry.All()
		type zoneInfo struct {
			ID       string `json:"id"`
			SonosIP  string `json:"sonos_ip"`
			State    string `json:"state"`
			Volume   int    `json:"volume"`
			QueueLen int    `json:"queue_len"`
		}

		result := make([]zoneInfo, 0, len(zones))
		for _, z := range zones {
			state, _, vol := z.FSM.GetState()
			result = append(result, zoneInfo{
				ID:       z.ID,
				SonosIP:  z.SonosIP,
				State:    state.String(),
				Volume:   vol,
				QueueLen: len(z.FSM.GetQueue()),
			})
		}

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(result)
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

// handleCalendarWebhook implements the POST /api/webhooks/calendar endpoint (§15).
// Security: HMAC-SHA256 validation with constant-time comparison.
// Invariant: CalendarWebhook(event) = Inject(E_temporal(event), CH_in)
func handleCalendarWebhook(eb *eventbus.EventBus, secret string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// Guard: reject requests if no secret is configured
		if secret == "" {
			slog.Warn("Calendar webhook called but no secret is configured")
			http.Error(w, "webhook not configured", http.StatusServiceUnavailable)
			return
		}

		// Limit body size to mitigate DoS (§ISO 27001 — input sanitization)
		r.Body = http.MaxBytesReader(w, r.Body, maxWebhookBodySize)
		body, err := io.ReadAll(r.Body)
		if err != nil {
			slog.Warn("Calendar webhook body read failed (possibly oversized)", "error", err)
			http.Error(w, "request body too large", http.StatusRequestEntityTooLarge)
			return
		}

		// Validate HMAC-SHA256 signature (constant-time comparison)
		signature := r.Header.Get("X-Webhook-Signature")
		if signature == "" {
			slog.Warn("Calendar webhook received without signature header",
				"remote_addr", r.RemoteAddr)
			http.Error(w, "missing signature", http.StatusForbidden)
			return
		}

		expectedMAC := computeHMAC(body, []byte(secret))
		if !hmac.Equal([]byte(signature), []byte(expectedMAC)) {
			slog.Warn("Calendar webhook HMAC validation failed",
				"remote_addr", r.RemoteAddr)
			http.Error(w, "invalid signature", http.StatusForbidden)
			return
		}

		// Deserialize payload
		var payload models.CalendarWebhookPayload
		if err := json.Unmarshal(body, &payload); err != nil {
			http.Error(w, "invalid request body", http.StatusBadRequest)
			return
		}

		// Map calendar event to FSM action (§15.2)
		var actionType models.EventType
		switch payload.EventType {
		case models.CalendarMeetingStart:
			actionType = models.ActionPause
			slog.Info("Calendar event: meeting started, injecting pause",
				"title", payload.Title,
				"zone_id", payload.ZoneID,
			)
		case models.CalendarMeetingEnd:
			actionType = models.ActionResume
			slog.Info("Calendar event: meeting ended, injecting resume",
				"title", payload.Title,
				"zone_id", payload.ZoneID,
			)
		default:
			http.Error(w, "unsupported event_type", http.StatusBadRequest)
			return
		}

		// Inject into EventBus — processed by existing Pause/Resume workers
		cmd := models.Command{
			Type:    actionType,
			Payload: nil,
			ZoneID:  ResolveZoneID(payload.ZoneID),
		}
		eb.Inject(cmd)

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusAccepted)
		_ = json.NewEncoder(w).Encode(map[string]string{"status": "accepted"})
	}
}

// computeHMAC generates a hex-encoded HMAC-SHA256 digest.
func computeHMAC(message, key []byte) string {
	mac := hmac.New(sha256.New, key)
	mac.Write(message)
	return hex.EncodeToString(mac.Sum(nil))
}
