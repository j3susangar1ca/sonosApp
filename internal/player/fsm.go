package player

import (
        "encoding/json"
        "log/slog"
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
        default:
                return "unknown"
        }
}

// Event represents an FSM event trigger.
type Event int

const (
        // EventAdd: track added to the queue
        EventAdd Event = iota
        // EventPlayNow: track added and should play immediately (skip current)
        EventPlayNow
        // EventPlayFromQueue: play a specific track from the queue
        EventPlayFromQueue
        // EventPlay: play command received
        EventPlay
        // EventPause: pause command received
        EventPause
        // EventResume: resume command received
        EventResume
        // EventSkip: skip current track
        EventSkip
        // EventAckOk: Sonos confirmed playback started
        EventAckOk
        // EventAckFail: Sonos rejected playback
        EventAckFail
        // EventEOF: track finished playing
        EventEOF
        // EventClear: clear the entire queue
        EventClear
        // EventSetVolume: volume change
        EventSetVolume
        // EventRemoveFromQueue: remove a specific track from queue
        EventRemoveFromQueue
)

// ActionHandler defines the interface for hardware-specific playback operations.
type ActionHandler interface {
        PlayTrack(track models.Track, volume int) error
        PauseTrack() error
        ResumeTrack() error
        SetVolume(volume int) error
}

// FSMObserver receives callbacks on FSM state and volume changes.
type FSMObserver interface {
        OnStateChange(oldState, newState State, currentTrack *models.Track, volume int)
        OnVolumeChange(volume int)
}

// JukeboxFSM is the finite state machine that governs the jukebox lifecycle.
type JukeboxFSM struct {
        mu            sync.Mutex
        state         State
        queue         *RingBuffer[models.Track]
        currentTrack  *models.Track
        volume        int
        actionHandler ActionHandler
        observers     []FSMObserver
        activeUsers   map[string]bool
        retryCount    int
        maxRetries    int
        // Deferred command buffer for events during Transitioning
        deferredCmds []deferredCmd
}

type deferredCmd struct {
        event Event
        data  interface{}
}

// JukeboxStateSnapshot is the serializable form of the FSM for persistence.
type JukeboxStateSnapshot struct {
        State        State         `json:"state"`
        Queue        []models.Track `json:"queue"`
        CurrentTrack *models.Track `json:"current_track"`
        Volume       int           `json:"volume"`
        ActiveUsers  []string      `json:"active_users"`
}

// NewJukeboxFSM creates a new FSM with the given initial volume and action handler.
func NewJukeboxFSM(initialVolume int, handler ActionHandler) *JukeboxFSM {
        return &JukeboxFSM{
                state:         StateIdle,
                queue:         NewRingBuffer[models.Track](16),
                volume:        initialVolume,
                actionHandler: handler,
                activeUsers:   make(map[string]bool),
                maxRetries:    3,
        }
}

// ProcessEvent is the main entry point for FSM state transitions.
func (f *JukeboxFSM) ProcessEvent(event Event, data interface{}) {
        f.mu.Lock()
        defer f.mu.Unlock()

        // If we're transitioning, buffer events for later replay
        if f.state == StateTransitioning && event != EventAckOk && event != EventAckFail {
                f.deferredCmds = append(f.deferredCmds, deferredCmd{event: event, data: data})
                slog.Info("FSM: buffering event during transition", "event", eventName(event), "buffered_count", len(f.deferredCmds))
                return
        }

        f.processEventInternal(event, data)
}

func (f *JukeboxFSM) processEventInternal(event Event, data interface{}) {
        switch event {
        case EventAdd:
                f.handleAdd(data)
        case EventPlayNow:
                f.handlePlayNow(data)
        case EventPlayFromQueue:
                f.handlePlayFromQueue(data)
        case EventPlay:
                f.handlePlay()
        case EventPause:
                f.handlePause()
        case EventResume:
                f.handleResume()
        case EventSkip:
                f.handleSkip()
        case EventAckOk:
                f.handleAckOk()
        case EventAckFail:
                f.handleAckFail()
        case EventEOF:
                f.handleEOF()
        case EventClear:
                f.handleClear()
        case EventSetVolume:
                f.handleSetVolume(data)
        case EventRemoveFromQueue:
                f.handleRemoveFromQueue(data)
        }
}

