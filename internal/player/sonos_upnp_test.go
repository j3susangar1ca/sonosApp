package player

import (
        "fmt"
        "io"
        "net/http"
        "net/http/httptest"
        "net/url"
        "strings"
        "sync/atomic"
        "testing"
        "time"

        "github.com/jesuslangarica/sonosApp/internal/models"
)

// TestCircuitBreakerStates validates Closed -> Open -> HalfOpen -> Closed transitions.
func TestCircuitBreakerStates(t *testing.T) {
        cb := NewCircuitBreaker(3, 50*time.Millisecond)

        if cb.GetState() != CBClosed {
                t.Errorf("expected closed state, got %s", cb.GetState().String())
        }
        if !cb.CanExecute() {
                t.Error("should be able to execute in closed state")
        }

        // Record 2 failures
        cb.RecordFailure()
        cb.RecordFailure()
        if cb.GetState() != CBClosed {
                t.Errorf("expected still closed state, got %s", cb.GetState().String())
        }

        // 3rd failure trips the circuit
        cb.RecordFailure()
        if cb.GetState() != CBOpen {
                t.Errorf("expected open state, got %s", cb.GetState().String())
        }
        if cb.CanExecute() {
                t.Error("should not be able to execute in open state")
        }

        // Cooldown wait
        time.Sleep(60 * time.Millisecond)

        // CanExecute should transition state to HalfOpen and allow execution
        if !cb.CanExecute() {
                t.Error("should allow execution now (moving to HalfOpen)")
        }
        if cb.GetState() != CBHalfOpen {
                t.Errorf("expected half_open state, got %s", cb.GetState().String())
        }

        // Failure in HalfOpen trips it back to Open immediately
        cb.RecordFailure()
        if cb.GetState() != CBOpen {
                t.Errorf("expected open state after failure in HalfOpen, got %s", cb.GetState().String())
        }

        // Wait cooldown again
        time.Sleep(60 * time.Millisecond)
        if !cb.CanExecute() {
                t.Error("should allow execution")
        }

        // Success in HalfOpen closes the circuit
        cb.RecordSuccess()
        if cb.GetState() != CBClosed {
                t.Errorf("expected closed state after success, got %s", cb.GetState().String())
        }
}

// TestJitterBackoffBounds validates backoff ranges and maximum cap.
func TestJitterBackoffBounds(t *testing.T) {
        cb := NewCircuitBreaker(3, 1*time.Second)
        p := NewSonosPlayer("127.0.0.1", cb, nil)

        for attempt := 0; attempt < 5; attempt++ {
                dur := p.jitterBackoff(attempt)
                if dur > 5*time.Second {
                        t.Errorf("backoff duration exceeds 5s limit: %s", dur)
                }
                if dur < 0 {
                        t.Errorf("negative backoff duration: %s", dur)
                }
        }
}

// TestSonosPlayerSuccess tests correct SOAP invocation format and successful execution.
func TestSonosPlayerSuccess(t *testing.T) {
        var setAVTransportCalled int32
        var playCalled int32
        var setVolumeCalled int32

        // Setup mock SOAP HTTP server
        server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
                soapAction := r.Header.Get("SOAPACTION")
                bodyBytes, err := io.ReadAll(r.Body)
                if err != nil {
                        w.WriteHeader(http.StatusBadRequest)
                        return
                }
                body := string(bodyBytes)

                // Assert Content-Type
                if r.Header.Get("Content-Type") != `text/xml; charset="utf-8"` {
                        w.WriteHeader(http.StatusBadRequest)
                        return
                }

                switch {
                case strings.Contains(soapAction, "SetAVTransportURI"):
                        atomic.AddInt32(&setAVTransportCalled, 1)
                        if !strings.Contains(body, "https://youtube.com/watch?v=abc&amp;t=10") {
                                t.Errorf("URL characters were not properly escaped in SOAP envelope body: %s", body)
                        }
                        w.WriteHeader(http.StatusOK)

                case strings.Contains(soapAction, "Play"):
                        atomic.AddInt32(&playCalled, 1)
                        w.WriteHeader(http.StatusOK)

                case strings.Contains(soapAction, "SetVolume"):
                        atomic.AddInt32(&setVolumeCalled, 1)
                        if !strings.Contains(body, "<DesiredVolume>15</DesiredVolume>") {
                                t.Errorf("Volume level was not set correctly in SOAP body: %s", body)
                        }
                        w.WriteHeader(http.StatusOK)

                default:
                        w.WriteHeader(http.StatusNotFound)
                }
        }))
        defer server.Close()

        u, err := url.Parse(server.URL)
        if err != nil {
                t.Fatal(err)
        }

        cb := NewCircuitBreaker(3, 100*time.Millisecond)
        player := NewSonosPlayer(u.Hostname(), cb, nil)
        // Inject mock server port
        var port int
        _, err = fmt.Sscanf(u.Port(), "%d", &port)
        if err != nil {
                t.Fatal(err)
        }
        player.port = port

        // Mock zero-duration backoff for instant execution
        player.backoffFn = func(attempt int) time.Duration { return 0 }

        track := models.Track{
                URL: "https://youtube.com/watch?v=abc&t=10",
        }

        err = player.PlayTrack(track, 15)
        if err != nil {
                t.Fatalf("unexpected error playing track: %v", err)
        }

        if atomic.LoadInt32(&setAVTransportCalled) != 1 {
                t.Error("SetAVTransportURI SOAP action was not called")
        }
        if atomic.LoadInt32(&playCalled) != 1 {
                t.Error("Play SOAP action was not called")
        }
        if atomic.LoadInt32(&setVolumeCalled) != 1 {
                t.Error("SetVolume SOAP action was not called")
        }

        if cb.GetState() != CBClosed {
                t.Errorf("expected closed state, got %s", cb.GetState().String())
        }
}

