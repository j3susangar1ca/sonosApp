package player

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"html"
	"log/slog"
	"math"
	"math/rand"
	"net/http"
	"sync"
	"time"

	"github.com/jesuslangarica/sonosApp/internal/models"
	"github.com/jesuslangarica/sonosApp/internal/telemetry"
)

// ErrCircuitOpen is returned when the Circuit Breaker blocks requests to Sonos.
var ErrCircuitOpen = errors.New("circuit breaker is open, sonos is offline")

// CBState represents the state of the Circuit Breaker.
type CBState int

const (
	// CBClosed: requests flow normally
	CBClosed CBState = iota
	// CBOpen: requests are blocked immediately
	CBOpen
	// CBHalfOpen: a probe request is allowed to check if Sonos is back online
	CBHalfOpen
)

// String returns the string representation of CBState.
func (s CBState) String() string {
	switch s {
	case CBClosed:
		return "closed"
	case CBOpen:
		return "open"
	case CBHalfOpen:
		return "half_open"
	default:
		return "unknown"
	}
}

// CircuitBreaker protects the caller from blocking on offline hardware.
type CircuitBreaker struct {
	mu           sync.RWMutex
	state        CBState
	failureCount int64
	maxFailures  int64
	coolDown     time.Duration
	openedAt     time.Time
}

// NewCircuitBreaker creates a new CircuitBreaker.
func NewCircuitBreaker(maxFailures int64, coolDown time.Duration) *CircuitBreaker {
	return &CircuitBreaker{
		state:       CBClosed,
		maxFailures: maxFailures,
		coolDown:    coolDown,
	}
}

// CanExecute checks if the request is allowed to proceed.
// If the cooldown timer has expired in Open state, transitions to HalfOpen.
func (cb *CircuitBreaker) CanExecute() bool {
	cb.mu.Lock()
	defer cb.mu.Unlock()

	if cb.state == CBOpen {
		if time.Since(cb.openedAt) >= cb.coolDown {
			cb.state = CBHalfOpen
			slog.Info("Circuit Breaker transitioned to HALF_OPEN (cooldown expired)")
			return true
		}
		return false
	}
	return true
}

// RecordSuccess resets the failure count and closes the circuit.
func (cb *CircuitBreaker) RecordSuccess() {
	cb.mu.Lock()
	defer cb.mu.Unlock()

	cb.failureCount = 0
	if cb.state != CBClosed {
		slog.Info("Circuit Breaker transitioned to CLOSED (success registered)")
		cb.state = CBClosed
	}
}

// RecordFailure increments failures and trips the circuit if threshold is reached.
func (cb *CircuitBreaker) RecordFailure() {
	cb.mu.Lock()
	defer cb.mu.Unlock()

	cb.failureCount++
	telemetry.IncrementCircuitBreakerFailures()
	slog.Warn("Circuit Breaker registered failure", "consecutive_failures", cb.failureCount, "max_failures", cb.maxFailures, "current_state", cb.state.String())

	if cb.state == CBClosed && cb.failureCount >= cb.maxFailures {
		cb.state = CBOpen
		cb.openedAt = time.Now()
		slog.Error("Circuit Breaker tripped to OPEN")
	} else if cb.state == CBHalfOpen {
		cb.state = CBOpen
		cb.openedAt = time.Now()
		slog.Error("Circuit Breaker tripped to OPEN from HALF_OPEN")
	}
}

// GetState returns the current state of the Circuit Breaker.
func (cb *CircuitBreaker) GetState() CBState {
	cb.mu.RLock()
	defer cb.mu.RUnlock()
	return cb.state
}

// SonosPlayer implements ActionHandler using UPnP SOAP actions.
type SonosPlayer struct {
	speakerIP string
	port      int
	client    *http.Client
	cb        *CircuitBreaker
	mu        sync.Mutex                      // Serializes SOAP actions on the player
	backoffFn func(attempt int) time.Duration // Custom backoff function for test mockability
}

// NewSonosPlayer initializes a SonosPlayer.
func NewSonosPlayer(speakerIP string, cb *CircuitBreaker) *SonosPlayer {
	return &SonosPlayer{
		speakerIP: speakerIP,
		port:      1400,
		client:    &http.Client{Timeout: 5 * time.Second},
		cb:        cb,
	}
}

// PlayTrack sets the URI, plays the track, and updates the volume.
func (s *SonosPlayer) PlayTrack(track models.Track, volume int) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	// Escape special characters in streaming URLs (such as & in YouTube query parameters)
	escapedURL := html.EscapeString(track.URL)

	setURIBody := fmt.Sprintf(`<?xml version="1.0" encoding="utf-8"?>
<s:Envelope xmlns:s="http://schemas.xmlsoap.org/soap/envelope/" s:encodingStyle="http://schemas.xmlsoap.org/soap/encoding/">
  <s:Body>
    <u:SetAVTransportURI xmlns:u="urn:schemas-upnp-org:service:AVTransport:1">
      <InstanceID>0</InstanceID>
      <CurrentURI>%s</CurrentURI>
      <CurrentURIMetaData></CurrentURIMetaData>
    </u:SetAVTransportURI>
  </s:Body>
</s:Envelope>`, escapedURL)

	err := s.callSOAP(ctx, "AVTransport", "SetAVTransportURI", setURIBody)
	if err != nil {
		return err
	}

	playBody := `<?xml version="1.0" encoding="utf-8"?>
<s:Envelope xmlns:s="http://schemas.xmlsoap.org/soap/envelope/" s:encodingStyle="http://schemas.xmlsoap.org/soap/encoding/">
  <s:Body>
    <u:Play xmlns:u="urn:schemas-upnp-org:service:AVTransport:1">
      <InstanceID>0</InstanceID>
      <Speed>1</Speed>
    </u:Play>
  </s:Body>
</s:Envelope>`

	err = s.callSOAP(ctx, "AVTransport", "Play", playBody)
	if err != nil {
		return err
	}

	return s.setVolumeInternal(ctx, volume)
}

