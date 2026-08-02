package backend

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	sdk "github.com/cameraui/sdk/go"
)

// The three Grafana delivery surfaces this backend supports. These strings
// are persisted in the plugin's config as the value of "grafana_mode" and in
// the parsed cfg map as "mode" — never change one once shipped.
const (
	grafanaModeAnnotations = "annotations"
	grafanaModeAlerts      = "alerts"
	grafanaModeIRM         = "irm"
)

// grafanaMode is a private second strategy layer, mirroring the package's
// public Backend interface one level down. "Grafana" is not one ingest
// endpoint — annotations, Alertmanager alerts and IRM alert groups differ in
// URL, auth and payload — so each gets its own file and its own unit tests,
// and grafana.Schema/ParseTarget/Send merely dispatch.
type grafanaMode interface {
	// id is the value of the "grafana_mode" config field selecting this mode.
	id() string
	// schema returns only this mode's exclusive fields. Fields shared across
	// modes (server, token) are declared once by grafana.Schema instead, so
	// the flattened StorageSchema never carries a duplicate key.
	schema() []sdk.JsonSchema
	// parse validates the raw config input for this mode and returns the
	// normalized cfg. It may read shared keys via grafanaServerAndToken.
	parse(input map[string]any) (map[string]string, error)
	// send delivers one notification. client and ctx are supplied by
	// grafana.Send so the mode holds no state of its own.
	send(ctx context.Context, client *http.Client, cfg map[string]string, notif sdk.Notification) error
}

// grafana implements Backend for Grafana. Which surface a notification is
// delivered to is chosen by the "grafana_mode" config field; see grafanaMode.
type grafana struct {
	// client performs the HTTP request. Defaulted by newGrafana; overridable
	// in tests so Send never touches the real network.
	client *http.Client
	// modes is the mode registry, populated by newGrafana.
	modes []grafanaMode
}

// newGrafana constructs a Grafana backend with a client suitable for
// production use (~10s timeout). Tests override the client field to point at
// an httptest server.
func newGrafana() *grafana {
	return &grafana{
		client: &http.Client{Timeout: 10 * time.Second},
		modes: []grafanaMode{
			newGrafanaAnnotations(),
			newGrafanaAlerts(),
		},
	}
}

func init() {
	Register(newGrafana())
}

func (g *grafana) ID() string    { return "grafana" }
func (g *grafana) Label() string { return "Grafana" }

// mode looks up a registered mode by its id.
func (g *grafana) mode(id string) (grafanaMode, bool) {
	for _, m := range g.modes {
		if m.id() == id {
			return m, true
		}
	}
	return nil, false
}

// grafanaServiceCondition gates a field on this backend being the selected
// service.
func grafanaServiceCondition() []sdk.SchemaCondition {
	return []sdk.SchemaCondition{{Key: "service", Value: "grafana"}}
}

// grafanaModeCondition gates a field on both this backend being selected and
// one specific mode being active. sdk.SchemaCondition slices AND together.
func grafanaModeCondition(mode string) []sdk.SchemaCondition {
	return []sdk.SchemaCondition{
		{Key: "service", Value: "grafana"},
		{Key: "grafana_mode", Value: mode},
	}
}

// Schema returns the mode selector, the fields shared by the annotations and
// alerts modes (both address the same Grafana instance with the same
// service-account token), and then each mode's own fields. The shared fields
// are declared here rather than by each mode so the flattened StorageSchema
// never carries a duplicate key.
func (g *grafana) Schema() []sdk.JsonSchema {
	instanceModes := []string{grafanaModeAnnotations, grafanaModeAlerts}
	instanceCond := []sdk.SchemaCondition{
		{Key: "service", Value: "grafana"},
		{Key: "grafana_mode", Operator: sdk.SchemaConditionIn, Value: instanceModes},
	}

	fields := []sdk.JsonSchema{
		{
			Type:         sdk.JsonSchemaTypeString,
			Key:          "grafana_mode",
			Title:        "Mode",
			Description:  "Which Grafana surface to deliver to: dashboard annotations, Grafana Alerting, or Grafana IRM / OnCall.",
			Enum:         []string{grafanaModeAnnotations, grafanaModeAlerts, grafanaModeIRM},
			DefaultValue: grafanaModeAnnotations,
			Required:     true,
			Condition:    grafanaServiceCondition(),
		},
		{
			Type:        sdk.JsonSchemaTypeString,
			Key:         "grafana_server",
			Title:       "Server",
			Description: "Base URL of the Grafana instance.",
			Placeholder: "https://grafana.example.com",
			Required:    true,
			Condition:   instanceCond,
		},
		{
			Type:        sdk.JsonSchemaTypeString,
			Key:         "grafana_token",
			Title:       "Service account token",
			Description: "Grafana service-account token, sent as an Authorization: Bearer header.",
			Format:      sdk.StringFormatPassword,
			Required:    true,
			Condition:   instanceCond,
		},
	}

	for _, m := range g.modes {
		fields = append(fields, m.schema()...)
	}
	return fields
}

