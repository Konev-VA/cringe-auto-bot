// Package handler обрабатывает сообщения чата и отвечает карточкой объявления.
package handler

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/mymmrac/telego"
	"github.com/mymmrac/telego/telegohandler"

	"autobot/internal/extract"
	"autobot/internal/listing"
	"autobot/internal/tgnet"
)

const (
	maxURLsPerMessage  = 3
	maxParallelFetches = 4
	sendTimeout        = 30 * time.Second
)

// Handler ищет ссылки на объявления в сообщениях и отвечает на них карточкой.
type Handler struct {
	bot     *telego.Bot
	ex      map[listing.Site]extract.Extractor
	client  *http.Client
	sem     chan struct{}
	timeout time.Duration
	log     *slog.Logger
}

// New собирает обработчик.
func New(bot *telego.Bot, ex map[listing.Site]extract.Extractor, client *http.Client, timeout time.Duration, log *slog.Logger) *Handler {
	if client == nil {
		client = &http.Client{Timeout: photoTimeout}
	}
	return &Handler{
		bot:     bot,
		ex:      ex,
		client:  client,
		sem:     make(chan struct{}, maxParallelFetches),
		timeout: timeout,
		log:     log,
	}
}

// HandleMessage ищет ссылки в тексте (или подписи) сообщения и отвечает на
// каждую асинхронно, чтобы не блокировать цикл обработки апдейтов.
func (h *Handler) HandleMessage(ctx *telegohandler.Context, msg telego.Message) error {
	text, entities := msg.Text, msg.Entities
	if text == "" {
		text, entities = msg.Caption, msg.CaptionEntities
	}

	urls := listing.ExtractURLs(text, entities, maxURLsPerMessage)
	if len(urls) == 0 {
		return nil
	}

	base := context.WithoutCancel(ctx)
	for _, raw := range urls {
		h.sem <- struct{}{}
		go func() {
			defer func() { <-h.sem }()
			h.process(base, msg.Chat.ID, msg.MessageID, raw)
		}()
	}
	return nil
}

func (h *Handler) process(base context.Context, chatID int64, messageID int, raw string) {
	site, u, err := listing.Detect(raw)
	if err != nil {
		return // не объявление о продаже авто — молчим
	}
	ex, ok := h.ex[site]
	if !ok {
		return
	}

	fetchCtx, cancel := context.WithTimeout(base, h.timeout)
	defer cancel()

	l, err := ex.Extract(fetchCtx, u)
	sendCtx, sendCancel := context.WithTimeout(base, sendTimeout)
	defer sendCancel()

	if err != nil {
		h.log.Warn("extract failed", "url", raw, "error", h.redact(err))
		h.sendText(sendCtx, chatID, messageID, "Не удалось получить данные объявления: "+raw)
		return
	}

	h.sendListing(sendCtx, chatID, messageID, l)
}

// redact убирает токен бота из текста ошибки перед записью в лог.
func (h *Handler) redact(err error) string {
	return tgnet.RedactToken(err.Error(), h.bot.Token())
}

func (h *Handler) sendText(ctx context.Context, chatID int64, messageID int, text string) {
	_, err := h.bot.SendMessage(ctx, &telego.SendMessageParams{
		ChatID:          telego.ChatID{ID: chatID},
		Text:            text,
		ReplyParameters: &telego.ReplyParameters{MessageID: messageID},
	})
	if err != nil {
		h.log.Error("send message failed", "chat_id", chatID, "error", h.redact(err))
	}
}

