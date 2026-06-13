package persist

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/jesuslangarica/sonosApp/internal/models"
	"github.com/jesuslangarica/sonosApp/internal/player"
)

// MockActionHandler is a dummy action handler for testing.
type MockActionHandler struct{}

func (m *MockActionHandler) PlayTrack(track models.Track, volume int) error { return nil }
func (m *MockActionHandler) PauseTrack() error                            { return nil }
func (m *MockActionHandler) ResumeTrack() error                           { return nil }
func (m *MockActionHandler) SetVolume(volume int) error                   { return nil }

func TestWriteDeltaAndRestore(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "persist-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	logPath := filepath.Join(tempDir, "state.log")
	snapPath := filepath.Join(tempDir, "state.json")

	p := NewPersister(logPath, snapPath)
	p.SetMaxDeltas(100) // high threshold to prevent early snapshot

	handler := &MockActionHandler{}
	fsm := player.NewJukeboxFSM(15, handler)

	// Simulate some activity on the FSM
	track1 := models.Track{
		ID:     "track-1",
		UserID: "user-alice",
		Src:    models.SourceYoutube,
		URL:    "https://youtube.com/watch?v=123",
		Dur:    180,
	}
	track2 := models.Track{
		ID:     "track-2",
		UserID: "user-bob",
		Src:    models.SourceSMBLocal,
		URL:    "smb://server/music/track2.mp3",
		Dur:    240,
	}

	// 1. Append Track 1
	fsm.ProcessEvent(player.EventAdd, track1)
	if err := p.WriteDelta(OpAppend, track1, fsm); err != nil {
		t.Fatalf("failed to write delta: %v", err)
	}

	// 2. Append Track 2
	fsm.ProcessEvent(player.EventAdd, track2)
	if err := p.WriteDelta(OpAppend, track2, fsm); err != nil {
		t.Fatalf("failed to write delta: %v", err)
	}

	// 3. Set Volume
	fsm.ProcessEvent(player.EventSetVolume, 45)
	if err := p.WriteDelta(OpVolume, 45, fsm); err != nil {
		t.Fatalf("failed to write delta: %v", err)
	}

	// 4. Simulate Dequeue (EventAckOk starts playing first item)
	fsm.ProcessEvent(player.EventAckOk, nil)
	if err := p.WriteDelta(OpDequeue, nil, fsm); err != nil {
		t.Fatalf("failed to write delta: %v", err)
	}

	// 5. Simulate Skip
	fsm.ProcessEvent(player.EventSkip, nil)
	if err := p.WriteDelta(OpSkip, nil, fsm); err != nil {
		t.Fatalf("failed to write delta: %v", err)
	}

	// Get a snapshot of original FSM state for verification
	originalSnap := fsm.ExportSnapshot()

	// Create a new FSM and restore state
	newFsm := player.NewJukeboxFSM(15, handler)
	if err := p.Restore(newFsm); err != nil {
		t.Fatalf("failed to restore: %v", err)
	}

	restoredSnap := newFsm.ExportSnapshot()

	// Compare queue contents, current track, volume, history, karma
	if len(originalSnap.Queue) != len(restoredSnap.Queue) {
		t.Errorf("expected queue length %d, got %d", len(originalSnap.Queue), len(restoredSnap.Queue))
	} else {
		for i := range originalSnap.Queue {
			if originalSnap.Queue[i].ID != restoredSnap.Queue[i].ID {
				t.Errorf("queue mismatch at index %d: expected %s, got %s", i, originalSnap.Queue[i].ID, restoredSnap.Queue[i].ID)
			}
		}
	}

	if (originalSnap.CurrentTrack == nil) != (restoredSnap.CurrentTrack == nil) {
		t.Errorf("current track presence mismatch: expected %v, got %v", originalSnap.CurrentTrack != nil, restoredSnap.CurrentTrack != nil)
	} else if originalSnap.CurrentTrack != nil {
		if originalSnap.CurrentTrack.ID != restoredSnap.CurrentTrack.ID {
			t.Errorf("current track ID mismatch: expected %s, got %s", originalSnap.CurrentTrack.ID, restoredSnap.CurrentTrack.ID)
		}
	}

	if originalSnap.Volume != restoredSnap.Volume {
		t.Errorf("volume mismatch: expected %d, got %d", originalSnap.Volume, restoredSnap.Volume)
	}
}

