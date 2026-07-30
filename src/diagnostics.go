// Package-level note: this file is a THROWAWAY diagnostic for issue #12. It
// measures what the closed NVR actually sends and what a Notifier plugin can
// actually observe, so the AI-descriptions design (docs/superpowers/specs/
// 2026-07-29-ai-descriptions-design.md) rests on measurements instead of
// guesses. It is deliberately not merged to main. Removal: delete this file,
// revert the two diag fields on NotifyPlugin, and drop the three call sites.
package main

import (
	"fmt"
	"sort"
	"strings"

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
