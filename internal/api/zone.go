package api

import (
	"sync"

	"github.com/jesuslangarica/sonosApp/internal/persist"
	"github.com/jesuslangarica/sonosApp/internal/player"
)

// Zone represents a single audio zone with its own FSM and hardware adapters.
// Each zone maintains independent mutable state per the invariant:
// ∀i,j ∈ [1,m], i≠j: q^(i) and q^(j) do not share mutable state (§20.2).
type Zone struct {
	ID            string
	FSM           *player.JukeboxFSM
	SonosIP       string
	ActionHandler player.ActionHandler
	Persister     *persist.Persister
}

// ZoneRegistry manages multiple independent audio zones.
// WorkerPool and LRUCache remain as shared singletons external to this registry.
//
// The registry's RWMutex protects exclusively the map of zones (registration/lookup),
// never the internal resources of each zone (those are protected by their own locks).
type ZoneRegistry struct {
	mu    sync.RWMutex
	zones map[string]*Zone
}

// NewZoneRegistry creates an empty zone registry.
func NewZoneRegistry() *ZoneRegistry {
	return &ZoneRegistry{
		zones: make(map[string]*Zone),
	}
}

// Register adds a zone to the registry.
func (zr *ZoneRegistry) Register(zone *Zone) {
	zr.mu.Lock()
	defer zr.mu.Unlock()
	zr.zones[zone.ID] = zone
}

// Get returns the zone for the given ID, or (nil, false) if not found.
func (zr *ZoneRegistry) Get(zoneID string) (*Zone, bool) {
	zr.mu.RLock()
	defer zr.mu.RUnlock()
	z, ok := zr.zones[zoneID]
	return z, ok
}

// GetDefault returns the "default" zone, or (nil, false) if absent.
func (zr *ZoneRegistry) GetDefault() (*Zone, bool) {
	return zr.Get("default")
}

// All returns a snapshot copy of all registered zones.
// The map values are pointers to the original Zone structs (not deep copies).
func (zr *ZoneRegistry) All() map[string]*Zone {
	zr.mu.RLock()
	defer zr.mu.RUnlock()
	out := make(map[string]*Zone, len(zr.zones))
	for id, z := range zr.zones {
		out[id] = z
	}
	return out
}

// ForEach invokes fn for every registered zone while holding a read lock.
// fn MUST NOT call any ZoneRegistry methods (would deadlock).
func (zr *ZoneRegistry) ForEach(fn func(zone *Zone)) {
	zr.mu.RLock()
	defer zr.mu.RUnlock()
	for _, z := range zr.zones {
		fn(z)
	}
}

// ResolveZoneID resolves a zone_id string: if empty, returns "default".
func ResolveZoneID(zoneID string) string {
	if zoneID == "" {
		return "default"
	}
	return zoneID
}