func TestSnapshotRotation(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "persist-rotate-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	logPath := filepath.Join(tempDir, "state.log")
	snapPath := filepath.Join(tempDir, "state.json")

	p := NewPersister(logPath, snapPath)
	p.SetMaxDeltas(3) // Rotates after 3 deltas

	handler := &MockActionHandler{}
	fsm := player.NewJukeboxFSM(10, handler)

	track := models.Track{ID: "track-rot", Src: models.SourceYoutube}

	// First delta
	if err := p.WriteDelta(OpAppend, track, fsm); err != nil {
		t.Fatalf("write delta 1 error: %v", err)
	}
	// Second delta
	if err := p.WriteDelta(OpVolume, 25, fsm); err != nil {
		t.Fatalf("write delta 2 error: %v", err)
	}

	// Verify log file contains 2 lines and snapshot does not exist yet
	logData, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("failed to read log path: %v", err)
	}
	// Count newlines
	lines := 0
	for _, b := range logData {
		if b == '\n' {
			lines++
		}
	}
	if lines != 2 {
		t.Errorf("expected 2 log entries, got %d", lines)
	}

	if _, err := os.Stat(snapPath); !os.IsNotExist(err) {
		t.Errorf("snapshot file should not exist yet")
	}

	// Third delta should trigger snapshot and truncate log
	if err := p.WriteDelta(OpClear, nil, fsm); err != nil {
		t.Fatalf("write delta 3 error: %v", err)
	}

	// Verify log file is now truncated (0 bytes) and snapshot exists
	logData, err = os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("failed to read log path after rotation: %v", err)
	}
	if len(logData) != 0 {
		t.Errorf("expected log file to be truncated to 0 bytes, got %d bytes", len(logData))
	}

	if _, err := os.Stat(snapPath); os.IsNotExist(err) {
		t.Errorf("snapshot file should exist after rotation")
	}

	// Check deltaCount reset
	p.mu.Lock()
	cnt := p.deltaCount
	p.mu.Unlock()
	if cnt != 0 {
		t.Errorf("expected delta count to reset to 0, got %d", cnt)
	}

	// Let's add a fourth delta to make sure it appends to the empty log
	if err := p.WriteDelta(OpVolume, 50, fsm); err != nil {
		t.Fatalf("write delta 4 error: %v", err)
	}

	logData, err = os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("failed to read log path after 4th delta: %v", err)
	}
	if len(logData) == 0 {
		t.Errorf("expected log data to contain the 4th delta, but it was empty")
	}
}

func TestRestoreWithSnapshotAndDeltas(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "persist-mixed-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	logPath := filepath.Join(tempDir, "state.log")
	snapPath := filepath.Join(tempDir, "state.json")

	p := NewPersister(logPath, snapPath)

	handler := &MockActionHandler{}
	fsm := player.NewJukeboxFSM(10, handler)

	track := models.Track{ID: "track-mixed", Src: models.SourceYoutube}
	fsm.ProcessEvent(player.EventAdd, track)

	// Manually write a snapshot
	snap := fsm.ExportSnapshot()
	if err := p.WriteSnapshot(snap); err != nil {
		t.Fatalf("failed to write snapshot: %v", err)
	}

	// Now append a delta after the snapshot
	if err := p.WriteDelta(OpVolume, 80, fsm); err != nil {
		t.Fatalf("failed to write delta: %v", err)
	}

	// Restore state into a fresh FSM
	newFsm := player.NewJukeboxFSM(10, handler)
	if err := p.Restore(newFsm); err != nil {
		t.Fatalf("failed to restore: %v", err)
	}

	// Verify both the snapshot data and the trailing delta are applied
	_, _, vol := newFsm.GetState()
	if vol != 80 {
		t.Errorf("expected restored volume to be 80 from delta, got %d", vol)
	}
	q := newFsm.GetQueue()
	if len(q) != 1 || q[0].ID != "track-mixed" {
		t.Errorf("expected queue to have 1 track 'track-mixed' from snapshot, got %v", q)
	}
}

func TestConcurrentWriteDelta(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "persist-concurrent-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	logPath := filepath.Join(tempDir, "state.log")
	snapPath := filepath.Join(tempDir, "state.json")

	p := NewPersister(logPath, snapPath)
	p.SetMaxDeltas(50)

	handler := &MockActionHandler{}
	fsm := player.NewJukeboxFSM(10, handler)

	const goroutines = 10
	const iterations = 10
	done := make(chan bool)

	for i := 0; i < goroutines; i++ {
		go func(id int) {
			for j := 0; j < iterations; j++ {
				track := models.Track{ID: "track", Src: models.SourceYoutube}
				_ = p.WriteDelta(OpAppend, track, fsm)
			}
			done <- true
		}(i)
	}

	for i := 0; i < goroutines; i++ {
		<-done
	}


	// Snapshot should be written if count exceeded maxDeltas, otherwise log contains goroutines * iterations.
	// Since goroutines * iterations = 100, and maxDeltas is 50, it must have rotated.
	// We verify that rotation took place and snapPath exists.
	if _, err := os.Stat(snapPath); os.IsNotExist(err) {
		t.Errorf("snapshot file should exist after rotation")
	}

	// Re-restore to verify parsing
	newFsm := player.NewJukeboxFSM(10, handler)
	if err := p.Restore(newFsm); err != nil {
		t.Errorf("failed to restore: %v", err)
	}
}

func TestEmptyRestore(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "persist-empty-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	logPath := filepath.Join(tempDir, "nonexistent.log")
	snapPath := filepath.Join(tempDir, "nonexistent.json")

	p := NewPersister(logPath, snapPath)
	handler := &MockActionHandler{}
	fsm := player.NewJukeboxFSM(10, handler)

	// Restoring when files don't exist shouldn't fail. It should do nothing.
	if err := p.Restore(fsm); err != nil {
		t.Errorf("Restore on non-existent files should succeed silently, got: %v", err)
	}
}
