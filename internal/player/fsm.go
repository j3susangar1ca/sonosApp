package player

import (
	"encoding/json"
	"log/slog"
	"math"
	"sync"
	"time"

	"github.com/jesuslangarica/sonosApp/internal/models"
)

// State represents the state of the FSM.
type State int

const (
	// StateIdle: system is waiting for tracks
	StateIdle State = iota
	// StatePlaying: system is playing a track
	StatePlaying
	// StatePaused: playback is paused
	StatePaused
	// StateTransitioning: system is resolving or loading next track
	StateTransitioning
	// StateAutoplay: playing recommended history tracks
	StateAutoplay
)

// String returns the string representation of the State.
func (s State) String() string {
	switch s {
	case StateIdle:
		return "idle"
	case StatePlaying:
		return "playing"
	case StatePaused:
		return "paused"
	case StateTransitioning:
		return "transitioning"
	case StateAutoplay:
		return "autoplay"
	default:
		return "unknown"
	}
}

// Event represents an FSM event trigger.
type Event int

const (
	// EventAdd: track added to the queue
	EventAdd Event = iota
	// EventPlay: play command received
	EventPlay
	// EventPause: pause command received
	EventPause
	// EventResume: resume command received
	EventResume
	// EventSkip: skip command received
	EventSkip
	// EventAckOk: hardware confirmed command ok
	EventAckOk
	// EventAckFail: hardware confirmed command fail
	EventAckFail
	// EventEOF: current track ended
	EventEOF
	// EventClear: clear queue command received
	EventClear
	// EventQueueEmpty: queue empty check
	EventQueueEmpty
	// EventSetVolume: volume change command received
	EventSetVolume
)

// String returns the string representation of the Event.
func (e Event) String() string {
	switch e {
	case EventAdd:
		return "add"
	case EventPlay:
		return "play"
	case EventPause:
		return "pause"
	case EventResume:
		return "resume"
	case EventSkip:
		return "skip"
	case EventAckOk:
		return "ack_ok"
	case EventAckFail:
		return "ack_fail"
	case EventEOF:
		return "eof"
	case EventClear:
		return "clear"
	case EventQueueEmpty:
		return "queue_empty"
	case EventSetVolume:
		return "set_volume"
	default:
		return "unknown"
	}
}

// isUserEvent returns true if the event is triggered by a user action.
func isUserEvent(event Event) bool {
	switch event {
	case EventAdd, EventPause, EventResume, EventSkip, EventSetVolume, EventClear:
		return true
	default:
		return false
	}
}

// deferredEvent wraps an event and its payload for buffering during transitions.
type deferredEvent struct {
	event   Event
	payload interface{}
}

// HistoryEntry represents an entry in the playback history.
type HistoryEntry struct {
	Track     models.Track
	Timestamp time.Time
}

// ActionHandler defines the actions that the player must invoke on hardware.
type ActionHandler interface {
	PlayTrack(track models.Track, volume int) error
	PauseTrack() error
	ResumeTrack() error
	SetVolume(volume int) error
}

// FSMObserver is notified when internal state or volume changes.
type FSMObserver interface {
	OnStateChange(oldState, newState State, currentTrack *models.Track, volume int)
	OnVolumeChange(volume int)
}

// JukeboxFSM implements the state machine, democracy, and history scoring.
type JukeboxFSM struct {
	mu           sync.RWMutex // Protects FSM state, queue, currentTrack, volume, B_cmd, history
	demoMu       sync.Mutex   // Protects activeUsers, skipVotes, and karma (FSM Lock < Democracy Lock)
	state        State
	queue        *RingBuffer[models.Track]
	currentTrack *models.Track
	volume       int
	history      []HistoryEntry
	trackStats   map[string]*models.Track // Tracks play/skip count across enqueue cycles

	// Democracy state
	activeUsers map[string]bool
	skipVotes   map[string]bool
	karma       map[string]float64
	alpha       float64 // Karma reward weight
	beta        float64 // Karma penalty weight

	// Deferred command buffer
	deferredCmds []deferredEvent

	// Retry control
	retryCounter int
	maxRetries   int

	// Integrations
	actionHandler ActionHandler
	observers     []FSMObserver
	lambda        float64 // Decay factor for autoplay scoring
}

