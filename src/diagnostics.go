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

// formatProbeDiag renders the outcome of a GetCamera probe for cameraID,
// found via the notification Data key matchedKey. The three outcomes are
// reported as observations, not conclusions: GetCamera checks a local cache
// first, which ConfigureCameras populates, so an OK result does not by
// itself prove an *unassigned* camera is reachable — it may be a cache hit
// on a camera this plugin was assigned (cross-check against question 2's
// assigned-camera list). Likewise GetCamera can return an error from
// internal device initialisation that has nothing to do with visibility, so
// an error does not by itself prove the camera is unreachable.
func formatProbeDiag(cameraID, matchedKey string, found bool, name string, err error) string {
	switch {
	case err != nil:
		return fmt.Sprintf("%s getCamera(%q via %s): ERROR %v -- inconclusive: could be visibility, or a device-init failure unrelated to visibility",
			diagPrefix, cameraID, matchedKey, err)
	case !found:
		return fmt.Sprintf("%s getCamera(%q via %s): nil (no error) -- camera was not returned to this plugin",
			diagPrefix, cameraID, matchedKey)
	default:
		return fmt.Sprintf("%s getCamera(%q via %s): OK name=%q -- reachable, but this may be a ConfigureCameras cache hit rather than proof this camera is unassigned; cross-check against question 2's assigned-camera list",
			diagPrefix, cameraID, matchedKey, name)
	}
}

// diagCameraIDKeys lists the notification Data key spellings this spike
// tries, in priority order. cameraId is only a guess (pre-existing code in
// src/backend/deeplink.go relies on it), and the NVR's actual spelling is
// exactly what question 1 exists to determine, so more than one candidate is
// tried and the log records which one, if any, matched.
var diagCameraIDKeys = []string{"cameraId", "cameraID", "camera"}

// findDiagCameraID returns the value and name of the first key in
// diagCameraIDKeys present in data with a non-empty value. ok is false when
// none matched, so the caller can say so explicitly instead of silently
// doing nothing.
func findDiagCameraID(data map[string]string) (id string, key string, ok bool) {
	for _, k := range diagCameraIDKeys {
		if v, present := data[k]; present && v != "" {
			return v, k, true
		}
	}
	return "", "", false
}

// formatNoCameraIDDiag reports that none of triedKeys carried a camera id, so
// the log states the reason questions 3 and 4 produced no further output
// instead of leaving that to be inferred from silence.
func formatNoCameraIDDiag(triedKeys []string) string {
	return fmt.Sprintf("%s getCamera: no camera-id key found in notification Data (tried: %s)",
		diagPrefix, strings.Join(triedKeys, ", "))
}

// diagCameras emits the assigned-camera inventory when enabled.
func (p *NotifyPlugin) diagCameras(cams []*sdk.CameraDevice) {
	if !diagEnabled(p.Storage) {
		return
	}
	p.logf("%s", formatCamerasDiag(toDiagCameras(cams)))
}

// diagProbeCamera looks for a camera id under any of diagCameraIDKeys in
// data, asks the host for that camera, and reports the outcome, returning
// the device so the caller can subscribe to its detection events. Returns
// nil when unavailable for any reason — always after logging why. Not unit
// tested beyond findDiagCameraID: p.API is nil in tests and
// *sdk.CameraDevice cannot be constructed outside the SDK, so the
// GetCamera call itself is verified by the live run instead — which is what
// the spike is for.
func (p *NotifyPlugin) diagProbeCamera(data map[string]string) *sdk.CameraDevice {
	if !diagEnabled(p.Storage) {
		return nil
	}
	cameraID, key, ok := findDiagCameraID(data)
	if !ok {
		p.logf("%s", formatNoCameraIDDiag(diagCameraIDKeys))
		return nil
	}
	if p.API == nil || p.API.DeviceManager == nil {
		p.logf("%s getCamera(%q via %s): skipped, no DeviceManager available", diagPrefix, cameraID, key)
		return nil
	}
	cam, err := p.API.DeviceManager.GetCamera(cameraID)
	name := ""
	if cam != nil {
		name = cam.Name()
	}
	p.logf("%s", formatProbeDiag(cameraID, key, cam != nil, name, err))
	return cam
}

// segmentHasSummary reports whether seg carries a non-blank
// Description.Summary. A non-nil Description with an empty Summary is
// useless to a notification, so it does not count.
func segmentHasSummary(seg sdk.EventSegment) bool {
	return seg.Description != nil && strings.TrimSpace(seg.Description.Summary) != ""
}

// eventHasSummary reports whether ev carries at least one segment with a
// non-blank Description.Summary. This is the one predicate shared by
// formatDetectionDiag (which lists which segments qualify) and diagLogCap's
// budget gate (which must never drop the message that answers question 4),
// so the strings.TrimSpace check lives in exactly one place.
func eventHasSummary(ev sdk.DetectionEvent) bool {
	for _, seg := range ev.Segments {
		if segmentHasSummary(seg) {
			return true
		}
	}
	return false
}

