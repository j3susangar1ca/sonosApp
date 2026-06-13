package streaming

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestStreamingProxy(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "streaming-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	// Create base directory for music
	baseDir := filepath.Join(tempDir, "music")
	if err := os.MkdirAll(baseDir, 0755); err != nil {
		t.Fatalf("failed to create music dir: %v", err)
	}

	// Create a dummy song file
	songData := []byte("audio-content-payload-for-sonos")
	songFile := filepath.Join(baseDir, "song.mp3")
	if err := os.WriteFile(songFile, songData, 0644); err != nil {
		t.Fatalf("failed to write dummy song: %v", err)
	}

	// Create a file outside baseDir to test path traversal
	secretFile := filepath.Join(tempDir, "secret.txt")
	if err := os.WriteFile(secretFile, []byte("classified"), 0644); err != nil {
		t.Fatalf("failed to write secret file: %v", err)
	}

	serverURL := "http://localhost:8080"
	proxy, err := NewStreamingProxy(baseDir, serverURL)
	if err != nil {
		t.Fatalf("failed to create StreamingProxy: %v", err)
	}

	t.Run("ValidRequest", func(t *testing.T) {
		signedURLStr, err := proxy.GenerateURL("song.mp3", 5*time.Second)
		if err != nil {
			t.Fatalf("failed to generate URL: %v", err)
		}

		u, err := url.Parse(signedURLStr)
		if err != nil {
			t.Fatalf("invalid generated URL: %v", err)
		}

		req := httptest.NewRequest("GET", u.String(), nil)
		w := httptest.NewRecorder()

		proxy.ServeHTTP(w, req)

		res := w.Result()
		defer res.Body.Close()

		if res.StatusCode != http.StatusOK {
			t.Errorf("expected status 200, got %d", res.StatusCode)
		}

		body, err := io.ReadAll(res.Body)
		if err != nil {
			t.Fatalf("failed to read body: %v", err)
		}

		if string(body) != string(songData) {
			t.Errorf("expected body '%s', got '%s'", string(songData), string(body))
		}
	})

	t.Run("ExpiredToken", func(t *testing.T) {
		signedURLStr, err := proxy.GenerateURL("song.mp3", -1*time.Second) // already expired
		if err != nil {
			t.Fatalf("failed to generate URL: %v", err)
		}

		u, err := url.Parse(signedURLStr)
		if err != nil {
			t.Fatalf("invalid generated URL: %v", err)
		}

		req := httptest.NewRequest("GET", u.String(), nil)
		w := httptest.NewRecorder()

		proxy.ServeHTTP(w, req)

		res := w.Result()
		defer res.Body.Close()

		if res.StatusCode != http.StatusForbidden {
			t.Errorf("expected status 403 Forbidden for expired token, got %d", res.StatusCode)
		}
	})

	t.Run("InvalidSignatureToken", func(t *testing.T) {
		signedURLStr, err := proxy.GenerateURL("song.mp3", 5*time.Second)
		if err != nil {
			t.Fatalf("failed to generate URL: %v", err)
		}

		u, err := url.Parse(signedURLStr)
		if err != nil {
			t.Fatalf("invalid generated URL: %v", err)
		}

		// Mutate token slightly
		q := u.Query()
		q.Set("token", "invalidtoken123")
		u.RawQuery = q.Encode()

		req := httptest.NewRequest("GET", u.String(), nil)
		w := httptest.NewRecorder()

		proxy.ServeHTTP(w, req)

		res := w.Result()
		defer res.Body.Close()

		if res.StatusCode != http.StatusForbidden {
			t.Errorf("expected status 403 Forbidden for invalid signature, got %d", res.StatusCode)
		}
	})

	t.Run("PathTraversalBlocked", func(t *testing.T) {
		// Attempt to request ../secret.txt relative to baseDir
		// We generate a valid signed URL for "../secret.txt", but the proxy must reject it on verification
		signedURLStr, err := proxy.GenerateURL("../secret.txt", 5*time.Second)
		if err != nil {
			t.Fatalf("failed to generate URL: %v", err)
		}

		u, err := url.Parse(signedURLStr)
		if err != nil {
			t.Fatalf("invalid generated URL: %v", err)
		}

		req := httptest.NewRequest("GET", u.String(), nil)
		w := httptest.NewRecorder()

		proxy.ServeHTTP(w, req)

		res := w.Result()
		defer res.Body.Close()

		if res.StatusCode != http.StatusForbidden {
			t.Errorf("expected status 403 Forbidden for path traversal, got %d", res.StatusCode)
		}
	})

	t.Run("PathTraversalSimilarNamePrefixBlocked", func(t *testing.T) {
		// Attempt to request a folder named "music_alternative" which resides in same parent dir
		// baseDir is /tmp/music, target is /tmp/music_alternative
		parentDir := filepath.Dir(baseDir)
		altDir := filepath.Join(parentDir, "music_alternative")
		if err := os.MkdirAll(altDir, 0755); err != nil {
			t.Fatalf("failed to create alt dir: %v", err)
		}
		defer os.RemoveAll(altDir)

		altFile := filepath.Join(altDir, "hack.mp3")
		if err := os.WriteFile(altFile, []byte("hack"), 0644); err != nil {
			t.Fatalf("failed to write alt file: %v", err)
		}

		// Request file via relative path leading to alternative folder
		signedURLStr, err := proxy.GenerateURL("../music_alternative/hack.mp3", 5*time.Second)
		if err != nil {
			t.Fatalf("failed to generate URL: %v", err)
		}

		u, err := url.Parse(signedURLStr)
		if err != nil {
			t.Fatalf("invalid generated URL: %v", err)
		}

		req := httptest.NewRequest("GET", u.String(), nil)
		w := httptest.NewRecorder()

		proxy.ServeHTTP(w, req)

		res := w.Result()
		defer res.Body.Close()

		if res.StatusCode != http.StatusForbidden {
			t.Errorf("expected status 403 Forbidden for similar folder prefix exploit, got %d", res.StatusCode)
		}
	})

	t.Run("RangeRequestSupported", func(t *testing.T) {
		signedURLStr, err := proxy.GenerateURL("song.mp3", 5*time.Second)
		if err != nil {
			t.Fatalf("failed to generate URL: %v", err)
		}

		u, err := url.Parse(signedURLStr)
		if err != nil {
			t.Fatalf("invalid generated URL: %v", err)
		}

		req := httptest.NewRequest("GET", u.String(), nil)
		// Request range from byte 6 to 12
		req.Header.Set("Range", "bytes=6-12")
		w := httptest.NewRecorder()

		proxy.ServeHTTP(w, req)

		res := w.Result()
		defer res.Body.Close()

		if res.StatusCode != http.StatusPartialContent {
			t.Errorf("expected status 206 Partial Content, got %d", res.StatusCode)
		}

		body, err := io.ReadAll(res.Body)
		if err != nil {
			t.Fatalf("failed to read partial body: %v", err)
		}

		// "audio-content-payload-for-sonos"
		// indices 6 to 12 inclusive: "content"
		expectedPartial := "content"
		if string(body) != expectedPartial {
			t.Errorf("expected partial content '%s', got '%s'", expectedPartial, string(body))
		}
	})
}
