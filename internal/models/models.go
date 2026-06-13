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
	URL       string    `json:"url"` // Resolved streaming URL, empty if not yet resolved
	Dur       int       `json:"dur"` // Duration in seconds
	PlayCount int       `json:"play_count"`
	SkipCount int       `json:"skip_count"`
	TauLast   time.Time `json:"tau_last"`
}

// EventType represents the type of action or event in the system.
type EventType string

const (
	ActionAddTrack   EventType = "add_track"
	ActionPlay       EventType = "play"
	ActionPause      EventType = "pause"
	ActionResume     EventType = "resume"
	ActionSkip       EventType = "skip"
	ActionSetVolume  EventType = "set_volume"
	ActionClearQueue EventType = "clear_queue"
)

// AddTrackPayload contains parameters for ActionAddTrack.
type AddTrackPayload struct {
	URL       string    `json:"url"`
	UserID    string    `json:"user_id"`
	Timestamp time.Time `json:"timestamp"`
}

// SetVolumePayload contains parameters for ActionSetVolume.
type SetVolumePayload struct {
	Level  int    `json:"level"`
	UserID string `json:"user_id"`
}

// Command represents an input action from a user or internal system.
type Command struct {
	Type    EventType   `json:"type"`
	Payload interface{} `json:"payload"`
}

// Envelope represents an event distributed by the EventBus to subscribers.
type Envelope struct {
	ID        string      `json:"id"`
	Type      EventType   `json:"type"`
	Payload   interface{} `json:"payload"`
	Timestamp time.Time   `json:"timestamp"`
}

