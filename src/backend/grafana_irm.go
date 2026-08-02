package backend

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"time"

	sdk "github.com/cameraui/sdk/go"
)

const (
	// grafanaIRMReceiver names this plugin as the sender in the Grafana
	// Alerting envelope's receiver field.
	grafanaIRMReceiver = "camera.ui"
	// grafanaIRMPayloadVersion is the version string Grafana stamps on its
	// webhook payload structure.
	grafanaIRMPayloadVersion = "1"
	// grafanaIRMUnresolvedEndsAt is the zero time Grafana documents as the
	// endsAt of an alert that has not resolved. A camera event is
	// instantaneous and never resolves on its own, so every alert we emit
	// carries it.
	grafanaIRMUnresolvedEndsAt = "0001-01-01T00:00:00Z"
	// grafanaIRMGroupKeyPrefix prefixes every groupKey, so all of this
	// plugin's alert groups are identifiable as camera.ui's even when a
	// notification names no camera.
	grafanaIRMGroupKeyPrefix = "camera.ui"
)

// grafanaIRM delivers each notification to a Grafana IRM / OnCall inbound
// webhook integration.
//
// Unlike the other two modes it addresses an integration URL rather than the
// Grafana instance, and it sends no Authorization header: the token embedded
// in that URL *is* the credential. That is also why the URL is a password
// field and why every error path here runs through grafanaPostJSON's
// redaction.
//
// Of the three modes this is the only one that renders the snapshot, and
// only when the publisher supplied a hosted ImageURL — notifier.go's
// resolveThumbnail converts a URL into inline bytes, never the reverse.
type grafanaIRM struct{}

func newGrafanaIRM() *grafanaIRM { return &grafanaIRM{} }

func (i *grafanaIRM) id() string { return grafanaModeIRM }

func (i *grafanaIRM) schema() []sdk.JsonSchema {
	return []sdk.JsonSchema{
		{
			Type:        sdk.JsonSchemaTypeString,
			Key:         "grafana_irm_url",
			Title:       "Integration URL",
			Description: "Inbound webhook URL of a Grafana IRM / OnCall integration. Treat it as a secret: the token is part of the URL.",
			Format:      sdk.StringFormatPassword,
			Required:    true,
			Condition:   grafanaModeCondition(grafanaModeIRM),
		},
	}
}

func (i *grafanaIRM) parse(input map[string]any) (map[string]string, error) {
	url, _ := input["grafana_irm_url"].(string)
	url = strings.TrimSpace(url)
	if url == "" {
		return nil, errors.New("grafana: irm: integration URL is required")
	}

	return map[string]string{"url": url}, nil
}

// grafanaIRMPayload is the body posted to a Grafana IRM / OnCall integration.
//
// It is deliberately a union of two payload shapes. IRM renders each alert
// group through Jinja2 templates chosen by the integration's *type*, and the
// two types users actually create read different bodies:
//
//   - a "grafana_alerting" integration templates against Grafana Alerting's
//     webhook contact-point payload, reading status, alerts[] and groupKey
//   - a "webhook" (formatted webhook) integration reads alert_uid, title,
//     message, image_url and link_to_upstream_details
//
// title, message and state mean the same thing in both shapes and no other
// field collides, so emitting both sets makes a single body render correctly
// under either integration type without asking the user which they created.
//
// 0.6.0 sent only the formatted-webhook fields, which left a
// grafana_alerting integration rendering "Status: Unknown (Template Warning:
// 'dict object' has no attribute 'alerts')" — see #31. The envelope below
// follows the schema Grafana documents for its webhook contact point.
type grafanaIRMPayload struct {
	// Grafana Alerting webhook envelope.
	Receiver          string            `json:"receiver"`
	Status            string            `json:"status"`
	Alerts            []grafanaIRMAlert `json:"alerts"`
	GroupLabels       map[string]string `json:"groupLabels"`
	CommonLabels      map[string]string `json:"commonLabels"`
	CommonAnnotations map[string]string `json:"commonAnnotations"`
	ExternalURL       string            `json:"externalURL,omitempty"`
	Version           string            `json:"version"`
	GroupKey          string            `json:"groupKey"`
	TruncatedAlerts   int               `json:"truncatedAlerts"`

	// Read by both shapes.
	Title   string `json:"title"`
	Message string `json:"message"`
	State   string `json:"state"`

	// OnCall formatted-webhook fields, retained so a "webhook" integration
	// keeps working exactly as it did in 0.6.0.
	AlertUID string `json:"alert_uid"`
	ImageURL string `json:"image_url,omitempty"`
	Link     string `json:"link_to_upstream_details,omitempty"`
}

