// Package-level note: this file is a THROWAWAY diagnostic for issue #12. It
// measures what the closed NVR actually sends and what a Notifier plugin can
// actually observe, so the AI-descriptions design (docs/superpowers/specs/
// 2026-07-29-ai-descriptions-design.md) rests on measurements instead of
// guesses. It is deliberately not merged to main. Removal: delete this file,
// revert the two diag fields on NotifyPlugin, and drop the four call sites:
// StorageSchema, ConfigureCameras, SendNotification, and the NewPlugin
// shutdown hook.
package main

import (
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	sdk "github.com/cameraui/sdk/go"
)

// diagPrefix begins every diagnostic line so a whole run can be recovered with
// `grep 'notify: diag:'` and pasted into the issue.
const diagPrefix = "notify: diag:"

// diagMaxLinesPerEvent bounds diagnostic lines per detection event, so one busy
// camera cannot flood the log during an observation window.
const diagMaxLinesPerEvent = 12

// diagEnabled reports whether diagnostics are switched on. Accepts both a real
// bool and the string "true" because a value round-tripped through the host's
// config form may arrive either way, and a spike that silently stays off
// because of a type mismatch wastes an evening.
func diagEnabled(s *sdk.DeviceStorage) bool {
	if s == nil {
		return false
	}
	switch v := s.GetValue("diagnostics", false).(type) {
	case bool:
		return v
	case string:
		return v == "true"
	default:
		return false
	}
}

