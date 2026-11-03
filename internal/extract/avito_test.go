package extract

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func loadPayload(t *testing.T, name string) avitoPayload {
	t.Helper()
	var p avitoPayload
	if err := json.Unmarshal(loadFixture(t, name), &p); err != nil {
		t.Fatalf("decode payload %s: %v", name, err)
	}
	return p
}

func TestParseAvitoPayload(t *testing.T) {
	const source = "https://avito.ru/novosibirsk/avtomobili/toyota_carina_1.6_mt_1982_190_000_km_8470651557"
	l, err := parseAvitoPayload(loadPayload(t, "avito_payload.json"), source)
	if err != nil {
		t.Fatalf("parseAvitoPayload: %v", err)
	}

	checks := []struct {
		name      string
		got, want string
	}{
		{"title", l.Title, "Toyota Carina 1.6 MT, 1982, 190 000 км"},
		{"price", l.Price, "350 000 ₽"},
		{"city", l.City, "Новосибирск"},
		{"source", l.SourceURL, source},
	}
	for _, c := range checks {
		if c.got != c.want {
			t.Errorf("%s = %q, want %q", c.name, c.got, c.want)
		}
	}

	for _, want := range []struct{ key, value string }{
		{"Год выпуска", "1982"},
		{"Модификация", "1.6 MT (115 л.с.)"},
		{"Объём двигателя", "1.6 л"},
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
	for _, s := range l.Specs {
		if s.Value == avitoAutoteka {
			t.Errorf("spec %q kept autoteka placeholder", s.Key)
		}
	}
	if len(l.Specs) > 10 {
		t.Errorf("len(specs) = %d, want <= 10", len(l.Specs))
	}
	if l.Specs[0].Key != "Год выпуска" {
		t.Errorf("first spec = %q, want Год выпуска", l.Specs[0].Key)
	}

	if len(l.Photos) == 0 {
		t.Fatalf("no photos parsed from gallery")
	}
	for _, u := range l.Photos {
		if !strings.HasPrefix(u, "https://") || !strings.Contains(u, ".img.avito.st/image/") {
			t.Errorf("bad photo url %q", u)
		}
		if strings.Contains(u, "&amp;") {
			t.Errorf("photo url has unescaped amp: %q", u)
		}
	}
}

func TestAvitoPhotosLimitAndDedupe(t *testing.T) {
	var media []string
	for i := range 12 {
		id := string(rune('a' + i))
		// Каждый снимок встречается дважды: состояние и DOM.
		for range 2 {
			media = append(media, "https://80.img.avito.st/image/1/1."+id+".photo"+id+".jpg?cqp=1&amp;x=2")
		}
	}

	got := avitoPhotos(avitoPayload{Media: media}, 10)
	if len(got) != 10 {
		t.Fatalf("len(photos) = %d, want 10", len(got))
	}
	for i, u := range got {
		id := string(rune('a' + i))
		if !strings.Contains(u, ".photo"+id+".jpg") {
			t.Errorf("photos[%d] = %q, want photo of id %q (order broken)", i, u, id)
		}
		if strings.Contains(u, "&amp;") {
			t.Errorf("photos[%d] not unescaped: %q", i, u)
		}
	}
}

func TestAvitoPhotosFallbackToGalleryHTML(t *testing.T) {
	html := loadFixture(t, "avito_gallery.html")

	fromState := avitoPhotos(avitoPayload{Media: []string{"https://00.img.avito.st/image/1/1.a.b.jpg"}}, 10)
	if len(fromState) != 1 {
		t.Fatalf("state source: got %d photos, want 1", len(fromState))
	}

	fromDOM := avitoPhotos(avitoPayload{GalleryHTML: string(html)}, 10)
	if len(fromDOM) == 0 {
		t.Fatal("dom fallback: no photos parsed from gallery html")
	}
	for _, u := range fromDOM {
		if !strings.Contains(u, ".img.avito.st/image/") {
			t.Errorf("dom fallback photo is not an avito image: %q", u)
		}
	}
}

func TestParseAvitoPayloadBlocked(t *testing.T) {
	_, err := parseAvitoPayload(avitoPayload{Title: "Доступ ограничен: проблема с IP"}, "")
	if !errors.Is(err, ErrBlocked) {
		t.Fatalf("err = %v, want ErrBlocked", err)
	}

	_, err = parseAvitoPayload(avitoPayload{Title: "Toyota Carina"}, "")
	if !errors.Is(err, ErrNoData) {
		t.Fatalf("err = %v, want ErrNoData", err)
	}
}
