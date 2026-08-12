package backend

import (
	"context"
	"encoding/json"
	"html"
	"net/http"
	"net/url"
	"strconv"
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

// grafanaAnnotationPatch is the JSON body sent to PATCH
// /api/annotations/:id. Only text and tags are revised — the annotation keeps
// the timestamp of the detection that created it, not of the description that
// arrived afterwards.
type grafanaAnnotationPatch struct {
	Tags []string `json:"tags"`
	Text string   `json:"text"`
}

// grafanaAnnotationCreated is the slice of the create response this mode
// reads: the id needed to patch the annotation later.
type grafanaAnnotationCreated struct {
	ID int64 `json:"id"`
}

func (a *grafanaAnnotations) send(ctx context.Context, client *http.Client, cfg map[string]string, notif sdk.Notification) (string, error) {
	payload := grafanaAnnotationPayload{
		Time: time.Now().UnixMilli(),
		Tags: grafanaAnnotationTags(cfg["tags"], notif),
		Text: grafanaAnnotationText(notif),
	}

	body, err := grafanaRequestJSON(ctx, client, http.MethodPost, cfg["server"]+"/api/annotations",
		grafanaAnnotationHeaders(cfg), payload, "grafana: annotations")
	if err != nil {
		return "", err
	}

	// The id is only needed to patch this annotation later; a response that
	// doesn't carry one still delivered fine, so it is not an error here —
	// the follow-up simply creates a second annotation instead of revising.
	var created grafanaAnnotationCreated
	if json.Unmarshal(body, &created) == nil && created.ID != 0 {
		return strconv.FormatInt(created.ID, 10), nil
	}
	return "", nil
}

// update revises an annotation already created for this tag, so the AI
// description replaces the initial detection text in place rather than
// stacking a second marker on the dashboard at almost the same timestamp.
func (a *grafanaAnnotations) update(ctx context.Context, client *http.Client, cfg map[string]string, notif sdk.Notification, prevID string) error {
	payload := grafanaAnnotationPatch{
		Tags: grafanaAnnotationTags(cfg["tags"], notif),
		Text: grafanaAnnotationText(notif),
	}

	_, err := grafanaRequestJSON(ctx, client, http.MethodPatch,
		cfg["server"]+"/api/annotations/"+url.PathEscape(prevID),
		grafanaAnnotationHeaders(cfg), payload, "grafana: annotations")
	return err
}

// grafanaAnnotationHeaders builds the bearer auth both requests share.
func grafanaAnnotationHeaders(cfg map[string]string) map[string]string {
	return map[string]string{"Authorization": "Bearer " + cfg["token"]}
}

// grafanaAnnotationTags builds the tag list a dashboard's annotation query
// filters on: a fixed camera.ui marker first, then the camera (omitted when
// the notification is not camera-scoped) and severity, then the user's
// comma-separated extras. Blank extras are dropped so a stray trailing comma
// cannot produce an empty tag.
func grafanaAnnotationTags(extra string, notif sdk.Notification) []string {
	tags := []string{"camera.ui"}
	if cam := grafanaCameraLabel(notif); cam != "" {
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
	// The clip gets its own anchor rather than sharing the deep link's: a
	// dashboard reader following the marker back to the event wants the
	// recording, and Grafana's tooltip has room for both.
	if link := VideoLink(notif); link != "" {
		parts = append(parts, `<a href="`+html.EscapeString(link)+`">`+html.EscapeString(VideoLinkLabel)+`</a>`)
	}
	return strings.Join(parts, "<br>")
}
