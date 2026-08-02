package backend

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"time"

	sdk "github.com/cameraui/sdk/go"
)

const (
	// grafanaDefaultAlertname is the alertname label used when the user
	// leaves the field blank. It is what Grafana notification policies match
	// on, so it needs a stable, recognizable default.
	grafanaDefaultAlertname = "CameraUINotification"
	// grafanaDefaultTTL is how long (seconds) an alert stays firing before
	// Grafana auto-resolves it.
	grafanaDefaultTTL = 300
	// grafanaMinTTL floors the TTL. Below roughly half a minute an alert can
	// resolve before a notification policy's group_wait has even elapsed, so
	// it would never reach a contact point.
	grafanaMinTTL = 30
)

// grafanaAlerts delivers each notification as a firing alert posted to
// Grafana's built-in Alertmanager, routed onward by whatever notification
// policies the user already has.
//
// Two things reconcile a point-in-time camera event with Alertmanager's
// state-based model. First, endsAt is set to startsAt+TTL so Grafana
// auto-resolves the alert with no second request and no plugin-side timer.
// Second, a unique event_id label is included: Alertmanager deduplicates on
// the exact label set, so without it two detections on one camera inside the
// TTL window would silently collapse into a single alert.
type grafanaAlerts struct{}

func newGrafanaAlerts() *grafanaAlerts { return &grafanaAlerts{} }

func (a *grafanaAlerts) id() string { return grafanaModeAlerts }

func (a *grafanaAlerts) schema() []sdk.JsonSchema {
	cond := grafanaModeCondition(grafanaModeAlerts)
	minTTL := float64(grafanaMinTTL)
	step := float64(30)

	return []sdk.JsonSchema{
		{
			Type:         sdk.JsonSchemaTypeString,
			Key:          "grafana_alertname",
			Title:        "Alert name",
			Description:  "The alertname label your Grafana notification policies match on.",
			DefaultValue: grafanaDefaultAlertname,
			Condition:    cond,
		},
		{
			Type:         sdk.JsonSchemaTypeNumber,
			Key:          "grafana_ttl",
			Title:        "Auto-resolve after (seconds)",
			Description:  "How long the alert stays firing before Grafana resolves it automatically.",
			DefaultValue: grafanaDefaultTTL,
			Minimum:      &minTTL,
			Step:         &step,
			Condition:    cond,
		},
	}
}

func (a *grafanaAlerts) parse(input map[string]any) (map[string]string, error) {
	server, token, err := grafanaServerAndToken(input)
	if err != nil {
		return nil, err
	}

	alertname, _ := input["grafana_alertname"].(string)
	alertname = strings.TrimSpace(alertname)
	if alertname == "" {
		alertname = grafanaDefaultAlertname
	}

	return map[string]string{
		"server":    server,
		"token":     token,
		"alertname": alertname,
		"ttl":       strconv.Itoa(grafanaParseTTL(input["grafana_ttl"])),
	}, nil
}

// grafanaParseTTL normalizes the TTL field to a whole number of seconds.
// This is the only non-string field any backend declares, and
// configuredDevice reads every schema key through Storage.GetValue(key, ""),
// so the value arrives as a float64 from the UI, an int from a test, a
// string from a hand-edited config, or "" / nil when unset. Anything
// unparseable means "not configured" and yields the default; a real value
// below the floor is clamped rather than rejected.
func grafanaParseTTL(v any) int {
	n, ok := grafanaTTLSeconds(v)
	if !ok {
		return grafanaDefaultTTL
	}
	if n < grafanaMinTTL {
		return grafanaMinTTL
	}
	return n
}

// grafanaTTLSeconds extracts an integer from the several concrete types the
// TTL field can arrive as, reporting false when there is no usable number.
func grafanaTTLSeconds(v any) (int, bool) {
	switch t := v.(type) {
	case float64:
		return int(t), true
	case float32:
		return int(t), true
	case int:
		return t, true
	case int64:
		return int(t), true
	case string:
		s := strings.TrimSpace(t)
		if s == "" {
			return 0, false
		}
		n, err := strconv.Atoi(s)
		if err != nil {
			return 0, false
		}
		return n, true
	default:
		return 0, false
	}
}

// grafanaAlertPayload is one entry of the array posted to Alertmanager's v2
// alerts endpoint.
type grafanaAlertPayload struct {
	Labels       map[string]string `json:"labels"`
	Annotations  map[string]string `json:"annotations"`
	StartsAt     string            `json:"startsAt"`
	EndsAt       string            `json:"endsAt"`
	GeneratorURL string            `json:"generatorURL,omitempty"`
}

func (a *grafanaAlerts) send(ctx context.Context, client *http.Client, cfg map[string]string, notif sdk.Notification) error {
	labels := map[string]string{
		"alertname": cfg["alertname"],
		"source":    "camera.ui",
		"severity":  grafanaSeverity(notif),
		"event_id":  grafanaEventID(notif),
	}
	if cam := grafanaCameraID(notif); cam != "" {
		labels["camera"] = cam
	}

	annotations := map[string]string{"summary": notif.Title}
	if notif.Body != "" {
		annotations["description"] = notif.Body
	}
	// Grafana's alert list will not render this, but it carries the snapshot
	// through to downstream notification templates that can.
	if notif.ImageURL != "" {
		annotations["image_url"] = notif.ImageURL
	}

	ttl, err := strconv.Atoi(cfg["ttl"])
	if err != nil || ttl < grafanaMinTTL {
		ttl = grafanaDefaultTTL
	}

	start := time.Now().UTC()
	payload := []grafanaAlertPayload{{
		Labels:      labels,
		Annotations: annotations,
		StartsAt:    start.Format(time.RFC3339),
		EndsAt:      start.Add(time.Duration(ttl) * time.Second).Format(time.RFC3339),
		// generatorURL is Alertmanager's standard "where did this come from"
		// link, surfaced as Source in the Grafana alert UI.
		GeneratorURL: grafanaAbsoluteDeepLink(notif),
	}}

	return grafanaPostJSON(ctx, client, cfg["server"]+"/api/alertmanager/grafana/api/v2/alerts",
		map[string]string{"Authorization": "Bearer " + cfg["token"]},
		payload, "grafana: alerts")
}
