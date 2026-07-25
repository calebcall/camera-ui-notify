package backend

import (
	"context"
	"testing"

	sdk "github.com/cameraui/sdk/go"
)

// fakeBackend is a minimal Backend implementation used to exercise the
// registry without depending on any real delivery backend (those land in
// Tasks 5-7).
type fakeBackend struct {
	id    string
	label string
}

func (f *fakeBackend) ID() string    { return f.id }
func (f *fakeBackend) Label() string { return f.label }
func (f *fakeBackend) Schema() []sdk.JsonSchema {
	return []sdk.JsonSchema{{Type: sdk.JsonSchemaTypeString, Key: f.id + "-field"}}
}
func (f *fakeBackend) ParseTarget(input map[string]any) (map[string]string, error) {
	return nil, nil
}
func (f *fakeBackend) Send(ctx context.Context, cfg map[string]string, n sdk.Notification) error {
	return nil
}

func TestRegisterGetAll(t *testing.T) {
	zed := &fakeBackend{id: "zed-registry-test", label: "Zed"}
	alpha := &fakeBackend{id: "alpha-registry-test", label: "Alpha"}

	Register(zed)
	Register(alpha)

	got, ok := Get("zed-registry-test")
	if !ok || got != Backend(zed) {
		t.Fatalf("Get(zed) = %v, %v; want %v, true", got, ok, zed)
	}

	got, ok = Get("alpha-registry-test")
	if !ok || got != Backend(alpha) {
		t.Fatalf("Get(alpha) = %v, %v; want %v, true", got, ok, alpha)
	}

	if _, ok := Get("nope-registry-test"); ok {
		t.Fatalf("Get(unknown) ok = true, want false")
	}

	all := All()
	idxAlpha, idxZed := -1, -1
	for i, b := range all {
		switch b.ID() {
		case "alpha-registry-test":
			idxAlpha = i
		case "zed-registry-test":
			idxZed = i
		}
	}
	if idxAlpha == -1 || idxZed == -1 {
		t.Fatalf("All() missing registered backends: %+v", all)
	}
	if idxAlpha >= idxZed {
		t.Fatalf("All() not sorted by ID: alpha at %d, zed at %d", idxAlpha, idxZed)
	}
	for i := 1; i < len(all); i++ {
		if all[i-1].ID() > all[i].ID() {
			t.Fatalf("All() not sorted: %q before %q", all[i-1].ID(), all[i].ID())
		}
	}
}

func TestRegisterPanicsOnDuplicate(t *testing.T) {
	Register(&fakeBackend{id: "dup-registry-test", label: "First"})

	defer func() {
		if r := recover(); r == nil {
			t.Fatalf("Register did not panic on duplicate id")
		}
	}()
	Register(&fakeBackend{id: "dup-registry-test", label: "Second"})
}

func TestPriorityScaleBoundaries(t *testing.T) {
	const lo, hi = 1, 5
	if got := PriorityScale(sdk.SeverityInfo, lo, hi); got != lo {
		t.Errorf("PriorityScale(Info,%d,%d) = %d, want %d", lo, hi, got, lo)
	}
	if got := PriorityScale(sdk.SeverityCritical, lo, hi); got != hi {
		t.Errorf("PriorityScale(Critical,%d,%d) = %d, want %d", lo, hi, got, hi)
	}
}

func TestPriorityScaleMonotonic(t *testing.T) {
	const lo, hi = 1, 5
	info := PriorityScale(sdk.SeverityInfo, lo, hi)
	warn := PriorityScale(sdk.SeverityWarn, lo, hi)
	errS := PriorityScale(sdk.SeverityError, lo, hi)
	crit := PriorityScale(sdk.SeverityCritical, lo, hi)

	if !(info <= warn && warn <= errS && errS <= crit) {
		t.Fatalf("PriorityScale not monotonic: info=%d warn=%d error=%d critical=%d", info, warn, errS, crit)
	}
	if info < lo || crit > hi {
		t.Fatalf("PriorityScale out of range [%d,%d]: info=%d critical=%d", lo, hi, info, crit)
	}

	// A wider range (0..10, matching the Gotify backend's usage) should
	// still be monotonic and hit the exact endpoints.
	const lo2, hi2 = 0, 10
	if got := PriorityScale(sdk.SeverityInfo, lo2, hi2); got != lo2 {
		t.Errorf("PriorityScale(Info,%d,%d) = %d, want %d", lo2, hi2, got, lo2)
	}
	if got := PriorityScale(sdk.SeverityCritical, lo2, hi2); got != hi2 {
		t.Errorf("PriorityScale(Critical,%d,%d) = %d, want %d", lo2, hi2, got, hi2)
	}
	warn2 := PriorityScale(sdk.SeverityWarn, lo2, hi2)
	err2 := PriorityScale(sdk.SeverityError, lo2, hi2)
	if !(lo2 <= warn2 && warn2 <= err2 && err2 <= hi2) {
		t.Fatalf("PriorityScale not monotonic over [%d,%d]: warn=%d error=%d", lo2, hi2, warn2, err2)
	}
}