func eventName(e Event) string {
        switch e {
        case EventAdd:
                return "Add"
        case EventPlayNow:
                return "PlayNow"
        case EventPlayFromQueue:
                return "PlayFromQueue"
        case EventPlay:
                return "Play"
        case EventPause:
                return "Pause"
        case EventResume:
                return "Resume"
        case EventSkip:
                return "Skip"
        case EventAckOk:
                return "AckOk"
        case EventAckFail:
                return "AckFail"
        case EventEOF:
                return "EOF"
        case EventClear:
                return "Clear"
        case EventSetVolume:
                return "SetVolume"
        case EventRemoveFromQueue:
                return "RemoveFromQueue"
        default:
                return "Unknown"
        }
}

// handleAdd enqueues a track and starts playback if idle.
func (f *JukeboxFSM) handleAdd(data interface{}) {
        track, ok := data.(models.Track)
        if !ok {
                slog.Error("FSM: EventAdd received non-Track data")
                return
        }
        f.queue.Enqueue(track)
        slog.Info("FSM: track enqueued", "track_id", track.ID, "title", track.Meta.Title, "queue_size", f.queue.Size())

        if f.state == StateIdle {
                f.triggerPlayNext()
        }
}

// handlePlayNow adds a track to the front of the queue and immediately plays it.
// If a track is currently playing, it gets skipped.
func (f *JukeboxFSM) handlePlayNow(data interface{}) {
        track, ok := data.(models.Track)
        if !ok {
                slog.Error("FSM: EventPlayNow received non-Track data")
                return
        }

        // Insert at front of queue by creating a new queue with this track first
        items := f.queue.All()
        newItems := []models.Track{track}
        newItems = append(newItems, items...)
        f.queue.Clear()
        for _, t := range newItems {
                f.queue.Enqueue(t)
        }

        slog.Info("FSM: track inserted at front for immediate playback", "track_id", track.ID, "title", track.Meta.Title)

        // If idle or playing, trigger play next (which will pick the front track)
        if f.state == StateIdle {
                f.triggerPlayNext()
        } else if f.state == StatePlaying || f.state == StatePaused {
                // Skip current to play the new track
                f.triggerPlayNext()
        }
        // If transitioning, the deferred replay will handle it
}

// handlePlayFromQueue moves a specific track to the front of the queue and plays it.
func (f *JukeboxFSM) handlePlayFromQueue(data interface{}) {
        trackID, ok := data.(string)
        if !ok {
                slog.Error("FSM: EventPlayFromQueue received non-string data")
                return
        }

        // Find and extract the track from the queue
        items := f.queue.All()
        var found *models.Track
        var remaining []models.Track
        for i := range items {
                if items[i].ID == trackID {
                        found = &items[i]
                } else {
                        remaining = append(remaining, items[i])
                }
        }

        if found == nil {
                slog.Warn("FSM: track not found in queue for PlayFromQueue", "track_id", trackID)
                return
        }

        // Rebuild queue with found track at front
        f.queue.Clear()
        f.queue.Enqueue(*found)
        for _, t := range remaining {
                f.queue.Enqueue(t)
        }

        slog.Info("FSM: track moved to front for playback", "track_id", trackID, "title", found.Meta.Title)

        if f.state == StateIdle {
                f.triggerPlayNext()
        } else if f.state == StatePlaying || f.state == StatePaused {
                f.triggerPlayNext()
        }
}

// handlePlay starts playback if idle and queue is non-empty.
func (f *JukeboxFSM) handlePlay() {
        if f.state == StateIdle && f.queue.Size() > 0 {
                f.triggerPlayNext()
        }
}

// handlePause pauses the current track.
func (f *JukeboxFSM) handlePause() {
        if f.state != StatePlaying {
                return
        }
        if f.actionHandler != nil {
                if err := f.actionHandler.PauseTrack(); err != nil {
                        slog.Error("FSM: PauseTrack failed", "error", err)
                        return
                }
        }
        f.transitionState(StatePaused)
}

// handleResume resumes from paused state.
func (f *JukeboxFSM) handleResume() {
        if f.state != StatePaused {
                return
        }
        if f.actionHandler != nil {
                if err := f.actionHandler.ResumeTrack(); err != nil {
                        slog.Error("FSM: ResumeTrack failed", "error", err)
                        return
                }
        }
        f.transitionState(StatePlaying)
}

// handleSkip skips the current track and plays the next one.
func (f *JukeboxFSM) handleSkip() {
        if f.state == StatePlaying || f.state == StatePaused {
                slog.Info("FSM: skipping current track", "current_track_id", func() string {
                        if f.currentTrack != nil {
                                return f.currentTrack.ID
                        }
                        return "none"
                }())
                f.triggerPlayNext()
        }
}