// NewJukeboxFSM creates and initializes a JukeboxFSM.
func NewJukeboxFSM(initialVolume int, handler ActionHandler) *JukeboxFSM {
	return &JukeboxFSM{
		state:         StateIdle,
		queue:         NewRingBuffer[models.Track](32),
		volume:        initialVolume,
		trackStats:    make(map[string]*models.Track),
		activeUsers:   make(map[string]bool),
		skipVotes:     make(map[string]bool),
		karma:         make(map[string]float64),
		alpha:         1.0,
		beta:          1.5,
		maxRetries:    3,
		actionHandler: handler,
		lambda:        0.0001, // Default decay factor
	}
}

// RegisterObserver registers an FSMObserver.
func (f *JukeboxFSM) RegisterObserver(obs FSMObserver) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.observers = append(f.observers, obs)
}

// AddUser adds a user to the active users set.
func (f *JukeboxFSM) AddUser(userID string) {
	f.demoMu.Lock()
	defer f.demoMu.Unlock()
	f.activeUsers[userID] = true
	slog.Info("User connected", "user_id", userID)
}

// RemoveUser removes a user from active users and cleans up their votes.
// Re-evaluates skip democracy immediately.
func (f *JukeboxFSM) RemoveUser(userID string) {
	f.mu.Lock()
	f.demoMu.Lock()

	delete(f.activeUsers, userID)
	delete(f.skipVotes, userID)

	slog.Info("User disconnected", "user_id", userID)
	authorized := f.isSkipAuthorized()

	f.demoMu.Unlock()
	f.mu.Unlock()

	if authorized && (f.state == StatePlaying || f.state == StatePaused) {
		slog.Info("Democracy threshold met after user disconnect, triggering skip")
		f.ProcessEvent(EventSkip, nil)
	}
}

// VoteSkip registers a skip vote from the user.
// Returns true if the vote was registered.
func (f *JukeboxFSM) VoteSkip(userID string) bool {
	f.mu.Lock()
	f.demoMu.Lock()

	if !f.activeUsers[userID] {
		f.demoMu.Unlock()
		f.mu.Unlock()
		return false
	}
	if f.currentTrack == nil {
		f.demoMu.Unlock()
		f.mu.Unlock()
		return false
	}

	f.skipVotes[userID] = true
	slog.Info("User voted to skip", "user_id", userID, "track_id", f.currentTrack.ID)
	authorized := f.isSkipAuthorized()

	f.demoMu.Unlock()
	f.mu.Unlock()

	if authorized && (f.state == StatePlaying || f.state == StatePaused) {
		slog.Info("Democracy threshold met after vote, triggering skip")
		f.ProcessEvent(EventSkip, nil)
	}
	return true
}

// userWeight calculates user weight using karma.
// f(k) = max(0.1, 1.0 + k)
func (f *JukeboxFSM) userWeight(userID string) float64 {
	k := f.karma[userID]
	w := 1.0 + k
	if w < 0.1 {
		return 0.1
	}
	return w
}

// isSkipAuthorized checks if the democracy threshold for skip has been met.
// must be called under Lock or RLock.
func (f *JukeboxFSM) isSkipAuthorized() bool {
	if len(f.activeUsers) == 0 {
		return false
	}

	var totalWeight float64
	for u := range f.activeUsers {
		totalWeight += f.userWeight(u)
	}

	threshold := math.Floor(totalWeight/2.0) + 1.0

	var voteWeight float64
	for u := range f.skipVotes {
		if f.activeUsers[u] {
			voteWeight += f.userWeight(u)
		}
	}

	slog.Debug("Democracy check", "votes_weight", voteWeight, "threshold", threshold)
	return voteWeight >= threshold
}

