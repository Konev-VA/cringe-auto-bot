package extract

import (
	"errors"
	"strings"
	"testing"
)

func TestParseFarpost(t *testing.T) {
	const source = "https://www.farpost.ru/vladivostok/auto/wheel/disc/oz-racing-ultraleggera-r18-136356352.html"
	l, err := parseViewbull(loadFixture(t, "farpost.html"), source, farpostSite)
	if err != nil {
		t.Fatalf("parseViewbull: %v", err)
	}

	checks := []struct {
		name      string
		got, want string
	}{
		{"title", l.Title, "OZ Racing Ultraleggera R18"},
		{"price", l.Price, "100 000 ₽"},
		{"city", l.City, "Владивосток"},
		{"source", l.SourceURL, source},
	}
	for _, c := range checks {
		if c.got != c.want {
			t.Errorf("%s = %q, want %q", c.name, c.got, c.want)
		}
	}

	if len(l.Photos) != 3 {
		t.Fatalf("len(photos) = %d, want 3", len(l.Photos))
	}
	for _, u := range l.Photos {
		if !strings.HasPrefix(u, "https://static.baza.farpost.ru/v/") || !strings.HasSuffix(u, "_bulletin") {
			t.Errorf("photo = %q", u)
		}
	}

	for _, want := range []struct{ key, value string }{
		{"Состояние товара", "Б/у"},
		{"Диаметр", `18"`},
		{"Ширина диска", `8.0"`},
		{"Вылет (ET)", "35 мм."},
		{"Сверловка (PCD)", "5x100"},
	} {
		got, ok := specValue(l.Specs, want.key)
		if !ok {
			t.Errorf("spec %q missing in %v", want.key, l.Specs)
			continue
		}
		if got != want.value {
			t.Errorf("spec %q = %q, want %q", want.key, got, want.value)
		}
	}

	if !strings.Contains(l.Description, "Диски состояния новых") {
		t.Errorf("description = %q", l.Description)
	}
	if strings.Contains(l.Description, "Доставка") {
		t.Error("description захватил блок доставки")
	}
}

func TestParseFarpostNoData(t *testing.T) {
	_, err := parseViewbull([]byte("<html><body><h1>Фарпост</h1></body></html>"), "https://www.farpost.ru/vladivostok/", farpostSite)
	if !errors.Is(err, ErrNoData) {
		t.Fatalf("err = %v, want ErrNoData", err)
	}
}
