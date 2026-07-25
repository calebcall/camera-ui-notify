// Package backend defines the pluggable delivery-backend abstraction used
// by the Notify plugin. Each concrete backend (ntfy, Gotify, generic
// webhook, ...) lives in its own file in this package and self-registers
// from an init() function via Register. Adding a new backend later is one
// new file + a version bump — nothing outside this package needs to change.
package backend

import (
	"context"
	"fmt"
	"sort"
	"sync"

	sdk "github.com/cameraui/sdk/go"
)

// Backend abstracts one notification delivery service. Implementations are
// stateless with respect to any single device — all per-device
// configuration flows through the cfg map returned by ParseTarget and
// passed back into Send.
type Backend interface {
	// ID is the stable identifier used in NotifierDevice.Metadata["service"]
	// and as the enum value in the "service" schema field. Never change an
	// existing ID once shipped: it is persisted in user devices.
	ID() string
	// Label is the human-readable name shown in the service dropdown.
	Label() string
	// Schema returns this backend's device-config fields. Each field should
	// be gated with Condition: []SchemaCondition{{Key: "service", Value: ID()}}
	// so it only renders when this backend is selected.
	Schema() []sdk.JsonSchema
	// ParseTarget validates the raw registration input (as submitted through
	// the schema from Schema, plus the "service" field) and returns the
	// normalized string-keyed config to persist in NotifierDevice.Metadata.
	// Returns an error describing what is missing or invalid.
	ParseTarget(input map[string]any) (map[string]string, error)
	// Send delivers a single notification using the config previously
	// produced by ParseTarget.
	Send(ctx context.Context, cfg map[string]string, n sdk.Notification) error
}

var (
	mu       sync.Mutex
	registry = map[string]Backend{}
)

// Register adds a backend to the registry under its ID(). Intended to be
// called from each backend's init() function. Panics if a backend with the
// same ID is already registered — this is a programming error (duplicate
// backend ids), not a runtime condition to recover from.
func Register(b Backend) {
	mu.Lock()
	defer mu.Unlock()

	id := b.ID()
	if _, exists := registry[id]; exists {
		panic(fmt.Sprintf("backend: duplicate id %q", id))
	}
	registry[id] = b
}

// Get looks up a registered backend by id.
func Get(id string) (Backend, bool) {
	mu.Lock()
	defer mu.Unlock()

	b, ok := registry[id]
	return b, ok
}

// All returns a snapshot of every registered backend, sorted by ID() for a
// stable iteration order (used to build the "service" enum and to flatten
// per-backend schemas).
func All() []Backend {
	mu.Lock()
	defer mu.Unlock()

	out := make([]Backend, 0, len(registry))
	for _, b := range registry {
		out = append(out, b)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID() < out[j].ID() })
	return out
}

// severityRank orders the four severities from least to most urgent.
var severityRank = map[sdk.Severity]int{
	sdk.SeverityInfo:     0,
	sdk.SeverityWarn:     1,
	sdk.SeverityError:    2,
	sdk.SeverityCritical: 3,
}

// PriorityScale maps an sdk.Severity onto an evenly-spaced integer point in
// [lo, hi], so backends can translate camera.ui's four-level severity into
// whatever priority range their API expects (e.g. ntfy's 1..5, Gotify's
// 0..10). SeverityInfo maps to lo, SeverityCritical maps to hi, and
// SeverityWarn/SeverityError fall strictly in between (monotonic
// non-decreasing across the four levels). Unknown/empty severities are
// treated as SeverityInfo.
func PriorityScale(sev sdk.Severity, lo, hi int) int {
	rank, ok := severityRank[sev]
	if !ok {
		rank = severityRank[sdk.SeverityInfo]
	}
	if hi == lo {
		return lo
	}
	const maxRank = 3 // len(severityRank) - 1
	return lo + (hi-lo)*rank/maxRank
}