// ProcessEvent processes incoming events.
// User events during transitions are buffered.
func (f *JukeboxFSM) ProcessEvent(event Event, payload interface{}) {
	f.mu.Lock()

	if f.state == StateTransitioning && isUserEvent(event) {
		f.deferredCmds = append(f.deferredCmds, deferredEvent{event: event, payload: payload})
		f.mu.Unlock()
		slog.Info("User event buffered during transitioning state", "event", event.String())
		return
	}

	oldState := f.state
	nextState, mutated := f.transition(event, payload)

	if mutated {
		f.state = nextState
		slog.Info("State transition", "event", event.String(), "old_state", oldState.String(), "new_state", nextState.String())

		if nextState != oldState {
			f.emitStateChange(oldState, nextState)
		}
	}

	// Post-transition autoplay resolution
	if f.state == StateAutoplay {
		track, found := f.SelectAutoplay(time.Now())
		if found {
			f.queue.Enqueue(track)
			f.state = StateTransitioning
			f.emitStateChange(oldState, StateTransitioning)
			f.retryCounter = f.maxRetries
			f.triggerPlayNext()
		} else {
			f.state = StateIdle
			f.emitStateChange(oldState, StateIdle)
		}
	}

	// Resolve B_cmd commands outside the critical section
	var toFlush []deferredEvent
	if f.state != StateTransitioning && len(f.deferredCmds) > 0 {
		toFlush = make([]deferredEvent, len(f.deferredCmds))
		copy(toFlush, f.deferredCmds)
		f.deferredCmds = nil
	}

	f.mu.Unlock()

	for _, cmd := range toFlush {
		slog.Info("Processing buffered user event", "event", cmd.event.String())
		f.ProcessEvent(cmd.event, cmd.payload)
	}
}

// transition performs the pure state calculation and mutations.
// must be called under Lock.
func (f *JukeboxFSM) transition(event Event, payload interface{}) (State, bool) {
	switch event {
	case EventAdd:
		if track, ok := payload.(models.Track); ok {
			// Restore history stats
			if stats, exists := f.trackStats[track.ID]; exists {
				track.PlayCount = stats.PlayCount
				track.SkipCount = stats.SkipCount
				track.TauLast = stats.TauLast
			}
			f.queue.Enqueue(track)
			slog.Info("Track added to queue", "track_id", track.ID, "queue_size", f.queue.Size())
		}
		if f.state == StateIdle {
			f.retryCounter = f.maxRetries
			f.triggerPlayNext()
			return StateTransitioning, true
		}
		return f.state, true

	case EventPlay:
		if f.state == StateIdle && f.queue.Size() > 0 {
			f.retryCounter = f.maxRetries
			f.triggerPlayNext()
			return StateTransitioning, true
		}

	case EventPause:
		if f.state == StatePlaying {
			if f.actionHandler != nil {
				go func() { _ = f.actionHandler.PauseTrack() }()
			}
			return StatePaused, true
		}

	case EventResume:
		if f.state == StatePaused {
			if f.actionHandler != nil {
				go func() { _ = f.actionHandler.ResumeTrack() }()
			}
			return StatePlaying, true
		}

	case EventSkip:
		if f.state == StatePlaying || f.state == StatePaused {
			// Update skip stats
			if f.currentTrack != nil {
				f.currentTrack.SkipCount++
				f.currentTrack.TauLast = time.Now()
				f.trackStats[f.currentTrack.ID] = f.currentTrack

				// Apply karma penalty to user who added it
				if f.currentTrack.UserID != "" {
					f.demoMu.Lock()
					f.karma[f.currentTrack.UserID] -= f.beta
					slog.Info("Karma penalized for skip", "user_id", f.currentTrack.UserID, "karma", f.karma[f.currentTrack.UserID])
					f.demoMu.Unlock()
				}
			}
			f.retryCounter = f.maxRetries
			f.triggerPlayNext()
			return StateTransitioning, true
		}

	case EventAckOk:
		if f.state == StateTransitioning {
			track, err := f.queue.Dequeue()
			if err == nil {
				f.currentTrack = &track
				// Clear skip votes for the new track
				f.demoMu.Lock()
				f.skipVotes = make(map[string]bool)
				f.demoMu.Unlock()
			}
			return StatePlaying, true
		}

	case EventAckFail:
		if f.state == StateTransitioning {
			if f.retryCounter > 0 {
				f.retryCounter--
				slog.Warn("Sonos command failed, retrying", "retries_left", f.retryCounter)
				f.triggerPlayNext()
				return StateTransitioning, true
			}
			slog.Error("Sonos command failed, retries exhausted. Resetting to Idle.")
			return StateIdle, true
		}

	case EventEOF:
		if f.state == StatePlaying {
			// Track finished playing successfully
			if f.currentTrack != nil {
				f.currentTrack.PlayCount++
				f.currentTrack.TauLast = time.Now()
				f.trackStats[f.currentTrack.ID] = f.currentTrack
				f.history = append(f.history, HistoryEntry{Track: *f.currentTrack, Timestamp: time.Now()})

				// Apply karma reward to user who added it
				if f.currentTrack.UserID != "" {
					f.demoMu.Lock()
					f.karma[f.currentTrack.UserID] += f.alpha
					slog.Info("Karma rewarded for completed play", "user_id", f.currentTrack.UserID, "karma", f.karma[f.currentTrack.UserID])
					f.demoMu.Unlock()
				}
			}

			if f.queue.Size() > 0 {
				f.retryCounter = f.maxRetries
				f.triggerPlayNext()
				return StateTransitioning, true
			}
			if len(f.history) > 0 {
				return StateAutoplay, true
			}
			return StateIdle, true
		}

	case EventClear:
		f.queue.Clear()
		f.currentTrack = nil
		f.demoMu.Lock()
		f.skipVotes = make(map[string]bool)
		f.demoMu.Unlock()
		return StateIdle, true

	case EventSetVolume:
		if level, ok := payload.(int); ok {
			if level < 0 {
				level = 0
			} else if level > 100 {
				level = 100
			}
			f.volume = level
			if f.actionHandler != nil {
				go func(l int) { _ = f.actionHandler.SetVolume(l) }(level)
			}
			f.emitVolumeChange(level)
			return f.state, true
		}
	}

	return f.state, false
}

