package extract

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/PuerkitoBio/goquery"
	"github.com/chromedp/chromedp"
	"golang.org/x/text/encoding/charmap"
	"golang.org/x/text/transform"
	"net/http/cookiejar"

	"autobot/internal/listing"
)

const (
	bazaMaxPhotos = 10
	bazaMaxSpecs  = 10
	// bazaMaxSpecValue — значения длиннее (номера замен, применимость) в карточке
	// бесполезны и вытесняют остальные характеристики.
	bazaMaxSpecValue = 120
	bazaAttempt      = 25 * time.Second
)

var (
	bazaPhotoRe  = regexp.MustCompile(`https://static\.baza\.drom\.ru/drom/[A-Za-z0-9_-]+_bulletin`)
	bazaPriority = []string{"Состояние", "Производитель", "Номер запчасти", "Наличие"}
)

// Baza разбирает объявления baza.drom.ru (запчасти и шины). Обычного HTTP
// обычно достаточно, а если Дром отдаёт проверку — берём страницу браузером.
type Baza struct {
	client  *http.Client
	browser *Browser
}

// NewBaza создаёт экстрактор baza.drom.ru. browser может быть nil — тогда
// страницы, отдавшие проверку, вернут ErrBlocked.
func NewBaza(client *http.Client, browser *Browser) *Baza {
	if client == nil {
		client = http.DefaultClient
	}
	// Куки важны: пройденная один раз проверка Дрома живёт в сессии.
	if client.Jar == nil {
		shared := *client
		if jar, err := cookiejar.New(nil); err == nil {
			shared.Jar = jar
			client = &shared
		}
	}
	return &Baza{client: client, browser: browser}
}

func (b *Baza) Extract(ctx context.Context, u *url.URL) (*listing.Listing, error) {
	body, blocked, err := b.fetchHTTP(ctx, u.String())
	switch {
	case err == nil && !blocked:
		l, err := parseBaza(body, u.String())
		if err == nil {
			return l, nil
		}
		if !errors.Is(err, ErrNoData) {
			return nil, err
		}
	case err != nil:
		return nil, err
	}

	// Страница проверки или разметка без данных: пробуем настоящий браузер.
	if b.browser == nil {
		return nil, fmt.Errorf("baza: %w", ErrBlocked)
	}
	html, err := b.fetchBrowser(ctx, u.String())
	if err != nil {
		return nil, err
	}
	return parseBaza([]byte(html), u.String())
}

// fetchHTTP качает страницу. blocked=true означает проверку Дрома (/verify).
func (b *Baza) fetchHTTP(ctx context.Context, raw string) (body []byte, blocked bool, err error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, raw, nil)
	if err != nil {
		return nil, false, fmt.Errorf("baza: %w", err)
	}
	req.Header.Set("User-Agent", dromUserAgent)
	req.Header.Set("Accept-Language", "ru-RU,ru;q=0.9")

	resp, err := b.client.Do(req)
	if err != nil {
		return nil, false, fmt.Errorf("baza: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, false, fmt.Errorf("baza: status %d: %w", resp.StatusCode, ErrBlocked)
	}
	if resp.Request != nil && strings.HasPrefix(resp.Request.URL.Path, "/verify") {
		return nil, true, nil
	}

	body, err = io.ReadAll(resp.Body)
	if err != nil {
		return nil, false, fmt.Errorf("baza: read body: %w", err)
	}
	return body, false, nil
}

// fetchBrowser открывает страницу в Chromium и отдаёт готовый HTML.
func (b *Baza) fetchBrowser(ctx context.Context, raw string) (string, error) {
	tabCtx, tabCancel := b.browser.newTab()
	defer tabCancel()

	ctx, cancel := context.WithTimeout(tabCtx, bazaAttempt)
	defer cancel()

	if err := chromedp.Do(ctx, chromedp.Navigate(raw)); err != nil {
		return "", fmt.Errorf("baza: %w", err)
	}

	// Дром уводит на /verify с ALTCHA: headless её не проходит, поэтому не ждём
	// впустую весь таймаут, а сразу сообщаем, что площадка закрыта.
	challenged, err := chromedp.Run(ctx, chromedp.Evaluate[bool](
		`!!document.querySelector('altcha-widget') || location.pathname.startsWith('/verify')`))
	if err != nil {
		return "", fmt.Errorf("baza: %w", err)
	}
	if challenged {
		return "", fmt.Errorf("baza: проверка браузера: %w", ErrBlocked)
	}

	if err := chromedp.Do(ctx, chromedp.WaitReady(chromedp.CSS(`[data-field="subject"]`))); err != nil {
		return "", fmt.Errorf("baza: %w", err)
	}
	html, err := chromedp.Run(ctx, chromedp.OuterHTML("html"))
	if err != nil {
		return "", fmt.Errorf("baza: %w", err)
	}
	return html, nil
}

