package persist

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/jesuslangarica/sonosApp/internal/player"
)

// DeltaOp represents a type of delta mutation on the player's queue state.
type DeltaOp string

const (
	OpAppend  DeltaOp = "append"
	OpDequeue DeltaOp = "dequeue"
	OpSkip    DeltaOp = "skip"
	OpVolume  DeltaOp = "volume"
	OpClear   DeltaOp = "clear"
)

// Delta represents a differential log entry written to state.log.
type Delta struct {
	Op        DeltaOp         `json:"op"`
	Data      json.RawMessage `json:"data,omitempty"`
	Timestamp time.Time       `json:"timestamp"`
}

// Persister manages state.log (Append-Only JSON deltas) and state.json (FSM snapshot).
// It coordinates safe concurrent reads/writes and atomic rotation of snapshots.
type Persister struct {
	logPath    string
	snapPath   string
	mu         sync.Mutex // (Nivel 3: persist.mu) Protects internal deltaCount, maxDeltas, and file operations.
	deltaCount int
	maxDeltas  int
}

// NewPersister initializes a new Persister.
func NewPersister(logPath, snapPath string) *Persister {
	return &Persister{
		logPath:   logPath,
		snapPath:  snapPath,
		maxDeltas: 1000,
	}
}

// SetMaxDeltas sets the threshold for maximum deltas before a snapshot is triggered.
func (p *Persister) SetMaxDeltas(max int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.maxDeltas = max
}

// WriteDelta writes a delta entry to the log path. If deltaCount reaches maxDeltas,
// a full snapshot is written and the log is truncated.
//
// In alignment with the lock ordering hierarchy: FSM Lock ≺ Democracy Lock ≺ Persist Lock ≺ Cache Lock,
// we DO NOT hold p.mu when calling any FSM methods.
func (p *Persister) WriteDelta(op DeltaOp, data interface{}, fsm *player.JukeboxFSM) error {
	var rawData json.RawMessage
	if data != nil {
		bytes, err := json.Marshal(data)
		if err != nil {
			return fmt.Errorf("failed to marshal delta data: %w", err)
		}
		rawData = json.RawMessage(bytes)
	}

	delta := Delta{
		Op:        op,
		Data:      rawData,
		Timestamp: time.Now(),
	}

	deltaBytes, err := json.Marshal(delta)
	if err != nil {
		return fmt.Errorf("failed to marshal delta: %w", err)
	}
	deltaBytes = append(deltaBytes, '\n')

	p.mu.Lock()
	// Ensure parent directory exists for logPath
	if err := os.MkdirAll(filepath.Dir(p.logPath), 0755); err != nil {
		p.mu.Unlock()
		return fmt.Errorf("failed to create log path directory: %w", err)
	}

	f, err := os.OpenFile(p.logPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		p.mu.Unlock()
		return fmt.Errorf("failed to open log file: %w", err)
	}

	if _, err := f.Write(deltaBytes); err != nil {
		_ = f.Close()
		p.mu.Unlock()
		return fmt.Errorf("failed to write delta to log: %w", err)
	}
	_ = f.Close()

	p.deltaCount++
	shouldSnapshot := p.deltaCount >= p.maxDeltas
	p.mu.Unlock()

	if shouldSnapshot {
		slog.Info("Max deltas threshold reached; generating state snapshot", "deltaCount", p.deltaCount, "maxDeltas", p.maxDeltas)
		// ExportSnapshot acquires FSM's locks. Since we released p.mu, we adhere to the Lock hierarchy.
		snap := fsm.ExportSnapshot()

		p.mu.Lock()
		defer p.mu.Unlock()
		// Double check under lock if we still need to snapshot (avoiding duplicate writes in parallel workloads)
		if p.deltaCount >= p.maxDeltas {
			if err := p.writeSnapshotLocked(snap); err != nil {
				return err
			}
		}
	}

	return nil
}

// WriteSnapshot serializes the full state into state.json and truncates state.log.
func (p *Persister) WriteSnapshot(snap player.JukeboxStateSnapshot) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.writeSnapshotLocked(snap)
}

