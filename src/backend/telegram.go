package backend

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"strings"
	"time"

	sdk "github.com/cameraui/sdk/go"
)

// defaultTelegramBaseURL is the production Telegram Bot API base, used when
// baseURL is left at its zero value.
const defaultTelegramBaseURL = "https://api.telegram.org"

// telegram implements Backend for the Telegram Bot API
// (https://core.telegram.org/bots/api). Delivery is a single HTTP POST to
// /bot{token}/sendMessage (text only) or /bot{token}/sendPhoto (an inline
// Thumbnail is present).
type telegram struct {
	// client performs the HTTP request. Defaulted by newTelegram;
	// overridable in tests so Send never touches the real network.
	client *http.Client
	// baseURL is the Bot API base, e.g. "https://api.telegram.org". Defaulted
	// by newTelegram; overridable in tests to point at an httptest server.
	baseURL string
}

// newTelegram constructs a Telegram backend with a client suitable for
// production use (~10s timeout) and the real Bot API base. Tests override
// the client and baseURL fields to point at an httptest server.
func newTelegram() *telegram {
	return &telegram{
		client:  &http.Client{Timeout: 10 * time.Second},
		baseURL: defaultTelegramBaseURL,
	}
}

func init() {
	Register(newTelegram())
}

func (tg *telegram) ID() string    { return "telegram" }
func (tg *telegram) Label() string { return "Telegram" }

func (tg *telegram) Schema() []sdk.JsonSchema {
	cond := []sdk.SchemaCondition{{Key: "service", Value: "telegram"}}
	return []sdk.JsonSchema{
		{
			Type:        sdk.JsonSchemaTypeString,
			Key:         "telegram_token",
			Title:       "Bot Token",
			Description: "The Telegram bot token issued by @BotFather.",
			Format:      sdk.StringFormatPassword,
			Required:    true,
			Condition:   cond,
		},
		{
			Type:        sdk.JsonSchemaTypeString,
			Key:         "telegram_chat",
			Title:       "Chat ID",
			Description: "The Telegram chat (or channel) ID to deliver to.",
			Required:    true,
			Condition:   cond,
		},
	}
}

// ParseTarget validates the raw registration input and returns the
// normalized config persisted in NotifierDevice.Metadata. Schema field keys
// are namespaced (telegram_*) so they don't collide with other backends'
// fields in the flattened NotificationSettings() form; the returned cfg
// uses the short keys Send reads.
func (tg *telegram) ParseTarget(input map[string]any) (map[string]string, error) {
	token, _ := input["telegram_token"].(string)
	token = strings.TrimSpace(token)
	if token == "" {
		return nil, errors.New("telegram: token is required")
	}

	chat, _ := input["telegram_chat"].(string)
	chat = strings.TrimSpace(chat)
	if chat == "" {
		return nil, errors.New("telegram: chat is required")
	}

	return map[string]string{
		"token": token,
		"chat":  chat,
	}, nil
}

// telegramInlineButton is a single button in a Telegram inline keyboard.
type telegramInlineButton struct {
	Text string `json:"text"`
	URL  string `json:"url"`
}

// telegramReplyMarkup wraps a Telegram inline keyboard.
type telegramReplyMarkup struct {
	InlineKeyboard [][]telegramInlineButton `json:"inline_keyboard"`
}

// telegramSendMessagePayload is the JSON body posted to sendMessage.
type telegramSendMessagePayload struct {
	ChatID      string               `json:"chat_id"`
	Text        string               `json:"text"`
	ReplyMarkup *telegramReplyMarkup `json:"reply_markup,omitempty"`
}

// telegramText builds the combined text used for both sendMessage's "text"
// and sendPhoto's "caption": the title, plus the body on its own line when
// non-empty.
func telegramText(notif sdk.Notification) string {
	if notif.Body == "" {
		return notif.Title
	}
	return notif.Title + "\n" + notif.Body
}