// triggerPlayNext gets the track at the head of the queue and plays it asynchronously.
// must be called under Lock.
func (f *JukeboxFSM) triggerPlayNext() {
	track, err := f.queue.Head()
	if err != nil {
		// Queue empty, trigger EventEOF to resolve next state
		go f.ProcessEvent(EventEOF, nil)
		return
	}

	if f.actionHandler == nil {
		// No action handler, auto-ACK for testing purposes
		go f.ProcessEvent(EventAckOk, nil)
		return
	}

	vol := f.volume
	go func(t models.Track, v int) {
		err := f.actionHandler.PlayTrack(t, v)
		if err == nil {
			f.ProcessEvent(EventAckOk, nil)
		} else {
			f.ProcessEvent(EventAckFail, nil)
		}
	}(track, vol)
}

// SelectAutoplay picks the best track from history to play.
// Score(t, now) = (PlayCount + 1) / (SkipCount + 1) * exp(-lambda * (now - TauLast))
func (f *JukeboxFSM) SelectAutoplay(now time.Time) (models.Track, bool) {
	if len(f.history) == 0 {
		return models.Track{}, false
	}

	var bestTrack models.Track
	bestScore := -1.0
	found := false

	seen := make(map[string]bool)
	for _, entry := range f.history {
		trackID := entry.Track.ID
		if seen[trackID] {
			continue
		}
		seen[trackID] = true

		// Lookup fresh stats
		statTrack := entry.Track
		if stats, ok := f.trackStats[trackID]; ok {
			statTrack.PlayCount = stats.PlayCount
			statTrack.SkipCount = stats.SkipCount
			statTrack.TauLast = stats.TauLast
		}

		score := f.scoreTrack(statTrack, now)
		if !found || score > bestScore {
			bestScore = score
			bestTrack = statTrack
			found = true
		}
	}

	return bestTrack, found
}

func (f *JukeboxFSM) scoreTrack(t models.Track, now time.Time) float64 {
	ratio := float64(t.PlayCount+1) / float64(t.SkipCount+1)
	diff := now.Sub(t.TauLast).Seconds()
	if diff < 0 {
		diff = 0
	}
	decay := math.Exp(-f.lambda * diff)
	return ratio * decay
}

// emitStateChange notifies observers of a state change.
func (f *JukeboxFSM) emitStateChange(oldState, newState State) {
	for _, obs := range f.observers {
		obs.OnStateChange(oldState, newState, f.currentTrack, f.volume)
	}
}

// emitVolumeChange notifies observers of a volume change.
func (f *JukeboxFSM) emitVolumeChange(vol int) {
	for _, obs := range f.observers {
		obs.OnVolumeChange(vol)
	}
}

// GetState returns the current state snapshot.
func (f *JukeboxFSM) GetState() (State, *models.Track, int) {
	f.mu.RLock()
	defer f.mu.RUnlock()
	return f.state, f.currentTrack, f.volume
}

