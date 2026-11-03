package extract

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"path"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/chromedp/chromedp"

	"autobot/internal/listing"
)

const (
	chromeUA           = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36"
	avitoAttempt       = 25 * time.Second
	avitoAttempts      = 2
	avitoAttemptMinGap = 5 * time.Second
	avitoMaxPhotos     = 10
	avitoMaxSpecs      = 10
	avitoPriceReady    = `[data-marker="item-view/item-price"]`
	avitoAutoteka      = "Проверить в Автотеке"
)

var avitoPhotoRe = regexp.MustCompile(`https://[0-9a-z]{2,4}\.img\.avito\.st/image/[^"'\s]+`)

var avitoPriority = []string{
	"Год выпуска", "Модификация", "Объём двигателя", "Тип двигателя", "Коробка передач",
	"Привод", "Пробег", "Тип кузова", "Цвет", "Руль",
}

// avitoSnapshotJS снимает состояние страницы объявления одним вызовом:
// Qrator-challenge к этому моменту уже пройден, потому что Go ждёт цену в DOM.
// Галерея грузит фотографии лениво (в DOM к моменту снимка 1–2 <img>), поэтому
// полный список снимков берётся из состояния приложения, а DOM галереи остаётся
// запасным источником.
const avitoSnapshotJS = `(() => {
  const q = s => document.querySelector(s);
  const clean = s => (s || "").replace(/\s+/g, " ").trim();
  const meta = n => (q('meta[property="' + n + '"]') || q('meta[name="' + n + '"]'))?.content || "";

  const mediaURLs = () => {
    const script = [...document.scripts].map(s => s.textContent || "").find(t => t.includes("__staticRouterHydrationData"));
    if (!script) return [];
    try {
      const start = script.indexOf("JSON.parse(") + "JSON.parse(".length;
      const state = JSON.parse(JSON.parse(script.slice(start, script.lastIndexOf(")"))));
      const findGallery = (node, depth) => {
        if (!node || typeof node !== "object" || depth > 10) return null;
        if (Array.isArray(node)) {
          for (const v of node) {
            const found = findGallery(v, depth + 1);
            if (found) return found;
          }
          return null;
        }
        if (Array.isArray(node.media) && node.media.length && node.media.every(m => m && m.urls)) return node;
        for (const k of Object.keys(node)) {
          const found = findGallery(node[k], depth + 1);
          if (found) return found;
        }
        return null;
      };
      const gallery = findGallery(state, 0);
      if (!gallery) return [];
      return gallery.media
        .filter(m => m && !m.isVideo && m.urls)
        .map(m => m.urls["1280x960"] || m.urls["640x480"] || Object.values(m.urls)[0]);
    } catch (e) {
      return [];
    }
  };

  const g = q('[data-marker="item-view/main-gallery"]');
  const p = q('[data-marker="item-view/item-params"]');
  const params = p ? [...p.querySelectorAll("li")].map(e => clean(e.innerText)).filter(Boolean) : [];
  const title = clean(meta("og:title")) || clean(q('[data-marker="item-view/title-info"]')?.innerText) ||
    clean(q("h1")?.innerText) || clean(document.title);
  return JSON.stringify({
    title,
    price: clean(q('[data-marker="item-view/item-price"]')?.innerText),
    city: clean(q('[data-marker="search-form/change-location"] a')?.getAttribute("title")),
    description: clean(q('[data-marker="item-view/item-description"]')?.innerText),
    params,
    media: mediaURLs(),
    galleryHtml: g ? g.outerHTML : ""
  });
})()`

// Avito разбирает объявления avito.ru через headless Chromium: обычный HTTP
// отдаёт 439 (Qrator), страница открывается только после прохождения challenge.
type Avito struct {
	chromePath string
	proxy      string

	once        sync.Once
	allocCtx    context.Context
	allocCancel context.CancelFunc
}

// NewAvito создаёт экстрактор avito.ru. Браузер поднимается при первом запросе.
// proxy (например socks5://host:1080) нужен там, где avito.ru блокирует адрес
// сервера: трафик браузера пойдёт через него.
func NewAvito(chromePath, proxy string) *Avito {
	return &Avito{chromePath: chromePath, proxy: proxy}
}

// Close останавливает общий браузерный аллокатор.
func (a *Avito) Close() {
	if a.allocCancel != nil {
		a.allocCancel()
	}
}

// Extract открывает страницу объявления в браузере и снимает с неё данные.
// Канал до avito.ru рвётся и отдаёт проверку на ровном месте, поэтому попыток
// несколько: каждая идёт в новой вкладке, и обычно со второй всё проходит.
func (a *Avito) Extract(ctx context.Context, u *url.URL) (*listing.Listing, error) {
	var lastErr error
	for range avitoAttempts {
		timeout := attemptTimeout(ctx, avitoAttempt)
		if timeout < avitoAttemptMinGap {
			break
		}

		payload, err := a.snapshot(ctx, u.String(), timeout)
		if err != nil {
			lastErr = err
			if ctx.Err() != nil {
				break
			}
			continue
		}

		l, err := parseAvitoPayload(payload, u.String())
		if err != nil {
			lastErr = err
			if errors.Is(err, ErrBlocked) || errors.Is(err, ErrNoData) {
				return nil, err
			}
			continue
		}
		return l, nil
	}
	if lastErr == nil {
		lastErr = ctx.Err()
	}
	return nil, lastErr
}

