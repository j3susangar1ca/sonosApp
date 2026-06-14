package api

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/jesuslangarica/sonosApp/internal/eventbus"
	"github.com/jesuslangarica/sonosApp/internal/models"
	"github.com/jesuslangarica/sonosApp/internal/persist"
	"github.com/jesuslangarica/sonosApp/internal/player"
	"github.com/jesuslangarica/sonosApp/internal/streaming"
	"github.com/jesuslangarica/sonosApp/internal/telemetry"
	"github.com/jesuslangarica/sonosApp/internal/ws"
)

// setupTestRouter creates a standard test fixture with a default zone and optional extra zones.
func setupTestRouter(t *testing.T, webhookSecret string) (*ZoneRegistry, *eventbus.EventBus, *ws.WebSocketHub, http.Handler) {
	t.Helper()

	fsm := player.NewJukeboxFSM(15, nil)
	telemetry.QueueSizeFunc = func() float64 {
		return float64(fsm.QueueSize())
	}
	telemetry.ActiveUsersFunc = func() float64 {
		return float64(len(fsm.GetActiveUsers()))
	}
	telemetry.RegisterGaugeFuncs()

	eb := eventbus.NewEventBus(10, 50*time.Millisecond)
	t.Cleanup(eb.Stop)

	hub := ws.NewWebSocketHub(fsm)
	hub.Start()
	t.Cleanup(hub.Stop)

	tempDir := t.TempDir()
	proxy, err := streaming.NewStreamingProxy(tempDir, "http://localhost:8080")
	if err != nil {
		t.Fatalf("failed to create proxy: %v", err)
	}

	registry := NewZoneRegistry()
	registry.Register(&Zone{
		ID:        "default",
		FSM:       fsm,
		Persister: persist.NewPersister(tempDir+"/state.log", tempDir+"/state.json"),
	})

	router := NewRouter(registry, eb, hub, proxy, webhookSecret)
	return registry, eb, hub, router
}