// ParseTarget dispatches validation to the selected mode and stamps the mode
// into the returned cfg so Send can dispatch on it later. An absent mode is
// treated as the default (annotations) to match the schema's DefaultValue.
func (g *grafana) ParseTarget(input map[string]any) (map[string]string, error) {
	modeID, _ := input["grafana_mode"].(string)
	modeID = strings.TrimSpace(modeID)
	if modeID == "" {
		modeID = grafanaModeAnnotations
	}

	m, ok := g.mode(modeID)
	if !ok {
		return nil, fmt.Errorf("grafana: unknown mode %q", modeID)
	}

	cfg, err := m.parse(input)
	if err != nil {
		return nil, err
	}
	cfg["mode"] = modeID
	return cfg, nil
}

// Send dispatches to the mode recorded in cfg by ParseTarget.
func (g *grafana) Send(ctx context.Context, cfg map[string]string, notif sdk.Notification) error {
	m, ok := g.mode(cfg["mode"])
	if !ok {
		return fmt.Errorf("grafana: unknown mode %q", cfg["mode"])
	}

	if ctx == nil {
		ctx = context.Background()
	}
	client := g.client
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second}
	}
	return m.send(ctx, client, cfg, notif)
}

// grafanaServerAndToken reads and validates the two config fields shared by
// the annotations and alerts modes.
func grafanaServerAndToken(input map[string]any) (string, string, error) {
	server, _ := input["grafana_server"].(string)
	server = strings.TrimSpace(server)
	if server == "" {
		return "", "", errors.New("grafana: server is required")
	}
	server = strings.TrimRight(server, "/")

	token, _ := input["grafana_token"].(string)
	token = strings.TrimSpace(token)
	if token == "" {
		return "", "", errors.New("grafana: token is required")
	}

	return server, token, nil
}

// grafanaPostJSON is the single HTTP path every mode uses: marshal, POST,
// check for 2xx. On a non-2xx it returns the status plus a capped slice of
// the response body, and on a transport failure it returns the redacted
// cause — in neither case does the request URL reach the error string. That
// matters most for IRM, whose integration URL is itself the credential.
// errPrefix is the "grafana: <mode>" string prepended to every error.
func grafanaPostJSON(ctx context.Context, client *http.Client, url string, headers map[string]string, payload any, errPrefix string) error {
	buf, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("%s: encode payload: %w", errPrefix, err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(buf))
	if err != nil {
		return fmt.Errorf("%s: build request: %w", errPrefix, RedactRequestError(err))
	}
	req.Header.Set("Content-Type", "application/json")
	for k, v := range headers {
		req.Header.Set(k, v)
	}

	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("%s: request failed: %w", errPrefix, RedactRequestError(err))
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		const maxBody = 512
		b, _ := io.ReadAll(io.LimitReader(resp.Body, maxBody))
		return fmt.Errorf("%s: server responded %d: %s", errPrefix, resp.StatusCode, strings.TrimSpace(string(b)))
	}

	return nil
}

// grafanaEventID returns a value unique to this notification, used as the
// Alertmanager "event_id" label and the IRM "alert_uid". Both surfaces
// deduplicate on it, so a non-unique value would silently collapse a burst of
// detections into one alert. Notification has no guaranteed unique field —
// Tag is a *collapse* key and is deliberately shared across events — so the
// publisher's own eventId is preferred, with random bytes as the fallback.
func grafanaEventID(notif sdk.Notification) string {
	if id := strings.TrimSpace(notif.Data["eventId"]); id != "" {
		return id
	}
	var b [8]byte
	if _, err := rand.Read(b[:]); err == nil {
		return hex.EncodeToString(b[:])
	}
	return strconv.FormatInt(time.Now().UnixNano(), 10)
}

// grafanaCameraID returns the publisher-supplied camera id, or "" when the
// notification is not camera-scoped (a plugin update, a system alert). Modes
// omit the camera label/tag entirely rather than emitting an empty one.
func grafanaCameraID(notif sdk.Notification) string {
	return strings.TrimSpace(notif.Data["cameraId"])
}

// grafanaSeverity returns camera.ui's severity verbatim, defaulting to info.
// It is deliberately not squashed into Prometheus's warning/critical
// convention: passing all four levels through is lossless, and routing on
// them is a one-line matcher either way.
func grafanaSeverity(notif sdk.Notification) string {
	if notif.Severity == "" {
		return string(sdk.SeverityInfo)
	}
	return string(notif.Severity)
}

// grafanaAbsoluteDeepLink returns the notification's deep link only when it
// is absolute, i.e. when base_url is configured and notifier.go has already
// qualified it. A router-relative link is useless in every Grafana surface —
// it would resolve against the Grafana host, not camera.ui — so it is
// dropped rather than emitted broken. Matches the idiom in discord.go,
// telegram.go and pushover.go.
func grafanaAbsoluteDeepLink(notif sdk.Notification) string {
	if strings.HasPrefix(notif.DeepLink, "http") {
		return notif.DeepLink
	}
	return ""
}
