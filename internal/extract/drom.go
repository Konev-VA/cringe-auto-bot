package extract

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"

	"github.com/PuerkitoBio/goquery"
	"golang.org/x/text/encoding/charmap"
	"golang.org/x/text/transform"

	"autobot/internal/listing"
)

const (
	dromUserAgent = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36"
	dromMaxPhotos = 10
	dromMaxSpecs  = 10
)

var dromPhotoRe = regexp.MustCompile(`https://[a-z0-9]{1,4}\.auto\.drom\.ru/photo/v2/([A-Za-z0-9_-]+)/gen1200\.jpg`)

var dromPriority = []string{
	"Год выпуска", "Двигатель", "Мощность", "Коробка передач", "Привод",
	"Пробег", "Цвет", "Руль", "Владельцы", "Комплектация",
}

// Drom разбирает объявления drom.ru обычным HTTP-запросом.
type Drom struct {
	client *http.Client
}

// NewDrom создаёт экстрактор drom.ru.
func NewDrom(client *http.Client) *Drom {
	if client == nil {
		client = http.DefaultClient
	}
	return &Drom{client: client}
}

// Extract загружает страницу объявления и разбирает её.
func (d *Drom) Extract(ctx context.Context, u *url.URL) (*listing.Listing, error) {
	body, err := d.fetch(ctx, u.String())
	if err != nil {
		return nil, err
	}
	return parseDrom(body, u.String())
}

func (d *Drom) fetch(ctx context.Context, raw string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, raw, nil)
	if err != nil {
		return nil, fmt.Errorf("drom: %w", err)
	}
	req.Header.Set("User-Agent", dromUserAgent)
	req.Header.Set("Accept-Language", "ru-RU,ru;q=0.9")

	resp, err := d.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("drom: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("drom: status %d: %w", resp.StatusCode, ErrBlocked)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("drom: read body: %w", err)
	}
	return body, nil
}

// parseDrom разбирает уже скачанную страницу (cp1251) объявления.
func parseDrom(body []byte, sourceURL string) (*listing.Listing, error) {
	doc, err := goquery.NewDocumentFromReader(transform.NewReader(bytes.NewReader(body), charmap.Windows1251.NewDecoder()))
	if err != nil {
		return nil, fmt.Errorf("drom: parse html: %w", err)
	}
	// Инлайн-<style> внутри карточки иначе попадает в текст.
	doc.Find("style,script").Remove()

	l := &listing.Listing{
		SourceURL:   sourceURL,
		Title:       dromText(doc, "h1"),
		Price:       dromText(doc, `[data-ftid="bulletin-price"]`),
		City:        strings.TrimSpace(strings.TrimPrefix(dromText(doc, `[data-ftid="city"]`), "Город:")),
		Description: dromText(doc, `[data-ftid="bulletin-description"]`),
		Specs:       dromSpecs(doc),
		Photos:      dromPhotos(body, dromMaxPhotos),
	}
	if l.Title == "" {
		if og, ok := doc.Find(`meta[property="og:title"]`).Attr("content"); ok {
			l.Title = strings.Join(strings.Fields(og), " ")
		}
	}
	l.Specs = listing.TrimSpecs(l.Specs, dromMaxSpecs, []string{"Год выпуска", "Двигатель", "Мощность"})
	// В блок описания попадает и строка "Город: ...", которую карточка и так
	// показывает в шапке.
	if idx := strings.LastIndex(l.Description, "Город:"); idx >= 0 &&
		strings.TrimSpace(l.Description[idx+len("Город:"):]) == l.City {
		l.Description = strings.TrimSpace(l.Description[:idx])
	}

	if l.Title == "" && l.Price == "" {
		return nil, ErrNoData
	}
	return l, nil
}

// dromText схлопывает пробелы в тексте первого совпадения селектора.
func dromText(doc *goquery.Document, sel string) string {
	return strings.Join(strings.Fields(doc.Find(sel).First().Text()), " ")
}

func dromSpecs(doc *goquery.Document) []listing.Spec {
	var specs []listing.Spec
	doc.Find(`table[data-ftid="bulletin-specifications"] tr[data-ftid^="specification-"]`).Each(func(_ int, row *goquery.Selection) {
		key := strings.Join(strings.Fields(row.Find(`th[data-ftid="property"]`).First().Text()), " ")
		// Внутри значения "Мощность" живёт кнопка "налог", а справа остаётся ", ".
		value := row.Find(`td[data-ftid="value"]`).First().Clone()
		value.Find("button").Remove()
		val := strings.TrimSuffix(strings.Join(strings.Fields(value.Text()), " "), ", ")
		val = strings.TrimSpace(strings.TrimSuffix(val, ","))
		if key == "" || val == "" {
			return
		}
		specs = append(specs, listing.Spec{Key: key, Value: val})
	})
	return listing.OrderSpecs(specs, dromPriority)
}

// dromPhotos вытаскивает URL фотографий из исходных байтов страницы,
// дедуплицируя по идентификатору снимка.
func dromPhotos(body []byte, limit int) []string {
	matches := dromPhotoRe.FindAllSubmatch(body, -1)
	photos := make([]string, 0, min(limit, len(matches)))
	seen := make(map[string]struct{}, len(matches))
	for _, m := range matches {
		id := string(m[1])
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		photos = append(photos, string(m[0]))
		if len(photos) == limit {
			break
		}
	}
	return photos
}
