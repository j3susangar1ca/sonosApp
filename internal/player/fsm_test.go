package player

import (
        "errors"
        "sync"
        "testing"
        "time"

        "github.com/jesuslangarica/sonosApp/internal/models"
)

// mockActionHandler simulates Sonos UPnP calls.
type mockActionHandler struct {
        mu          sync.Mutex
        playCount   int
        pauseCount  int
        resumeCount int
        volCount    int
        playErr     error
        pauseErr    error
        resumeErr   error
        volErr      error
}

func (m *mockActionHandler) PlayTrack(track models.Track, volume int) error {
        m.mu.Lock()
        defer m.mu.Unlock()
        m.playCount++
        return m.playErr
}

func (m *mockActionHandler) PauseTrack() error {
        m.mu.Lock()
        defer m.mu.Unlock()
        m.pauseCount++
        return m.pauseErr
}

func (m *mockActionHandler) ResumeTrack() error {
        m.mu.Lock()
        defer m.mu.Unlock()
        m.resumeCount++
        return m.resumeErr
}

func (m *mockActionHandler) SetVolume(volume int) error {
        m.mu.Lock()
        defer m.mu.Unlock()
        m.volCount++
        return m.volErr
}

func (m *mockActionHandler) counts() (int, int, int, int) {
        m.mu.Lock()
        defer m.mu.Unlock()
        return m.playCount, m.pauseCount, m.resumeCount, m.volCount
}

// TestRingBuffer verifies general queue correctness.
func TestRingBuffer(t *testing.T) {
        r := NewRingBuffer[int](2)

        if _, err := r.Dequeue(); err != ErrEmptyBuffer {
                t.Errorf("expected ErrEmptyBuffer, got %v", err)
        }

        r.Enqueue(10)
        r.Enqueue(20)

        if r.Size() != 2 {
                t.Errorf("expected size 2, got %d", r.Size())
        }

        // Triggers resize
        r.Enqueue(30)
        if r.Size() != 3 {
                t.Errorf("expected size 3 after resize, got %d", r.Size())
        }

        h, err := r.Head()
        if err != nil || h != 10 {
                t.Errorf("expected head 10, got %v (err: %v)", h, err)
        }

        v1, _ := r.Dequeue()
        v2, _ := r.Dequeue()
        v3, _ := r.Dequeue()

        if v1 != 10 || v2 != 20 || v3 != 30 {
                t.Errorf("unexpected dequeue values: %d, %d, %d", v1, v2, v3)
        }

        if r.Size() != 0 {
                t.Errorf("expected size 0, got %d", r.Size())
        }
}

// TestFSMTransitions verifies basic playback flow and FSM state progression.
func TestFSMTransitions(t *testing.T) {
        handler := &mockActionHandler{}
        fsm := NewJukeboxFSM(15, handler)

        state, _, vol := fsm.GetState()
        if state != StateIdle || vol != 15 {
                t.Errorf("invalid initial state or volume")
        }

        // Trigger Add
        track := models.Track{ID: "track1", Src: models.SourceYoutube, UserID: "userA"}
        fsm.ProcessEvent(EventAdd, track)

        fsm.AddUser("userA")

        // Wait for async play task to complete and ACK
        time.Sleep(50 * time.Millisecond)

        state, current, _ := fsm.GetState()
        if state != StatePlaying {
                t.Errorf("expected StatePlaying, got %s", state.String())
        }
        if current == nil || current.ID != "track1" {
                t.Errorf("expected current track to be track1")
        }

        // Pause
        fsm.ProcessEvent(EventPause, nil)
        time.Sleep(10 * time.Millisecond)
        state, _, _ = fsm.GetState()
        if state != StatePaused {
                t.Errorf("expected StatePaused, got %s", state.String())
        }

        // Resume
        fsm.ProcessEvent(EventResume, nil)
        time.Sleep(10 * time.Millisecond)
        state, _, _ = fsm.GetState()
        if state != StatePlaying {
                t.Errorf("expected StatePlaying, got %s", state.String())
        }

        // Skip (immediate, no voting)
        fsm.ProcessEvent(EventSkip, nil)
        time.Sleep(50 * time.Millisecond)

        // After skip with empty queue, should go to idle
        state, _, _ = fsm.GetState()
        if state != StateIdle {
                t.Errorf("expected StateIdle after skip with empty queue, got %s", state.String())
        }

        pCount, pauseCount, resumeCount, _ := handler.counts()
        if pauseCount != 1 || resumeCount != 1 {
                t.Errorf("incorrect pause/resume calls: pause=%d, resume=%d", pauseCount, resumeCount)
        }
        _ = pCount
}

