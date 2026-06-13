package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/jesuslangarica/sonosApp/internal/api"
	"github.com/jesuslangarica/sonosApp/internal/cache"
	"github.com/jesuslangarica/sonosApp/internal/eventbus"
	"github.com/jesuslangarica/sonosApp/internal/extractor"
	"github.com/jesuslangarica/sonosApp/internal/models"
	"github.com/jesuslangarica/sonosApp/internal/persist"
	"github.com/jesuslangarica/sonosApp/internal/player"
	"github.com/jesuslangarica/sonosApp/internal/streaming"
	"github.com/jesuslangarica/sonosApp/internal/telemetry"
	"github.com/jesuslangarica/sonosApp/internal/ws"
)

// OrchestratorFSMObserver implements player.FSMObserver.
// It bridges the FSM state changes to the WebSocket Hub and the Persister.
type OrchestratorFSMObserver struct {
	persister *persist.Persister
	hub       *ws.WebSocketHub
	fsm       *player.JukeboxFSM
}

func (o *OrchestratorFSMObserver) OnStateChange(oldState, newState player.State, currentTrack *models.Track, volume int) {
	// 1. If transitioning from Transitioning to Playing, a song has successfully dequeued.
	if oldState == player.StateTransitioning && newState == player.StatePlaying {
		slog.Info("Track successfully dequeued, writing persistence record")
		_ = o.persister.WriteDelta(persist.OpDequeue, nil, o.fsm)
		telemetry.IncrementPlayCount()
	}

	// 2. Broadcast Jukebox FSM state update to all active WebSocket connections.
	msg, err := json.Marshal(map[string]interface{}{
		"event":         "state_change",
		"old_state":     oldState.String(),
		"new_state":     newState.String(),
		"current_track": currentTrack,
		"volume":        volume,
	})
	if err == nil {
		o.hub.Broadcast(msg)
	}
}

func (o *OrchestratorFSMObserver) OnVolumeChange(volume int) {
	// 1. Write set volume operation to persistence log.
	_ = o.persister.WriteDelta(persist.OpVolume, volume, o.fsm)

	// 2. Broadcast volume change event to all WebSocket clients.
	msg, err := json.Marshal(map[string]interface{}{
		"event":  "volume_change",
		"volume": volume,
	})
	if err == nil {
		o.hub.Broadcast(msg)
	}
}

