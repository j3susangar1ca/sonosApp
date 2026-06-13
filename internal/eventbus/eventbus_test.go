package eventbus

import (
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/jesuslangarica/sonosApp/internal/models"
)

// TestEventBusPublishSubscribe verifies that published commands reach subscribers correctly.
func TestEventBusPublishSubscribe(t *testing.T) {
	eb := NewEventBus(10, 100*time.Millisecond)
	defer eb.Stop()

	ch1 := make(chan models.Envelope, 5)
	ch2 := make(chan models.Envelope, 5)

	eb.Subscribe(models.ActionAddTrack, "sub1", ch1)
	eb.Subscribe(models.ActionAddTrack, "sub2", ch2)

	cmd := models.Command{
		Type:    models.ActionAddTrack,
		Payload: models.AddTrackPayload{URL: "https://youtube.com/watch?v=123", UserID: "user1"},
	}

	eb.Inject(cmd)

	// Verify both channels receive the command envelope
	var env1, env2 models.Envelope
	select {
	case env1 = <-ch1:
		eb.Ack(env1.ID, "sub1")
	case <-time.After(200 * time.Millisecond):
		t.Fatal("sub1 did not receive envelope in time")
	}

	select {
	case env2 = <-ch2:
		eb.Ack(env2.ID, "sub2")
	case <-time.After(200 * time.Millisecond):
		t.Fatal("sub2 did not receive envelope in time")
	}

	if env1.ID == "" || env2.ID == "" {
		t.Error("Envelope IDs should not be empty")
	}
	if env1.ID != env2.ID {
		t.Errorf("Subscribers should receive the same envelope ID for a single publication, got %s and %s", env1.ID, env2.ID)
	}

	payload, ok := env1.Payload.(models.AddTrackPayload)
	if !ok || payload.URL != "https://youtube.com/watch?v=123" {
		t.Errorf("invalid envelope payload: %+v", env1.Payload)
	}
}

// TestEventBusRetransmission verifies that envelopes are retransmitted if no ACK is received.
func TestEventBusRetransmission(t *testing.T) {
	// 50ms retransmission interval
	eb := NewEventBus(10, 50*time.Millisecond)
	defer eb.Stop()

	ch := make(chan models.Envelope, 10)
	eb.Subscribe(models.ActionPause, "sub1", ch)

	cmd := models.Command{
		Type:    models.ActionPause,
		Payload: "userA",
	}

	eb.Inject(cmd)

	// Wait for the first delivery
	var firstEnv models.Envelope
	select {
	case firstEnv = <-ch:
		// We do NOT call ACK. This should trigger retransmission.
	case <-time.After(200 * time.Millisecond):
		t.Fatal("initial delivery failed")
	}

	// Verify retransmission 1
	select {
	case env := <-ch:
		if env.ID != firstEnv.ID {
			t.Errorf("expected retransmitted ID %s, got %s", firstEnv.ID, env.ID)
		}
	case <-time.After(100 * time.Millisecond):
		t.Fatal("first retransmission did not occur")
	}

	// Verify retransmission 2
	select {
	case env := <-ch:
		if env.ID != firstEnv.ID {
			t.Errorf("expected retransmitted ID %s, got %s", firstEnv.ID, env.ID)
		}
	case <-time.After(100 * time.Millisecond):
		t.Fatal("second retransmission did not occur")
	}

	// Now send ACK, and check that no more retransmissions occur
	eb.Ack(firstEnv.ID, "sub1")

	// Drain the channel (there might be a queued retransmission)
	time.Sleep(70 * time.Millisecond)
	for len(ch) > 0 {
		<-ch
	}

	// Wait to see if any new retransmission comes (should not come)
	select {
	case env := <-ch:
		t.Errorf("unexpected retransmission received after ACK: ID %s", env.ID)
	case <-time.After(100 * time.Millisecond):
		// Success: no retransmission
	}
}

// TestEventBusUnsubscribe verifies that unsubscribing cancels active retransmissions.
func TestEventBusUnsubscribe(t *testing.T) {
	eb := NewEventBus(10, 50*time.Millisecond)
	defer eb.Stop()

	ch := make(chan models.Envelope, 10)
	eb.Subscribe(models.ActionResume, "sub1", ch)

	eb.Inject(models.Command{Type: models.ActionResume})

	// Wait for initial delivery
	select {
	case <-ch:
		// No ACK
	case <-time.After(100 * time.Millisecond):
		t.Fatal("initial delivery failed")
	}

	// Unsubscribe immediately
	eb.Unsubscribe(models.ActionResume, "sub1")

	// Drain the channel
	time.Sleep(70 * time.Millisecond)
	for len(ch) > 0 {
		<-ch
	}

	// Wait to see if any retransmission occurs (should be canceled)
	select {
	case env := <-ch:
		t.Errorf("received retransmission after unsubscribe: %s", env.ID)
	case <-time.After(100 * time.Millisecond):
		// Success
	}
}

// TestEventBusConcurrency checks that the bus behaves correctly under concurrent subscribe/unsubscribe/ACK.
func TestEventBusConcurrency(t *testing.T) {
	eb := NewEventBus(100, 50*time.Millisecond)
	defer eb.Stop()

	var wg sync.WaitGroup
	subCount := 20

	channels := make([]chan models.Envelope, subCount)
	for i := 0; i < subCount; i++ {
		channels[i] = make(chan models.Envelope, 100)
	}

	// Spin up concurrent subscribers
	for i := 0; i < subCount; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			subID := fmt.Sprintf("sub-%d", id)
			eb.Subscribe(models.ActionSkip, subID, channels[id])

			// Simulate listening and ACKing
			go func() {
				for env := range channels[id] {
					eb.Ack(env.ID, subID)
				}
			}()
		}(i)
	}

	wg.Wait()

	// Inject concurrent commands
	cmdCount := 50
	for i := 0; i < cmdCount; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			eb.Inject(models.Command{
				Type:    models.ActionSkip,
				Payload: idx,
			})
		}(i)
	}

	wg.Wait()

	// Wait for delivery stabilization
	time.Sleep(150 * time.Millisecond)

	// Cleanup channels: unsubscribe first to cancel all pending deliveries and avoid races.
	for i := 0; i < subCount; i++ {
		subID := fmt.Sprintf("sub-%d", i)
		eb.Unsubscribe(models.ActionSkip, subID)
	}

	for i := 0; i < subCount; i++ {
		close(channels[i])
	}
}