// attemptTimeout ограничивает попытку остатком времени вызывающего.
func attemptTimeout(ctx context.Context, limit time.Duration) time.Duration {
	deadline, ok := ctx.Deadline()
	if !ok {
		return limit
	}
	if remaining := time.Until(deadline); remaining < limit {
		return remaining
	}
	return limit
}

// snapshot открывает страницу в новой вкладке и снимает с неё снимок.
func (a *Avito) snapshot(ctx context.Context, target string, timeout time.Duration) (avitoPayload, error) {
	tabCtx, tabCancel := chromedp.NewContext(a.allocator())
	defer tabCancel()

	ctx, cancel := context.WithTimeout(tabCtx, timeout)
	defer cancel()

	err := chromedp.Do(ctx,
		chromedp.Navigate(target),
		// Ожидание селектора одновременно ждёт прохождения Qrator-challenge.
		chromedp.WaitReady(chromedp.CSS(avitoPriceReady)),
	)
	if err != nil {
		return avitoPayload{}, fmt.Errorf("avito: %w", err)
	}

	raw, err := chromedp.Run(ctx, chromedp.Evaluate[string](avitoSnapshotJS))
	if err != nil {
		return avitoPayload{}, fmt.Errorf("avito: %w", err)
	}

	var payload avitoPayload
	if err := json.Unmarshal([]byte(raw), &payload); err != nil {
		return avitoPayload{}, fmt.Errorf("avito: decode snapshot: %w", err)
	}
	return payload, nil
}

// allocator лениво создаёт один общий аллокатор браузера на процесс.
func (a *Avito) allocator() context.Context {
	a.once.Do(func() {
		opts := append(chromedp.DefaultExecAllocatorOptions[:],
			chromedp.ExecPath(a.chromePath),
			chromedp.Flag("headless", "new"),
			chromedp.Flag("no-sandbox", true),
			chromedp.Flag("disable-dev-shm-usage", true),
			chromedp.Flag("disable-gpu", true),
			chromedp.UserAgent(chromeUA),
		)
		if a.proxy != "" {
			opts = append(opts, chromedp.Flag("proxy-server", a.proxy))
		}
		a.allocCtx, a.allocCancel = chromedp.NewExecAllocator(context.Background(), opts...)
	})
	return a.allocCtx
}

// avitoPayload — снимок страницы, который возвращает avitoSnapshotJS.
type avitoPayload struct {
	Title       string   `json:"title"`
	Price       string   `json:"price"`
	City        string   `json:"city"`
	Description string   `json:"description"`
	Params      []string `json:"params"`
	Media       []string `json:"media"`
	GalleryHTML string   `json:"galleryHtml"`
}

// parseAvitoPayload превращает снимок страницы в объявление.
func parseAvitoPayload(p avitoPayload, sourceURL string) (*listing.Listing, error) {
	if p.Price == "" {
		if strings.Contains(p.Title, "Доступ ограничен") {
			return nil, ErrBlocked
		}
		return nil, ErrNoData
	}

	l := &listing.Listing{
		SourceURL:   sourceURL,
		Title:       p.Title,
		Price:       p.Price,
		City:        p.City,
		Description: p.Description,
		Specs:       avitoSpecs(p.Params),
		Photos:      avitoPhotos(p, avitoMaxPhotos),
	}
	l.Specs = listing.TrimSpecs(l.Specs, avitoMaxSpecs,
		[]string{"Год выпуска", "Объём двигателя", "Модификация", "Мощность"})
	return l, nil
}

func avitoSpecs(params []string) []listing.Spec {
	specs := make([]listing.Spec, 0, len(params))
	for _, line := range params {
		key, value, ok := strings.Cut(line, ": ")
		key, value = strings.TrimSpace(key), strings.TrimSpace(value)
		if !ok || key == "" || value == "" || value == avitoAutoteka {
			continue
		}
		specs = append(specs, listing.Spec{Key: key, Value: value})
	}
	return listing.OrderSpecs(specs, avitoPriority)
}

// avitoPhotos собирает ссылки на фотографии: сначала снимки из состояния
// страницы (полный список объявления), затем — запасной вариант из HTML
// контейнера галереи, где ленивая загрузка оставляет 1–2 <img>.
func avitoPhotos(p avitoPayload, limit int) []string {
	urls := p.Media
	if len(urls) == 0 {
		urls = avitoPhotoRe.FindAllString(p.GalleryHTML, -1)
	}
	photos := make([]string, 0, min(limit, len(urls)))
	seen := make(map[string]struct{}, len(urls))
	for _, raw := range urls {
		raw = strings.ReplaceAll(raw, "&amp;", "&")
		photoID := raw
		if u, err := url.Parse(raw); err == nil {
			base := path.Base(u.Path)
			if parts := strings.Split(base, "."); len(parts) > 1 {
				photoID = strings.Join(parts[1:], ".")
			}
		}
		if _, ok := seen[photoID]; ok {
			continue
		}
		seen[photoID] = struct{}{}
		photos = append(photos, raw)
		if len(photos) == limit {
			break
		}
	}
	return photos
}