// blockingActionHandler allows blocking PlayTrack execution to test transitioning states.
type blockingActionHandler struct {
        playCalled chan struct{}
        proceed    chan struct{}
}

func (b *blockingActionHandler) PlayTrack(track models.Track, volume int) error {
        close(b.playCalled)
        <-b.proceed
        return nil
}
func (b *blockingActionHandler) PauseTrack() error          { return nil }
func (b *blockingActionHandler) ResumeTrack() error         { return nil }
func (b *blockingActionHandler) SetVolume(volume int) error { return nil }

// TestFSMDeferredCmds validates buffering and flushing of user commands during StateTransitioning.
func TestFSMDeferredCmds(t *testing.T) {
        handler := &blockingActionHandler{
                playCalled: make(chan struct{}),
                proceed:    make(chan struct{}),
        }
        fsm := NewJukeboxFSM(10, handler)

        track := models.Track{ID: "track1"}
        fsm.ProcessEvent(EventAdd, track)

        // Wait for PlayTrack to be called and block
        <-handler.playCalled

        // Now state is guaranteed to be StateTransitioning
        fsm.ProcessEvent(EventPause, nil)

        state, _, _ := fsm.GetState()
        if state != StateTransitioning {
                t.Errorf("expected state to remain Transitioning, got %s", state.String())
        }

        // Tell PlayTrack to finish. This will trigger EventAckOk in background goroutine
        close(handler.proceed)

        // Wait a bit for the transition and flush to complete
        time.Sleep(20 * time.Millisecond)

        state, _, _ = fsm.GetState()
        if state != StatePaused {
                t.Errorf("expected state to become Paused after flush, got %s", state.String())
        }
}

// TestFSMSkipImmediate verifies that skip immediately transitions without voting.
func TestFSMSkipImmediate(t *testing.T) {
        handler := &mockActionHandler{}
        fsm := NewJukeboxFSM(10, handler)

        fsm.AddUser("userA")
        fsm.AddUser("userB")

        // Add two tracks
        track1 := models.Track{ID: "track1", UserID: "userA"}
        track2 := models.Track{ID: "track2", UserID: "userB"}
        fsm.ProcessEvent(EventAdd, track1)
        time.Sleep(10 * time.Millisecond)
        fsm.ProcessEvent(EventAdd, track2)
        time.Sleep(50 * time.Millisecond)

        // Should be playing track1
        state, current, _ := fsm.GetState()
        if state != StatePlaying {
                t.Errorf("expected StatePlaying, got %s", state.String())
        }
        if current == nil || current.ID != "track1" {
                t.Errorf("expected track1 playing, got %v", current)
        }

        // Skip immediately (no voting needed)
        fsm.ProcessEvent(EventSkip, nil)
        time.Sleep(50 * time.Millisecond)

        // Should now be playing track2
        state, current, _ = fsm.GetState()
        if state != StatePlaying {
                t.Errorf("expected StatePlaying after skip, got %s", state.String())
        }
        if current == nil || current.ID != "track2" {
                t.Errorf("expected track2 playing after skip, got %v", current)
        }
}