// grafanaIRMAlert is one entry of the payload's alerts array, matching the
// Alert object Grafana documents for its webhook contact point. Fields
// Grafana fills from a real alert rule but camera.ui has no analogue for
// (values, silenceURL, dashboardURL, panelURL) are omitted rather than sent
// empty.
type grafanaIRMAlert struct {
	Status       string            `json:"status"`
	Labels       map[string]string `json:"labels"`
	Annotations  map[string]string `json:"annotations"`
	StartsAt     string            `json:"startsAt"`
	EndsAt       string            `json:"endsAt"`
	GeneratorURL string            `json:"generatorURL,omitempty"`
	Fingerprint  string            `json:"fingerprint"`
	ImageURL     string            `json:"imageURL,omitempty"`
}

func (i *grafanaIRM) send(ctx context.Context, client *http.Client, cfg map[string]string, notif sdk.Notification) error {
	message := notif.Body
	if message == "" {
		message = notif.Title
	}

	labels := map[string]string{
		"alertname": grafanaDefaultAlertname,
		"source":    grafanaIRMReceiver,
		"severity":  grafanaSeverity(notif),
	}
	groupLabels := map[string]string{"alertname": grafanaDefaultAlertname}
	if cam := grafanaCameraID(notif); cam != "" {
		labels["camera"] = cam
		groupLabels["camera"] = cam
	}

	annotations := map[string]string{"summary": notif.Title}
	if notif.Body != "" {
		annotations["description"] = notif.Body
	}

	link := grafanaAbsoluteDeepLink(notif)

	alert := grafanaIRMAlert{
		Status:      "firing",
		Labels:      labels,
		Annotations: annotations,
		StartsAt:    time.Now().UTC().Format(time.RFC3339),
		EndsAt:      grafanaIRMUnresolvedEndsAt,
		// generatorURL is where Grafana points "see the source of this
		// alert", which for us is the camera page the event came from.
		GeneratorURL: link,
		// fingerprint identifies the alert instance. Using the per-event id
		// keeps two detections on one camera distinct inside their shared
		// group, the same role event_id plays in alerts mode.
		Fingerprint: grafanaEventID(notif),
		ImageURL:    notif.ImageURL,
	}

	payload := grafanaIRMPayload{
		Receiver:          grafanaIRMReceiver,
		Status:            "firing",
		Alerts:            []grafanaIRMAlert{alert},
		GroupLabels:       groupLabels,
		CommonLabels:      labels,
		CommonAnnotations: annotations,
		ExternalURL:       grafanaIRMExternalURL(notif),
		Version:           grafanaIRMPayloadVersion,
		GroupKey:          grafanaIRMGroupKey(notif),
		TruncatedAlerts:   0,

		Title:   notif.Title,
		Message: message,
		State:   "alerting",

		AlertUID: grafanaEventID(notif),
		ImageURL: notif.ImageURL,
		Link:     link,
	}

	return grafanaPostJSON(ctx, client, cfg["url"], nil, payload, "grafana: irm")
}

// grafanaIRMGroupKey decides which IRM alert group a notification joins.
// Grouping is per camera, so a busy camera cannot bury a quiet one and each
// group's history belongs to one physical location. A notification that
// names no camera (a plugin update, a system alert) falls back to the bare
// prefix rather than inventing a camera.
func grafanaIRMGroupKey(notif sdk.Notification) string {
	if cam := grafanaCameraID(notif); cam != "" {
		return grafanaIRMGroupKeyPrefix + ":" + cam
	}
	return grafanaIRMGroupKeyPrefix
}

// grafanaIRMExternalURL derives the camera.ui origin for the envelope's
// externalURL field, which Grafana documents as the instance sending the
// webhook. The absolute deep link (produced by notifier.go when base_url is
// configured) already carries that origin, so no additional config is
// needed. Returns "" when the deep link is relative or unparseable, in which
// case the field is omitted rather than guessed at.
func grafanaIRMExternalURL(notif sdk.Notification) string {
	link := grafanaAbsoluteDeepLink(notif)
	if link == "" {
		return ""
	}
	u, err := url.Parse(link)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return ""
	}
	return u.Scheme + "://" + u.Host
}