// formatDetectionDiag renders one detection-event message.
//
// nowMs is passed in rather than read from the clock so the output is
// deterministic under test. elapsedMs is the answer to question 4: how long
// after event start a Summary becomes available, which sets the feature's
// timeout default. It is -1 when StartTime is unset, so an unknown elapsed time
// is never mistaken for a fast one.
//
// msgSegments is the length of this message's own Segments slice, not the
// event's total segment count: the SDK sends only the current segment on
// segment-* messages and none on start/end, so this is always 0 or 1 and
// must not be read as "this event has N segments overall". segmentIndex
// (ev.SegmentIndex) is reported alongside it so a summary can be pinned to
// which segment produced it.
func formatDetectionDiag(eventType sdk.DetectionEventType, ev sdk.DetectionEvent, nowMs int64) string {
	withSummary := make([]string, 0, len(ev.Segments))
	for i, seg := range ev.Segments {
		if segmentHasSummary(seg) {
			withSummary = append(withSummary, fmt.Sprintf("%d", i))
		}
	}

	elapsed := int64(-1)
	if ev.StartTime > 0 {
		elapsed = nowMs - ev.StartTime
	}

	return fmt.Sprintf(
		"%s detectionEvent: type=%s eventId=%q cameraId=%q state=%q msgSegments=%d segmentIndex=%d withSummary=[%s] elapsedMs=%d",
		diagPrefix, string(eventType), ev.ID, ev.CameraID, string(ev.State),
		len(ev.Segments), ev.SegmentIndex, strings.Join(withSummary, ","), elapsed,
	)
}

// formatDetectionBudgetExhaustedDiag renders the one-time notice logged when
// an event id's line budget runs out, so the log states why messages stopped
// instead of trailing off into unexplained silence.
func formatDetectionBudgetExhaustedDiag(eventID string, capacity int) string {
	return fmt.Sprintf("%s detectionEvent: eventId=%q budget exhausted (cap=%d), suppressing further non-summary messages for this event",
		diagPrefix, eventID, capacity)
}

// diagGateAction is what a detection-event message should do, as decided by
// diagLogCap.decide.
type diagGateAction int

const (
	// diagGateSuppress means say nothing: budget is spent and the one-time
	// notice for this event id has already fired.
	diagGateSuppress diagGateAction = iota
	// diagGateLogMessage means log the detection-event line itself.
	diagGateLogMessage
	// diagGateLogNotice means log the one-time exhaustion notice instead of
	// the detection-event line.
	diagGateLogNotice
)

// diagLogCap bounds diagnostic lines per event id so a busy camera cannot flood
// the log during an observation window.
type diagLogCap struct {
	mu       sync.Mutex
	limit    int
	seen     map[string]int
	notified map[string]bool
}

func newDiagLogCap(limit int) *diagLogCap {
	return &diagLogCap{limit: limit, seen: map[string]int{}, notified: map[string]bool{}}
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

// noticeOnce reports true the first time eventID's budget is found
// exhausted, and false on every call after that for the same eventID — so
// the caller can log exactly one exhaustion notice per event id, then fall
// silent.
func (c *diagLogCap) noticeOnce(eventID string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.notified[eventID] {
		return false
	}
	c.notified[eventID] = true
	return true
}

// decide applies the summary exemption and the line budget to determine what
// should happen with one detection-event message. A message whose event
// carries a non-blank Summary (hasSummary) is always logged and never
// consumes budget: summary-carrying messages are rare, and the one
// observation the spike exists to capture must never be the one dropped for
// running out of budget. Every other message consumes budget as before; once
// exhausted, decide reports the one-time notice exactly once and suppress
// after that.
func (c *diagLogCap) decide(eventID string, hasSummary bool) diagGateAction {
	if hasSummary {
		return diagGateLogMessage
	}
	if c.allow(eventID) {
		return diagGateLogMessage
	}
	if c.noticeOnce(eventID) {
		return diagGateLogNotice
	}
	return diagGateSuppress
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

// formatSubscribeDiag confirms a camera's detection-event subscription was
// registered, in this file's "label: fields" convention.
func formatSubscribeDiag(cameraID string) string {
	return fmt.Sprintf("%s subscribe: camera=%q ok", diagPrefix, cameraID)
}

// diagObserveCamera subscribes to cam's detection events once, logging each
// message per the diagLogCap gate (formatDetectionDiag while budget or a
// summary allows it, one exhaustion notice, then silence). diagEnabled is
// re-checked on every message, not just at subscribe time, so switching the
// setting off in the UI stops output immediately rather than only on the
// next plugin restart. Not unit tested: it needs a real *sdk.CameraDevice,
// which cannot be built outside the SDK. Its testable halves — the
// registry, the gate, and the formatters — are covered separately.
func (p *NotifyPlugin) diagObserveCamera(cam *sdk.CameraDevice) {
	if cam == nil || !diagEnabled(p.Storage) {
		return
	}
	rt := p.diagRT()

	disposable := cam.OnDetectionEvent(func(eventType sdk.DetectionEventType, ev sdk.DetectionEvent) {
		if !diagEnabled(p.Storage) {
			return
		}
		switch rt.cap.decide(ev.ID, eventHasSummary(ev)) {
		case diagGateLogMessage:
			p.logf("%s", formatDetectionDiag(eventType, ev, time.Now().UnixMilli()))
		case diagGateLogNotice:
			p.logf("%s", formatDetectionBudgetExhaustedDiag(ev.ID, diagMaxLinesPerEvent))
		}
	})

	if rt.subs.add(cam.ID(), disposable) {
		p.logf("%s", formatSubscribeDiag(cam.ID()))
	}
}

// DiagShutdown releases every subscription. Wired to APIEventShutdown so the
// spike leaves nothing running when the host tears the plugin down.
func (p *NotifyPlugin) DiagShutdown() {
	p.diagRT().subs.disposeAll()
}
