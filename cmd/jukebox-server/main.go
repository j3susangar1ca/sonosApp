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
	"strings"
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

// zoneDef is parsed from CLI flags: "id:ip" or just "id" (mock mode).
type zoneDef struct {
	ID string
	IP string
}

// parseZoneDefs parses zone flags in the form "id:ip" or just "id".
func parseZoneDefs(raw []string) []zoneDef {
	var defs []zoneDef
	for _, s := range raw {
		parts := strings.SplitN(s, ":", 2)
		zd := zoneDef{ID: parts[0]}
		if len(parts) == 2 {
			zd.IP = parts[1]
		}
		defs = append(defs, zd)
	}
	return defs
}

// zoneFlags collects multiple --zone flags.
type zoneFlags []string

func (f *zoneFlags) String() string { return strings.Join(*f, ", ") }
func (f *zoneFlags) Set(value string) error {
	*f = append(*f, value)
	return nil
}

// OrchestratorFSMObserver implements player.FSMObserver.
// Each zone gets its own observer that emits broadcasts tagged with zone_id (§20.3).
type OrchestratorFSMObserver struct {
	zoneID    string
	persister *persist.Persister
	hub       *ws.WebSocketHub
	fsm       *player.JukeboxFSM
}

func (o *OrchestratorFSMObserver) OnStateChange(oldState, newState player.State, currentTrack *models.Track, volume int) {
	// 1. If transitioning from Transitioning to Playing, a song has successfully dequeued.
	if oldState == player.StateTransitioning && newState == player.StatePlaying {
		slog.Info("Track successfully dequeued, writing persistence record", "zone_id", o.zoneID)
		_ = o.persister.WriteDelta(persist.OpDequeue, nil, o.fsm)
		telemetry.IncrementPlayCount()
	}

	// 2. Broadcast Jukebox FSM state update to all active WebSocket connections (§20.3).
	msg, err := json.Marshal(map[string]interface{}{
		"event":         "state_change",
		"zone_id":       o.zoneID,
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

	// 2. Broadcast volume change event to all WebSocket clients (§20.3).
	msg, err := json.Marshal(map[string]interface{}{
		"event":   "volume_change",
		"zone_id": o.zoneID,
		"volume":  volume,
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
	stateLogOpt := flag.String("state-log", getEnv("STATE_LOG", "state.log"), "Path prefix for append-only delta log files")
	stateJsonOpt := flag.String("state-json", getEnv("STATE_JSON", "state.json"), "Path prefix for snapshot JSON files")
	maxDeltasOpt := flag.Int("max-deltas", 1000, "Maximum deltas before triggering a snapshot rotation")
	ytdlpPathOpt := flag.String("ytdlp-path", getEnv("YTDLP_PATH", "yt-dlp"), "Path to the yt-dlp executable")
	calendarSecretOpt := flag.String("calendar-secret", getEnv("CALENDAR_WEBHOOK_SECRET", ""), "Shared secret for calendar webhook HMAC-SHA256 validation")

	// Multi-zone flags: --zone default:192.168.1.10 --zone kitchen:192.168.1.11
	// If no --zone flags are provided, a single "default" zone is created.
	var zones zoneFlags
	flag.Var(&zones, "zone", "Zone definition in the form 'id:ip' or 'id' (mock mode). Can be specified multiple times.")

	// Legacy single-zone flag for backward compatibility
	sonosIPOpt := flag.String("sonos-ip", getEnv("SONOS_IP", ""), "IP of the Sonos speaker (legacy, creates a 'default' zone)")

	flag.Parse()

	// Initialize Structured Logging in JSON format targeting stdout
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	}))
	slog.SetDefault(logger)

	// Resolve zone definitions: --zone flags take priority, then --sonos-ip legacy flag
	var zoneDefs []zoneDef
	if len(zones) > 0 {
		zoneDefs = parseZoneDefs(zones)
	} else {
		// Legacy mode: single "default" zone
		zoneDefs = []zoneDef{{ID: "default", IP: *sonosIPOpt}}
	}

	slog.Info("Initializing Jukebox Server subsystems...",
		"port", *portOpt,
		"baseDir", *baseDirOpt,
		"zones", fmt.Sprintf("%v", zoneDefs),
		"ytdlpPath", *ytdlpPathOpt,
	)

	// 1. Initialize Streaming Proxy & LRU Cache (shared singletons — §20.1)
	proxy, err := streaming.NewStreamingProxy(*baseDirOpt, *serverURLOpt)
	if err != nil {
		slog.Error("Failed to initialize Streaming Proxy", "error", err)
		os.Exit(1)
	}

	lruCache := cache.NewLRUCache(100)

	// 2. Initialize EventBus & WorkerPool (shared singletons — §20.1)
	eb := eventbus.NewEventBus(128, 500*time.Millisecond)
	pool := extractor.NewWorkerPool(*ytdlpPathOpt)

	// 3. Initialize ZoneRegistry (§20)
	registry := api.NewZoneRegistry()

	// 4. Initialize each zone with independent FSM, CB, Sonos adapter, and Persister (§20.2)
	for _, zd := range zoneDefs {
		var actionHandler player.ActionHandler
		if zd.IP != "" {
			cb := player.NewCircuitBreaker(3, 15*time.Second)
			actionHandler = player.NewSonosPlayer(zd.IP, cb)
			slog.Info("Initialized hardware SonosPlayer for zone", "zone_id", zd.ID, "ip", zd.IP)
		} else {
			slog.Info("Zone running in mock mode (FSM auto-acks transitions)", "zone_id", zd.ID)
		}

		fsm := player.NewJukeboxFSM(15, actionHandler)

		// Per-zone persistence files
		logPath := *stateLogOpt
		snapPath := *stateJsonOpt
		if len(zoneDefs) > 1 || zd.ID != "default" {
			logPath = fmt.Sprintf("state_%s.log", zd.ID)
			snapPath = fmt.Sprintf("state_%s.json", zd.ID)
		}
		persister := persist.NewPersister(logPath, snapPath)
		persister.SetMaxDeltas(*maxDeltasOpt)

		zone := &api.Zone{
			ID:            zd.ID,
			FSM:           fsm,
			SonosIP:       zd.IP,
			ActionHandler: actionHandler,
			Persister:     persister,
		}
		registry.Register(zone)
	}

	// 5. Wire telemetry dynamic Gauge callbacks — aggregated across all zones
	telemetry.QueueSizeFunc = func() float64 {
		var total int
		registry.ForEach(func(zone *api.Zone) {
			total += len(zone.FSM.GetQueue())
		})
		return float64(total)
	}
	telemetry.ActiveUsersFunc = func() float64 {
		// Users are global (shared across zones — §20.1), so we read from the default zone.
		// In practice, all zones share the same user set via the Hub.
		if z, ok := registry.GetDefault(); ok {
			return float64(len(z.FSM.GetActiveUsers()))
		}
		return 0
	}
	telemetry.RegisterGaugeFuncs()

	// 6. Initialize WebSocket Hub — uses the default zone's FSM for user management.
	// Users are global across zones (§20.1: Ua is not vectorized).
	defaultZone, hasDefault := registry.GetDefault()
	if !hasDefault {
		slog.Error("No 'default' zone configured. At least one zone must be 'default'.")
		os.Exit(1)
	}
	hub := ws.NewWebSocketHub(defaultZone.FSM)

	// 7. Register per-zone Orchestrator Observers
	registry.ForEach(func(zone *api.Zone) {
		observer := &OrchestratorFSMObserver{
			zoneID:    zone.ID,
			persister: zone.Persister,
			hub:       hub,
			fsm:       zone.FSM,
		}
		zone.FSM.RegisterObserver(observer)
	})

	// 8. RESTORE FSM State BEFORE listening to network requests (Synchronous Restore per zone)
	slog.Info("Restoring Jukebox state from storage files...")
	registry.ForEach(func(zone *api.Zone) {
		if err := zone.Persister.Restore(zone.FSM); err != nil {
			slog.Warn("Restore sequence completed with errors or files do not exist", "zone_id", zone.ID, "error", err)
		} else {
			slog.Info("State restoration completed successfully", "zone_id", zone.ID)
		}
	})

	// 9. Start Workers
	pool.Start()
	hub.Start()

	// 10. Subscribe zone-aware workers to the EventBus
	subscribeEventBusWorkers(eb, registry, pool, lruCache)

	// 11. Start HTTP Server
	router := api.NewRouter(registry, eb, hub, proxy, *calendarSecretOpt)
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

	// Write final state snapshot per zone to disk to prevent any loss of queue/state
	slog.Info("Saving final state snapshots to disk...")
	registry.ForEach(func(zone *api.Zone) {
		finalSnap := zone.FSM.ExportSnapshot()
		if err := zone.Persister.WriteSnapshot(finalSnap); err != nil {
			slog.Error("Failed to save final snapshot", "zone_id", zone.ID, "error", err)
		} else {
			slog.Info("Final state snapshot persisted successfully", "zone_id", zone.ID)
		}
	})
}

// subscribeEventBusWorkers configures zone-aware workers that drain commands
// from the EventBus, resolve the target zone, and interact with per-zone FSMs.
func subscribeEventBusWorkers(
	eb *eventbus.EventBus,
	registry *api.ZoneRegistry,
	pool *extractor.WorkerPool,
	lruCache *cache.LRUCache,
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

			zoneID := api.ResolveZoneID(payload.ZoneID)
			zone, zoneOk := registry.Get(zoneID)
			if !zoneOk {
				slog.Error("Zone not found for add_track command", "zone_id", zoneID)
				eb.Ack(env.ID, "orchestrator_add")
				continue
			}

			// 1. Try to resolve URL from cache first to avoid heavy yt-dlp invocations
			if cachedTrack, found := lruCache.Get(payload.URL); found {
				slog.Info("LRU Cache hit: serving resolved track from cache", "url", payload.URL, "track_id", cachedTrack.ID, "zone_id", zoneID)
				cachedTrack.UserID = payload.UserID

				zone.FSM.ProcessEvent(player.EventAdd, cachedTrack)
				_ = zone.Persister.WriteDelta(persist.OpAppend, cachedTrack, zone.FSM)
				eb.Ack(env.ID, "orchestrator_add")
				continue
			}

			// 2. Fallback: Submit extraction task to WorkerPool (shared singleton)
			resChan := pool.Submit(payload.URL, 2) // Priority 2 represents a user requested song
			go func(rChan <-chan extractor.Result, envelopeID string, userID string, rawURL string, z *api.Zone, zID string) {
				res := <-rChan
				if res.Err != nil {
					slog.Error("Failed to extract streaming URL", "url", rawURL, "zone_id", zID, "error", res.Err)
				} else {
					track := res.Track
					track.UserID = userID

					// Save resolved track in LRU cache (shared singleton)
					lruCache.Add(rawURL, track)

					// Append resolved track to per-zone Jukebox queue
					z.FSM.ProcessEvent(player.EventAdd, track)
					_ = z.Persister.WriteDelta(persist.OpAppend, track, z.FSM)
				}
				eb.Ack(envelopeID, "orchestrator_add")
			}(resChan, env.ID, payload.UserID, payload.URL, zone, zoneID)
		}
	}()

	// Worker 2: Skip Track Commands
	chSkip := make(chan models.Envelope, 100)
	eb.Subscribe(models.ActionSkip, "orchestrator_skip", chSkip)
	go func() {
		for env := range chSkip {
			zoneID := api.ResolveZoneID(env.ZoneID)
			zone, zoneOk := registry.Get(zoneID)
			if !zoneOk {
				slog.Error("Zone not found for skip command", "zone_id", zoneID)
				eb.Ack(env.ID, "orchestrator_skip")
				continue
			}

			userID, _ := env.Payload.(string)
			if userID != "" {
				slog.Info("Processing skip vote command", "user_id", userID, "zone_id", zoneID)
				zone.FSM.VoteSkip(userID)
			} else {
				slog.Info("Processing force skip command", "zone_id", zoneID)
				zone.FSM.ProcessEvent(player.EventSkip, nil)
			}
			_ = zone.Persister.WriteDelta(persist.OpSkip, nil, zone.FSM)
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

			zoneID := api.ResolveZoneID(payload.ZoneID)
			zone, zoneOk := registry.Get(zoneID)
			if !zoneOk {
				slog.Error("Zone not found for set_volume command", "zone_id", zoneID)
				eb.Ack(env.ID, "orchestrator_vol")
				continue
			}

			slog.Info("Processing set volume command", "level", payload.Level, "zone_id", zoneID)
			zone.FSM.ProcessEvent(player.EventSetVolume, payload.Level)
			// Volume change is logged as a delta via OnVolumeChange observer hook.
			eb.Ack(env.ID, "orchestrator_vol")
		}
	}()

	// Worker 4: Clear Queue Commands
	chClear := make(chan models.Envelope, 100)
	eb.Subscribe(models.ActionClearQueue, "orchestrator_clear", chClear)
	go func() {
		for env := range chClear {
			zoneID := api.ResolveZoneID(env.ZoneID)
			zone, zoneOk := registry.Get(zoneID)
			if !zoneOk {
				slog.Error("Zone not found for clear command", "zone_id", zoneID)
				eb.Ack(env.ID, "orchestrator_clear")
				continue
			}

			slog.Info("Processing clear queue command", "zone_id", zoneID)
			zone.FSM.ProcessEvent(player.EventClear, nil)
			_ = zone.Persister.WriteDelta(persist.OpClear, nil, zone.FSM)
			eb.Ack(env.ID, "orchestrator_clear")
		}
	}()

	// Worker 5: Pause Track Commands
	chPause := make(chan models.Envelope, 100)
	eb.Subscribe(models.ActionPause, "orchestrator_pause", chPause)
	go func() {
		for env := range chPause {
			zoneID := api.ResolveZoneID(env.ZoneID)
			zone, zoneOk := registry.Get(zoneID)
			if !zoneOk {
				slog.Error("Zone not found for pause command", "zone_id", zoneID)
				eb.Ack(env.ID, "orchestrator_pause")
				continue
			}

			slog.Info("Processing pause command", "zone_id", zoneID)
			zone.FSM.ProcessEvent(player.EventPause, nil)
			eb.Ack(env.ID, "orchestrator_pause")
		}
	}()

	// Worker 6: Resume Track Commands
	chResume := make(chan models.Envelope, 100)
	eb.Subscribe(models.ActionResume, "orchestrator_resume", chResume)
	go func() {
		for env := range chResume {
			zoneID := api.ResolveZoneID(env.ZoneID)
			zone, zoneOk := registry.Get(zoneID)
			if !zoneOk {
				slog.Error("Zone not found for resume command", "zone_id", zoneID)
				eb.Ack(env.ID, "orchestrator_resume")
				continue
			}

			slog.Info("Processing resume command", "zone_id", zoneID)
			zone.FSM.ProcessEvent(player.EventResume, nil)
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