func main() {
	// Define and parse CLI flags (also fallback to env vars)
	portOpt := flag.String("port", getEnv("PORT", "8080"), "HTTP port to listen on")
	baseDirOpt := flag.String("base-dir", getEnv("BASE_DIR", "."), "Base media directory for the secure streaming proxy")
	serverURLOpt := flag.String("server-url", getEnv("SERVER_URL", "http://localhost:8080"), "Base URL of this server for streaming URLs")
	sonosIPOpt := flag.String("sonos-ip", getEnv("SONOS_IP", ""), "IP of the Sonos speaker (optional, runs in mock if empty)")
	stateLogOpt := flag.String("state-log", getEnv("STATE_LOG", "state.log"), "Path to the append-only delta log file")
	stateJsonOpt := flag.String("state-json", getEnv("STATE_JSON", "state.json"), "Path to the snapshot JSON file")
	maxDeltasOpt := flag.Int("max-deltas", 1000, "Maximum deltas before triggering a snapshot rotation")
	ytdlpPathOpt := flag.String("ytdlp-path", getEnv("YTDLP_PATH", "yt-dlp"), "Path to the yt-dlp executable")
	flag.Parse()

	// Initialize Structured Logging in JSON format targeting stdout
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	}))
	slog.SetDefault(logger)

	slog.Info("Initializing Jukebox Server subsystems...",
		"port", *portOpt,
		"baseDir", *baseDirOpt,
		"sonosIP", *sonosIPOpt,
		"ytdlpPath", *ytdlpPathOpt,
	)

	// 1. Initialize Streaming Proxy & LRU Cache
	proxy, err := streaming.NewStreamingProxy(*baseDirOpt, *serverURLOpt)
	if err != nil {
		slog.Error("Failed to initialize Streaming Proxy", "error", err)
		os.Exit(1)
	}

	lruCache := cache.NewLRUCache(100)

	// 2. Initialize EventBus & WorkerPool
	eb := eventbus.NewEventBus(128, 500*time.Millisecond)
	pool := extractor.NewWorkerPool(*ytdlpPathOpt)

	// 3. Initialize Persister
	persister := persist.NewPersister(*stateLogOpt, *stateJsonOpt)
	persister.SetMaxDeltas(*maxDeltasOpt)

	// 4. Initialize Sonos Hardware Action Handler or fall back to Mock
	var actionHandler player.ActionHandler
	if *sonosIPOpt != "" {
		cb := player.NewCircuitBreaker(3, 15*time.Second)
		actionHandler = player.NewSonosPlayer(*sonosIPOpt, cb)
		slog.Info("Initialized hardware SonosPlayer", "ip", *sonosIPOpt)
	} else {
		slog.Info("No Sonos IP supplied. Jukebox running in mock mode (FSM auto-acks transitions)")
	}

	// 5. Initialize Jukebox FSM
	fsm := player.NewJukeboxFSM(15, actionHandler)

	// 6. Initialize WebSocket Hub
	hub := ws.NewWebSocketHub(fsm)

	// 7. Register Orchestrator Observer in FSM to propagate events to WS Hub & Persister
	observer := &OrchestratorFSMObserver{
		persister: persister,
		hub:       hub,
		fsm:       fsm,
	}
	fsm.RegisterObserver(observer)

	// 8. RESTORE FSM State BEFORE listening to network requests (Synchronous Restore)
	slog.Info("Restoring Jukebox state from storage files...")
	if err := persister.Restore(fsm); err != nil {
		slog.Warn("Restore sequence completed with errors or files do not exist", "error", err)
	} else {
		slog.Info("State restoration completed successfully")
	}

	// 9. Start Workers
	pool.Start()
	hub.Start()

	// 10. Subscribe workers to the EventBus to consume and route client commands
	subscribeEventBusWorkers(eb, fsm, pool, lruCache, persister)

	// 11. Start HTTP Server
	router := api.NewRouter(fsm, eb, hub, proxy)
	server := &http.Server{
		Addr:    ":" + *portOpt,
		Handler: router,
	}

	go func() {
		slog.Info("Jukebox Server API listening for requests", "addr", server.Addr)
		if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			slog.Error("HTTP Server closed unexpectedly", "error", err)
			os.Exit(1)
		}
	}()

	// 12. Handle Graceful Shutdown
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)
	<-sigChan

	slog.Info("Shutdown signal received; initiating graceful termination...")

	// Shutdown HTTP Server first to reject incoming requests immediately
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer shutdownCancel()
	if err := server.Shutdown(shutdownCtx); err != nil {
		slog.Error("HTTP Server shutdown error", "error", err)
	}

	// Terminate concurrent background workers
	hub.Stop()
	eb.Stop()
	pool.Stop()

	// Write final state snapshot to disk to prevent any loss of queue/state
	slog.Info("Saving final state snapshot to disk...")
	finalSnap := fsm.ExportSnapshot()
	if err := persister.WriteSnapshot(finalSnap); err != nil {
		slog.Error("Failed to save final snapshot", "error", err)
	} else {
		slog.Info("Final state snapshot persisted successfully. Exiting Jukebox.")
	}
}

