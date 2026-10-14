package morningreport

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"image"
	"image/jpeg"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

const (
	botToken = "SECRET"
	htmlMode = "HTML"
)

type call struct {
	method string
	fields map[string]string
	files  []string
	json   map[string]any
}

func botServer(t *testing.T, fail bool) (*httptest.Server, *[]call) {
	t.Helper()
	var mu sync.Mutex
	var calls []call
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c := call{method: r.URL.Path[strings.LastIndex(r.URL.Path, "/")+1:], fields: map[string]string{}}
		if !strings.HasPrefix(r.URL.Path, "/bot"+botToken+"/") {
			t.Errorf("path %s", r.URL.Path)
		}
		if strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
			_ = json.NewDecoder(r.Body).Decode(&c.json)
		} else {
			mr, err := r.MultipartReader()
			if err != nil {
				t.Errorf("multipart: %v", err)
			}
			for mr != nil {
				part, err := mr.NextPart()
				if err != nil {
					break
				}
				body, _ := io.ReadAll(io.LimitReader(part, 32<<20))
				if part.FileName() != "" {
					c.files = append(c.files, part.FormName())
				} else {
					c.fields[part.FormName()] = string(body)
				}
			}
		}
		mu.Lock()
		calls = append(calls, c)
		mu.Unlock()
		if fail {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = io.WriteString(w, `{"ok":false,"description":"Bad Request: chat not found"}`)
			return
		}
		_, _ = io.WriteString(w, `{"ok":true}`)
	}))
	t.Cleanup(srv.Close)
	return srv, &calls
}

func TestTelegramMessageIsHTML(t *testing.T) {
	t.Parallel()
	srv, calls := botServer(t, false)
	tg := Telegram{Token: botToken, ChatID: "-5365451857", BaseURL: srv.URL}
	if err := tg.Message(context.Background(), "<b>hi</b>"); err != nil {
		t.Fatal(err)
	}
	c := (*calls)[0]
	if c.method != "sendMessage" || c.json["parse_mode"] != htmlMode || c.json["chat_id"] != "-5365451857" || c.json["text"] != "<b>hi</b>" {
		t.Fatalf("%+v", c)
	}
}

func TestTelegramFallsBackToPlainText(t *testing.T) {
	t.Parallel()
	var texts []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req map[string]any
		_ = json.NewDecoder(r.Body).Decode(&req)
		text, _ := req["text"].(string)
		texts = append(texts, text)
		if req["parse_mode"] == htmlMode {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = io.WriteString(w, `{"ok":false,"description":"Bad Request: can't parse entities: Unexpected end tag"}`)
			return
		}
		_, _ = io.WriteString(w, `{"ok":true}`)
	}))
	t.Cleanup(srv.Close)
	if err := (Telegram{Token: botToken, ChatID: "1", BaseURL: srv.URL}).Message(context.Background(), "<b>Dew</b> 2 &lt; 4 &amp; up"); err != nil {
		t.Fatal(err)
	}
	if len(texts) != 2 || texts[1] != "Dew 2 < 4 & up" {
		t.Fatalf("sent %q", texts)
	}
}

func TestTelegramPhotosGoAsAlbumsOfTen(t *testing.T) {
	t.Parallel()
	srv, calls := botServer(t, false)
	tg := Telegram{Token: botToken, ChatID: "1", BaseURL: srv.URL}
	photos := make([]Photo, 11)
	for i := range photos {
		photos[i] = Photo{Caption: "<b>t</b>", JPEG: []byte{0xff, 0xd8, 0xff}}
	}
	if err := tg.Photos(context.Background(), photos); err != nil {
		t.Fatal(err)
	}
	if len(*calls) != 2 || (*calls)[0].method != "sendMediaGroup" || (*calls)[1].method != "sendPhoto" {
		t.Fatalf("calls %+v", *calls)
	}
	var media []map[string]string
	if err := json.Unmarshal([]byte((*calls)[0].fields["media"]), &media); err != nil {
		t.Fatal(err)
	}
	if len(media) != 10 || len((*calls)[0].files) != 10 {
		t.Fatalf("album of %d media, %d files", len(media), len((*calls)[0].files))
	}
	for _, m := range media {
		if m["type"] != "photo" || !strings.HasPrefix(m["media"], "attach://p") || m["parse_mode"] != htmlMode {
			t.Fatalf("media %+v", m)
		}
	}
	single := (*calls)[1]
	if len(single.files) != 1 || single.files[0] != "photo" || single.fields["parse_mode"] != htmlMode {
		t.Fatalf("single %+v", single)
	}
}

func TestTelegramErrorsCarryTheReasonNotTheToken(t *testing.T) {
	t.Parallel()
	srv, _ := botServer(t, true)
	err := Telegram{Token: botToken, ChatID: "1", BaseURL: srv.URL}.Message(context.Background(), "x")
	if err == nil || !strings.Contains(err.Error(), "chat not found") {
		t.Fatalf("err %v", err)
	}
	err = Telegram{Token: botToken, ChatID: "1", BaseURL: "http://127.0.0.1:1"}.Message(context.Background(), "x")
	if err == nil || strings.Contains(err.Error(), botToken) {
		t.Fatalf("err %v", err)
	}
}

func jpegOf(t *testing.T, w, h int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	if _, err := rand.Read(img.Pix); err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: 90}); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestFitPhoto(t *testing.T) {
	t.Parallel()
	small := jpegOf(t, 300, 200)
	got, err := FitPhoto(small)
	if err != nil || !bytes.Equal(got, small) {
		t.Fatalf("a photo that fits is sent as is: %v", err)
	}
	got, err = FitPhoto(jpegOf(t, 4000, 1000))
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := jpeg.DecodeConfig(bytes.NewReader(got))
	if err != nil || cfg.Width != MaxPhotoSide || cfg.Height != 640 {
		t.Fatalf("%dx%d %v, want %dx640", cfg.Width, cfg.Height, err, MaxPhotoSide)
	}
	if _, err := FitPhoto([]byte("not a jpeg")); err == nil {
		t.Fatal("want an error")
	}
}
