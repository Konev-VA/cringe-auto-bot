//go:build live

package extract

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"os"
	"testing"
	"time"

	"autobot/internal/listing"
)

// Живые проверки парсеров: сеть обязательна, токен бота не нужен.
// Запуск: go test ./internal/extract -run TestLive -tags live -v
const (
	liveDromURL  = "https://auto.drom.ru/novosibirsk/subaru/domingo/148849580.html"
	liveAvitoURL = "https://www.avito.ru/novosibirsk/avtomobili/toyota_carina_1.6_mt_1982_190_000_km_8470651557"
)

func TestLiveDrom(t *testing.T) {
	u, err := url.Parse(liveDromURL)
	if err != nil {
		t.Fatalf("parse url: %v", err)
	}

	l, err := NewDrom(&http.Client{Timeout: time.Minute}).Extract(context.Background(), u)
	if err != nil {
		t.Fatalf("extract: %v", err)
	}
	reportListing(t, l)

	if l.Title == "" || l.Price == "" || l.City == "" {
		t.Errorf("incomplete listing: title=%q price=%q city=%q", l.Title, l.Price, l.City)
	}
	if len(l.Photos) < 2 {
		t.Errorf("len(photos) = %d, want >= 2", len(l.Photos))
	}
	if len(l.Specs) == 0 {
		t.Error("no specs parsed")
	}
}

func TestLiveAvito(t *testing.T) {
	u, err := url.Parse(liveAvitoURL)
	if err != nil {
		t.Fatalf("parse url: %v", err)
	}

	chromePath := os.Getenv("CHROME_PATH")
	if chromePath == "" {
		chromePath = "/usr/bin/chromium"
	}
	avito := NewAvito(chromePath)
	defer avito.Close()

	l, err := avito.Extract(context.Background(), u)
	if err != nil {
		t.Fatalf("extract: %v", err)
	}
	reportListing(t, l)

	if l.Title == "" || l.Price == "" || l.City == "" {
		t.Errorf("incomplete listing: title=%q price=%q city=%q", l.Title, l.Price, l.City)
	}
	if len(l.Photos) < 2 {
		t.Errorf("len(photos) = %d, want >= 2", len(l.Photos))
	}
	if len(l.Specs) == 0 {
		t.Error("no specs parsed")
	}
}

func reportListing(t *testing.T, l *listing.Listing) {
	t.Helper()
	raw, err := json.MarshalIndent(l, "", "  ")
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	t.Logf("extracted:\n%s", raw)
}

// TestLiveDumpFixtures обновляет testdata из живых страниц.
// Запуск: AUTOBOT_DUMP_FIXTURES=1 go test ./internal/extract -run TestLiveDump -tags live -v
func TestLiveDumpFixtures(t *testing.T) {
	if os.Getenv("AUTOBOT_DUMP_FIXTURES") == "" {
		t.Skip("set AUTOBOT_DUMP_FIXTURES=1 to refresh testdata")
	}

	u, err := url.Parse(liveAvitoURL)
	if err != nil {
		t.Fatalf("parse url: %v", err)
	}
	avito := NewAvito(os.Getenv("CHROME_PATH"))
	defer avito.Close()

	payload, err := avito.snapshot(context.Background(), u.String())
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	raw, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if err := os.WriteFile("testdata/avito_payload.json", raw, 0o644); err != nil {
		t.Fatalf("write payload: %v", err)
	}
	if err := os.WriteFile("testdata/avito_gallery.html", []byte(payload.GalleryHTML), 0o644); err != nil {
		t.Fatalf("write gallery: %v", err)
	}
	t.Logf("photos parsed: %d", len(avitoPhotos(payload, avitoMaxPhotos)))
}