// telegramReplyMarkupFor builds the "Open camera" inline-keyboard reply
// markup for an absolute DeepLink, or nil when there is none.
func telegramReplyMarkupFor(notif sdk.Notification) *telegramReplyMarkup {
	if !strings.HasPrefix(notif.DeepLink, "http") {
		return nil
	}
	return &telegramReplyMarkup{
		InlineKeyboard: [][]telegramInlineButton{
			{{Text: "Open camera", URL: notif.DeepLink}},
		},
	}
}

// Send delivers a single notification via the Telegram Bot API. Telegram
// has no severity/priority concept, so notif.Severity is not mapped to
// anything.
func (tg *telegram) Send(ctx context.Context, cfg map[string]string, notif sdk.Notification) error {
	baseURL := tg.baseURL
	if baseURL == "" {
		baseURL = defaultTelegramBaseURL
	}
	baseURL = strings.TrimRight(baseURL, "/")
	token := cfg["token"]
	chat := cfg["chat"]

	if ctx == nil {
		ctx = context.Background()
	}

	var req *http.Request
	var err error
	if len(notif.Thumbnail) > 0 {
		req, err = tg.newSendPhotoRequest(ctx, baseURL, token, chat, notif)
	} else {
		req, err = tg.newSendMessageRequest(ctx, baseURL, token, chat, notif)
	}
	if err != nil {
		return fmt.Errorf("telegram: build request: %w", err)
	}

	client := tg.client
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second}
	}

	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("telegram: request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		const maxBody = 512
		b, _ := io.ReadAll(io.LimitReader(resp.Body, maxBody))
		return fmt.Errorf("telegram: server responded %d: %s", resp.StatusCode, strings.TrimSpace(string(b)))
	}

	return nil
}

// newSendMessageRequest builds the JSON sendMessage request used when there
// is no image to deliver.
func (tg *telegram) newSendMessageRequest(ctx context.Context, baseURL, token, chat string, notif sdk.Notification) (*http.Request, error) {
	payload := telegramSendMessagePayload{
		ChatID:      chat,
		Text:        telegramText(notif),
		ReplyMarkup: telegramReplyMarkupFor(notif),
	}

	buf, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("encode payload: %w", err)
	}

	reqURL := baseURL + "/bot" + token + "/sendMessage"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, reqURL, bytes.NewReader(buf))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	return req, nil
}

// newSendPhotoRequest builds the multipart/form-data sendPhoto request used
// when notif.Thumbnail is present: chat_id, a "photo" file part (filename
// snapshot.jpg, Content-Type image/jpeg), a "caption" field carrying the
// same title/body text, and — when DeepLink is absolute — a "reply_markup"
// field carrying the inline keyboard as a JSON string (multipart form
// fields are always strings; the Bot API accepts reply_markup this way for
// non-JSON requests).
func (tg *telegram) newSendPhotoRequest(ctx context.Context, baseURL, token, chat string, notif sdk.Notification) (*http.Request, error) {
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)

	if err := mw.WriteField("chat_id", chat); err != nil {
		return nil, err
	}
	if err := mw.WriteField("caption", telegramText(notif)); err != nil {
		return nil, err
	}
	if markup := telegramReplyMarkupFor(notif); markup != nil {
		markupJSON, err := json.Marshal(markup)
		if err != nil {
			return nil, err
		}
		if err := mw.WriteField("reply_markup", string(markupJSON)); err != nil {
			return nil, err
		}
	}

	h := make(textproto.MIMEHeader)
	h.Set("Content-Disposition", `form-data; name="photo"; filename="snapshot.jpg"`)
	h.Set("Content-Type", "image/jpeg")
	part, err := mw.CreatePart(h)
	if err != nil {
		return nil, err
	}
	if _, err := part.Write(notif.Thumbnail); err != nil {
		return nil, err
	}
	if err := mw.Close(); err != nil {
		return nil, err
	}

	reqURL := baseURL + "/bot" + token + "/sendPhoto"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, reqURL, &buf)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", mw.FormDataContentType())
	return req, nil
}