// TestSonosPlayerRetriesAndBreaks verifies retry loops and circuit tripping.
func TestSonosPlayerRetriesAndBreaks(t *testing.T) {
        var totalRequests int32

        server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
                atomic.AddInt32(&totalRequests, 1)
                w.WriteHeader(http.StatusInternalServerError) // Force failure
        }))
        defer server.Close()

        u, err := url.Parse(server.URL)
        if err != nil {
                t.Fatal(err)
        }

        cb := NewCircuitBreaker(2, 50*time.Millisecond) // Max 2 failed calls trips it
        player := NewSonosPlayer(u.Hostname(), cb, nil)
        var port int
        _, err = fmt.Sscanf(u.Port(), "%d", &port)
        if err != nil {
                t.Fatal(err)
        }
        player.port = port
        player.backoffFn = func(attempt int) time.Duration { return 0 }

        track := models.Track{URL: "https://youtube.com"}

        // 1st request should fail. It will run 1 initial + 3 retries = 4 requests.
        // All fail. RecordFailure is called once since retries are wrapped as a single logic unit.
        err = player.PlayTrack(track, 10)
        if err == nil {
                t.Error("expected error but got nil")
        }

        if atomic.LoadInt32(&totalRequests) != 4 {
                t.Errorf("expected 4 requests (1 initial + 3 retries), got %d", atomic.LoadInt32(&totalRequests))
        }
        if cb.GetState() != CBClosed {
                t.Errorf("circuit should still be closed (failure count is 1, threshold is 2)")
        }

        // Reset counter for assertions
        atomic.StoreInt32(&totalRequests, 0)

        // 2nd request should fail again. Will run 1 initial + 3 retries = 4 requests.
        // RecordFailure is called a second time. This meets threshold 2. Trips circuit to Open!
        err = player.PlayTrack(track, 10)
        if err == nil {
                t.Error("expected error but got nil")
        }

        if atomic.LoadInt32(&totalRequests) != 4 {
                t.Errorf("expected 4 requests, got %d", atomic.LoadInt32(&totalRequests))
        }
        if cb.GetState() != CBOpen {
                t.Errorf("circuit should be open, got %s", cb.GetState().String())
        }

        // 3rd request should fail immediately with ErrCircuitOpen WITHOUT contacting the server
        atomic.StoreInt32(&totalRequests, 0)
        err = player.PlayTrack(track, 10)
        if err != ErrCircuitOpen {
                t.Errorf("expected ErrCircuitOpen, got %v", err)
        }
        if atomic.LoadInt32(&totalRequests) != 0 {
                t.Errorf("expected 0 requests to hit the server, got %d", atomic.LoadInt32(&totalRequests))
        }
}

// TestSonosPlayerTemporaryFailureSuccess verifies successful recovery on retry.
func TestSonosPlayerTemporaryFailureSuccess(t *testing.T) {
        var totalRequests int32

        server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
                reqNum := atomic.AddInt32(&totalRequests, 1)
                if reqNum <= 2 { // Fail first 2 attempts
                        w.WriteHeader(http.StatusInternalServerError)
                } else { // Succeed on the 3rd attempt (which is the 2nd retry)
                        w.WriteHeader(http.StatusOK)
                }
        }))
        defer server.Close()

        u, err := url.Parse(server.URL)
        if err != nil {
                t.Fatal(err)
        }

        cb := NewCircuitBreaker(3, 1*time.Second)
        player := NewSonosPlayer(u.Hostname(), cb, nil)
        var port int
        _, err = fmt.Sscanf(u.Port(), "%d", &port)
        if err != nil {
                t.Fatal(err)
        }
        player.port = port
        player.backoffFn = func(attempt int) time.Duration { return 0 }

        err = player.PauseTrack()
        if err != nil {
                t.Fatalf("expected PauseTrack to eventually succeed: %v", err)
        }

        if atomic.LoadInt32(&totalRequests) != 3 {
                t.Errorf("expected 3 requests to hit the server (2 failures + 1 success), got %d", atomic.LoadInt32(&totalRequests))
        }
        if cb.GetState() != CBClosed {
                t.Errorf("circuit should be closed, got %s", cb.GetState().String())
        }
}
