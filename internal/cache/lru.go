package cache

import (
	"container/list"
	"context"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"sync"
	"time"

	"github.com/jesuslangarica/sonosApp/internal/models"
)

// cacheEntry wraps the value and its expiration time inside the LRU list.
type cacheEntry struct {
	key        string
	value      models.Track
	expiration time.Time
}

// LRUCache implements a thread-safe LRU cache with TTL and HTTP HEAD passive validation.
type LRUCache struct {
	capacity   int
	list       *list.List
	items      map[string]*list.Element
	mu         sync.Mutex
	httpClient *http.Client
}

// NewLRUCache instantiates an LRUCache.
func NewLRUCache(capacity int) *LRUCache {
	if capacity <= 0 {
		capacity = 32
	}
	return &LRUCache{
		capacity:   capacity,
		list:       list.New(),
		items:      make(map[string]*list.Element),
		httpClient: &http.Client{Timeout: 2 * time.Second},
	}
}

// ExtractExpiry parses the streaming URL and returns its 'expire' Unix timestamp.
// If the param is missing or malformed, defaults to now + 5 minutes.
func ExtractExpiry(streamURL string) time.Time {
	u, err := url.Parse(streamURL)
	if err != nil {
		return time.Now().Add(5 * time.Minute)
	}

	expireStr := u.Query().Get("expire")
	if expireStr == "" {
		return time.Now().Add(5 * time.Minute)
	}

	sec, err := strconv.ParseInt(expireStr, 10, 64)
	if err != nil {
		return time.Now().Add(5 * time.Minute)
	}

	return time.Unix(sec, 0)
}

// Get retrieves a track from cache.
// If expired, the entry is evicted immediately.
// HTTP HEAD validation is performed asynchronously in background.
func (c *LRUCache) Get(key string) (models.Track, bool) {
	c.mu.Lock()
	elem, exists := c.items[key]
	if !exists {
		c.mu.Unlock()
		return models.Track{}, false
	}

	entry := elem.Value.(*cacheEntry)
	if time.Now().After(entry.expiration) {
		c.list.Remove(elem)
		delete(c.items, key)
		c.mu.Unlock()
		slog.Info("LRU Cache entry expired", "key", key)
		return models.Track{}, false
	}

	// Move to front (recently used)
	c.list.MoveToFront(elem)

	track := entry.value
	streamURL := track.URL
	c.mu.Unlock()

	// Passive check (HEAD request) executed ASYNC in background
	// Return cached value immediately; if validation fails, remove entry for next call
	go func(url, k string) {
		if !c.validateStreamURL(url) {
			c.mu.Lock()
			defer c.mu.Unlock()
			// Double-checked verification to prevent race condition
			if currentElem, stillExists := c.items[k]; stillExists && currentElem == elem {
				c.list.Remove(currentElem)
				delete(c.items, k)
				slog.Info("LRU Cache entry evicted due to failed HEAD validation", "key", k)
			}
		}
	}(streamURL, key)

	return track, true
}

// Add inserts or updates a track in the cache, evicting the oldest element if full.
func (c *LRUCache) Add(key string, track models.Track) {
	c.mu.Lock()
	defer c.mu.Unlock()

	expiration := ExtractExpiry(track.URL)

	if elem, exists := c.items[key]; exists {
		c.list.MoveToFront(elem)
		entry := elem.Value.(*cacheEntry)
		entry.value = track
		entry.expiration = expiration
		return
	}

	// Evict oldest if full
	if c.list.Len() >= c.capacity {
		back := c.list.Back()
		if back != nil {
			c.list.Remove(back)
			delete(c.items, back.Value.(*cacheEntry).key)
			slog.Info("LRU Cache evicted oldest entry", "key", back.Value.(*cacheEntry).key)
		}
	}

	entry := &cacheEntry{
		key:        key,
		value:      track,
		expiration: expiration,
	}
	elem := c.list.PushFront(entry)
	c.items[key] = elem
}

// Remove explicitly deletes an entry from the cache.
func (c *LRUCache) Remove(key string) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if elem, exists := c.items[key]; exists {
		c.list.Remove(elem)
		delete(c.items, key)
	}
}

// Size returns the current number of elements in the cache.
func (c *LRUCache) Size() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.list.Len()
}

// validateStreamURL executes a lightweight HTTP HEAD query to verify URL availability.
func (c *LRUCache) validateStreamURL(streamURL string) bool {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, "HEAD", streamURL, nil)
	if err != nil {
		return false
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		slog.Warn("HEAD validation failed (network error)", "url", streamURL, "error", err)
		return false
	}
	defer resp.Body.Close()

	return resp.StatusCode == http.StatusOK
}
