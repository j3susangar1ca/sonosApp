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

	// Add user so skip votes work
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

	// EOF
	fsm.ProcessEvent(EventEOF, nil)
	time.Sleep(50 * time.Millisecond)
	state, _, _ = fsm.GetState()
	// Should go to Autoplay then to Idle since history has only 1 track but it got finished, wait...
	// Wait, EOF on track1 adds it to history. Autoplay picks it from history.
	// Since history contains track1, Autoplay will re-enqueue track1 and transition to StatePlaying!
	// Let's verify:
	if state != StatePlaying {
		t.Errorf("expected Autoplay to trigger replay of track1, got state %s", state.String())
	}

	pCount, pauseCount, resumeCount, _ := handler.counts()
	if pCount < 2 {
		t.Errorf("expected at least 2 play calls on Sonos handler, got %d", pCount)
	}
	if pauseCount != 1 || resumeCount != 1 {
		t.Errorf("incorrect pause/resume calls")
	}
}

// TestFSMDeferredCmds validates buffering and flushing of user commands during StateTransitioning.
func TestFSMDeferredCmds(t *testing.T) {
	handler := &mockActionHandler{}
	fsm := NewJukeboxFSM(10, handler)

	// Force state to Transitioning by putting it manually or enqueuing and not immediately ACK'ing.
	// Let's trigger Add. State will be StateTransitioning. We do NOT wait, but immediately send EventPause.
	track := models.Track{ID: "track1"}
	fsm.ProcessEvent(EventAdd, track)

	// Since we haven't processed EventAckOk yet, state is StateTransitioning.
	// Let's send a user event (e_pause).
	fsm.ProcessEvent(EventPause, nil)

	state, _, _ := fsm.GetState()
	if state != StateTransitioning {
		t.Errorf("expected state to remain Transitioning, got %s", state.String())
	}

	// Now ACK the transition. It should transition to StatePlaying and then immediately flush the deferred EventPause, going to StatePaused!
	fsm.ProcessEvent(EventAckOk, nil)
	time.Sleep(10 * time.Millisecond)

	state, _, _ = fsm.GetState()
	if state != StatePaused {
		t.Errorf("expected state to become Paused after flush, got %s", state.String())
	}
}

// TestFSMDemocracy validates votes, weights, user disconnects, and karma rewards/penalties.
func TestFSMDemocracy(t *testing.T) {
	handler := &mockActionHandler{}
	fsm := NewJukeboxFSM(10, handler)

	fsm.AddUser("userA")
	fsm.AddUser("userB")
	fsm.AddUser("userC")

	// Trigger Add
	track := models.Track{ID: "track1", UserID: "userA"}
	fsm.ProcessEvent(EventAdd, track)
	time.Sleep(10 * time.Millisecond)
	fsm.ProcessEvent(EventAckOk, nil)

	// 3 users. Threshold = floor(3/2) + 1 = 2 votes.
	// userA votes to skip. Total votes weight = 1.0 (threshold 2.0). Skip pending.
	if ok := fsm.VoteSkip("userA"); !ok {
		t.Errorf("vote should be registered")
	}

	state, _, _ := fsm.GetState()
	if state != StatePlaying {
		t.Errorf("should still be playing, got %s", state.String())
	}

	// userB votes to skip. Total votes weight = 2.0. Skip authorized! Should transition to transitioning.
	fsm.VoteSkip("userB")
	time.Sleep(10 * time.Millisecond)

	state, _, _ = fsm.GetState()
	if state != StateTransitioning {
		t.Errorf("expected state Transitioning after skip authorized, got %s", state.String())
	}

	// Since userA's track got skipped, their karma should be penalized by beta (1.5)
	karma := fsm.GetKarma()
	if karma["userA"] != -1.5 {
		t.Errorf("expected userA karma to be -1.5, got %v", karma["userA"])
	}

	// userA weight should now be w(-1.5) = 1.0 - 1.5 = -0.5 -> max(0.1, -0.5) = 0.1
	weight := fsm.userWeight("userA")
	if weight != 0.1 {
		t.Errorf("expected userA weight to be 0.1, got %v", weight)
	}
}

// TestFSMDisconnectDemocracy validates skip when a user disconnects and reduces the threshold.
func TestFSMDisconnectDemocracy(t *testing.T) {
	handler := &mockActionHandler{}
	fsm := NewJukeboxFSM(10, handler)

	fsm.AddUser("userA")
	fsm.AddUser("userB")

	track := models.Track{ID: "track1", UserID: "userA"}
	fsm.ProcessEvent(EventAdd, track)
	time.Sleep(10 * time.Millisecond)
	fsm.ProcessEvent(EventAckOk, nil)

	// 2 users active. Threshold = floor(2/2) + 1 = 2 votes.
	// userB votes to skip. Votes = 1. Skip pending.
	fsm.VoteSkip("userB")

	state, _, _ := fsm.GetState()
	if state != StatePlaying {
		t.Errorf("should still be playing")
	}

	// userA (who didn't vote skip) disconnects. Active users is now only {"userB"}.
	// Votes = {"userB"}, ActiveUsers = {"userB"}. Threshold = floor(1/2) + 1 = 1.
	// Votes weight = 1.0 >= 1.0. Skip authorized!
	fsm.RemoveUser("userA")
	time.Sleep(10 * time.Millisecond)

	state, _, _ = fsm.GetState()
	if state != StateTransitioning {
		t.Errorf("expected skip to trigger on disconnect, got state %s", state.String())
	}
}

// TestFSMAckFailRetry verifies retry and hardware_unreachable state resets.
func TestFSMAckFailRetry(t *testing.T) {
	handler := &mockActionHandler{playErr: errors.New("network timeout")}
	fsm := NewJukeboxFSM(10, handler)

	track := models.Track{ID: "track1"}
	fsm.ProcessEvent(EventAdd, track)
	time.Sleep(10 * time.Millisecond)

	// Init transitions to StateTransitioning, triggers play which fails, EventAckFail injected.
	// fsm should retry up to 3 times, then go to StateIdle.
	// Wait for goroutines and retries to finish
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
