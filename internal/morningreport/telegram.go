package morningreport

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptrace"
	"strings"
	"sync/atomic"
	"time"
)

// Telegram sends through the Bot API.
type Telegram struct {
	Token  string
	ChatID string
	// BaseURL is the Bot API, https://api.telegram.org by default.
	BaseURL string
	Client  *http.Client
}

// MediaGroupMax is the most photos one album holds.
const MediaGroupMax = 10

// Photo is one photo to send.
type Photo struct {
	Caption string
	JPEG    []byte
}

func (t Telegram) endpoint(method string) string {
	base := t.BaseURL
	if base == "" {
		base = "https://api.telegram.org"
	}
	return strings.TrimRight(base, "/") + "/bot" + t.Token + "/" + method
}

func (t Telegram) client() *http.Client {
	if t.Client != nil {
		return t.Client
	}
	return &http.Client{Timeout: 2 * time.Minute}
}

// Message sends HTML text. Text Telegram can't parse as HTML goes again as
// plain text, so a stray character never loses the report.
func (t Telegram) Message(ctx context.Context, html string) error {
	err := t.message(ctx, html, "HTML")
	if err != nil && strings.Contains(err.Error(), "can't parse entities") {
		return t.message(ctx, PlainText(html), "")
	}
	return err
}

func (t Telegram) message(ctx context.Context, text, mode string) error {
	req := map[string]any{"chat_id": t.ChatID, "text": text, "disable_web_page_preview": true}
	if mode != "" {
		req["parse_mode"] = mode
	}
	body, err := json.Marshal(req)
	if err != nil {
		return err
	}
	return t.call(ctx, "sendMessage", "application/json", bytes.NewReader(body))
}

// PlainText strips the report's HTML tags and entities.
func PlainText(html string) string {
	var b strings.Builder
	in := false
	for _, r := range html {
		switch {
		case r == '<':
			in = true
		case r == '>' && in:
			in = false
		case !in:
			b.WriteRune(r)
		}
	}
	return strings.NewReplacer("&lt;", "<", "&gt;", ">", "&amp;", "&").Replace(b.String())
}

// Photos sends compressed photos, up to MediaGroupMax per album; a single
// photo goes on its own since an album needs two.
func (t Telegram) Photos(ctx context.Context, photos []Photo) error {
	for i := 0; i < len(photos); i += MediaGroupMax {
		group := photos[i:min(i+MediaGroupMax, len(photos))]
		if len(group) == 1 {
			if err := t.photo(ctx, group[0]); err != nil {
				return err
			}
			continue
		}
		if err := t.album(ctx, group); err != nil {
			return err
		}
	}
	return nil
}

func (t Telegram) photo(ctx context.Context, p Photo) error {
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	_ = w.WriteField("chat_id", t.ChatID)
	_ = w.WriteField("caption", p.Caption)
	_ = w.WriteField("parse_mode", "HTML")
	fw, err := w.CreateFormFile("photo", "preview.jpg")
	if err != nil {
		return err
	}
	if _, err := fw.Write(p.JPEG); err != nil {
		return err
	}
	if err := w.Close(); err != nil {
		return err
	}
	return t.call(ctx, "sendPhoto", w.FormDataContentType(), &buf)
}

func (t Telegram) album(ctx context.Context, group []Photo) error {
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	media := make([]map[string]string, 0, len(group))
	for i, p := range group {
		name := fmt.Sprintf("p%d", i)
		media = append(media, map[string]string{"type": "photo", "media": "attach://" + name, "caption": p.Caption, "parse_mode": "HTML"})
		fw, err := w.CreateFormFile(name, name+".jpg")
		if err != nil {
			return err
		}
		if _, err := fw.Write(p.JPEG); err != nil {
			return err
		}
	}
	m, err := json.Marshal(media)
	if err != nil {
		return err
	}
	_ = w.WriteField("chat_id", t.ChatID)
	_ = w.WriteField("media", string(m))
	if err := w.Close(); err != nil {
		return err
	}
	return t.call(ctx, "sendMediaGroup", w.FormDataContentType(), &buf)
}

// ErrOutcomeUnknown marks a send that may have reached Telegram: the request
// went out but no answer came back that says whether it was taken. Such a
// send is not tried again, so a report never goes out twice.
var ErrOutcomeUnknown = errors.New("telegram may have received it")

func (t Telegram) call(ctx context.Context, method, contentType string, body io.Reader) error {
	var written atomic.Bool
	trace := &httptrace.ClientTrace{WroteHeaders: func() { written.Store(true) }}
	req, err := http.NewRequestWithContext(httptrace.WithClientTrace(ctx, trace), http.MethodPost, t.endpoint(method), body)
	if err != nil {
		return fmt.Errorf("telegram %s: %w", method, redacted{err, t.Token})
	}
	req.Header.Set("Content-Type", contentType)
	resp, err := t.client().Do(req)
	if err != nil {
		if written.Load() {
			return fmt.Errorf("telegram %s: %w: %w", method, ErrOutcomeUnknown, redacted{err, t.Token})
		}
		return fmt.Errorf("telegram %s: %w", method, redacted{err, t.Token})
	}
	defer resp.Body.Close()
	var out struct {
		OK          bool   `json:"ok"`
		Description string `json:"description"`
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil || json.Unmarshal(raw, &out) != nil {
		return fmt.Errorf("telegram %s answered %s: %w", method, resp.Status, ErrOutcomeUnknown)
	}
	if !out.OK {
		return fmt.Errorf("telegram %s: %s", method, out.Description)
	}
	return nil
}

// redacted keeps the bot token out of an error's text, which carries the
// request URL.
type redacted struct {
	err   error
	token string
}

func (r redacted) Error() string {
	if r.token == "" {
		return r.err.Error()
	}
	return strings.ReplaceAll(r.err.Error(), r.token, "<token>")
}

func (r redacted) Unwrap() error { return r.err }
