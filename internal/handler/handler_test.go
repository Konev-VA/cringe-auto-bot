package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mymmrac/telego"
	"github.com/mymmrac/telego/telegohandler"

	"autobot/internal/extract"
	"autobot/internal/listing"
)

// testToken обязан проходить проверку telego: цифры, двоеточие и 35 символов.
var testToken = "123456:" + strings.Repeat("x", 35)

// tinyJPEG — минимальный валидный JPEG-заголовок, достаточный для распознавания.
var tinyJPEG = []byte{0xFF, 0xD8, 0xFF, 0xE0, 0x00, 0x10, 'J', 'F', 'I', 'F', 0x00, 0x01}

// botRequest — один вызов Bot API, снятый фейковым сервером.
type botRequest struct {
	Method string
	Fields map[string]string
	Files  map[string][]byte
}

func (r botRequest) field(name string) string { return r.Fields[name] }

// fakeTelegram принимает вызовы Bot API и запоминает, что именно отправили.
type fakeTelegram struct {
	mu       sync.Mutex
	methods  []string
	requests chan botRequest
	server   *httptest.Server
}

func newFakeTelegram(t *testing.T) *fakeTelegram {
	t.Helper()
	f := &fakeTelegram{requests: make(chan botRequest, 32)}
	f.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		method := strings.TrimPrefix(r.URL.Path, "/bot"+testToken+"/")
		req := botRequest{Method: method, Fields: map[string]string{}, Files: map[string][]byte{}}

		if strings.HasPrefix(r.Header.Get("Content-Type"), "multipart/form-data") {
			reader, err := r.MultipartReader()
			if err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			for {
				part, err := reader.NextPart()
				if err == io.EOF {
					break
				}
				if err != nil {
					http.Error(w, err.Error(), http.StatusBadRequest)
					return
				}
				data, _ := io.ReadAll(part)
				if name := part.FileName(); name != "" {
					req.Files[part.FormName()] = data
				} else {
					req.Fields[part.FormName()] = string(data)
				}
				_ = part.Close()
			}
		} else {
			body, _ := io.ReadAll(r.Body)
			var fields map[string]any
			_ = json.Unmarshal(body, &fields)
			for k, v := range fields {
				switch val := v.(type) {
				case string:
					req.Fields[k] = val
				default:
					b, _ := json.Marshal(val)
					req.Fields[k] = string(b)
				}
			}
		}

		f.mu.Lock()
		f.methods = append(f.methods, method)
		f.mu.Unlock()

		result := `{"message_id":1,"date":1,"chat":{"id":-100,"type":"group"},"text":"ok"}`
		if method == "sendMediaGroup" {
			result = `[{"message_id":1,"date":1,"chat":{"id":-100,"type":"group"}}]`
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"ok":true,"result":%s}`, result)

		select {
		case f.requests <- req:
		default:
		}
	}))
	t.Cleanup(f.server.Close)
	return f
}

func (f *fakeTelegram) next(t *testing.T) botRequest {
	t.Helper()
	select {
	case req := <-f.requests:
		return req
	case <-time.After(20 * time.Second):
		t.Fatal("no bot api request received")
		return botRequest{}
	}
}

func (f *fakeTelegram) sent() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.methods...)
}

// imageServer отдаёт картинку по любому пути, кроме перечисленных в broken.
func imageServer(t *testing.T, broken ...string) *httptest.Server {
	t.Helper()
	brokenSet := make(map[string]struct{}, len(broken))
	for _, p := range broken {
		brokenSet[p] = struct{}{}
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, ok := brokenSet[r.URL.Path]; ok {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "image/jpeg")
		_, _ = w.Write(tinyJPEG)
	}))
	t.Cleanup(srv.Close)
	return srv
}

type stubExtractor struct {
	listing *listing.Listing
	err     error
}

func (s stubExtractor) Extract(context.Context, *url.URL) (*listing.Listing, error) {
	return s.listing, s.err
}

func newTestHandler(t *testing.T, ex extract.Extractor) (*Handler, *fakeTelegram) {
	t.Helper()
	fake := newFakeTelegram(t)
	bot, err := telego.NewBot(testToken, telego.WithAPIServer(fake.server.URL), telego.WithDiscardLogger())
	if err != nil {
		t.Fatalf("new bot: %v", err)
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	h := New(bot, map[listing.Site]extract.Extractor{
		listing.SiteDrom:  ex,
		listing.SiteAvito: ex,
	}, &http.Client{Timeout: 5 * time.Second}, 10*time.Second, log)
	return h, fake
}

const dromURL = "https://auto.drom.ru/novosibirsk/subaru/domingo/148849580.html"

// testListing собирает объявление с фотографиями, которые отдаёт base.
func testListing(base string, photos int) *listing.Listing {
	l := &listing.Listing{
		SourceURL:   dromURL,
		Title:       "Продажа Subaru Domingo, 1986 год в Новосибирске",
		Price:       "50 000 ₽",
		City:        "Новосибирск",
		Description: "продам чудо авто",
		Specs: []listing.Spec{
			{Key: "Год выпуска", Value: "1986"},
			{Key: "Двигатель", Value: "бензин, 1.2 л"},
			{Key: "Мощность", Value: "52 л.с."},
		},
	}
	for i := range photos {
		l.Photos = append(l.Photos, fmt.Sprintf("%s/photo-%d.jpg", base, i+1))
	}
	return l
}

func TestProcessSendsMediaGroup(t *testing.T) {
	images := imageServer(t)
	h, fake := newTestHandler(t, stubExtractor{listing: testListing(images.URL, 3)})
	h.process(context.Background(), -100, 42, dromURL)

	req := fake.next(t)
	if req.Method != "sendMediaGroup" {
		t.Fatalf("method = %q, want sendMediaGroup", req.Method)
	}
	if chat := req.field("chat_id"); chat != "-100" {
		t.Errorf("chat_id = %q, want -100", chat)
	}
	if reply := req.field("reply_parameters"); !strings.Contains(reply, `"message_id":42`) {
		t.Errorf("reply_parameters = %q, want message_id 42", reply)
	}

	if len(req.Files) != 3 {
		t.Fatalf("uploaded files = %d, want 3", len(req.Files))
	}
	for name, data := range req.Files {
		if len(data) == 0 || data[0] != 0xFF || data[1] != 0xD8 {
			t.Errorf("file %s is not a jpeg (%d bytes)", name, len(data))
		}
	}

	// У альбома подпись и parse_mode живут в первом элементе media, а не в поле запроса.
	var media []struct {
		Type      string `json:"type"`
		Media     string `json:"media"`
		Caption   string `json:"caption"`
		ParseMode string `json:"parse_mode"`
	}
	if err := json.Unmarshal([]byte(req.field("media")), &media); err != nil {
		t.Fatalf("decode media: %v (%q)", err, req.field("media"))
	}
	if len(media) != 3 {
		t.Fatalf("media items = %d, want 3", len(media))
	}
	for i, item := range media {
		if item.Type != "photo" {
			t.Errorf("media[%d].type = %q, want photo", i, item.Type)
		}
		if want := fmt.Sprintf("attach://photo-%d.jpg", i+1); item.Media != want {
			t.Errorf("media[%d].media = %q, want %q", i, item.Media, want)
		}
		if i > 0 && item.Caption != "" {
			t.Errorf("media[%d] has caption, only the first item may", i)
		}
	}

	for _, want := range []string{
		"<b>Продажа Subaru Domingo, 1986 год в Новосибирске</b>",
		"50 000 ₽ · Новосибирск",
		"Год выпуска: 1986",
		"Мощность: 52 л.с.",
		"продам чудо авто",
	} {
		if !strings.Contains(media[0].Caption, want) {
			t.Errorf("caption lacks %q:\n%s", want, media[0].Caption)
		}
	}
	if media[0].ParseMode != "HTML" {
		t.Errorf("parse_mode = %q, want HTML", media[0].ParseMode)
	}
}

func TestProcessSendsSinglePhoto(t *testing.T) {
	images := imageServer(t)
	l := testListing(images.URL, 2)
	l.Photos = l.Photos[:1]

	h, fake := newTestHandler(t, stubExtractor{listing: l})
	h.process(context.Background(), -100, 42, dromURL)

	req := fake.next(t)
	if req.Method != "sendPhoto" {
		t.Fatalf("method = %q, want sendPhoto", req.Method)
	}
	if len(req.Files) != 1 {
		t.Errorf("uploaded files = %d, want 1", len(req.Files))
	}
	if caption := req.field("caption"); !strings.Contains(caption, "Год выпуска: 1986") {
		t.Errorf("caption = %q", caption)
	}
}

func TestProcessSkipsBrokenPhoto(t *testing.T) {
	images := imageServer(t, "/photo-1.jpg")
	h, fake := newTestHandler(t, stubExtractor{listing: testListing(images.URL, 3)})
	h.process(context.Background(), -100, 42, dromURL)

	req := fake.next(t)
	if req.Method != "sendMediaGroup" {
		t.Fatalf("method = %q, want sendMediaGroup", req.Method)
	}
	if len(req.Files) != 2 {
		t.Errorf("uploaded files = %d, want 2 (битый снимок выпадает)", len(req.Files))
	}
	if media := req.field("media"); !strings.Contains(media, "attach://photo-2.jpg") ||
		strings.Contains(media, "attach://photo-1.jpg") {
		t.Errorf("media = %q, want photos 2 and 3", media)
	}
}

// TestProcessSendsDescriptionFollowUp проверяет, что длинное описание доезжает
// отдельными сообщениями после альбома.
func TestProcessSendsDescriptionFollowUp(t *testing.T) {
	images := imageServer(t)
	l := testListing(images.URL, 2)
	words := make([]string, 0, 900)
	for i := range 900 {
		words = append(words, fmt.Sprintf("слово%d", i))
	}
	l.Description = strings.Join(words, " ")

	h, fake := newTestHandler(t, stubExtractor{listing: l})
	h.process(context.Background(), -100, 42, dromURL)

	album := fake.next(t)
	if album.Method != "sendMediaGroup" {
		t.Fatalf("method = %q, want sendMediaGroup", album.Method)
	}

	var text strings.Builder
	for {
		select {
		case req := <-fake.requests:
			if req.Method != "sendMessage" {
				t.Fatalf("follow-up method = %q, want sendMessage", req.Method)
			}
			if _, has := req.Fields["reply_parameters"]; has {
				t.Error("follow-up must not reply to the original message")
			}
			text.WriteString(req.field("text"))
		default:
			if text.Len() == 0 {
				t.Fatal("no follow-up message with the description tail")
			}
			if !strings.Contains(text.String(), "слово899") {
				t.Error("описание оборвалось: последнее слово не доехало")
			}
			return
		}
	}
}

func TestProcessWithoutPhotos(t *testing.T) {
	h, fake := newTestHandler(t, stubExtractor{listing: testListing("https://example.com", 0)})
	h.process(context.Background(), -100, 42, dromURL)

	req := fake.next(t)
	if req.Method != "sendMessage" {
		t.Fatalf("method = %q, want sendMessage", req.Method)
	}
	if text := req.field("text"); !strings.Contains(text, "Год выпуска: 1986") {
		t.Errorf("text = %q", text)
	}
}

func TestProcessFallsBackToTextWhenPhotosFail(t *testing.T) {
	images := imageServer(t, "/photo-1.jpg", "/photo-2.jpg")
	h, fake := newTestHandler(t, stubExtractor{listing: testListing(images.URL, 2)})
	h.process(context.Background(), -100, 42, dromURL)

	req := fake.next(t)
	if req.Method != "sendMessage" {
		t.Fatalf("method = %q, want sendMessage fallback", req.Method)
	}
	if text := req.field("text"); !strings.Contains(text, "Мощность: 52 л.с.") {
		t.Errorf("text = %q", text)
	}
}

func TestProcessReportsExtractError(t *testing.T) {
	h, fake := newTestHandler(t, stubExtractor{err: extract.ErrBlocked})
	h.process(context.Background(), -100, 42, dromURL)

	req := fake.next(t)
	if req.Method != "sendMessage" {
		t.Fatalf("method = %q, want sendMessage", req.Method)
	}
	if text := req.field("text"); text != "Не удалось получить данные объявления: "+dromURL {
		t.Errorf("text = %q", text)
	}
}

func TestProcessIgnoresNonListingLinks(t *testing.T) {
	h, fake := newTestHandler(t, stubExtractor{listing: testListing("https://example.com", 0)})

	h.process(context.Background(), -100, 42, "https://www.avito.ru/novosibirsk/telefony/iphone_14_128gb_1234567890")
	h.process(context.Background(), -100, 42, "https://example.com")

	if got := fake.sent(); len(got) != 0 {
		t.Fatalf("bot answered non-listing links: %v", got)
	}
}

// TestUpdateLoop проверяет полный путь апдейта: telegohandler → разбор ссылки →
// отправка карточки в Bot API.
func TestUpdateLoop(t *testing.T) {
	images := imageServer(t)
	h, fake := newTestHandler(t, stubExtractor{listing: testListing(images.URL, 2)})

	updates := make(chan telego.Update, 1)
	bh, err := telegohandler.NewBotHandler(h.bot, updates)
	if err != nil {
		t.Fatalf("new bot handler: %v", err)
	}
	bh.HandleMessage(h.HandleMessage)

	stopped := make(chan struct{})
	go func() {
		defer close(stopped)
		if err := bh.Start(); err != nil {
			t.Errorf("bot handler start: %v", err)
		}
	}()
	t.Cleanup(func() {
		close(updates)
		select {
		case <-stopped:
		case <-time.After(10 * time.Second):
			t.Error("bot handler did not stop")
		}
	})

	updates <- telego.Update{
		UpdateID: 1,
		Message: &telego.Message{
			MessageID: 42,
			Chat:      telego.Chat{ID: -100, Type: "group"},
			Text:      "🔥 смотри " + dromURL,
			Entities: []telego.MessageEntity{
				{Type: telego.EntityTypeURL, Offset: 10, Length: len(dromURL)},
			},
		},
	}

	req := fake.next(t)
	if req.Method != "sendMediaGroup" {
		t.Fatalf("method = %q, want sendMediaGroup", req.Method)
	}
	if reply := req.field("reply_parameters"); !strings.Contains(reply, `"message_id":42`) {
		t.Errorf("reply_parameters = %q, want message_id 42", reply)
	}
}
