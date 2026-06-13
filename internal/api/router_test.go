package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/jesuslangarica/sonosApp/internal/eventbus"
	"github.com/jesuslangarica/sonosApp/internal/models"
	"github.com/jesuslangarica/sonosApp/internal/player"
	"github.com/jesuslangarica/sonosApp/internal/streaming"
	"github.com/jesuslangarica/sonosApp/internal/telemetry"
	"github.com/jesuslangarica/sonosApp/internal/ws"
)

func TestRouterEndpoints(t *testing.T) {
	// Initialize subsystems
	fsm := player.NewJukeboxFSM(15, nil)
	telemetry.QueueSizeFunc = func() float64 {
		return float64(len(fsm.GetQueue()))
	}
	telemetry.ActiveUsersFunc = func() float64 {
		return float64(len(fsm.GetActiveUsers()))
	}
	telemetry.RegisterGaugeFuncs()
	eb := eventbus.NewEventBus(10, 50*time.Millisecond)
	defer eb.Stop()

	hub := ws.NewWebSocketHub(fsm)
	hub.Start()
	defer hub.Stop()

	tempDir := t.TempDir()
	proxy, err := streaming.NewStreamingProxy(tempDir, "http://localhost:8080")
	if err != nil {
		t.Fatalf("failed to create proxy: %v", err)
	}

	router := NewRouter(fsm, eb, hub, proxy)

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