func TestRouterEndpoints(t *testing.T) {
	const testWebhookSecret = "test-secret-key-for-hmac"
	registry, eb, _, router := setupTestRouter(t, testWebhookSecret)
	defaultZone, _ := registry.GetDefault()
	fsm := defaultZone.FSM

	t.Run("AddTrackValid", func(t *testing.T) {
		// Subscribe to eb to verify injection
		ch := make(chan models.Envelope, 1)
		eb.Subscribe(models.ActionAddTrack, "test-sub", ch)
		defer eb.Unsubscribe(models.ActionAddTrack, "test-sub")

		payload := map[string]string{
			"url":     "https://youtube.com/watch?v=123",
			"user_id": "alice",
		}
		body, _ := json.Marshal(payload)

		req := httptest.NewRequest("POST", "/api/tracks", bytes.NewBuffer(body))
		w := httptest.NewRecorder()

		router.ServeHTTP(w, req)

		if w.Code != http.StatusAccepted {
			t.Errorf("expected status 202, got %d", w.Code)
		}

		// Verify event was received in eventbus
		select {
		case env := <-ch:
			eb.Ack(env.ID, "test-sub")
			p, ok := env.Payload.(models.AddTrackPayload)
			if !ok {
				t.Fatalf("expected AddTrackPayload, got %T", env.Payload)
			}
			if p.URL != "https://youtube.com/watch?v=123" || p.UserID != "alice" {
				t.Errorf("incorrect payload values: %+v", p)
			}
			if env.ZoneID != "default" {
				t.Errorf("expected zone_id 'default', got %s", env.ZoneID)
			}
		case <-time.After(100 * time.Millisecond):
			t.Error("timed out waiting for eventbus injection")
		}
	})

	t.Run("AddTrackInvalid", func(t *testing.T) {
		payload := map[string]string{
			"url": "",
		}
		body, _ := json.Marshal(payload)

		req := httptest.NewRequest("POST", "/api/tracks", bytes.NewBuffer(body))
		w := httptest.NewRecorder()

		router.ServeHTTP(w, req)

		if w.Code != http.StatusBadRequest {
			t.Errorf("expected status 400, got %d", w.Code)
		}
	})

	t.Run("Skip", func(t *testing.T) {
		ch := make(chan models.Envelope, 1)
		eb.Subscribe(models.ActionSkip, "test-sub", ch)
		defer eb.Unsubscribe(models.ActionSkip, "test-sub")

		payload := map[string]string{
			"user_id": "alice",
		}
		body, _ := json.Marshal(payload)

		req := httptest.NewRequest("POST", "/api/skip", bytes.NewBuffer(body))
		w := httptest.NewRecorder()

		router.ServeHTTP(w, req)

		if w.Code != http.StatusAccepted {
			t.Errorf("expected status 202, got %d", w.Code)
		}

		select {
		case env := <-ch:
			eb.Ack(env.ID, "test-sub")
			userID, ok := env.Payload.(string)
			if !ok {
				t.Fatalf("expected string user_id, got %T", env.Payload)
			}
			if userID != "alice" {
				t.Errorf("expected user_id alice, got %s", userID)
			}
		case <-time.After(100 * time.Millisecond):
			t.Error("timed out waiting for skip injection")
		}
	})

	t.Run("SetVolumeValid", func(t *testing.T) {
		ch := make(chan models.Envelope, 1)
		eb.Subscribe(models.ActionSetVolume, "test-sub", ch)
		defer eb.Unsubscribe(models.ActionSetVolume, "test-sub")

		payload := map[string]interface{}{
			"level":   50,
			"user_id": "alice",
		}
		body, _ := json.Marshal(payload)

		req := httptest.NewRequest("POST", "/api/volume", bytes.NewBuffer(body))
		w := httptest.NewRecorder()

		router.ServeHTTP(w, req)

		if w.Code != http.StatusAccepted {
			t.Errorf("expected status 202, got %d", w.Code)
		}

		select {
		case env := <-ch:
			eb.Ack(env.ID, "test-sub")
			p, ok := env.Payload.(models.SetVolumePayload)
			if !ok {
				t.Fatalf("expected SetVolumePayload, got %T", env.Payload)
			}
			if p.Level != 50 || p.UserID != "alice" {
				t.Errorf("incorrect payload values: %+v", p)
			}
		case <-time.After(100 * time.Millisecond):
			t.Error("timed out waiting for volume injection")
		}
	})

	t.Run("SetVolumeInvalid", func(t *testing.T) {
		payload := map[string]interface{}{
			"level": 150,
		}
		body, _ := json.Marshal(payload)

		req := httptest.NewRequest("POST", "/api/volume", bytes.NewBuffer(body))
		w := httptest.NewRecorder()

		router.ServeHTTP(w, req)

		if w.Code != http.StatusBadRequest {
			t.Errorf("expected status 400, got %d", w.Code)
		}
	})

	t.Run("ClearQueue", func(t *testing.T) {
		ch := make(chan models.Envelope, 1)
		eb.Subscribe(models.ActionClearQueue, "test-sub", ch)
		defer eb.Unsubscribe(models.ActionClearQueue, "test-sub")

		req := httptest.NewRequest("POST", "/api/clear", nil)
		w := httptest.NewRecorder()

		router.ServeHTTP(w, req)

		if w.Code != http.StatusAccepted {
			t.Errorf("expected status 202, got %d", w.Code)
		}

		select {
		case env := <-ch:
			eb.Ack(env.ID, "test-sub")
		case <-time.After(100 * time.Millisecond):
			t.Error("timed out waiting for clear queue injection")
		}
	})

	t.Run("Pause", func(t *testing.T) {
		ch := make(chan models.Envelope, 1)
		eb.Subscribe(models.ActionPause, "test-sub", ch)
		defer eb.Unsubscribe(models.ActionPause, "test-sub")

		req := httptest.NewRequest("POST", "/api/pause", nil)
		w := httptest.NewRecorder()

		router.ServeHTTP(w, req)

		if w.Code != http.StatusAccepted {
			t.Errorf("expected status 202, got %d", w.Code)
		}

		select {
		case env := <-ch:
			eb.Ack(env.ID, "test-sub")
		case <-time.After(100 * time.Millisecond):
			t.Error("timed out waiting for pause injection")
		}
	})

	t.Run("Resume", func(t *testing.T) {
		ch := make(chan models.Envelope, 1)
		eb.Subscribe(models.ActionResume, "test-sub", ch)
		defer eb.Unsubscribe(models.ActionResume, "test-sub")

		req := httptest.NewRequest("POST", "/api/resume", nil)
		w := httptest.NewRecorder()

		router.ServeHTTP(w, req)

		if w.Code != http.StatusAccepted {
			t.Errorf("expected status 202, got %d", w.Code)
		}

		select {
		case env := <-ch:
			eb.Ack(env.ID, "test-sub")
		case <-time.After(100 * time.Millisecond):
			t.Error("timed out waiting for resume injection")
		}
	})

	t.Run("GetQueue", func(t *testing.T) {
		// Clear queue to start clean
		fsm.ProcessEvent(player.EventClear, nil)

		track1 := models.Track{ID: "track1"}
		track2 := models.Track{ID: "track2"}
		fsm.ProcessEvent(player.EventAdd, track1)
		fsm.ProcessEvent(player.EventAdd, track2)

		// Wait briefly for the asynchronous mock player transition to finish
		time.Sleep(50 * time.Millisecond)

		req := httptest.NewRequest("GET", "/api/queue", nil)
		w := httptest.NewRecorder()

		router.ServeHTTP(w, req)

		if w.Code != http.StatusOK {
			t.Errorf("expected status 200, got %d", w.Code)
		}

		var queue []models.Track
		if err := json.Unmarshal(w.Body.Bytes(), &queue); err != nil {
			t.Fatalf("failed to unmarshal queue: %v", err)
		}

		// track1 should be dequeued playing, track2 should still be in queue
		if len(queue) != 1 {
			t.Fatalf("expected queue length of 1, got %d", len(queue))
		}
		if queue[0].ID != "track2" {
			t.Errorf("expected track ID track2, got %s", queue[0].ID)
		}
	})

	t.Run("GetQueue_ZoneNotFound", func(t *testing.T) {
		req := httptest.NewRequest("GET", "/api/queue?zone_id=nonexistent", nil)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)
		if w.Code != http.StatusNotFound {
			t.Errorf("expected status 404 for unknown zone, got %d", w.Code)
		}
	})

	t.Run("GetUsers", func(t *testing.T) {
		fsm.AddUser("alice")
		fsm.AddUser("charlie")
		fsm.VoteSkip("charlie")

		req := httptest.NewRequest("GET", "/api/users", nil)
		w := httptest.NewRecorder()

		router.ServeHTTP(w, req)

		if w.Code != http.StatusOK {
			t.Errorf("expected status 200, got %d", w.Code)
		}

		var res struct {
			Users map[string]bool    `json:"users"`
			Votes map[string]bool    `json:"votes"`
			Karma map[string]float64 `json:"karma"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &res); err != nil {
			t.Fatalf("failed to unmarshal users response: %v", err)
		}

		if !res.Users["charlie"] || !res.Users["alice"] {
			t.Error("expected users charlie and alice to be active")
		}
		if !res.Votes["charlie"] {
			t.Error("expected user charlie to have voted")
		}
		if res.Votes["alice"] {
			t.Error("expected user alice to not have voted")
		}
	})

	t.Run("GetZones", func(t *testing.T) {
		req := httptest.NewRequest("GET", "/api/zones", nil)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)

		if w.Code != http.StatusOK {
			t.Errorf("expected status 200, got %d", w.Code)
		}

		var zones []struct {
			ID       string `json:"id"`
			State    string `json:"state"`
			Volume   int    `json:"volume"`
			QueueLen int    `json:"queue_len"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &zones); err != nil {
			t.Fatalf("failed to unmarshal zones response: %v", err)
		}
		if len(zones) != 1 {
			t.Fatalf("expected 1 zone, got %d", len(zones))
		}
		if zones[0].ID != "default" {
			t.Errorf("expected zone id 'default', got %s", zones[0].ID)
		}
	})

	t.Run("GetMetrics", func(t *testing.T) {
		req := httptest.NewRequest("GET", "/metrics", nil)
		w := httptest.NewRecorder()

		router.ServeHTTP(w, req)

		if w.Code != http.StatusOK {
			t.Errorf("expected status 200, got %d", w.Code)
		}

		expectedMetrics := []string{
			"jukebox_queue_size",
			"jukebox_active_users",
			"jukebox_play_count_total",
			"jukebox_skip_count_total",
			"jukebox_cb_f_total",
			"jukebox_youtube_resolution_latency_seconds",
			"jukebox_sonos_soap_latency_seconds",
		}

		for _, m := range expectedMetrics {
			if !bytes.Contains(w.Body.Bytes(), []byte(m)) {
				t.Errorf("expected metric %s to be present in metrics output", m)
			}
		}
	})
}

