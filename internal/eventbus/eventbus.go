package eventbus

import (
	"context"
	"crypto/rand"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/jesuslangarica/sonosApp/internal/models"
)

type deliveryKey struct {
	eventID string
	subID   string
}

// EventBus implements the Pub/Sub bus with At-Least-Once delivery semantics and retransmissions.
type EventBus struct {
	mu          sync.RWMutex
	chIn        chan models.Command
	subscribers map[models.EventType]map[string]chan<- models.Envelope
	deliveries  map[deliveryKey]context.CancelFunc
	tAck        time.Duration
	stopCtx     context.Context
	stopCancel  context.CancelFunc
}

// NewEventBus creates, initializes, and starts a new EventBus.
func NewEventBus(bufferSize int, tAck time.Duration) *EventBus {
	ctx, cancel := context.WithCancel(context.Background())
	eb := &EventBus{
		chIn:        make(chan models.Command, bufferSize),
		subscribers: make(map[models.EventType]map[string]chan<- models.Envelope),
		deliveries:  make(map[deliveryKey]context.CancelFunc),
		tAck:        tAck,
		stopCtx:     ctx,
		stopCancel:  cancel,
	}
	go eb.distributeLoop()
	return eb
}

// Inject pushes a command into the EventBus's central channel (CH_in).
func (eb *EventBus) Inject(cmd models.Command) {
	select {
	case <-eb.stopCtx.Done():
		slog.Warn("Attempted to inject command to stopped EventBus", "type", cmd.Type)
		return
	default:
	}

	select {
	case eb.chIn <- cmd:
	case <-eb.stopCtx.Done():
		slog.Warn("EventBus stopped while injecting command", "type", cmd.Type)
	}
}

// Subscribe registers a subscriber channel for a specific EventType.
func (eb *EventBus) Subscribe(eType models.EventType, subID string, ch chan<- models.Envelope) {
	eb.mu.Lock()
	defer eb.mu.Unlock()

	if _, exists := eb.subscribers[eType]; !exists {
		eb.subscribers[eType] = make(map[string]chan<- models.Envelope)
	}
	eb.subscribers[eType][subID] = ch
	slog.Info("Subscriber registered", "event_type", string(eType), "sub_id", subID)
}

// Unsubscribe removes a subscriber and cleans up their pending deliveries.
func (eb *EventBus) Unsubscribe(eType models.EventType, subID string) {
	eb.mu.Lock()
	defer eb.mu.Unlock()

	if subs, exists := eb.subscribers[eType]; exists {
		delete(subs, subID)
		if len(subs) == 0 {
			delete(eb.subscribers, eType)
		}
	}

	// Cancel and remove any pending deliveries for this subscriber
	for key, cancel := range eb.deliveries {
		if key.subID == subID {
			cancel()
			delete(eb.deliveries, key)
		}
	}
	slog.Info("Subscriber unregistered", "event_type", string(eType), "sub_id", subID)
}

// Ack confirms receipt of a message, canceling its active retransmission routine.
func (eb *EventBus) Ack(eventID string, subID string) {
	eb.mu.Lock()
	defer eb.mu.Unlock()

	key := deliveryKey{eventID: eventID, subID: subID}
	if cancel, exists := eb.deliveries[key]; exists {
		cancel()
		delete(eb.deliveries, key)
		slog.Debug("ACK received, delivery canceled", "event_id", eventID, "sub_id", subID)
	}
}

// Stop stops the event bus, canceling all retransmissions and closing channels.
func (eb *EventBus) Stop() {
	eb.stopCancel()

	eb.mu.Lock()
	defer eb.mu.Unlock()

	for key, cancel := range eb.deliveries {
		cancel()
		delete(eb.deliveries, key)
	}

	// Safely close chIn
	select {
	case <-eb.chIn:
		// already closed
	default:
		close(eb.chIn)
	}
	slog.Info("EventBus stopped")
}

// distributeLoop drains commands from chIn and distributes them.
func (eb *EventBus) distributeLoop() {
	for {
		select {
		case <-eb.stopCtx.Done():
			return
		case cmd, ok := <-eb.chIn:
			if !ok {
				return
			}
			eb.distributeCommand(cmd)
		}
	}
}

// distributeCommand forwards commands to all registered subscribers of the command's Type.
func (eb *EventBus) distributeCommand(cmd models.Command) {
	eb.mu.RLock()
	subs, exists := eb.subscribers[cmd.Type]
	if !exists || len(subs) == 0 {
		eb.mu.RUnlock()
		slog.Debug("No subscribers registered for event", "type", string(cmd.Type))
		return
	}

	// Copy subscriber channels to avoid holding read lock during delivery initialization
	channels := make(map[string]chan<- models.Envelope, len(subs))
	for subID, ch := range subs {
		channels[subID] = ch
	}
	eb.mu.RUnlock()

	eventID, err := newUUID()
	if err != nil {
		slog.Error("Failed to generate event ID", "error", err)
		return
	}

	env := models.Envelope{
		ID:        eventID,
		Type:      cmd.Type,
		Payload:   cmd.Payload,
		Timestamp: time.Now(),
		ZoneID:    cmd.ZoneID,
	}

	eb.mu.Lock()
	defer eb.mu.Unlock()

	// Check if stop was called while copy was in progress
	select {
	case <-eb.stopCtx.Done():
		return
	default:
	}

	for subID, ch := range channels {
		key := deliveryKey{eventID: eventID, subID: subID}
		ctx, cancel := context.WithCancel(eb.stopCtx)
		eb.deliveries[key] = cancel

		go eb.retransmitRoutine(eventID, subID, ch, env, ctx)
	}
}

// retransmitRoutine handles the initial non-blocking delivery and subsequent tick retransmissions.
func (eb *EventBus) retransmitRoutine(eventID string, subID string, ch chan<- models.Envelope, env models.Envelope, ctx context.Context) {
	// Initial delivery attempt (non-blocking)
	select {
	case ch <- env:
		slog.Debug("Envelope dispatched on initial attempt", "event_id", eventID, "sub_id", subID)
	case <-ctx.Done():
		return
	default:
		slog.Warn("Subscriber channel full on initial attempt, fallback to retransmission", "event_id", eventID, "sub_id", subID)
	}

	ticker := time.NewTicker(eb.tAck)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			select {
			case ch <- env:
				slog.Warn("Envelope retransmitted successfully", "event_id", eventID, "sub_id", subID)
			case <-ctx.Done():
				return
			default:
				slog.Error("Failed to retransmit envelope (subscriber channel full)", "event_id", eventID, "sub_id", subID)
			}
		}
	}
}

// newUUID generates a cryptographically secure UUID v4.
func newUUID() (string, error) {
	uuid := make([]byte, 16)
	_, err := rand.Read(uuid)
	if err != nil {
		return "", err
	}
	// Version 4 and RFC 4122 Variant setting
	uuid[6] = (uuid[6] & 0x0f) | 0x40
	uuid[8] = (uuid[8] & 0x3f) | 0x80

	return fmt.Sprintf("%x-%x-%x-%x-%x",
		uuid[0:4], uuid[4:6], uuid[6:8], uuid[8:10], uuid[10:16]), nil
}
