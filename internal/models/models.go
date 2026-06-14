package models

import "time"

// Source represents the origin of the content.
type Source string

const (
	// SourceYoutube represents content from YouTube.
	SourceYoutube Source = "youtube"
	// SourceSMBLocal represents content from a local SMB share.
	SourceSMBLocal Source = "smb_local"
)

// Metadata contains track description information.
type Metadata struct {
	Title     string `json:"title"`
	Thumbnail string `json:"thumbnail"`
	Duration  int    `json:"duration"` // Duration in seconds
}

// Track represents a media track with its playback history and metadata.
type Track struct {
	ID        string    `json:"id"`
	UserID    string    `json:"user_id"` // User who added the track
	Src       Source    `json:"src"`
	Meta      Metadata  `json:"meta"`
	URL       string    `json:"url"`       // Resolved streaming URL, empty if not yet resolved
	Dur       int       `json:"dur"`       // Duration in seconds
	MIMEType  string    `json:"mime_type"` // MIME type of the stream (e.g. "audio/mp4", "audio/mpeg")
	Ext       string    `json:"ext"`       // File extension (e.g. "m4a", "mp3", "webm")
	PlayCount int       `json:"play_count"`
	SkipCount int       `json:"skip_count"`
	TauLast   time.Time `json:"tau_last"`
}

// HistoryEntry registra una reproducción completada.
type HistoryEntry struct {
	Track    Track     `json:"track"`
	PlayedAt time.Time `json:"played_at"`
}

// EventType represents the type of action or event in the system.
type EventType string

const (
	ActionAddTrack      EventType = "add_track"
	ActionPlayNow       EventType = "play_now"
	ActionPlayFromQueue EventType = "play_from_queue"
	ActionPlay          EventType = "play"
	ActionPause         EventType = "pause"
	ActionResume        EventType = "resume"
	ActionSkip          EventType = "skip"
	ActionSetVolume     EventType = "set_volume"
	ActionClearQueue    EventType = "clear_queue"
	ActionRemoveFromQueue EventType = "remove_from_queue"
)

// AddTrackPayload contains parameters for ActionAddTrack.
type AddTrackPayload struct {
	URL       string    `json:"url"`
	UserID    string    `json:"user_id"`
	Timestamp time.Time `json:"timestamp"`
	ZoneID    string    `json:"zone_id,omitempty"`
}

// PlayNowPayload contains parameters for ActionPlayNow (play immediately).
type PlayNowPayload struct {
	URL       string    `json:"url"`
	UserID    string    `json:"user_id"`
	Timestamp time.Time `json:"timestamp"`
	ZoneID    string    `json:"zone_id,omitempty"`
}

// PlayFromQueuePayload contains parameters for ActionPlayFromQueue.
type PlayFromQueuePayload struct {
	TrackID   string `json:"track_id"`
	UserID    string `json:"user_id"`
	ZoneID    string `json:"zone_id,omitempty"`
}

// RemoveFromQueuePayload contains parameters for ActionRemoveFromQueue.
type RemoveFromQueuePayload struct {
	TrackID string `json:"track_id"`
	ZoneID  string `json:"zone_id,omitempty"`
}

// SetVolumePayload contains parameters for ActionSetVolume.
type SetVolumePayload struct {
	Level  int    `json:"level"`
	UserID string `json:"user_id"`
	ZoneID string `json:"zone_id,omitempty"`
}

// Command represents an input action from a user or internal system.
type Command struct {
	Type    EventType   `json:"type"`
	Payload interface{} `json:"payload"`
	ZoneID  string      `json:"zone_id,omitempty"`
}

// Envelope represents an event distributed by the EventBus to subscribers.
type Envelope struct {
	ID        string      `json:"id"`
	Type      EventType   `json:"type"`
	Payload   interface{} `json:"payload"`
	Timestamp time.Time   `json:"timestamp"`
	ZoneID    string      `json:"zone_id,omitempty"`
}

// CalendarEventType represents the type of calendar event (§15).
type CalendarEventType string

const (
	// CalendarMeetingStart signals the beginning of a meeting, triggering ActionPause.
	CalendarMeetingStart CalendarEventType = "meeting_start"
	// CalendarMeetingEnd signals the end of a meeting, triggering ActionResume.
	CalendarMeetingEnd CalendarEventType = "meeting_end"
)

// CalendarWebhookPayload represents the incoming calendar webhook body (§15.3).
// ZoneID is pre-wired for multi-zone support (§20).
type CalendarWebhookPayload struct {
	EventType CalendarEventType `json:"event_type"`
	Title     string            `json:"title,omitempty"`
	Timestamp time.Time         `json:"timestamp"`
	ZoneID    string            `json:"zone_id,omitempty"`
}