// GetQueue returns all tracks currently in the queue.
func (f *JukeboxFSM) GetQueue() []models.Track {
	f.mu.RLock()
	defer f.mu.RUnlock()
	return f.queue.All()
}

// GetActiveUsers returns a copy of the active users set.
func (f *JukeboxFSM) GetActiveUsers() map[string]bool {
	f.demoMu.Lock()
	defer f.demoMu.Unlock()
	users := make(map[string]bool, len(f.activeUsers))
	for u, active := range f.activeUsers {
		users[u] = active
	}
	return users
}

// GetVotes returns a copy of the skip votes set.
func (f *JukeboxFSM) GetVotes() map[string]bool {
	f.demoMu.Lock()
	defer f.demoMu.Unlock()
	votes := make(map[string]bool, len(f.skipVotes))
	for u, voted := range f.skipVotes {
		votes[u] = voted
	}
	return votes
}

// GetKarma returns a copy of user karma scores.
func (f *JukeboxFSM) GetKarma() map[string]float64 {
	f.demoMu.Lock()
	defer f.demoMu.Unlock()
	karmaCopy := make(map[string]float64, len(f.karma))
	for u, k := range f.karma {
		karmaCopy[u] = k
	}
	return karmaCopy
}

// JukeboxStateSnapshot represents a full snapshot of the FSM's play state and user karma.
type JukeboxStateSnapshot struct {
	Queue        []models.Track     `json:"queue"`
	CurrentTrack *models.Track      `json:"current_track"`
	Volume       int                `json:"volume"`
	History      []HistoryEntry     `json:"history"`
	Karma        map[string]float64 `json:"karma"`
}

// ExportSnapshot returns a deep copy of the FSM state.
func (f *JukeboxFSM) ExportSnapshot() JukeboxStateSnapshot {
	f.mu.RLock()
	defer f.mu.RUnlock()
	f.demoMu.Lock()
	defer f.demoMu.Unlock()

	var qTracks []models.Track
	if f.queue != nil {
		qTracks = f.queue.All()
	}

	karmaCopy := make(map[string]float64, len(f.karma))
	for u, k := range f.karma {
		karmaCopy[u] = k
	}

	// Copy history
	histCopy := make([]HistoryEntry, len(f.history))
	copy(histCopy, f.history)

	var currCopy *models.Track
	if f.currentTrack != nil {
		c := *f.currentTrack
		currCopy = &c
	}

	return JukeboxStateSnapshot{
		Queue:        qTracks,
		CurrentTrack: currCopy,
		Volume:       f.volume,
		History:      histCopy,
		Karma:        karmaCopy,
	}
}

// ImportSnapshot overrides the current FSM state with the given snapshot.
func (f *JukeboxFSM) ImportSnapshot(snap JukeboxStateSnapshot) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.demoMu.Lock()
	defer f.demoMu.Unlock()

	f.queue.Clear()
	for _, t := range snap.Queue {
		f.queue.Enqueue(t)
	}
	f.currentTrack = snap.CurrentTrack
	f.volume = snap.Volume
	f.history = snap.History

	f.karma = make(map[string]float64)
	for u, k := range snap.Karma {
		f.karma[u] = k
	}
}

// ApplyDelta replays a specific delta operation onto the FSM directly.
func (f *JukeboxFSM) ApplyDelta(op string, data []byte) error {
	f.mu.Lock()
	defer f.mu.Unlock()

	switch op {
	case "append":
		var t models.Track
		if err := json.Unmarshal(data, &t); err != nil {
			return err
		}
		// Restore stats if they exist
		if stats, exists := f.trackStats[t.ID]; exists {
			t.PlayCount = stats.PlayCount
			t.SkipCount = stats.SkipCount
			t.TauLast = stats.TauLast
		}
		f.queue.Enqueue(t)

	case "dequeue":
		track, err := f.queue.Dequeue()
		if err == nil {
			f.currentTrack = &track
		}

	case "skip":
		if f.currentTrack != nil {
			f.currentTrack.SkipCount++
			f.currentTrack.TauLast = time.Now()
			f.trackStats[f.currentTrack.ID] = f.currentTrack
		}

	case "volume":
		var vol int
		if err := json.Unmarshal(data, &vol); err != nil {
			return err
		}
		f.volume = vol

	case "clear":
		f.queue.Clear()
		f.currentTrack = nil
	}
	return nil
}