func testHMAC(body []byte, secret string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	return hex.EncodeToString(mac.Sum(nil))
}

func TestCalendarWebhook(t *testing.T) {
	const testSecret = "calendar-test-secret"
	_, eb, _, router := setupTestRouter(t, testSecret)

	t.Run("ValidHMAC_MeetingStart", func(t *testing.T) {
		ch := make(chan models.Envelope, 1)
		eb.Subscribe(models.ActionPause, "test-cal-pause", ch)
		defer eb.Unsubscribe(models.ActionPause, "test-cal-pause")

		payload := map[string]interface{}{
			"event_type": "meeting_start",
			"title":      "Daily Standup",
		}
		body, _ := json.Marshal(payload)
		sig := testHMAC(body, testSecret)

		req := httptest.NewRequest("POST", "/api/webhooks/calendar", bytes.NewBuffer(body))
		req.Header.Set("X-Webhook-Signature", sig)
		w := httptest.NewRecorder()

		router.ServeHTTP(w, req)

		if w.Code != http.StatusAccepted {
			t.Errorf("expected status 202, got %d: %s", w.Code, w.Body.String())
		}

		select {
		case env := <-ch:
			eb.Ack(env.ID, "test-cal-pause")
			if env.Type != models.ActionPause {
				t.Errorf("expected ActionPause, got %s", env.Type)
			}
		case <-time.After(200 * time.Millisecond):
			t.Error("timed out waiting for ActionPause injection")
		}
	})

	t.Run("ValidHMAC_MeetingEnd", func(t *testing.T) {
		ch := make(chan models.Envelope, 1)
		eb.Subscribe(models.ActionResume, "test-cal-resume", ch)
		defer eb.Unsubscribe(models.ActionResume, "test-cal-resume")

		payload := map[string]interface{}{
			"event_type": "meeting_end",
			"title":      "Daily Standup",
		}
		body, _ := json.Marshal(payload)
		sig := testHMAC(body, testSecret)

		req := httptest.NewRequest("POST", "/api/webhooks/calendar", bytes.NewBuffer(body))
		req.Header.Set("X-Webhook-Signature", sig)
		w := httptest.NewRecorder()

		router.ServeHTTP(w, req)

		if w.Code != http.StatusAccepted {
			t.Errorf("expected status 202, got %d: %s", w.Code, w.Body.String())
		}

		select {
		case env := <-ch:
			eb.Ack(env.ID, "test-cal-resume")
			if env.Type != models.ActionResume {
				t.Errorf("expected ActionResume, got %s", env.Type)
			}
		case <-time.After(200 * time.Millisecond):
			t.Error("timed out waiting for ActionResume injection")
		}
	})

	t.Run("InvalidHMAC", func(t *testing.T) {
		payload := map[string]interface{}{
			"event_type": "meeting_start",
		}
		body, _ := json.Marshal(payload)

		req := httptest.NewRequest("POST", "/api/webhooks/calendar", bytes.NewBuffer(body))
		req.Header.Set("X-Webhook-Signature", "invalid-signature")
		w := httptest.NewRecorder()

		router.ServeHTTP(w, req)

		if w.Code != http.StatusForbidden {
			t.Errorf("expected status 403, got %d", w.Code)
		}
	})

	t.Run("MissingSignature", func(t *testing.T) {
		payload := map[string]interface{}{
			"event_type": "meeting_start",
		}
		body, _ := json.Marshal(payload)

		req := httptest.NewRequest("POST", "/api/webhooks/calendar", bytes.NewBuffer(body))
		// No X-Webhook-Signature header
		w := httptest.NewRecorder()

		router.ServeHTTP(w, req)

		if w.Code != http.StatusForbidden {
			t.Errorf("expected status 403, got %d", w.Code)
		}
	})

	t.Run("OversizedBody", func(t *testing.T) {
		// Body larger than maxWebhookBodySize (4096 bytes)
		bigBody := []byte(strings.Repeat("x", 5000))
		sig := testHMAC(bigBody, testSecret)

		req := httptest.NewRequest("POST", "/api/webhooks/calendar", bytes.NewBuffer(bigBody))
		req.Header.Set("X-Webhook-Signature", sig)
		w := httptest.NewRecorder()

		router.ServeHTTP(w, req)

		if w.Code != http.StatusRequestEntityTooLarge {
			t.Errorf("expected status 413, got %d", w.Code)
		}
	})
}

