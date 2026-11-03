//go:build live

package handler

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/mymmrac/telego"

	"autobot/internal/extract"
	"autobot/internal/listing"
)

// Сквозной прогон: реальные страницы объявлений → карточка в Bot API.
// Запуск: go test ./internal/handler -run TestLiveSend -tags live -v
func TestLiveSend(t *testing.T) {
	cases := []struct {
		name     string
		url      string
		site     listing.Site
		caption  []string
		followUp bool
	}{
		{
			name: "drom",
			url:  "https://auto.drom.ru/novosibirsk/subaru/domingo/148849580.html",
			site: listing.SiteDrom,
			caption: []string{
				"Продажа Subaru Domingo, 1986 год в Новосибирске",
				"50 000 ₽ · Новосибирск",
				"Год выпуска: 1986",
				"Двигатель: бензин, 1.2 л",
				"Мощность: 52 л.с.",
			},
		},
		{
			name: "avito",
			url:  "https://www.avito.ru/novosibirsk/avtomobili/toyota_carina_1.6_mt_1982_190_000_km_8470651557",
			site: listing.SiteAvito,
			caption: []string{
				"Toyota Carina 1.6 MT, 1982, 190 000 км",
				"350 000 ₽ · Новосибирск",
				"Год выпуска: 1982",
				"Модификация: 1.6 MT (115 л.с.)",
				"Объём двигателя: 1.6 л",
			},
		},
		{
			// Объявление с длинным описанием: раньше такое обрывалось в подписи.
			name: "drom-long-description",
			url:  "https://auto.drom.ru/krasnoyarsk/ford/fusion/532448742.html",
			site: listing.SiteDrom,
			caption: []string{
				"Продажа Ford Fusion, 2007 год в Красноярске",
				"350 000 ₽ · Красноярск",
				"Год выпуска: 2007",
			},
		},
	}

	fake := newFakeTelegram(t)
	bot, err := telego.NewBot(testToken, telego.WithAPIServer(fake.server.URL), telego.WithDiscardLogger())
	if err != nil {
		t.Fatalf("new bot: %v", err)
	}

	chromePath := os.Getenv("CHROME_PATH")
	if chromePath == "" {
		chromePath = "/usr/bin/chromium"
	}
	avito := extract.NewAvito(chromePath, os.Getenv("AVITO_PROXY"))
	defer avito.Close()

	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	h := New(bot, map[listing.Site]extract.Extractor{
		listing.SiteDrom:  extract.NewDrom(&http.Client{Timeout: time.Minute}),
		listing.SiteAvito: avito,
	}, &http.Client{Timeout: 30 * time.Second}, 90*time.Second, log)

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h.process(context.Background(), -100, 42, tc.url)

			req := fake.next(t)
			if req.Method != "sendMediaGroup" {
				t.Fatalf("method = %q, want sendMediaGroup", req.Method)
			}

			var media []struct {
				Media   string `json:"media"`
				Caption string `json:"caption"`
			}
			if err := json.Unmarshal([]byte(req.field("media")), &media); err != nil {
				t.Fatalf("decode media: %v", err)
			}
			if len(media) < 2 {
				t.Fatalf("media items = %d, want >= 2 photos", len(media))
			}
			if len(req.Files) != len(media) {
				t.Errorf("uploaded files = %d, want %d (снимки качает бот)", len(req.Files), len(media))
			}
			for name, data := range req.Files {
				if len(data) < 1024 {
					t.Errorf("file %s = %d bytes, want a real photo", name, len(data))
				}
			}
			for _, want := range tc.caption {
				if !strings.Contains(media[0].Caption, want) {
					t.Errorf("caption lacks %q:\n%s", want, media[0].Caption)
				}
			}
			t.Logf("album of %d uploaded photos:\n%s", len(media), media[0].Caption)

			var tail strings.Builder
			for {
				select {
				case next := <-fake.requests:
					if next.Method != "sendMessage" {
						t.Fatalf("continuation method = %q, want sendMessage", next.Method)
					}
					tail.WriteString(next.field("text"))
				default:
					// Если подпись обрезана многоточием, хвост описания обязан
					// приехать отдельным сообщением.
					truncated := strings.HasSuffix(media[0].Caption, "…")
					if truncated && tail.Len() == 0 {
						t.Error("подпись обрезана, но продолжение описания не пришло")
					}
					if !truncated && tail.Len() > 0 {
						t.Errorf("описание влезло в подпись, но пришло продолжение")
					}
					if tail.Len() > 0 {
						t.Logf("description continuation (%d units): %s", unitLen(tail.String()), truncateUnits(tail.String(), 300))
					}
					return
				}
			}
		})
	}
}
