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