// writeSnapshotLocked performs the actual snapshot writing and log truncation.
// Must be called with p.mu held.
func (p *Persister) writeSnapshotLocked(snap player.JukeboxStateSnapshot) error {
	if err := os.MkdirAll(filepath.Dir(p.snapPath), 0755); err != nil {
		return fmt.Errorf("failed to create snap path directory: %w", err)
	}

	snapBytes, err := json.Marshal(snap)
	if err != nil {
		return fmt.Errorf("failed to marshal snapshot: %w", err)
	}

	// Write to temporary file and rename atomically to guarantee integrity.
	tempFile := p.snapPath + ".tmp"
	if err := os.WriteFile(tempFile, snapBytes, 0644); err != nil {
		return fmt.Errorf("failed to write temporary snapshot file: %w", err)
	}
	if err := os.Rename(tempFile, p.snapPath); err != nil {
		_ = os.Remove(tempFile)
		return fmt.Errorf("failed to rename snapshot file: %w", err)
	}

	// Truncate the log file to 0 bytes.
	if err := os.Truncate(p.logPath, 0); err != nil {
		return fmt.Errorf("failed to truncate log file: %w", err)
	}

	p.deltaCount = 0
	slog.Info("State snapshot written successfully and delta log truncated", "snapPath", p.snapPath, "logPath", p.logPath)
	return nil
}

// Restore loads the snapshot from snapPath (if it exists) and then replays the delta logs from logPath.
// To guarantee lock safety, no Persister locks are held during calls to JukeboxFSM.
func (p *Persister) Restore(fsm *player.JukeboxFSM) error {
	p.mu.Lock()

	// 1. Read Snapshot file if it exists
	var snapBytes []byte
	var snapExists bool
	if _, err := os.Stat(p.snapPath); err == nil {
		snapBytes, err = os.ReadFile(p.snapPath)
		if err != nil {
			p.mu.Unlock()
			return fmt.Errorf("failed to read snapshot file: %w", err)
		}
		snapExists = true
	} else if !errors.Is(err, os.ErrNotExist) {
		p.mu.Unlock()
		return fmt.Errorf("failed to stat snapshot file: %w", err)
	}

	// 2. Read all Delta logs
	var deltas []Delta
	if _, err := os.Stat(p.logPath); err == nil {
		f, err := os.Open(p.logPath)
		if err != nil {
			p.mu.Unlock()
			return fmt.Errorf("failed to open log file for restore: %w", err)
		}
		defer f.Close()

		scanner := bufio.NewScanner(f)
		for scanner.Scan() {
			line := scanner.Bytes()
			if len(line) == 0 {
				continue
			}
			var d Delta
			if err := json.Unmarshal(line, &d); err != nil {
				p.mu.Unlock()
				return fmt.Errorf("failed to unmarshal delta line: %w", err)
			}
			deltas = append(deltas, d)
		}
		if err := scanner.Err(); err != nil {
			p.mu.Unlock()
			return fmt.Errorf("failed to scan log file: %w", err)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		p.mu.Unlock()
		return fmt.Errorf("failed to stat log file: %w", err)
	}

	p.mu.Unlock()

	// Apply snapshot if available (locks FSM internally, no Persister lock is held)
	if snapExists {
		var snap player.JukeboxStateSnapshot
		if err := json.Unmarshal(snapBytes, &snap); err != nil {
			return fmt.Errorf("failed to unmarshal snapshot data: %w", err)
		}
		fsm.ImportSnapshot(snap)
		slog.Info("Restored base FSM state from snapshot", "path", p.snapPath)
	}

	// Apply deltas sequentially (locks FSM internally, no Persister lock is held)
	for i, d := range deltas {
		if err := fsm.ApplyDelta(string(d.Op), d.Data); err != nil {
			return fmt.Errorf("failed to apply delta at index %d (op=%s): %w", i, d.Op, err)
		}
	}
	if len(deltas) > 0 {
		slog.Info("Successfully replayed deltas onto FSM", "count", len(deltas))
	}

	// Restore deltaCount
	p.mu.Lock()
	p.deltaCount = len(deltas)
	p.mu.Unlock()

	return nil
}