// parseBaza разбирает страницу объявления. Дром отдаёт cp1251, а браузер —
// уже utf-8, поэтому кодировку определяем по содержимому.
func parseBaza(body []byte, sourceURL string) (*listing.Listing, error) {
	var src io.Reader = bytes.NewReader(body)
	if !utf8.Valid(body) {
		src = transform.NewReader(src, charmap.Windows1251.NewDecoder())
	}
	doc, err := goquery.NewDocumentFromReader(src)
	if err != nil {
		return nil, fmt.Errorf("baza: parse html: %w", err)
	}
	doc.Find("style,script:not([type='application/ld+json'])").Remove()

	product, breadcrumbs := bazaJSONLD(doc)

	// Признак объявления: есть карточка товара, поле темы или цена. Иначе это
	// не объявление (например, страница проверки Дрома).
	subject := bazaField(doc, "subject")
	hasPrice := doc.Find("[data-bulletin-price]").Length() > 0 || product.price > 0
	if subject == "" && !hasPrice && product.name == "" {
		return nil, ErrNoData
	}

	l := &listing.Listing{
		SourceURL:   sourceURL,
		Title:       firstNonEmpty(product.name, subject, bazaText(doc, "h1")),
		Price:       bazaPrice(doc, product),
		City:        bazaCity(breadcrumbs, bazaText(doc, "h1")),
		Description: bazaField(doc, "text"),
		Specs:       bazaSpecs(doc),
		Photos:      bazaPhotos(product, body),
	}
	return l, nil
}

// bazaJSONLD достаёт из разметки schema.org название, цену и снимки.
func bazaJSONLD(doc *goquery.Document) (product bazaProduct, breadcrumbs []string) {
	doc.Find(`script[type="application/ld+json"]`).Each(func(_ int, s *goquery.Selection) {
		var raw map[string]any
		if err := json.Unmarshal([]byte(s.Text()), &raw); err != nil {
			return
		}
		switch raw["@type"] {
		case "Product":
			if name, ok := raw["name"].(string); ok {
				product.name = strings.TrimSpace(name)
			}
			if images, ok := raw["image"].([]any); ok {
				for _, img := range images {
					if s, ok := img.(string); ok {
						product.images = append(product.images, s)
					}
				}
			} else if img, ok := raw["image"].(string); ok {
				product.images = append(product.images, img)
			}
			if offers, ok := raw["offers"].(map[string]any); ok {
				if price, ok := offers["price"].(float64); ok {
					product.price = int(price)
				}
			}
		case "BreadcrumbList":
			items, _ := raw["itemListElement"].([]any)
			for _, item := range items {
				entry, ok := item.(map[string]any)
				if !ok {
					continue
				}
				if name, ok := entry["name"].(string); ok {
					breadcrumbs = append(breadcrumbs, name)
				}
			}
		}
	})
	return product, breadcrumbs
}

type bazaProduct struct {
	name   string
	price  int
	images []string
}

// bazaField достаёт текст поля по data-field.
func bazaField(doc *goquery.Document, field string) string {
	return bazaClean(doc.Find(fmt.Sprintf(`[data-field="%s"]`, field)).First().Text())
}

func bazaText(doc *goquery.Document, sel string) string {
	return bazaClean(doc.Find(sel).First().Text())
}

// bazaClean схлопывает пробелы. Стрелки-разделители «⁠» Дром вставляет для
// переноса строки — в тексте они лишние.
func bazaClean(s string) string {
	s = strings.ReplaceAll(s, "\u2060", " ")
	return strings.Join(strings.Fields(s), " ")
}

func bazaPrice(doc *goquery.Document, product bazaProduct) string {
	if product.price > 0 {
		return formatRub(product.price)
	}
	if raw, ok := doc.Find("[data-bulletin-price]").First().Attr("data-bulletin-price"); ok {
		if v, err := strconv.Atoi(strings.TrimSpace(raw)); err == nil && v > 0 {
			return formatRub(v)
		}
	}
	return bazaField(doc, "price")
}

// formatRub печатает цену в виде "140 000 ₽".
func formatRub(v int) string {
	s := strconv.Itoa(v)
	var b strings.Builder
	for i, r := range s {
		if i > 0 && (len(s)-i)%3 == 0 {
			b.WriteByte(' ')
		}
		b.WriteRune(r)
	}
	return b.String() + " ₽"
}

// bazaCity берёт город из хлебных крошек («Дром Барнаул»), иначе из заголовка.
func bazaCity(breadcrumbs []string, h1 string) string {
	for _, name := range breadcrumbs {
		if rest, ok := strings.CutPrefix(name, "Дром "); ok && rest != "" {
			return rest
		}
	}
	if idx := strings.LastIndex(h1, " в "); idx > 0 {
		return strings.TrimSpace(h1[idx+len(" в "):])
	}
	return ""
}

func bazaSpecs(doc *goquery.Document) []listing.Spec {
	var specs []listing.Spec
	doc.Find(".field").Each(func(_ int, f *goquery.Selection) {
		key := bazaClean(f.Find(".label").First().Text())
		value := bazaClean(f.Find(".value").First().Text())
		if key == "" || value == "" || len([]rune(value)) > bazaMaxSpecValue {
			return
		}
		specs = append(specs, listing.Spec{Key: strings.TrimSuffix(key, ":"), Value: value})
	})
	specs = listing.OrderSpecs(specs, bazaPriority)
	return listing.TrimSpecs(specs, bazaMaxSpecs, bazaPriority)
}

// bazaPhotos берёт снимки из разметки schema.org, иначе — по ссылкам на CDN.
func bazaPhotos(product bazaProduct, body []byte) []string {
	urls := product.images
	if len(urls) == 0 {
		urls = bazaPhotoRe.FindAllString(string(body), -1)
	}

	photos := make([]string, 0, min(bazaMaxPhotos, len(urls)))
	seen := make(map[string]struct{}, len(urls))
	for _, u := range urls {
		u = strings.ReplaceAll(u, "&amp;", "&")
		if _, ok := seen[u]; ok {
			continue
		}
		seen[u] = struct{}{}
		photos = append(photos, u)
		if len(photos) == bazaMaxPhotos {
			break
		}
	}
	return photos
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v = strings.TrimSpace(v); v != "" {
			return v
		}
	}
	return ""
}
