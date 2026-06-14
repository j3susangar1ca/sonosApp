package api

import (
	"testing"
)

func TestNewZoneRegistry(t *testing.T) {
	zr := NewZoneRegistry()
	if zr == nil {
		t.Fatal("NewZoneRegistry returned nil")
	}
	if zr.zones == nil {
		t.Fatal("zones map is nil")
	}
	if len(zr.zones) != 0 {
		t.Errorf("expected 0 zones, got %d", len(zr.zones))
	}
}

func TestZoneRegistry_RegisterAndGet(t *testing.T) {
	zr := NewZoneRegistry()

	// Test getting non-existent zone
	_, ok := zr.Get("nonexistent")
	if ok {
		t.Error("expected non-existent zone to return false")
	}

	// Register a zone
	z1 := &Zone{ID: "zone1"}
	zr.Register(z1)

	// Get the registered zone
	got, ok := zr.Get("zone1")
	if !ok {
		t.Fatal("expected to find registered zone")
	}
	if got != z1 {
		t.Errorf("expected %v, got %v", z1, got)
	}

	// Register another zone
	z2 := &Zone{ID: "zone2"}
	zr.Register(z2)

	got, ok = zr.Get("zone2")
	if !ok || got != z2 {
		t.Errorf("failed to get zone2")
	}
}

func TestZoneRegistry_GetDefault(t *testing.T) {
	zr := NewZoneRegistry()

	// Default doesn't exist yet
	_, ok := zr.GetDefault()
	if ok {
		t.Error("expected default zone to not exist initially")
	}

	// Register default zone
	defZone := &Zone{ID: "default"}
	zr.Register(defZone)

	got, ok := zr.GetDefault()
	if !ok {
		t.Fatal("expected to find default zone")
	}
	if got != defZone {
		t.Errorf("expected %v, got %v", defZone, got)
	}
}

func TestZoneRegistry_All(t *testing.T) {
	zr := NewZoneRegistry()
	z1 := &Zone{ID: "z1"}
	z2 := &Zone{ID: "z2"}

	zr.Register(z1)
	zr.Register(z2)

	all := zr.All()
	if len(all) != 2 {
		t.Fatalf("expected 2 zones, got %d", len(all))
	}
	if all["z1"] != z1 || all["z2"] != z2 {
		t.Errorf("zones in All() snapshot do not match registered zones")
	}

	// Modifying the returned map should not affect registry
	delete(all, "z1")
	all2 := zr.All()
	if len(all2) != 2 {
		t.Errorf("modifying snapshot affected internal state")
	}
}

func TestZoneRegistry_ForEach(t *testing.T) {
	zr := NewZoneRegistry()
	z1 := &Zone{ID: "z1"}
	z2 := &Zone{ID: "z2"}

	zr.Register(z1)
	zr.Register(z2)

	count := 0
	found := make(map[string]bool)

	zr.ForEach(func(z *Zone) {
		count++
		found[z.ID] = true
	})

	if count != 2 {
		t.Errorf("ForEach visited %d zones, expected 2", count)
	}
	if !found["z1"] || !found["z2"] {
		t.Errorf("ForEach did not visit all zones: %v", found)
	}
}

func TestResolveZoneID(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"", "default"},
		{"default", "default"},
		{"other", "other"},
		{"  ", "  "},
	}

	for _, tt := range tests {
		got := ResolveZoneID(tt.input)
		if got != tt.expected {
			t.Errorf("ResolveZoneID(%q) = %q, expected %q", tt.input, got, tt.expected)
		}
	}
}
