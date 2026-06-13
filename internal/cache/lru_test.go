package cache

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/jesuslangarica/sonosApp/internal/models"
)

// TestExtractExpiry validates URL query parameter extraction.
func TestExtractExpiry(t *testing.T) {
	// 1. Valid expire parameter
	nowSec := time.Now().Unix()
	urlWithExpire := fmt.Sprintf("https://googlevideo.com/playback?expire=%d&quality=high", nowSec+100)
	expTime := ExtractExpiry(urlWithExpire)
	if expTime.Unix() != nowSec+100 {
		t.Errorf("expected expiration Unix %d, got %d", nowSec+100, expTime.Unix())
	}

	// 2. Missing expire parameter
	urlNoExpire := "https://googlevideo.com/playback?quality=high"
	expTimeNo := ExtractExpiry(urlNoExpire)
	diff := time.Until(expTimeNo)
	if diff < 4*time.Minute || diff > 6*time.Minute {
		t.Errorf("expected default 5min expiration, got diff: %s", diff)
	}

	// 3. Malformed expire parameter
	urlBadExpire := "https://googlevideo.com/playback?expire=not-a-number"
	expTimeBad := ExtractExpiry(urlBadExpire)
	diffBad := time.Until(expTimeBad)
	if diffBad < 4*time.Minute || diffBad > 6*time.Minute {
		t.Errorf("expected default 5min expiration for bad input, got diff: %s", diffBad)
	}
}

// TestLRUBasicEviction verifies basic map/list eviction mechanics.
func TestLRUBasicEviction(t *testing.T) {
	c := NewLRUCache(3)

	t1 := models.Track{ID: "track1", URL: "https://youtube.com/watch?v=1"}
	t2 := models.Track{ID: "track2", URL: "https://youtube.com/watch?v=2"}
	t3 := models.Track{ID: "track3", URL: "https://youtube.com/watch?v=3"}
	t4 := models.Track{ID: "track4", URL: "https://youtube.com/watch?v=4"}

	// We must mock httpClient to avoid contacting real URLs and return success.
	// For these basic tests, we'll configure tracks to point to a local test server.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	t1.URL = server.URL + "/t1"
	t2.URL = server.URL + "/t2"
	t3.URL = server.URL + "/t3"
	t4.URL = server.URL + "/t4"

	c.Add("t1", t1)
	c.Add("t2", t2)
	c.Add("t3", t3)

	if c.Size() != 3 {
		t.Errorf("expected size 3, got %d", c.Size())
	}

	// Touch t1 to make it most recently used. Order will be: t1, t3, t2
	_, found := c.Get("t1")
	if !found {
		t.Fatal("t1 should be found")
	}

	// Insert t4. Capacity is 3, so t2 (least recently used) must be evicted.
	c.Add("t4", t4)

	if c.Size() != 3 {
		t.Errorf("expected size 3, got %d", c.Size())
	}

	if _, exists := c.Get("t2"); exists {
		t.Error("t2 should have been evicted")
	}

	// t1, t3, and t4 should still exist
	for _, key := range []string{"t1", "t3", "t4"} {
		if _, exists := c.Get(key); !exists {
			t.Errorf("expected key %s to exist", key)
		}
	}
}

// TestLRUTTLExpiration checks that expired values are dropped instantly.
func TestLRUTTLExpiration(t *testing.T) {
	c := NewLRUCache(5)

	// Past expiration URL
	urlPast := "https://youtube.com/watch?v=old&expire=1000000000"
	track := models.Track{ID: "oldTrack", URL: urlPast}

	c.Add("old", track)

	// Get should fail and evict the item
	_, exists := c.Get("old")
	if exists {
		t.Error("expected old entry to be treated as missing/expired")
	}

	if c.Size() != 0 {
		t.Errorf("expected size 0, got %d", c.Size())
	}
}

// TestLRUPassiveHEADValidation checks validation of live URLs.
func TestLRUPassiveHEADValidation(t *testing.T) {
	c := NewLRUCache(5)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "HEAD" {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		if r.URL.Path == "/good" {
			w.WriteHeader(http.StatusOK)
		} else {
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	goodTrack := models.Track{ID: "good", URL: server.URL + "/good"}
	badTrack := models.Track{ID: "bad", URL: server.URL + "/bad"}

	c.Add("g", goodTrack)
	c.Add("b", badTrack)

	// Good track HEAD returns 200, should be found
	_, foundG := c.Get("g")
	if !foundG {
		t.Error("expected good track to be found and validated")
	}

	// Bad track HEAD returns 404, should NOT be found and must be evicted
	_, foundB := c.Get("b")
	if foundB {
		t.Error("expected bad track to fail validation and be skipped")
	}

	if c.Size() != 1 { // Only good track remains
		t.Errorf("expected cache size 1, got %d", c.Size())
	}
}

// TestLRUConcurrency verifies no races under concurrent access.
func TestLRUConcurrency(t *testing.T) {
	c := NewLRUCache(10)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	var wg sync.WaitGroup
	workers := 20
	iterations := 50

	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(workerID int) {
			defer wg.Done()
			for j := 0; j < iterations; j++ {
				key := fmt.Sprintf("key-%d", j%5) // high collision
				track := models.Track{
					ID:  key,
					URL: fmt.Sprintf("%s/track-%d", server.URL, j),
				}
				c.Add(key, track)
				c.Get(key)
			}
		}(i)
	}

	wg.Wait()
}
