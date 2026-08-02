package backend

import (
	"context"
	"html"
	"net/http"
	"strings"
	"time"

	sdk "github.com/cameraui/sdk/go"
)

// grafanaAnnotations delivers each notification as an organization-wide
// Grafana annotation (https://grafana.com/docs/grafana/latest/developers/http_api/annotations/).
// It is deliberately not pinned to a dashboard or panel: a tag-filtered
// annotation query is the idiomatic Grafana pattern and survives dashboard
// renames and edits, which a stored dashboardUID does not.
type grafanaAnnotations struct{}

func newGrafanaAnnotations() *grafanaAnnotations { return &grafanaAnnotations{} }

func (a *grafanaAnnotations) id() string { return grafanaModeAnnotations }

func (a *grafanaAnnotations) schema() []sdk.JsonSchema {
	return []sdk.JsonSchema{
		{
			Type:        sdk.JsonSchemaTypeString,
			Key:         "grafana_tags",
			Title:       "Extra tags",
			Description: "Optional comma-separated tags added to every annotation, alongside the automatic camera.ui, camera:<id> and severity:<level> tags.",
			Placeholder: "home,security",
			Condition:   grafanaModeCondition(grafanaModeAnnotations),
		},
	}
}

func (a *grafanaAnnotations) parse(input map[string]any) (map[string]string, error) {
	server, token, err := grafanaServerAndToken(input)
	if err != nil {
		return nil, err
	}

	tags, _ := input["grafana_tags"].(string)

	return map[string]string{
		"server": server,
		"token":  token,
		"tags":   strings.TrimSpace(tags),
	}, nil
}

// grafanaAnnotationPayload is the JSON body posted to /api/annotations.
// timeEnd is deliberately absent: camera.ui supplies no event duration, so
// any region width would be arbitrary and these stay point-in-time.
type grafanaAnnotationPayload struct {
	Time int64    `json:"time"`
	Tags []string `json:"tags"`
	Text string   `json:"text"`
}

func (a *grafanaAnnotations) send(ctx context.Context, client *http.Client, cfg map[string]string, notif sdk.Notification) error {
	payload := grafanaAnnotationPayload{
		Time: time.Now().UnixMilli(),
		Tags: grafanaAnnotationTags(cfg["tags"], notif),
		Text: grafanaAnnotationText(notif),
	}

	return grafanaPostJSON(ctx, client, cfg["server"]+"/api/annotations",
		map[string]string{"Authorization": "Bearer " + cfg["token"]},
		payload, "grafana: annotations")
}

// grafanaAnnotationTags builds the tag list a dashboard's annotation query
// filters on: a fixed camera.ui marker first, then the camera (omitted when
// the notification is not camera-scoped) and severity, then the user's
// comma-separated extras. Blank extras are dropped so a stray trailing comma
// cannot produce an empty tag.
func grafanaAnnotationTags(extra string, notif sdk.Notification) []string {
	tags := []string{"camera.ui"}
	if cam := grafanaCameraID(notif); cam != "" {
		tags = append(tags, "camera:"+cam)
	}
	tags = append(tags, "severity:"+grafanaSeverity(notif))

	for _, t := range strings.Split(extra, ",") {
		if t = strings.TrimSpace(t); t != "" {
			tags = append(tags, t)
		}
	}
	return tags
}

// grafanaAnnotationText renders the annotation tooltip. Grafana renders this
// field as sanitized HTML, so every value taken from the notification is
// escaped before the markup is assembled. The deep link becomes an anchor
// only when it is absolute; its label reuses DeepLinkLabel so it reads
// "Open camera" or "Open in camera.ui" exactly as the other backends do.
func grafanaAnnotationText(notif sdk.Notification) string {
	parts := []string{"<b>" + html.EscapeString(notif.Title) + "</b>"}
	if notif.Body != "" {
		parts = append(parts, html.EscapeString(notif.Body))
	}
	if link := grafanaAbsoluteDeepLink(notif); link != "" {
		parts = append(parts, `<a href="`+html.EscapeString(link)+`">`+html.EscapeString(DeepLinkLabel(notif))+`</a>`)
	}
	return strings.Join(parts, "<br>")
}
