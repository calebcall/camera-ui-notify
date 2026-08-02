package backend

import (
	"context"
	"encoding/base64"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	sdk "github.com/cameraui/sdk/go"
)

const (
	// grafanaDefaultAlertname is the alertname label used when the user
	// leaves the field blank. It is what notification policies match on, so
	// it needs a stable, recognizable default.
	grafanaDefaultAlertname = "CameraUINotification"
	// grafanaDefaultTTL is how long (seconds) an alert stays firing before
	// Alertmanager auto-resolves it.
	grafanaDefaultTTL = 300
	// grafanaMinTTL floors the TTL. Below roughly half a minute an alert can
	// resolve before a notification policy's group_wait has even elapsed, so
	// it would never reach a receiver.
	grafanaMinTTL = 30
)

// Parse-time validation errors for this mode.
var (
	errGrafanaAMURLRequired      = errors.New("grafana: alertmanager: Alertmanager URL is required")
	errGrafanaAMPasswordRequired = errors.New("grafana: alertmanager: password is required when a username is set")
	errGrafanaAMUserRequired     = errors.New("grafana: alertmanager: username is required when a password is set")
)

// grafanaAlertmanager delivers each notification as a firing alert posted
// straight to an Alertmanager's own v2 API.
//
// It deliberately does NOT go through Grafana. Up to 0.6.1 this mode posted
// to {grafanaServer}/api/alertmanager/grafana/api/v2/alerts, on the
// assumption that Grafana's built-in Alertmanager would accept injected
// alerts. It does not. Grafana's route table declares built-in-Alertmanager
// operations with a literal "grafana" segment and external ones with
// {DatasourceUID}, and there is no POST .../alertmanager/grafana/api/v2/alerts
// — reads are supported, writes are not. The forking handler
// (pkg/services/ngalert/api/forking_alertmanager.go) resolves an Alertmanager
// *datasource* by UID and always returns the external proxy, never the
// built-in service, so posting with "grafana" as the UID fails with
// 400 "bad request data". Grafana-managed alerts can only originate from
// Grafana's own rule evaluation. See #33.
//
// Addressing the Alertmanager directly sidesteps all of that and works the
// same for a standalone Alertmanager, Mimir/Cortex, and Grafana Cloud's
// hosted Alertmanager (which authenticates with basic auth: the instance ID
// as the username and an API token as the password).
type grafanaAlertmanager struct{}

func newGrafanaAlertmanager() *grafanaAlertmanager { return &grafanaAlertmanager{} }

func (a *grafanaAlertmanager) id() string { return grafanaModeAlertmanager }

func (a *grafanaAlertmanager) schema() []sdk.JsonSchema {
	cond := grafanaModeCondition(grafanaModeAlertmanager)
	minTTL := float64(grafanaMinTTL)

	return []sdk.JsonSchema{
		{
			Type:        sdk.JsonSchemaTypeString,
			Key:         "grafana_am_url",
			Title:       "Alertmanager URL",
			Description: "Base URL of the Alertmanager itself, not of Grafana. Grafana's built-in Alertmanager cannot receive posted alerts, so this must be a standalone Alertmanager, Mimir/Cortex, or Grafana Cloud's hosted Alertmanager.",
			Placeholder: "https://alertmanager.example.com",
			Required:    true,
			Condition:   cond,
		},
		{
			Type:        sdk.JsonSchemaTypeString,
			Key:         "grafana_am_user",
			Title:       "Username",
			Description: "Optional. Basic-auth username. For Grafana Cloud this is the Alertmanager instance ID.",
			Condition:   cond,
		},
		{
			Type:        sdk.JsonSchemaTypeString,
			Key:         "grafana_am_password",
			Title:       "Password",
			Description: "Optional. Basic-auth password. For Grafana Cloud this is an API token. Required if a username is set.",
			Format:      sdk.StringFormatPassword,
			Condition:   cond,
		},
		{
			Type:         sdk.JsonSchemaTypeString,
			Key:          "grafana_alertname",
			Title:        "Alert name",
			Description:  "The alertname label your notification policies match on.",
			DefaultValue: grafanaDefaultAlertname,
			Condition:    cond,
		},
		{
			Type:         sdk.JsonSchemaTypeNumber,
			Key:          "grafana_ttl",
			Title:        "Auto-resolve after (seconds)",
			Description:  "How long the alert stays firing before Alertmanager resolves it automatically.",
			DefaultValue: grafanaDefaultTTL,
			Minimum:      &minTTL,
			Condition:    cond,
		},
	}
}

