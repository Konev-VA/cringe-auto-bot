package listing

import (
	"errors"
	"net/url"
	"regexp"
	"strings"
	"unicode/utf16"

	"github.com/mymmrac/telego"
)

// Site — площадка объявлений.
type Site string

const (
	SiteDrom     Site = "drom"
	SiteDromBaza Site = "drom-baza"
	SiteAvito    Site = "avito"
)

// ErrNotListing — ссылка не является объявлением о продаже автомобиля.
var ErrNotListing = errors.New("listing: not a supported listing URL")

var (
	dromPathRe = regexp.MustCompile(`^/([^/]+)/([^/]+)/([^/]+)/(\d{5,})\.html$`)
	// Объявление на baza.drom.ru: короткая форма /g<номер>.html или
	// человекочитаемая /<город>/<категория>/<слаг>-<номер>.html
	bazaPathRe       = regexp.MustCompile(`^/g\d{6,}\.html$`)
	bazaReadablePath = regexp.MustCompile(`^/(?:[^/]+/)*[^/]+-\d{6,}\.html$`)
	// Любое объявление: /<город>/<категория>/<слаг>_<id>. Категория не важна:
	// бот отвечает и на автозапчасти, и на всё остальное.
	avitoPathRe = regexp.MustCompile(`^/[^/]+/[^/]+/[^/]+_\d{6,}$`)
	urlRe       = regexp.MustCompile(`https?://[^\s<>"']+`)
)

// Detect нормализует URL и определяет площадку.
func Detect(raw string) (Site, *url.URL, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return "", nil, ErrNotListing
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return "", nil, ErrNotListing
	}

	u.Scheme = "https"
	u.Fragment = ""
	u.RawFragment = ""
	u.Host = strings.ToLower(u.Host)
	for _, prefix := range []string{"www.", "m."} {
		if strings.HasPrefix(u.Host, prefix) {
			u.Host = strings.TrimPrefix(u.Host, prefix)
			break
		}
	}

	switch {
	case u.Host == "baza.drom.ru":
		if !bazaPathRe.MatchString(u.Path) && !bazaReadablePath.MatchString(u.Path) {
			return "", nil, ErrNotListing
		}
		return SiteDromBaza, u, nil
	case u.Host == "drom.ru" || strings.HasSuffix(u.Host, ".drom.ru"):
		if !dromPathRe.MatchString(u.Path) {
			return "", nil, ErrNotListing
		}
		return SiteDrom, u, nil
	case u.Host == "avito.ru" || strings.HasSuffix(u.Host, ".avito.ru"):
		if !avitoPathRe.MatchString(u.Path) {
			return "", nil, ErrNotListing
		}
		return SiteAvito, u, nil
	default:
		return "", nil, ErrNotListing
	}
}

// ExtractURLs достаёт ссылки из текста и entities, сохраняя порядок,
// без дублей, не более limit.
func ExtractURLs(text string, entities []telego.MessageEntity, limit int) []string {
	if limit <= 0 {
		return nil
	}

	var found []string
	if len(entities) > 0 {
		for _, e := range entities {
			switch e.Type {
			case telego.EntityTypeURL:
				if s := strings.TrimSpace(utf16Slice(text, e.Offset, e.Length)); s != "" {
					found = append(found, s)
				}
			case telego.EntityTypeTextLink:
				if s := strings.TrimSpace(e.URL); s != "" {
					found = append(found, s)
				}
			}
		}
	} else {
		for _, s := range urlRe.FindAllString(text, -1) {
			found = append(found, strings.TrimRight(s, ".,;:!?)»"))
		}
	}

	seen := make(map[string]struct{}, len(found))
	urls := make([]string, 0, min(limit, len(found)))
	for _, raw := range found {
		key := raw
		if _, u, err := Detect(raw); err == nil {
			key = u.String()
		}
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		urls = append(urls, raw)
		if len(urls) == limit {
			break
		}
	}
	return urls
}

// utf16Slice режет строку по смещениям в единицах UTF-16 — так Telegram
// считает Offset/Length у entities.
func utf16Slice(s string, off, length int) string {
	if off < 0 || length <= 0 {
		return ""
	}
	units := utf16.Encode([]rune(s))
	if off >= len(units) {
		return ""
	}
	end := min(off+length, len(units))
	return string(utf16.Decode(units[off:end]))
}
