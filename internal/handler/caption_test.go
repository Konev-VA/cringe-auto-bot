package handler

import (
	"fmt"
	"strings"
	"testing"

	"autobot/internal/listing"
)

func TestCaptionFitsLimit(t *testing.T) {
	l := &listing.Listing{
		Title:       "Toyota Carina 1.6 MT, 1982, 190 000 км",
		Price:       "350 000 ₽",
		City:        "Новосибирск",
		Description: strings.Repeat("подробное описание состояния автомобиля ", 80),
		Specs: []listing.Spec{
			{Key: "Год выпуска", Value: "1982"},
			{Key: "Модификация", Value: "1.6 MT (115 л.с.)"},
			{Key: "Объём двигателя", Value: "1.6 л"},
			{Key: "Тип двигателя", Value: "Бензин"},
			{Key: "Коробка передач", Value: "Механика"},
			{Key: "Привод", Value: "Задний"},
			{Key: "Пробег", Value: "190 000 км"},
			{Key: "Тип кузова", Value: "Седан"},
			{Key: "Цвет", Value: "Бежевый"},
			{Key: "Руль", Value: "Правый"},
		},
	}

	got, rest := card(l)

	if n := unitLen(got); n > 1000 {
		t.Errorf("caption length = %d units, want <= 1000", n)
	}
	if !strings.Contains(got, "Год выпуска: 1982") {
		t.Errorf("caption lost specs:\n%s", got)
	}
	if !strings.HasSuffix(got, "…") {
		t.Errorf("caption does not end with ellipsis:\n%s", got)
	}
	if len(rest) == 0 {
		t.Fatal("описание не влезло в подпись, но остаток не отдан отдельными сообщениями")
	}
	for _, want := range []string{
		"<b>Toyota Carina 1.6 MT, 1982, 190 000 км</b>",
		"350 000 ₽ · Новосибирск",
		"Модификация: 1.6 MT (115 л.с.)",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("caption lacks %q:\n%s", want, got)
		}
	}
}

func TestCaptionWithoutDescription(t *testing.T) {
	l := &listing.Listing{
		Title: "Subaru Domingo",
		Specs: []listing.Spec{{Key: "Год выпуска", Value: "1986"}},
	}

	got := caption(l)

	if strings.Contains(got, "·") {
		t.Errorf("caption has dangling separator: %q", got)
	}
	if strings.Contains(got, "\n\n\n") || strings.HasSuffix(got, "\n") || strings.HasPrefix(got, "\n") {
		t.Errorf("caption has empty blocks: %q", got)
	}
	want := "<b>Subaru Domingo</b>\n\nГод выпуска: 1986"
	if got != want {
		t.Errorf("caption = %q, want %q", got, want)
	}
}

func TestCaptionEscapesHTML(t *testing.T) {
	l := &listing.Listing{
		Title:       "Toyota <Carina> & Co",
		Price:       "350 000 ₽",
		Description: "a < b & c > d",
	}

	got := caption(l)

	if strings.Contains(got, "<Carina>") {
		t.Errorf("title is not escaped: %q", got)
	}
	if !strings.Contains(got, "&lt;Carina&gt; &amp; Co") {
		t.Errorf("escaped title missing: %q", got)
	}
	if !strings.HasSuffix(got, "a &lt; b &amp; c &gt; d") {
		t.Errorf("escaped description missing: %q", got)
	}
}

// TestDescriptionSpillsIntoMessages проверяет, что длинное описание не теряется:
// хвост уходит отдельными сообщениями в пределах лимита Telegram.
func TestDescriptionSpillsIntoMessages(t *testing.T) {
	words := make([]string, 0, 600)
	for i := range 600 {
		words = append(words, fmt.Sprintf("слово%d", i))
	}
	desc := strings.Join(words, " ")
	l := &listing.Listing{
		Title:       "Лада 2107, 2007 год в Кемерово",
		Price:       "17 500 ₽",
		City:        "Кемерово",
		Description: desc,
		Specs:       []listing.Spec{{Key: "Год выпуска", Value: "2007"}},
	}

	captionText, rest := card(l)
	if len(rest) == 0 {
		t.Fatal("no follow-up messages for a long description")
	}

	var joined strings.Builder
	joined.WriteString(captionText)
	for _, part := range rest {
		if n := unitLen(part); n > messageLimit {
			t.Errorf("part length = %d units, want <= %d", n, messageLimit)
		}
		if strings.TrimSpace(part) == "" {
			t.Error("empty description part")
		}
		joined.WriteString("\n")
		joined.WriteString(part)
	}

	// Текст не должен теряться: сравнение по символам без пробелов и многоточия.
	strip := func(s string) string {
		s = strings.ReplaceAll(s, "…", "")
		return strings.Join(strings.Fields(s), "")
	}
	if got, want := strip(joined.String()), strip(desc); !strings.Contains(got, want) && got != want {
		t.Fatalf("description lost text: got %d chars, want %d", len(got), len(want))
	}
	if !strings.Contains(joined.String(), "слово599") {
		t.Error("последнее слово описания потерялось — хвост не доехал")
	}
}

func TestCaptionSinglePartForShortDescription(t *testing.T) {
	l := &listing.Listing{
		Title:       "Subaru Domingo",
		Price:       "50 000 ₽",
		Description: "короткое описание",
	}
	captionText, rest := card(l)
	if len(rest) != 0 {
		t.Errorf("short description spilled into %d messages", len(rest))
	}
	if !strings.HasSuffix(captionText, "короткое описание") {
		t.Errorf("caption = %q", captionText)
	}
}

func TestCaptionPrunesSpecsButKeepsPower(t *testing.T) {
	specs := []listing.Spec{
		{Key: "Год выпуска", Value: "1986"},
		{Key: "Двигатель", Value: "бензин, 1.2 л"},
		{Key: "Мощность", Value: "52 л.с."},
	}
	for i := range 7 {
		specs = append(specs, listing.Spec{Key: "Хвост" + string(rune('0'+i)), Value: strings.Repeat("х", 60)})
	}
	l := &listing.Listing{
		Title:       strings.Repeat("длинное название ", 20),
		Description: strings.Repeat("описание ", 400),
		Specs:       specs,
	}

	got := caption(l)

	if n := unitLen(got); n > 1000 {
		t.Fatalf("caption length = %d units, want <= 1000", n)
	}
	for _, want := range []string{"Год выпуска: 1986", "Двигатель: бензин, 1.2 л", "Мощность: 52 л.с."} {
		if !strings.Contains(got, want) {
			t.Errorf("caption dropped %q:\n%s", want, got)
		}
	}
}
