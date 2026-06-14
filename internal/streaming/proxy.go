package streaming

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

// StreamingProxy acts as a secure, memory-efficient media gateway.
// It serves local files AND proxies remote URLs to Sonos players
// using ephemeral HMAC tokens and strict security checks.
type StreamingProxy struct {
	baseDir   string
	secretKey []byte
	serverURL string

	// Remote URL proxy store: token -> RemoteTrack
	remoteMu    sync.RWMutex
	remoteStore map[string]*RemoteTrack
}

// RemoteTrack holds a remote streaming URL and metadata for proxying.
type RemoteTrack struct {
	URL      string    // The actual googlevideo.com URL
	MIMEType string    // e.g. "audio/mp4"
	Title    string    // Track title for logging
	TrackID  string    // YouTube video ID
	Created  time.Time // When the proxy entry was created
}

// NewStreamingProxy initializes a StreamingProxy.
// The baseDir is resolved to its absolute path.
func NewStreamingProxy(baseDir, serverURL string) (*StreamingProxy, error) {
	absBase, err := filepath.Abs(baseDir)
	if err != nil {
		return nil, fmt.Errorf("failed to resolve base directory: %w", err)
	}

	secret := make([]byte, 32)
	if _, err := rand.Read(secret); err != nil {
		return nil, fmt.Errorf("failed to generate secure cryptopgrahic key: %w", err)
	}

	return &StreamingProxy{
		baseDir:     absBase,
		secretKey:   secret,
		serverURL:   strings.TrimSuffix(serverURL, "/"),
		remoteStore: make(map[string]*RemoteTrack),
	}, nil
}

// GenerateURL creates a signed URL with an ephemeral token valid for the specified TTL.
func (p *StreamingProxy) GenerateURL(relPath string, ttl time.Duration) (string, error) {
	cleaned := filepath.Clean(relPath)
	expiresAt := time.Now().Add(ttl).Unix()

	payload := fmt.Sprintf("%s:%d", cleaned, expiresAt)
	mac := hmac.New(sha256.New, p.secretKey)
	mac.Write([]byte(payload))
	signature := hex.EncodeToString(mac.Sum(nil))

	params := url.Values{}
	params.Set("file", cleaned)
	params.Set("expires", strconv.FormatInt(expiresAt, 10))
	params.Set("token", signature)

	return fmt.Sprintf("%s/stream?%s", p.serverURL, params.Encode()), nil
}

// GenerateRemoteURL registers a remote streaming URL and returns a proxied URL
// that Sonos can access through this server. This solves the IP-restriction problem
// where googlevideo.com URLs only work from the server's IP, not Sonos's IP.
func (p *StreamingProxy) GenerateRemoteURL(remoteURL, mimeType, title, trackID string) (string, error) {
	// Generate a unique token for this remote track
	tokenBytes := make([]byte, 16)
	if _, err := rand.Read(tokenBytes); err != nil {
		return "", fmt.Errorf("failed to generate remote token: %w", err)
	}
	token := hex.EncodeToString(tokenBytes)

	track := &RemoteTrack{
		URL:      remoteURL,
		MIMEType: mimeType,
		Title:    title,
		TrackID:  trackID,
		Created:  time.Now(),
	}

	p.remoteMu.Lock()
	p.remoteStore[token] = track
	p.remoteMu.Unlock()

	// Set expiration to 6 hours (YouTube URLs typically expire in ~6h)
	expiresAt := time.Now().Add(6 * time.Hour).Unix()

	// Create HMAC signature for the remote token
	payload := fmt.Sprintf("remote:%s:%d", token, expiresAt)
	mac := hmac.New(sha256.New, p.secretKey)
	mac.Write([]byte(payload))
	signature := hex.EncodeToString(mac.Sum(nil))

	params := url.Values{}
	params.Set("remote", token)
	params.Set("expires", strconv.FormatInt(expiresAt, 10))
	params.Set("sig", signature)

	proxyURL := fmt.Sprintf("%s/stream?%s", p.serverURL, params.Encode())
	slog.Info("Generated remote proxy URL", "track_id", trackID, "title", title, "mime", mimeType, "proxy_url_len", len(proxyURL))
	return proxyURL, nil
}