func (h *Handler) sendListing(ctx context.Context, chatID int64, messageID int, l *listing.Listing) {
	start := time.Now()
	text, rest := card(l)
	reply := &telego.ReplyParameters{MessageID: messageID}
	files := h.downloadPhotos(ctx, l.Photos)
	downloaded := time.Since(start)

	// Пока уходит альбом, держим наготове второе соединение: в блокирующих
	// сетях новый dial стоит 2–4 с, и продолжение описания ждало бы именно его.
	warm := make(chan struct{})
	go func() {
		defer close(warm)
		h.warmConnection(ctx, chatID, l.SourceURL)
	}()

	err := h.sendWithRetry(ctx, func() error {
		switch len(files) {
		case 0:
			_, err := h.bot.SendMessage(ctx, &telego.SendMessageParams{
				ChatID:          telego.ChatID{ID: chatID},
				Text:            text,
				ParseMode:       telego.ModeHTML,
				ReplyParameters: reply,
			})
			return err
		case 1:
			_, err := h.bot.SendPhoto(ctx, &telego.SendPhotoParams{
				ChatID:          telego.ChatID{ID: chatID},
				Photo:           files[0].inputFile(),
				Caption:         text,
				ParseMode:       telego.ModeHTML,
				ReplyParameters: reply,
			})
			return err
		default:
			media := make([]telego.InputMedia, 0, len(files))
			for i, f := range files {
				m := &telego.InputMediaPhoto{Type: telego.MediaTypePhoto, Media: f.inputFile()}
				if i == 0 {
					m.Caption, m.ParseMode = text, telego.ModeHTML
				}
				media = append(media, m)
			}
			_, err := h.bot.SendMediaGroup(ctx, &telego.SendMediaGroupParams{
				ChatID:          telego.ChatID{ID: chatID},
				Media:           media,
				ReplyParameters: reply,
			})
			return err
		}
	})

	if err != nil {
		h.log.Error("send listing failed",
			"chat_id", chatID,
			"url", l.SourceURL,
			"photos", len(files),
			"error", h.redact(err),
		)
		return
	}

	<-warm
	descStart := time.Now()
	h.sendDescription(ctx, chatID, l.SourceURL, rest)
	descDone := time.Since(descStart)

	// Тайминги нужны, чтобы понимать, где именно уходит пауза: скачивание
	// снимков, сама отправка карточки или продолжение описания.
	h.log.Info("listing sent",
		"chat_id", chatID,
		"url", l.SourceURL,
		"photos", len(files),
		"parts", len(rest),
		"download_ms", downloaded.Milliseconds(),
		"send_ms", (descStart.Sub(start) - downloaded).Milliseconds(),
		"desc_ms", descDone.Milliseconds(),
		"total_ms", (descStart.Sub(start) + descDone).Milliseconds(),
	)
}

// warmConnection открывает и держит соединение с Bot API, чтобы следующее
// сообщение уходило по уже установленному каналу.
func (h *Handler) warmConnection(ctx context.Context, chatID int64, sourceURL string) {
	warmCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	if _, err := h.bot.GetMe(warmCtx); err != nil && !errors.Is(err, context.Canceled) {
		h.log.Warn("warm connection failed", "chat_id", chatID, "url", sourceURL, "error", h.redact(err))
	}
}

// sendDescription отправляет остаток описания, который не влез в подпись.
func (h *Handler) sendDescription(ctx context.Context, chatID int64, sourceURL string, parts []string) {
	for i, part := range parts {
		_, err := h.bot.SendMessage(ctx, &telego.SendMessageParams{
			ChatID:    telego.ChatID{ID: chatID},
			Text:      part,
			ParseMode: telego.ModeHTML,
		})
		if err != nil {
			h.log.Error("send description failed",
				"chat_id", chatID,
				"url", sourceURL,
				"part", i+1,
				"error", h.redact(err),
			)
			return
		}
	}
}

// sendWithRetry повторяет отправку, только если запрос не дошёл до сервера
// (обрыв на dial). В блокирующих сетях такие обрывы идут пачками, а любая
// другая ошибка повторного запроса не заслуживает — альбом задвоится.
func (h *Handler) sendWithRetry(ctx context.Context, send func() error) error {
	const attempts = 3

	var err error
	for attempt := 1; attempt <= attempts; attempt++ {
		if err = send(); err == nil || !tgnet.IsDialError(err) {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Duration(attempt) * 200 * time.Millisecond):
		}
	}
	return err
}
