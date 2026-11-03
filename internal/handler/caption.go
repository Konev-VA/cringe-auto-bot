package handler

import (
	"html"
	"strings"

	"autobot/internal/listing"
)

const (
	// captionLimit — бюджет подписи в единицах UTF-16: 1024 это лимит Telegram,
	// запас оставлен на служебные символы разметки.
	captionLimit = 1000
	// messageLimit — бюджет отдельного сообщения: лимит Telegram 4096.
	messageLimit    = 4000
	maxCaptionSpecs = 10
)

var requiredSpecKeys = []string{"Год выпуска", "Двигатель", "Объём двигателя", "Мощность", "Модификация"}

// card собирает карточку объявления: подпись к фотографиям и, если описание не
// влезло в лимит подписи, куски описания для отдельных сообщений. Описание не
// выбрасывается: то, что не поместилось, уходит следующими сообщениями.
func card(l *listing.Listing) (caption string, rest []string) {
	head := ""
	if t := strings.TrimSpace(l.Title); t != "" {
		// Название длиннее бюджета обрезается до сборки блоков, чтобы
		// инвариант "подпись ≤ limit" держался при любых данных.
		if unitLen(t) > captionLimit/2 {
			t = truncateUnits(t, captionLimit/2)
		}
		head = "<b>" + html.EscapeString(t) + "</b>"
	}
	if pc := listing.JoinNonEmpty(" · ", l.Price, l.City); pc != "" {
		head = listing.JoinNonEmpty("\n\n", head, html.EscapeString(pc))
	}

	desc := strings.TrimSpace(l.Description)
	specs := listing.TrimSpecs(l.Specs, maxCaptionSpecs, requiredSpecKeys)

	for {
		base := joinBlocks(head, specLines(specs), "")
		if budget := captionLimit - unitLen(base) - 2; budget > 0 {
			inCaption, rest := descriptionParts(desc, budget)
			return joinBlocks(head, specLines(specs), inCaption), rest
		}

		next := listing.TrimSpecs(specs, len(specs)-1, requiredSpecKeys)
		if len(next) >= len(specs) {
			// Дальше режутся только "обязательные" характеристики: подпись
			// упирается в лимит, описание уходит целиком отдельными сообщениями.
			return truncateUnits(base, captionLimit), descriptionChunks(desc, messageLimit)
		}
		specs = next
	}
}

// caption возвращает только подпись, без продолжения.
func caption(l *listing.Listing) string {
	c, _ := card(l)
	return c
}

// descriptionParts отдаёт часть описания для подписи и остаток кусками для
// отдельных сообщений. budget — сколько единиц UTF-16 доступно в подписи.
func descriptionParts(desc string, budget int) (string, []string) {
	if desc == "" || budget <= 0 {
		return "", descriptionChunks(desc, messageLimit)
	}
	if unitLen(html.EscapeString(desc)) <= budget {
		return html.EscapeString(desc), nil
	}

	// Один юнит резервируется под многоточие.
	fits, tail := takeRunes(desc, budget-1)
	if idx := strings.LastIndexByte(fits, ' '); idx > len(fits)/2 {
		// Не рвать слово посередине: режем по последнему пробелу.
		fits, tail = fits[:idx], desc[idx+1:]
	}
	inCaption := ""
	if fits != "" {
		inCaption = html.EscapeString(fits) + "…"
	}
	return inCaption, descriptionChunks(tail, messageLimit)
}

// descriptionChunks режет текст на части, которые влезают в отдельное
// сообщение после HTML-экранирования.
func descriptionChunks(desc string, limit int) []string {
	var chunks []string
	for desc != "" {
		head, tail := takeRunes(desc, limit)
		if tail != "" {
			// Не рвать слово посередине, если рядом есть пробел.
			if idx := strings.LastIndexByte(head, ' '); idx > len(head)/2 {
				head, tail = head[:idx], desc[idx+1:]
			}
		}
		if head = strings.TrimSpace(head); head != "" {
			chunks = append(chunks, html.EscapeString(head))
		}
		desc = tail
	}
	return chunks
}

// takeRunes отрезает максимальный префикс, чей HTML-экранированный вид
// укладывается в limit единиц UTF-16, и возвращает остаток исходного текста.
func takeRunes(s string, limit int) (head, tail string) {
	units := 0
	for i, r := range s {
		n := unitLen(html.EscapeString(string(r)))
		if units+n > limit {
			return s[:i], s[i:]
		}
		units += n
	}
	return s, ""
}

func specLines(specs []listing.Spec) string {
	lines := make([]string, 0, len(specs))
	for _, s := range specs {
		if s.Key == "" || s.Value == "" {
			continue
		}
		lines = append(lines, html.EscapeString(s.Key+": "+s.Value))
	}
	return strings.Join(lines, "\n")
}

// joinBlocks склеивает непустые блоки одной пустой строкой.
func joinBlocks(parts ...string) string {
	kept := make([]string, 0, len(parts))
	for _, p := range parts {
		if strings.TrimSpace(p) != "" {
			kept = append(kept, p)
		}
	}
	return strings.Join(kept, "\n\n")
}

// truncateUnits обрезает строку до limit единиц UTF-16, добавляя многоточие.
func truncateUnits(s string, limit int) string {
	if limit <= 0 {
		return ""
	}
	if unitLen(s) <= limit {
		return s
	}
	var b strings.Builder
	units := 0
	for _, r := range s {
		n := utf16Len(r)
		if units+n > limit-1 {
			break
		}
		units += n
		b.WriteRune(r)
	}
	if b.Len() == 0 {
		return ""
	}
	b.WriteString("…")
	return b.String()
}

func unitLen(s string) int {
	units := 0
	for _, r := range s {
		units += utf16Len(r)
	}
	return units
}

func utf16Len(r rune) int {
	if r > 0xFFFF {
		return 2
	}
	return 1
}
