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
	"github.com/jesuslangarica/sonosApp/internal/ws"
)

func TestRouterEndpoints(t *testing.T) {
	// Initialize subsystems
	fsm := player.NewJukeboxFSM(15, nil)
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
}