// TestFSMPlayNow verifies that PlayNow inserts at front and plays immediately.
func TestFSMPlayNow(t *testing.T) {
        handler := &mockActionHandler{}
        fsm := NewJukeboxFSM(10, handler)

        // Add a track normally
        track1 := models.Track{ID: "track1", UserID: "userA"}
        fsm.ProcessEvent(EventAdd, track1)
        time.Sleep(50 * time.Millisecond)

        state, _, _ := fsm.GetState()
        if state != StatePlaying {
                t.Errorf("expected StatePlaying, got %s", state.String())
        }

        // PlayNow should skip current and play the new track
        track2 := models.Track{ID: "track2", UserID: "userB"}
        fsm.ProcessEvent(EventPlayNow, track2)
        time.Sleep(50 * time.Millisecond)

        state, current, _ := fsm.GetState()
        if state != StatePlaying {
                t.Errorf("expected StatePlaying after PlayNow, got %s", state.String())
        }
        if current == nil || current.ID != "track2" {
                t.Errorf("expected track2 playing after PlayNow, got %v", current)
        }
}

// TestFSMPlayFromQueue verifies that PlayFromQueue moves a track to front.
func TestFSMPlayFromQueue(t *testing.T) {
        handler := &mockActionHandler{}
        fsm := NewJukeboxFSM(10, handler)

        // Add 3 tracks
        track1 := models.Track{ID: "track1", UserID: "userA"}
        track2 := models.Track{ID: "track2", UserID: "userB"}
        track3 := models.Track{ID: "track3", UserID: "userC"}

        fsm.ProcessEvent(EventAdd, track1)
        time.Sleep(50 * time.Millisecond) // Wait for track1 to start playing

        fsm.ProcessEvent(EventAdd, track2)
        fsm.ProcessEvent(EventAdd, track3)
        time.Sleep(20 * time.Millisecond)

        // Queue should have: [track1(playing), track2, track3]
        queue := fsm.GetQueue()
        if len(queue) < 2 {
                t.Errorf("expected at least 2 tracks in queue, got %d", len(queue))
        }

        // Play track3 from queue (move to front)
        fsm.ProcessEvent(EventPlayFromQueue, "track3")
        time.Sleep(50 * time.Millisecond)

        // Now track3 should be playing
        current := fsm.currentTrack
        if current == nil || current.ID != "track3" {
                t.Errorf("expected track3 to be playing after PlayFromQueue, got %v", current)
        }
}

// TestFSMRemoveFromQueue verifies removing a track from the queue.
func TestFSMRemoveFromQueue(t *testing.T) {
        handler := &mockActionHandler{}
        fsm := NewJukeboxFSM(10, handler)

        track1 := models.Track{ID: "track1", UserID: "userA"}
        track2 := models.Track{ID: "track2", UserID: "userB"}
        track3 := models.Track{ID: "track3", UserID: "userC"}

        fsm.ProcessEvent(EventAdd, track1)
        time.Sleep(50 * time.Millisecond)
        fsm.ProcessEvent(EventAdd, track2)
        fsm.ProcessEvent(EventAdd, track3)

        // Remove track2
        fsm.ProcessEvent(EventRemoveFromQueue, "track2")

        queue := fsm.GetQueue()
        for _, track := range queue {
                if track.ID == "track2" {
                        t.Errorf("track2 should have been removed from queue")
                }
        }
}

// TestFSMAckFailRetry verifies retry and idle state resets.
func TestFSMAckFailRetry(t *testing.T) {
        handler := &mockActionHandler{playErr: errors.New("network timeout")}
        fsm := NewJukeboxFSM(10, handler)

        track := models.Track{ID: "track1"}
        fsm.ProcessEvent(EventAdd, track)
        time.Sleep(10 * time.Millisecond)

        // Init transitions to StateTransitioning, triggers play which fails, EventAckFail injected.
        // fsm should retry up to 3 times, then go to StateIdle.
        time.Sleep(150 * time.Millisecond)

        state, _, _ := fsm.GetState()
        if state != StateIdle {
                t.Errorf("expected state to reset to Idle after all failed retries, got %s", state.String())
        }

        pCount, _, _, _ := handler.counts()
        if pCount != 4 { // Initial attempt + 3 retries = 4
                t.Errorf("expected 4 play calls on Sonos handler, got %d", pCount)
        }
}