// formatNotificationDiag renders one notification's shape as a single line.
//
// Data keys are sorted because Go randomises map iteration order: without
// sorting, the same notification would render differently run to run, the test
// could not assert on the output, and two log lines could not be compared by
// eye. Values are quoted with %q so an empty string is distinguishable from a
// missing key — which is the whole point of question 1.
func formatNotificationDiag(n *sdk.Notification) string {
	if n == nil {
		return diagPrefix + " notification: <nil>"
	}

	keys := make([]string, 0, len(n.Data))
	for k := range n.Data {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	pairs := make([]string, 0, len(keys))
	for _, k := range keys {
		pairs = append(pairs, fmt.Sprintf("%s=%q", k, n.Data[k]))
	}

	return fmt.Sprintf(
		"%s notification: title=%q tag=%q severity=%q deepLink=%q bodyLen=%d hasThumbnail=%t hasImageURL=%t dataKeys=%d data{%s}",
		diagPrefix, n.Title, n.Tag, string(n.Severity), n.DeepLink,
		len(n.Body), len(n.Thumbnail) > 0, n.ImageURL != "",
		len(n.Data), strings.Join(pairs, " "),
	)
}

// diagNotification emits the notification diagnostic when enabled. Read-only:
// it never touches the notification it is handed.
func (p *NotifyPlugin) diagNotification(n *sdk.Notification) {
	if !diagEnabled(p.Storage) {
		return
	}
	p.logf("%s", formatNotificationDiag(n))
}

// diagCamera is the minimal projection of a camera the diagnostics need.
//
// Formatting is defined over this rather than over *sdk.CameraDevice because
// CameraDevice's fields are unexported and it has no exported constructor, so a
// test cannot build one carrying a meaningful id or name. Keeping the format
// logic here makes it fully testable and leaves toDiagCameras as a trivial loop.
type diagCamera struct {
	ID   string
	Name string
}

// formatCamerasDiag reports which cameras the host handed this plugin. Zero is
// the expected answer today (Notify declares provides/consumes empty and is
// assigned no cameras) — question 2 exists to confirm that rather than assume it.
func formatCamerasDiag(cams []diagCamera) string {
	if len(cams) == 0 {
		return diagPrefix + " configureCameras: 0 cameras assigned to Notify"
	}
	parts := make([]string, 0, len(cams))
	for _, c := range cams {
		parts = append(parts, fmt.Sprintf("%s(%q)", c.ID, c.Name))
	}
	return fmt.Sprintf("%s configureCameras: %d camera(s) assigned: %s",
		diagPrefix, len(cams), strings.Join(parts, " "))
}

// toDiagCameras projects SDK camera devices onto diagCamera, dropping nils.
func toDiagCameras(cams []*sdk.CameraDevice) []diagCamera {
	out := make([]diagCamera, 0, len(cams))
	for _, c := range cams {
		if c == nil {
			continue
		}
		out = append(out, diagCamera{ID: c.ID(), Name: c.Name()})
	}
	return out
}

// formatProbeDiag renders the outcome of a GetCamera probe. The three outcomes
// are spelled out in the message because this single line decides whether the
// feature needs a per-camera hub assignment or can look cameras up lazily.
func formatProbeDiag(cameraID string, found bool, name string, err error) string {
	switch {
	case err != nil:
		return fmt.Sprintf("%s getCamera(%q): ERROR %v -- unassigned cameras are NOT reachable",
			diagPrefix, cameraID, err)
	case !found:
		return fmt.Sprintf("%s getCamera(%q): nil (no error) -- camera not visible to this plugin",
			diagPrefix, cameraID)
	default:
		return fmt.Sprintf("%s getCamera(%q): OK name=%q -- unassigned cameras ARE reachable",
			diagPrefix, cameraID, name)
	}
}

// diagCameras emits the assigned-camera inventory when enabled.
func (p *NotifyPlugin) diagCameras(cams []*sdk.CameraDevice) {
	if !diagEnabled(p.Storage) {
		return
	}
	p.logf("%s", formatCamerasDiag(toDiagCameras(cams)))
}

// diagProbeCamera asks the host for a camera by id and reports the outcome,
// returning the device so the caller can subscribe to its detection events.
// Returns nil when unavailable for any reason. Not unit tested: p.API is nil in
// tests and *sdk.CameraDevice cannot be constructed outside the SDK, so this
// adapter is verified by the live run instead — which is what the spike is for.
func (p *NotifyPlugin) diagProbeCamera(cameraID string) *sdk.CameraDevice {
	if !diagEnabled(p.Storage) || cameraID == "" {
		return nil
	}
	if p.API == nil || p.API.DeviceManager == nil {
		p.logf("%s getCamera(%q): skipped, no DeviceManager available", diagPrefix, cameraID)
		return nil
	}
	cam, err := p.API.DeviceManager.GetCamera(cameraID)
	name := ""
	if cam != nil {
		name = cam.Name()
	}
	p.logf("%s", formatProbeDiag(cameraID, cam != nil, name, err))
	return cam
}

// formatDetectionDiag renders one detection-event message.
//
// nowMs is passed in rather than read from the clock so the output is
// deterministic under test. elapsedMs is the answer to question 4: how long
// after event start a Summary becomes available, which sets the feature's
// timeout default. It is -1 when StartTime is unset, so an unknown elapsed time
// is never mistaken for a fast one.
//
// A segment counts as carrying a description only when Summary is non-blank: a
// non-nil Description with an empty Summary is useless to a notification, and
// counting it would overstate availability.
func formatDetectionDiag(eventType sdk.DetectionEventType, ev sdk.DetectionEvent, nowMs int64) string {
	withSummary := make([]string, 0, len(ev.Segments))
	for i, seg := range ev.Segments {
		if seg.Description != nil && strings.TrimSpace(seg.Description.Summary) != "" {
			withSummary = append(withSummary, fmt.Sprintf("%d", i))
		}
	}

	elapsed := int64(-1)
	if ev.StartTime > 0 {
		elapsed = nowMs - ev.StartTime
	}

	return fmt.Sprintf(
		"%s detectionEvent: type=%s eventId=%q cameraId=%q state=%q segments=%d withSummary=[%s] elapsedMs=%d",
		diagPrefix, string(eventType), ev.ID, ev.CameraID, string(ev.State),
		len(ev.Segments), strings.Join(withSummary, ","), elapsed,
	)
}

// diagLogCap bounds diagnostic lines per event id so a busy camera cannot flood
// the log during an observation window.
type diagLogCap struct {
	mu    sync.Mutex
	limit int
	seen  map[string]int
}

func newDiagLogCap(limit int) *diagLogCap {
	return &diagLogCap{limit: limit, seen: map[string]int{}}
}

// allow reports whether another line may be emitted for eventID, counting this
// request. Each event id gets its own budget so one chatty event cannot silence
// the others.
func (c *diagLogCap) allow(eventID string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.limit <= 0 {
		return false
	}
	c.seen[eventID]++
	return c.seen[eventID] <= c.limit
}

// diagDisposer is the only part of *sdk.Disposable the registry uses. Narrowing
// to it makes the registry testable without SDK internals.
type diagDisposer interface{ Dispose() }

// diagSubscriptions tracks one detection-event subscription per camera id.
type diagSubscriptions struct {
	mu   sync.Mutex
	subs map[string]diagDisposer
}

func newDiagSubscriptions() *diagSubscriptions {
	return &diagSubscriptions{subs: map[string]diagDisposer{}}
}

// add registers d for cameraID and reports whether it was stored. A duplicate
// is refused AND disposed immediately: the caller has already created a live
// subscription by then, so dropping it on the floor would leak it.
func (s *diagSubscriptions) add(cameraID string, d diagDisposer) bool {
	if d == nil {
		return false
	}
	s.mu.Lock()
	if _, exists := s.subs[cameraID]; exists {
		s.mu.Unlock()
		d.Dispose()
		return false
	}
	s.subs[cameraID] = d
	s.mu.Unlock()
	return true
}

func (s *diagSubscriptions) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.subs)
}