// PauseTrack sends the SOAP Pause action.
func (s *SonosPlayer) PauseTrack() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	pauseBody := `<?xml version="1.0" encoding="utf-8"?>
<s:Envelope xmlns:s="http://schemas.xmlsoap.org/soap/envelope/" s:encodingStyle="http://schemas.xmlsoap.org/soap/encoding/">
  <s:Body>
    <u:Pause xmlns:u="urn:schemas-upnp-org:service:AVTransport:1">
      <InstanceID>0</InstanceID>
    </u:Pause>
  </s:Body>
</s:Envelope>`

	return s.callSOAP(ctx, "AVTransport", "Pause", pauseBody)
}

// ResumeTrack sends the SOAP Play action.
func (s *SonosPlayer) ResumeTrack() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	playBody := `<?xml version="1.0" encoding="utf-8"?>
<s:Envelope xmlns:s="http://schemas.xmlsoap.org/soap/envelope/" s:encodingStyle="http://schemas.xmlsoap.org/soap/encoding/">
  <s:Body>
    <u:Play xmlns:u="urn:schemas-upnp-org:service:AVTransport:1">
      <InstanceID>0</InstanceID>
      <Speed>1</Speed>
    </u:Play>
  </s:Body>
</s:Envelope>`

	return s.callSOAP(ctx, "AVTransport", "Play", playBody)
}

// SetVolume updates the master volume channel (0-100).
func (s *SonosPlayer) SetVolume(volume int) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	return s.setVolumeInternal(ctx, volume)
}

func (s *SonosPlayer) setVolumeInternal(ctx context.Context, volume int) error {
	if volume < 0 {
		volume = 0
	} else if volume > 100 {
		volume = 100
	}

	volBody := fmt.Sprintf(`<?xml version="1.0" encoding="utf-8"?>
<s:Envelope xmlns:s="http://schemas.xmlsoap.org/soap/envelope/" s:encodingStyle="http://schemas.xmlsoap.org/soap/encoding/">
  <s:Body>
    <u:SetVolume xmlns:u="urn:schemas-upnp-org:service:RenderingControl:1">
      <InstanceID>0</InstanceID>
      <Channel>Master</Channel>
      <DesiredVolume>%d</DesiredVolume>
    </u:SetVolume>
  </s:Body>
</s:Envelope>`, volume)

	return s.callSOAP(ctx, "RenderingControl", "SetVolume", volBody)
}

// callSOAP executes the query after validating the Circuit Breaker and running retries.
func (s *SonosPlayer) callSOAP(ctx context.Context, service string, action string, body string) error {
	if !s.cb.CanExecute() {
		return ErrCircuitOpen
	}

	err := s.executeWithRetry(ctx, func(reqCtx context.Context) error {
		return s.postSOAPRaw(reqCtx, service, action, body)
	})

	if err != nil {
		s.cb.RecordFailure()
		return err
	}

	s.cb.RecordSuccess()
	return nil
}

// postSOAPRaw performs the raw HTTP POST request to the Sonos UPNP SOAP control endpoint.
func (s *SonosPlayer) postSOAPRaw(ctx context.Context, service string, action string, body string) error {
	url := fmt.Sprintf("http://%s:%d/MediaRenderer/%s/Control", s.speakerIP, s.port, service)

	req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewBufferString(body))
	if err != nil {
		return err
	}

	req.Header.Set("Content-Type", `text/xml; charset="utf-8"`)
	req.Header.Set("SOAPACTION", fmt.Sprintf("urn:schemas-upnp-org:service:%s:1#%s", service, action))

	startTime := time.Now()
	resp, err := s.client.Do(req)
	duration := time.Since(startTime).Seconds()
	telemetry.ObserveSonosLatency(duration)

	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return fmt.Errorf("SOAP action %s failed: status code %d", action, resp.StatusCode)
	}

	return nil
}

// executeWithRetry retries the SOAP action up to 3 times with exponential backoff and random jitter.
func (s *SonosPlayer) executeWithRetry(ctx context.Context, action func(ctx context.Context) error) error {
	var err error
	for i := 0; i < 4; i++ { // 1 initial attempt + 3 retries
		if i > 0 {
			backoff := s.jitterBackoff(i - 1)
			slog.Warn("Sonos UPNP call failed, retrying with backoff", "attempt", i, "backoff", backoff.String(), "error", err)

			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(backoff):
			}
		}

		err = action(ctx)
		if err == nil {
			return nil
		}
	}
	return err
}

// jitterBackoff calculates: min(2^attempt * 500ms + rand(0, 500ms), 5000ms)
func (s *SonosPlayer) jitterBackoff(attempt int) time.Duration {
	if s.backoffFn != nil {
		return s.backoffFn(attempt)
	}
	base := float64(time.Second) * 0.5 * math.Pow(2.0, float64(attempt))
	jitter := float64(rand.Intn(500)) * float64(time.Millisecond)
	dur := time.Duration(base) + time.Duration(jitter)
	maxDur := 5 * time.Second
	if dur > maxDur {
		return maxDur
	}
	return dur
}