// cleanupExpiredRemotes removes expired remote track entries.
func (p *StreamingProxy) cleanupExpiredRemotes() {
	p.remoteMu.Lock()
	defer p.remoteMu.Unlock()

	now := time.Now()
	for token, track := range p.remoteStore {
		// Remove entries older than 6 hours
		if now.Sub(track.Created) > 6*time.Hour {
			delete(p.remoteStore, token)
		}
	}
}

// ServeHTTP implements the http.Handler interface to serve local files or proxy remote URLs.
func (p *StreamingProxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	query := r.URL.Query()

	// Route: Remote URL proxying
	if remoteToken := query.Get("remote"); remoteToken != "" {
		p.serveRemote(w, r, remoteToken, query)
		return
	}

	// Route: Local file serving (original behavior)
	p.serveLocal(w, r, query)
}

// serveRemote proxies a remote streaming URL to the Sonos player.
// This is the key fix: Sonos cannot access googlevideo.com URLs directly
// because they are IP-restricted. By proxying through the server,
// the server fetches the content and streams it to Sonos.
func (p *StreamingProxy) serveRemote(w http.ResponseWriter, r *http.Request, token string, query url.Values) {
	expiresParam := query.Get("expires")
	sigParam := query.Get("sig")

	if expiresParam == "" || sigParam == "" {
		http.Error(w, "Missing required parameters", http.StatusBadRequest)
		return
	}

	// 1. Verify expiration
	expiresAt, err := strconv.ParseInt(expiresParam, 10, 64)
	if err != nil {
		http.Error(w, "Invalid expiration parameter", http.StatusBadRequest)
		return
	}
	if time.Now().Unix() > expiresAt {
		slog.Warn("Remote streaming request blocked: token expired", "token", token)
		http.Error(w, "Token expired", http.StatusForbidden)
		return
	}

	// 2. Verify HMAC signature
	payload := fmt.Sprintf("remote:%s:%d", token, expiresAt)
	mac := hmac.New(sha256.New, p.secretKey)
	mac.Write([]byte(payload))
	expectedSig := hex.EncodeToString(mac.Sum(nil))

	if !hmac.Equal([]byte(sigParam), []byte(expectedSig)) {
		slog.Warn("Remote streaming request blocked: invalid signature", "token", token)
		http.Error(w, "Invalid signature", http.StatusForbidden)
		return
	}

	// 3. Look up the remote track
	p.remoteMu.RLock()
	track, exists := p.remoteStore[token]
	p.remoteMu.RUnlock()

	if !exists {
		slog.Warn("Remote streaming request blocked: unknown token", "token", token)
		http.Error(w, "Track not found", http.StatusNotFound)
		return
	}

	// 4. Fetch from the remote URL and stream to Sonos
	slog.Info("Proxying remote stream", "track_id", track.TrackID, "title", track.Title, "remote_url_len", len(track.URL))

	// Periodic cleanup
	go p.cleanupExpiredRemotes()

	// Create HTTP request to the remote URL with proper headers
	req, err := http.NewRequestWithContext(r.Context(), "GET", track.URL, nil)
	if err != nil {
		slog.Error("Failed to create remote request", "error", err)
		http.Error(w, "Internal error", http.StatusInternalServerError)
		return
	}

	// Set headers that YouTube/Google Video expects
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36")
	req.Header.Set("Accept", "*/*")
	req.Header.Set("Accept-Encoding", "identity") // No compression for streaming

	// If the original request has a Range header, forward it
	if rangeHeader := r.Header.Get("Range"); rangeHeader != "" {
		req.Header.Set("Range", rangeHeader)
	}

	client := &http.Client{
		Timeout: 0, // No timeout for streaming
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			// Follow redirects but preserve headers
			return nil
		},
	}

	resp, err := client.Do(req)
	if err != nil {
		slog.Error("Failed to fetch remote stream", "track_id", track.TrackID, "error", err)
		http.Error(w, "Failed to fetch stream", http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()

	// Check remote response status
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusPartialContent {
		slog.Error("Remote stream returned error", "track_id", track.TrackID, "status", resp.StatusCode)
		http.Error(w, fmt.Sprintf("Remote error: %d", resp.StatusCode), http.StatusBadGateway)
		return
	}

	// Set response headers for Sonos
	contentType := track.MIMEType
	if ct := resp.Header.Get("Content-Type"); ct != "" {
		contentType = ct
	}
	w.Header().Set("Content-Type", contentType)

	// Forward Content-Length if available
	if cl := resp.Header.Get("Content-Length"); cl != "" {
		w.Header().Set("Content-Length", cl)
	}

	// Forward Content-Range if partial content
	if cr := resp.Header.Get("Content-Range"); cr != "" {
		w.Header().Set("Content-Range", cr)
	}

	// Support range requests from Sonos
	w.Header().Set("Accept-Ranges", "bytes")

	// Set status code
	if resp.StatusCode == http.StatusPartialContent {
		w.WriteHeader(http.StatusPartialContent)
	} else {
		w.WriteHeader(http.StatusOK)
	}

	// Stream the content - use io.Copy for memory efficiency
	written, err := io.Copy(w, resp.Body)
	if err != nil {
		slog.Warn("Remote stream transfer interrupted", "track_id", track.TrackID, "bytes_written", written, "error", err)
	} else {
		slog.Info("Remote stream completed", "track_id", track.TrackID, "bytes_written", written)
	}
}

// serveLocal serves local files (original streaming proxy behavior).
func (p *StreamingProxy) serveLocal(w http.ResponseWriter, r *http.Request, query url.Values) {
	fileParam := query.Get("file")
	expiresParam := query.Get("expires")
	tokenParam := query.Get("token")

	if fileParam == "" || expiresParam == "" || tokenParam == "" {
		http.Error(w, "Missing required parameters", http.StatusBadRequest)
		return
	}

	// 1. Verify Expiration
	expiresAt, err := strconv.ParseInt(expiresParam, 10, 64)
	if err != nil {
		http.Error(w, "Invalid expiration parameter", http.StatusBadRequest)
		return
	}

	if time.Now().Unix() > expiresAt {
		slog.Warn("Streaming request blocked: token expired", "file", fileParam, "expires", expiresParam)
		http.Error(w, "Token expired", http.StatusForbidden)
		return
	}

	// 2. Cryptographic HMAC validation
	payload := fmt.Sprintf("%s:%d", fileParam, expiresAt)
	mac := hmac.New(sha256.New, p.secretKey)
	mac.Write([]byte(payload))
	expectedMac := mac.Sum(nil)

	actualMac, err := hex.DecodeString(tokenParam)
	if err != nil || !hmac.Equal(actualMac, expectedMac) {
		slog.Warn("Streaming request blocked: invalid signature token", "file", fileParam)
		http.Error(w, "Invalid token signature", http.StatusForbidden)
		return
	}

	// 3. Strict Path Traversal Prevention
	baseAbs, err := filepath.Abs(p.baseDir)
	if err != nil {
		http.Error(w, "Internal configuration error", http.StatusInternalServerError)
		return
	}

	targetPath := filepath.Join(baseAbs, fileParam)
	targetAbs, err := filepath.Abs(targetPath)
	if err != nil {
		http.Error(w, "Invalid file path", http.StatusBadRequest)
		return
	}

	if targetAbs != baseAbs && !strings.HasPrefix(targetAbs, baseAbs+string(filepath.Separator)) {
		slog.Error("Streaming request blocked: path traversal attempt detected", "requested", fileParam, "resolved", targetAbs)
		http.Error(w, "Access denied", http.StatusForbidden)
		return
	}

	// 4. File access validation
	info, err := os.Stat(targetAbs)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			http.Error(w, "File not found", http.StatusNotFound)
		} else {
			http.Error(w, "Error accessing file", http.StatusInternalServerError)
		}
		return
	}

	if info.IsDir() {
		http.Error(w, "Access denied", http.StatusForbidden)
		return
	}

	f, err := os.Open(targetAbs)
	if err != nil {
		slog.Error("Failed to open file for streaming", "path", targetAbs, "error", err)
		http.Error(w, "Failed to open file", http.StatusInternalServerError)
		return
	}
	defer f.Close()

	slog.Info("Streaming local file", "path", targetAbs, "size", info.Size())
	http.ServeContent(w, r, info.Name(), info.ModTime(), f)
}

// MarshalJSON implements json.Marshaler for debugging.
func (p *StreamingProxy) MarshalJSON() ([]byte, error) {
	p.remoteMu.RLock()
	defer p.remoteMu.RUnlock()
	return json.Marshal(map[string]interface{}{
		"server_url":    p.serverURL,
		"remote_tracks": len(p.remoteStore),
	})
}