// handleAckOk is called when Sonos confirms playback started successfully.
func (f *JukeboxFSM) handleAckOk() {
        if f.state != StateTransitioning {
                return
        }

        // Dequeue the track that just started playing
        _, _ = f.queue.Dequeue()
        f.retryCount = 0
        f.transitionState(StatePlaying)

        // Replay any deferred commands
        f.replayDeferred()
}

// handleAckFail is called when Sonos rejects playback.
func (f *JukeboxFSM) handleAckFail() {
        if f.state != StateTransitioning {
                return
        }

        f.retryCount++
        slog.Warn("FSM: playback ack failed", "retry_count", f.retryCount, "max_retries", f.maxRetries)

        if f.retryCount >= f.maxRetries {
                slog.Error("FSM: max retries exceeded, returning to idle")
                f.retryCount = 0
                // Remove the failed track from queue
                _, _ = f.queue.Dequeue()
                f.currentTrack = nil
                f.transitionState(StateIdle)

                // Try next track if available
                if f.queue.Size() > 0 {
                        f.triggerPlayNext()
                }

                f.replayDeferred()
                return
        }

        // Retry: trigger play next again
        f.triggerPlayNext()
}

// handleEOF is called when the current track finishes playing.
func (f *JukeboxFSM) handleEOF() {
        if f.state != StatePlaying {
                return
        }

        f.currentTrack = nil

        if f.queue.Size() > 0 {
                f.triggerPlayNext()
        } else {
                f.transitionState(StateIdle)
        }
}

// handleClear clears the entire queue and stops playback.
func (f *JukeboxFSM) handleClear() {
        f.queue.Clear()
        f.currentTrack = nil
        f.retryCount = 0
        f.transitionState(StateIdle)
        slog.Info("FSM: queue cleared")
}

// handleSetVolume updates the volume.
func (f *JukeboxFSM) handleSetVolume(data interface{}) {
        level, ok := data.(int)
        if !ok {
                // Try float (JSON numbers often decode as float64)
                if f, ok := data.(float64); ok {
                        level = int(f)
                } else {
                        return
                }
        }
        if level < 0 {
                level = 0
        } else if level > 100 {
                level = 100
        }
        f.volume = level

        if f.actionHandler != nil {
                if err := f.actionHandler.SetVolume(level); err != nil {
                        slog.Error("FSM: SetVolume failed", "error", err)
                        return
                }
        }

        for _, obs := range f.observers {
                obs.OnVolumeChange(level)
        }
}

// handleRemoveFromQueue removes a specific track from the queue by ID.
func (f *JukeboxFSM) handleRemoveFromQueue(data interface{}) {
        trackID, ok := data.(string)
        if !ok {
                slog.Error("FSM: EventRemoveFromQueue received non-string data")
                return
        }

        items := f.queue.All()
        var remaining []models.Track
        for _, t := range items {
                if t.ID != trackID {
                        remaining = append(remaining, t)
                }
        }

        f.queue.Clear()
        for _, t := range remaining {
                f.queue.Enqueue(t)
        }

        slog.Info("FSM: track removed from queue", "track_id", trackID, "remaining", len(remaining))
}

// triggerPlayNext peeks the next track and sends it to the action handler.
func (f *JukeboxFSM) triggerPlayNext() {
        track, err := f.queue.Head()
        if err != nil {
                // Queue is empty
                f.currentTrack = nil
                f.transitionState(StateIdle)
                return
        }

        f.currentTrack = &track
        f.transitionState(StateTransitioning)

        if f.actionHandler != nil {
                go func() {
                        err := f.actionHandler.PlayTrack(track, f.volume)
                        if err != nil {
                                slog.Error("FSM: PlayTrack error from action handler", "track_id", track.ID, "error", err)
                                f.ProcessEvent(EventAckFail, nil)
                        } else {
                                f.ProcessEvent(EventAckOk, nil)
                        }
                }()
        } else {
                // Mock mode: auto-ack success after a short delay
                go func() {
                        time.Sleep(500 * time.Millisecond)
                        f.ProcessEvent(EventAckOk, nil)
                }()
        }
}

// transitionState changes the FSM state and notifies observers.
func (f *JukeboxFSM) transitionState(newState State) {
        oldState := f.state
        f.state = newState

        for _, obs := range f.observers {
                obs.OnStateChange(oldState, newState, f.currentTrack, f.volume)
        }

        slog.Info("FSM: state transition", "old", oldState.String(), "new", newState.String())
}

