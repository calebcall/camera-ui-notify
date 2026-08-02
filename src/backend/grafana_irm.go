package backend

import (
	"context"
	"errors"
	"net/http"
	"strings"

	sdk "github.com/cameraui/sdk/go"
)

// grafanaIRM delivers each notification to a Grafana IRM / OnCall inbound
// webhook integration, opening one alert group per event.
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

// grafanaIRMPayload is Grafana IRM / OnCall's formatted-webhook field set.
// alert_uid drives grouping, so it is unique per event: every detection
// opens its own alert group rather than folding into an existing one.
type grafanaIRMPayload struct {
	AlertUID string `json:"alert_uid"`
	Title    string `json:"title"`
	Message  string `json:"message"`
	ImageURL string `json:"image_url,omitempty"`
	Link     string `json:"link_to_upstream_details,omitempty"`
	State    string `json:"state"`
}

func (i *grafanaIRM) send(ctx context.Context, client *http.Client, cfg map[string]string, notif sdk.Notification) error {
	message := notif.Body
	if message == "" {
		message = notif.Title
	}

	payload := grafanaIRMPayload{
		AlertUID: grafanaEventID(notif),
		Title:    notif.Title,
		Message:  message,
		ImageURL: notif.ImageURL,
		Link:     grafanaAbsoluteDeepLink(notif),
		State:    "alerting",
	}

	return grafanaPostJSON(ctx, client, cfg["url"], nil, payload, "grafana: irm")
}
