package streaming

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// StreamingProxy acts as a secure, memory-efficient media gateway.
// It serves local files to Sonos players using ephemeral HMAC tokens
// and strictly prevents directory traversal attacks.
type StreamingProxy struct {
	baseDir   string
	secretKey []byte
	serverURL string
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
		baseDir:   absBase,
		secretKey: secret,
		serverURL: strings.TrimSuffix(serverURL, "/"),
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

// ServeHTTP implements the http.Handler interface to securely serve local files.
func (p *StreamingProxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	query := r.URL.Query()
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
		slog.Warn("Streaming request blocked: token expired", "file", fileParam, "expires", expiresAt)
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
	// Resolve baseDir to double check
	baseAbs, err := filepath.Abs(p.baseDir)
	if err != nil {
		http.Error(w, "Internal configuration error", http.StatusInternalServerError)
		return
	}

	// Join baseDir and fileParam, then get the absolute path
	targetPath := filepath.Join(baseAbs, fileParam)
	targetAbs, err := filepath.Abs(targetPath)
	if err != nil {
		http.Error(w, "Invalid file path", http.StatusBadRequest)
		return
	}

	// Ensure the clean target path is strictly nested under baseDir
	// To prevent bypasses like /media/music_alternative when base is /media/music,
	// we check for exact match or suffix starting with separator.
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

	// http.ServeContent handles range requests natively and uses sendfile/zero-copy under the hood.
	slog.Info("Streaming local file", "path", targetAbs, "size", info.Size())
	http.ServeContent(w, r, info.Name(), info.ModTime(), f)
}