func (a *grafanaAlertmanager) parse(input map[string]any) (map[string]string, error) {
	amURL, _ := input["grafana_am_url"].(string)
	amURL = strings.TrimSpace(amURL)
	if amURL == "" {
		return nil, errGrafanaAMURLRequired
	}
	amURL = strings.TrimRight(amURL, "/")

	user, _ := input["grafana_am_user"].(string)
	user = strings.TrimSpace(user)
	password, _ := input["grafana_am_password"].(string)
	password = strings.TrimSpace(password)
	// Half-configured basic auth authenticates as nobody and surfaces as a
	// confusing 401 at send time, so reject it at parse time — the same
	// pairing rule the generic webhook backend applies to its custom header
	// name and value.
	if user != "" && password == "" {
		return nil, errGrafanaAMPasswordRequired
	}
	if password != "" && user == "" {
		return nil, errGrafanaAMUserRequired
	}

	alertname, _ := input["grafana_alertname"].(string)
	alertname = strings.TrimSpace(alertname)
	if alertname == "" {
		alertname = grafanaDefaultAlertname
	}

	return map[string]string{
		"url":       amURL,
		"user":      user,
		"password":  password,
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
// alerts endpoint. It matches the postableAlert schema in Alertmanager's
// OpenAPI spec: labels (required) and generatorURL from the alert
// definition, plus startsAt, endsAt and annotations.
type grafanaAlertPayload struct {
	Labels       map[string]string `json:"labels"`
	Annotations  map[string]string `json:"annotations"`
	StartsAt     string            `json:"startsAt"`
	EndsAt       string            `json:"endsAt"`
	GeneratorURL string            `json:"generatorURL,omitempty"`
}

func (a *grafanaAlertmanager) send(ctx context.Context, client *http.Client, cfg map[string]string, notif sdk.Notification) error {
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
	// Alertmanager will not render this, but it carries the snapshot through
	// to downstream receiver templates that can.
	if notif.ImageURL != "" {
		annotations["image_url"] = notif.ImageURL
	}

	// cfg["ttl"] was already normalized by grafanaParseTTL during parse, but
	// re-derive the floor here too: an unparseable value zeroes out and then
	// clamps to the minimum, which is the correct fallback behavior.
	ttl, _ := strconv.Atoi(cfg["ttl"])
	if ttl < grafanaMinTTL {
		ttl = grafanaMinTTL
	}

	start := time.Now().UTC()
	payload := []grafanaAlertPayload{{
		Labels:      labels,
		Annotations: annotations,
		StartsAt:    start.Format(time.RFC3339),
		EndsAt:      start.Add(time.Duration(ttl) * time.Second).Format(time.RFC3339),
		// generatorURL is Alertmanager's standard "where did this come from"
		// link, surfaced as Source in its UI.
		GeneratorURL: grafanaAbsoluteDeepLink(notif),
	}}

	headers := map[string]string{}
	if user := cfg["user"]; user != "" {
		headers["Authorization"] = "Basic " +
			base64.StdEncoding.EncodeToString([]byte(user+":"+cfg["password"]))
	}

	return grafanaPostJSON(ctx, client, cfg["url"]+"/api/v2/alerts",
		headers, payload, "grafana: alertmanager")
}