func TestMultiZone(t *testing.T) {
	const testSecret = "mz-secret"

	// Create two zones: "office" and "kitchen"
	fsmOffice := player.NewJukeboxFSM(15, nil)
	fsmKitchen := player.NewJukeboxFSM(20, nil)

	eb := eventbus.NewEventBus(10, 50*time.Millisecond)
	t.Cleanup(eb.Stop)

	hub := ws.NewWebSocketHub(fsmOffice)
	hub.Start()
	t.Cleanup(hub.Stop)

	tempDir := t.TempDir()
	proxy, err := streaming.NewStreamingProxy(tempDir, "http://localhost:8080")
	if err != nil {
		t.Fatalf("failed to create proxy: %v", err)
	}

	registry := NewZoneRegistry()
	registry.Register(&Zone{
		ID:        "default",
		FSM:       fsmOffice,
		SonosIP:   "192.168.1.10",
		Persister: persist.NewPersister(tempDir+"/state_office.log", tempDir+"/state_office.json"),
	})
	registry.Register(&Zone{
		ID:        "kitchen",
		FSM:       fsmKitchen,
		SonosIP:   "192.168.1.11",
		Persister: persist.NewPersister(tempDir+"/state_kitchen.log", tempDir+"/state_kitchen.json"),
	})

	router := NewRouter(registry, eb, hub, proxy, testSecret)

	t.Run("GetZones_MultiZone", func(t *testing.T) {
		req := httptest.NewRequest("GET", "/api/zones", nil)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)

		if w.Code != http.StatusOK {
			t.Errorf("expected status 200, got %d", w.Code)
		}

		var zones []struct {
			ID      string `json:"id"`
			SonosIP string `json:"sonos_ip"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &zones); err != nil {
			t.Fatalf("failed to unmarshal zones response: %v", err)
		}
		if len(zones) != 2 {
			t.Fatalf("expected 2 zones, got %d", len(zones))
		}
	})

	t.Run("GetQueue_PerZone", func(t *testing.T) {
		// Add tracks to office zone
		fsmOffice.ProcessEvent(player.EventClear, nil)
		fsmOffice.ProcessEvent(player.EventAdd, models.Track{ID: "office-track"})

		// Add tracks to kitchen zone
		fsmKitchen.ProcessEvent(player.EventClear, nil)
		fsmKitchen.ProcessEvent(player.EventAdd, models.Track{ID: "kitchen-track"})

		time.Sleep(50 * time.Millisecond)

		// Query office (default) zone queue
		req := httptest.NewRequest("GET", "/api/queue?zone_id=default", nil)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)

		if w.Code != http.StatusOK {
			t.Errorf("expected status 200 for office queue, got %d", w.Code)
		}

		// Query kitchen zone queue
		req = httptest.NewRequest("GET", "/api/queue?zone_id=kitchen", nil)
		w = httptest.NewRecorder()
		router.ServeHTTP(w, req)

		if w.Code != http.StatusOK {
			t.Errorf("expected status 200 for kitchen queue, got %d", w.Code)
		}
	})

	t.Run("AddTrack_WithZoneID", func(t *testing.T) {
		ch := make(chan models.Envelope, 1)
		eb.Subscribe(models.ActionAddTrack, "test-mz-add", ch)
		defer eb.Unsubscribe(models.ActionAddTrack, "test-mz-add")

		payload := map[string]string{
			"url":     "https://youtube.com/watch?v=456",
			"user_id": "bob",
			"zone_id": "kitchen",
		}
		body, _ := json.Marshal(payload)

		req := httptest.NewRequest("POST", "/api/tracks", bytes.NewBuffer(body))
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)

		if w.Code != http.StatusAccepted {
			t.Errorf("expected status 202, got %d", w.Code)
		}

		select {
		case env := <-ch:
			eb.Ack(env.ID, "test-mz-add")
			if env.ZoneID != "kitchen" {
				t.Errorf("expected zone_id 'kitchen', got '%s'", env.ZoneID)
			}
			p, ok := env.Payload.(models.AddTrackPayload)
			if !ok {
				t.Fatalf("expected AddTrackPayload, got %T", env.Payload)
			}
			if p.ZoneID != "kitchen" {
				t.Errorf("expected payload zone_id 'kitchen', got '%s'", p.ZoneID)
			}
		case <-time.After(200 * time.Millisecond):
			t.Error("timed out waiting for multi-zone add track injection")
		}
	})

	t.Run("ZoneIndependence_Volume", func(t *testing.T) {
		// Office volume should be 15 (initial), kitchen should be 20 (initial)
		_, _, officeVol := fsmOffice.GetState()
		_, _, kitchenVol := fsmKitchen.GetState()

		if officeVol != 15 {
			t.Errorf("expected office volume 15, got %d", officeVol)
		}
		if kitchenVol != 20 {
			t.Errorf("expected kitchen volume 20, got %d", kitchenVol)
		}

		// Change kitchen volume directly
		fsmKitchen.ProcessEvent(player.EventSetVolume, 50)
		_, _, kitchenVol = fsmKitchen.GetState()
		_, _, officeVol = fsmOffice.GetState()

		if kitchenVol != 50 {
			t.Errorf("expected kitchen volume 50 after change, got %d", kitchenVol)
		}
		if officeVol != 15 {
			t.Errorf("expected office volume to remain 15, got %d (zone isolation violated)", officeVol)
		}
	})
}