// subscribeEventBusWorkers configures workers that drain commands from the EventBus and interact with Jukebox subsystems.
func subscribeEventBusWorkers(
	eb *eventbus.EventBus,
	fsm *player.JukeboxFSM,
	pool *extractor.WorkerPool,
	lruCache *cache.LRUCache,
	persister *persist.Persister,
) {
	// Worker 1: Add Track Commands
	chAdd := make(chan models.Envelope, 100)
	eb.Subscribe(models.ActionAddTrack, "orchestrator_add", chAdd)
	go func() {
		for env := range chAdd {
			payload, ok := env.Payload.(models.AddTrackPayload)
			if !ok {
				slog.Error("Malformed AddTrackPayload envelope payload", "type", fmt.Sprintf("%T", env.Payload))
				eb.Ack(env.ID, "orchestrator_add")
				continue
			}

			// 1. Try to resolve URL from cache first to avoid heavy yt-dlp invocations
			if cachedTrack, found := lruCache.Get(payload.URL); found {
				slog.Info("LRU Cache hit: serving resolved track from cache", "url", payload.URL, "track_id", cachedTrack.ID)
				cachedTrack.UserID = payload.UserID

				fsm.ProcessEvent(player.EventAdd, cachedTrack)
				_ = persister.WriteDelta(persist.OpAppend, cachedTrack, fsm)
				eb.Ack(env.ID, "orchestrator_add")
				continue
			}

			// 2. Fallback: Submit extraction task to WorkerPool
			resChan := pool.Submit(payload.URL, 2) // Priority 2 represents a user requested song
			go func(rChan <-chan extractor.Result, envelopeID string, userID string, rawURL string) {
				res := <-rChan
				if res.Err != nil {
					slog.Error("Failed to extract streaming URL", "url", rawURL, "error", res.Err)
				} else {
					track := res.Track
					track.UserID = userID

					// Save resolved track in LRU cache
					lruCache.Add(rawURL, track)

					// Append resolved track to Jukebox queue
					fsm.ProcessEvent(player.EventAdd, track)
					_ = persister.WriteDelta(persist.OpAppend, track, fsm)
				}
				eb.Ack(envelopeID, "orchestrator_add")
			}(resChan, env.ID, payload.UserID, payload.URL)
		}
	}()

	// Worker 2: Skip Track Commands
	chSkip := make(chan models.Envelope, 100)
	eb.Subscribe(models.ActionSkip, "orchestrator_skip", chSkip)
	go func() {
		for env := range chSkip {
			userID, _ := env.Payload.(string)
			if userID != "" {
				slog.Info("Processing skip vote command", "user_id", userID)
				fsm.VoteSkip(userID)
			} else {
				slog.Info("Processing force skip command")
				fsm.ProcessEvent(player.EventSkip, nil)
			}
			_ = persister.WriteDelta(persist.OpSkip, nil, fsm)
			eb.Ack(env.ID, "orchestrator_skip")
		}
	}()

	// Worker 3: Set Volume Commands
	chVol := make(chan models.Envelope, 100)
	eb.Subscribe(models.ActionSetVolume, "orchestrator_vol", chVol)
	go func() {
		for env := range chVol {
			payload, ok := env.Payload.(models.SetVolumePayload)
			if !ok {
				slog.Error("Malformed SetVolumePayload envelope payload", "type", fmt.Sprintf("%T", env.Payload))
				eb.Ack(env.ID, "orchestrator_vol")
				continue
			}

			slog.Info("Processing set volume command", "level", payload.Level)
			fsm.ProcessEvent(player.EventSetVolume, payload.Level)
			// Volume change is logged as a delta via OnVolumeChange observer hook.
			eb.Ack(env.ID, "orchestrator_vol")
		}
	}()

	// Worker 4: Clear Queue Commands
	chClear := make(chan models.Envelope, 100)
	eb.Subscribe(models.ActionClearQueue, "orchestrator_clear", chClear)
	go func() {
		for env := range chClear {
			slog.Info("Processing clear queue command")
			fsm.ProcessEvent(player.EventClear, nil)
			_ = persister.WriteDelta(persist.OpClear, nil, fsm)
			eb.Ack(env.ID, "orchestrator_clear")
		}
	}()

	// Worker 5: Pause Track Commands
	chPause := make(chan models.Envelope, 100)
	eb.Subscribe(models.ActionPause, "orchestrator_pause", chPause)
	go func() {
		for env := range chPause {
			slog.Info("Processing pause command")
			fsm.ProcessEvent(player.EventPause, nil)
			eb.Ack(env.ID, "orchestrator_pause")
		}
	}()

	// Worker 6: Resume Track Commands
	chResume := make(chan models.Envelope, 100)
	eb.Subscribe(models.ActionResume, "orchestrator_resume", chResume)
	go func() {
		for env := range chResume {
			slog.Info("Processing resume command")
			fsm.ProcessEvent(player.EventResume, nil)
			eb.Ack(env.ID, "orchestrator_resume")
		}
	}()
}

func getEnv(key, fallback string) string {
	if val, ok := os.LookupEnv(key); ok {
		return val
	}
	return fallback
}