// disposeAll releases every subscription and empties the registry, so calling
// it twice is safe.
func (s *diagSubscriptions) disposeAll() {
	s.mu.Lock()
	subs := make([]diagDisposer, 0, len(s.subs))
	for _, d := range s.subs {
		subs = append(subs, d)
	}
	s.subs = map[string]diagDisposer{}
	s.mu.Unlock()

	for _, d := range subs {
		d.Dispose()
	}
}

// diagRuntime holds the spike's mutable state.
type diagRuntime struct {
	cap  *diagLogCap
	subs *diagSubscriptions
}

// diagRT lazily builds the runtime. Lazy because tests construct NotifyPlugin as
// a bare struct literal (see newTestPlugin) and never call NewPlugin, so a
// constructor-initialised field would be nil in exactly the code paths tests
// exercise.
func (p *NotifyPlugin) diagRT() *diagRuntime {
	p.diagOnce.Do(func() {
		p.diag = &diagRuntime{
			cap:  newDiagLogCap(diagMaxLinesPerEvent),
			subs: newDiagSubscriptions(),
		}
	})
	return p.diag
}

// diagObserveCamera subscribes to cam's detection events once, logging each
// message until that event's line budget is spent. Not unit tested: it needs a
// real *sdk.CameraDevice, which cannot be built outside the SDK. Its two
// testable halves — the registry and the formatter — are covered separately.
func (p *NotifyPlugin) diagObserveCamera(cam *sdk.CameraDevice) {
	if cam == nil || !diagEnabled(p.Storage) {
		return
	}
	rt := p.diagRT()

	disposable := cam.OnDetectionEvent(func(eventType sdk.DetectionEventType, ev sdk.DetectionEvent) {
		if !rt.cap.allow(ev.ID) {
			return
		}
		p.logf("%s", formatDetectionDiag(eventType, ev, time.Now().UnixMilli()))
	})

	if rt.subs.add(cam.ID(), disposable) {
		p.logf("%s subscribed to detection events for camera %q", diagPrefix, cam.ID())
	}
}

// DiagShutdown releases every subscription. Wired to APIEventShutdown so the
// spike leaves nothing running when the host tears the plugin down.
func (p *NotifyPlugin) DiagShutdown() {
	p.diagRT().subs.disposeAll()
}
