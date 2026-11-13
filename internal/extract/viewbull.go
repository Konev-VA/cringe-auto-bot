package extract

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
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

	"autobot/internal/listing"
)

const (
	viewbullMaxPhotos = 10
	viewbullMaxSpecs  = 10
	// viewbullMaxSpecValue — значения длиннее (номера замен, применимость) в
	// карточке бесполезны и вытесняют остальные характеристики.
	viewbullMaxSpecValue = 120
	viewbullAttempt      = 25 * time.Second
)

// viewbullSite — то, чем площадки на движке viewbull отличаются друг от друга:
// разметка, поля и проверка у baza.drom.ru и farpost.ru одинаковые.
type viewbullSite struct {
	name       string         // префикс ошибок
	photoRe    *regexp.Regexp // CDN снимков площадки
	cityPrefix string         // как площадка подписывает город в хлебных крошках
	priority   []string       // порядок характеристик в карточке
	required   []string       // характеристики, которые не вытесняются из карточки
}

var (
	bazaSite = viewbullSite{
		name:       "baza",
		photoRe:    regexp.MustCompile(`https://static\.baza\.drom\.ru/drom/[A-Za-z0-9_-]+_bulletin`),
		cityPrefix: "Дром ",
		priority:   []string{"Состояние", "Производитель", "Номер запчасти", "Наличие"},
		required:   []string{"Состояние", "Производитель", "Номер запчасти", "Наличие"},
	}
	farpostSite = viewbullSite{
		name:       "farpost",
		photoRe:    regexp.MustCompile(`https://static\.baza\.farpost\.ru/[^"'\s]*_bulletin`),
		cityPrefix: "Фарпост ",
		priority: []string{
			"Год выпуска", "Двигатель", "Мощность", "Коробка передач", "Привод",
			"Пробег", "Состояние товара", "Состояние", "Диаметр", "Ширина диска",
			"Вылет (ET)", "Сверловка (PCD)", "Количество дисков в комплекте", "Производитель",
		},
		required: []string{"Год выпуска", "Двигатель", "Мощность", "Состояние товара", "Диаметр"},
	}
)

// Viewbull разбирает объявления на движке viewbull: baza.drom.ru (запчасти и
// шины) и farpost.ru. Обычного HTTP обычно достаточно, а если площадка отдаёт
// проверку — берём страницу браузером.
type Viewbull struct {
	client  *http.Client
	browser *Browser
	site    viewbullSite
}

// NewBaza создаёт экстрактор baza.drom.ru. browser может быть nil — тогда
// страницы, отдавшие проверку, вернут ErrBlocked.
func NewBaza(client *http.Client, browser *Browser) *Viewbull {
	return newViewbull(client, browser, bazaSite)
}

// NewFarpost создаёт экстрактор farpost.ru. browser может быть nil — тогда
// страницы, отдавшие проверку, вернут ErrBlocked.
func NewFarpost(client *http.Client, browser *Browser) *Viewbull {
	return newViewbull(client, browser, farpostSite)
}

func newViewbull(client *http.Client, browser *Browser, site viewbullSite) *Viewbull {
	if client == nil {
		client = http.DefaultClient
	}
	// Куки важны: пройденная один раз проверка площадки живёт в сессии.
	if client.Jar == nil {
		shared := *client
		if jar, err := cookiejar.New(nil); err == nil {
			shared.Jar = jar
			client = &shared
		}
	}
	return &Viewbull{client: client, browser: browser, site: site}
}