// replayDeferred processes events that were buffered during Transitioning state.
func (f *JukeboxFSM) replayDeferred() {
        if len(f.deferredCmds) == 0 {
                return
        }

        cmds := f.deferredCmds
        f.deferredCmds = nil

        for _, cmd := range cmds {
                slog.Info("FSM: replaying deferred event", "event", eventName(cmd.event))
                f.processEventInternal(cmd.event, cmd.data)
        }
}

// RegisterObserver adds an observer to receive FSM state change callbacks.
func (f *JukeboxFSM) RegisterObserver(obs FSMObserver) {
        f.mu.Lock()
        defer f.mu.Unlock()
        f.observers = append(f.observers, obs)
}

// AddUser registers an active user in the session.
func (f *JukeboxFSM) AddUser(userID string) {
        f.mu.Lock()
        defer f.mu.Unlock()
        f.activeUsers[userID] = true
}

// RemoveUser removes a user from the active session.
func (f *JukeboxFSM) RemoveUser(userID string) {
        f.mu.Lock()
        defer f.mu.Unlock()
        delete(f.activeUsers, userID)
}

// GetState returns the current state, current track, and volume.
func (f *JukeboxFSM) GetState() (State, *models.Track, int) {
        f.mu.Lock()
        defer f.mu.Unlock()
        return f.state, f.currentTrack, f.volume
}

// GetQueue returns all tracks in the queue.
func (f *JukeboxFSM) GetQueue() []models.Track {
        f.mu.Lock()
        defer f.mu.Unlock()
        return f.queue.All()
}

// QueueSize returns the current number of tracks in the queue.
func (f *JukeboxFSM) QueueSize() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.queue.Size()
}

// GetActiveUsers returns a map of active user IDs.
func (f *JukeboxFSM) GetActiveUsers() map[string]bool {
        f.mu.Lock()
        defer f.mu.Unlock()
        result := make(map[string]bool)
        for k, v := range f.activeUsers {
                result[k] = v
        }
        return result
}

// GetVotes is kept for API compatibility but returns empty (voting removed).
func (f *JukeboxFSM) GetVotes() map[string]bool {
        return make(map[string]bool)
}

// GetKarma is kept for API compatibility but returns empty (karma removed).
func (f *JukeboxFSM) GetKarma() map[string]float64 {
        return make(map[string]float64)
}

// VoteSkip is kept for API compatibility but now just does an immediate skip.
func (f *JukeboxFSM) VoteSkip(userID string) {
        f.ProcessEvent(EventSkip, nil)
}

// ExportSnapshot returns a serializable snapshot of the FSM state.
func (f *JukeboxFSM) ExportSnapshot() JukeboxStateSnapshot {
        f.mu.Lock()
        defer f.mu.Unlock()

        var users []string
        for u := range f.activeUsers {
                users = append(users, u)
        }

        return JukeboxStateSnapshot{
                State:        f.state,
                Queue:        f.queue.All(),
                CurrentTrack: f.currentTrack,
                Volume:       f.volume,
                ActiveUsers:  users,
        }
}

// ImportSnapshot restores the FSM from a snapshot.
func (f *JukeboxFSM) ImportSnapshot(snap JukeboxStateSnapshot) {
        f.mu.Lock()
        defer f.mu.Unlock()

        f.state = snap.State
        f.queue.Clear()
        for _, t := range snap.Queue {
                f.queue.Enqueue(t)
        }
        f.currentTrack = snap.CurrentTrack
        f.volume = snap.Volume
        f.activeUsers = make(map[string]bool)
        for _, u := range snap.ActiveUsers {
                f.activeUsers[u] = true
        }
}

// ApplyDelta applies a single delta operation to the FSM.
func (f *JukeboxFSM) ApplyDelta(op string, data json.RawMessage) error {
        switch op {
        case "append":
                var track models.Track
                if err := json.Unmarshal(data, &track); err != nil {
                        return err
                }
                f.queue.Enqueue(track)
        case "dequeue":
                _, _ = f.queue.Dequeue()
        case "skip":
                // No-op: skip is transient
        case "volume":
                var vol int
                if err := json.Unmarshal(data, &vol); err != nil {
                        return err
                }
                f.volume = vol
        case "clear":
                f.queue.Clear()
                f.currentTrack = nil
        default:
                slog.Warn("FSM: unknown delta op", "op", op)
        }
        return nil
}