func (v *Viewbull) Extract(ctx context.Context, u *url.URL) (*listing.Listing, error) {
	body, blocked, err := v.fetchHTTP(ctx, u.String())
	switch {
	case err == nil && !blocked:
		l, err := parseViewbull(body, u.String(), v.site)
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
	if v.browser == nil {
		return nil, fmt.Errorf("%s: %w", v.site.name, ErrBlocked)
	}
	html, err := v.fetchBrowser(ctx, u.String())
	if err != nil {
		return nil, err
	}
	return parseViewbull([]byte(html), u.String(), v.site)
}

// fetchHTTP качает страницу. blocked=true означает проверку площадки (/verify).
func (v *Viewbull) fetchHTTP(ctx context.Context, raw string) (body []byte, blocked bool, err error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, raw, nil)
	if err != nil {
		return nil, false, fmt.Errorf("%s: %w", v.site.name, err)
	}
	req.Header.Set("User-Agent", dromUserAgent)
	req.Header.Set("Accept-Language", "ru-RU,ru;q=0.9")

	resp, err := v.client.Do(req)
	if err != nil {
		return nil, false, fmt.Errorf("%s: %w", v.site.name, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, false, fmt.Errorf("%s: status %d: %w", v.site.name, resp.StatusCode, ErrBlocked)
	}
	if resp.Request != nil && strings.HasPrefix(resp.Request.URL.Path, "/verify") {
		return nil, true, nil
	}

	body, err = io.ReadAll(resp.Body)
	if err != nil {
		return nil, false, fmt.Errorf("%s: read body: %w", v.site.name, err)
	}
	return body, false, nil
}

// fetchBrowser открывает страницу в Chromium и отдаёт готовый HTML.
func (v *Viewbull) fetchBrowser(ctx context.Context, raw string) (string, error) {
	tabCtx, tabCancel := v.browser.newTab()
	defer tabCancel()

	ctx, cancel := context.WithTimeout(tabCtx, viewbullAttempt)
	defer cancel()

	if err := chromedp.Do(ctx, chromedp.Navigate(raw)); err != nil {
		return "", fmt.Errorf("%s: %w", v.site.name, err)
	}

	// Площадка уводит на /verify с ALTCHA: headless её не проходит, поэтому не
	// ждём впустую весь таймаут, а сразу сообщаем, что площадка закрыта.
	challenged, err := chromedp.Run(ctx, chromedp.Evaluate[bool](
		`!!document.querySelector('altcha-widget') || location.pathname.startsWith('/verify')`))
	if err != nil {
		return "", fmt.Errorf("%s: %w", v.site.name, err)
	}
	if challenged {
		return "", fmt.Errorf("%s: проверка браузера: %w", v.site.name, ErrBlocked)
	}

	if err := chromedp.Do(ctx, chromedp.WaitReady(chromedp.CSS(`[data-field="subject"]`))); err != nil {
		return "", fmt.Errorf("%s: %w", v.site.name, err)
	}
	html, err := chromedp.Run(ctx, chromedp.OuterHTML("html"))
	if err != nil {
		return "", fmt.Errorf("%s: %w", v.site.name, err)
	}
	return html, nil
}

// parseViewbull разбирает страницу объявления. Площадки отдают cp1251, а
// браузер — уже utf-8, поэтому кодировку определяем по содержимому.
func parseViewbull(body []byte, sourceURL string, site viewbullSite) (*listing.Listing, error) {
	var src io.Reader = bytes.NewReader(body)
	if !utf8.Valid(body) {
		src = transform.NewReader(src, charmap.Windows1251.NewDecoder())
	}
	doc, err := goquery.NewDocumentFromReader(src)
	if err != nil {
		return nil, fmt.Errorf("%s: parse html: %w", site.name, err)
	}
	doc.Find("style,script:not([type='application/ld+json'])").Remove()

	product, breadcrumbs := viewbullJSONLD(doc)

	// Признак объявления: есть карточка товара, поле темы или цена. Иначе это
	// не объявление (например, страница проверки площадки).
	subject := viewbullField(doc, "subject")
	hasPrice := doc.Find("[data-bulletin-price]").Length() > 0 || product.price > 0
	if subject == "" && !hasPrice && product.name == "" {
		return nil, ErrNoData
	}

	return &listing.Listing{
		SourceURL:   sourceURL,
		Title:       firstNonEmpty(product.name, subject, viewbullText(doc, "h1")),
		Price:       viewbullPrice(doc, product),
		City:        viewbullCity(breadcrumbs, viewbullText(doc, "h1"), site.cityPrefix),
		Description: viewbullField(doc, "text"),
		Specs:       viewbullSpecs(doc, site.priority, site.required),
		Photos:      viewbullPhotos(product, body, site.photoRe),
	}, nil
}

// viewbullJSONLD достаёт из разметки schema.org название, цену и снимки.
func viewbullJSONLD(doc *goquery.Document) (product viewbullProduct, breadcrumbs []string) {
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

type viewbullProduct struct {
	name   string
	price  int
	images []string
}

// viewbullField достаёт текст поля по data-field.
func viewbullField(doc *goquery.Document, field string) string {
	return viewbullClean(doc.Find(fmt.Sprintf(`[data-field="%s"]`, field)).First().Text())
}

func viewbullText(doc *goquery.Document, sel string) string {
	return viewbullClean(doc.Find(sel).First().Text())
}

// viewbullClean схлопывает пробелы. Стрелки-разделители «⁠» Дром вставляет для
// переноса строки — в тексте они лишние.
func viewbullClean(s string) string {
	s = strings.ReplaceAll(s, "\u2060", " ")
	return strings.Join(strings.Fields(s), " ")
}

func viewbullPrice(doc *goquery.Document, product viewbullProduct) string {
	if product.price > 0 {
		return formatRub(product.price)
	}
	if raw, ok := doc.Find("[data-bulletin-price]").First().Attr("data-bulletin-price"); ok {
		if v, err := strconv.Atoi(strings.TrimSpace(raw)); err == nil && v > 0 {
			return formatRub(v)
		}
	}
	return viewbullField(doc, "price")
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

// viewbullCity берёт город из хлебных крошек («Дром Барнаул», «Фарпост
// Владивосток»), иначе из заголовка.
func viewbullCity(breadcrumbs []string, h1, prefix string) string {
	for _, name := range breadcrumbs {
		if rest, ok := strings.CutPrefix(name, prefix); ok && rest != "" {
			return rest
		}
	}
	if idx := strings.LastIndex(h1, " в "); idx > 0 {
		return strings.TrimSpace(h1[idx+len(" в "):])
	}
	return ""
}

func viewbullSpecs(doc *goquery.Document, priority, required []string) []listing.Spec {
	var specs []listing.Spec
	doc.Find(".field").Each(func(_ int, f *goquery.Selection) {
		key := viewbullClean(f.Find(".label").First().Text())
		value := viewbullClean(f.Find(".value").First().Text())
		if key == "" || value == "" || len([]rune(value)) > viewbullMaxSpecValue {
			return
		}
		specs = append(specs, listing.Spec{Key: strings.TrimSuffix(key, ":"), Value: value})
	})
	specs = listing.OrderSpecs(specs, priority)
	return listing.TrimSpecs(specs, viewbullMaxSpecs, required)
}

// viewbullPhotos берёт снимки из разметки schema.org, иначе — по ссылкам на CDN.
func viewbullPhotos(product viewbullProduct, body []byte, photoRe *regexp.Regexp) []string {
	urls := product.images
	if len(urls) == 0 {
		urls = photoRe.FindAllString(string(body), -1)
	}

	photos := make([]string, 0, min(viewbullMaxPhotos, len(urls)))
	seen := make(map[string]struct{}, len(urls))
	for _, u := range urls {
		u = strings.ReplaceAll(u, "&amp;", "&")
		if _, ok := seen[u]; ok {
			continue
		}
		seen[u] = struct{}{}
		photos = append(photos, u)
		if len(photos) == viewbullMaxPhotos {
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
