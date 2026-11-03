package extract

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestParseBaza(t *testing.T) {
	const source = "https://baza.drom.ru/g18515686821.html"
	l, err := parseBaza(loadFixture(t, "baza.html"), source)
	if err != nil {
		t.Fatalf("parseBaza: %v", err)
	}

	checks := []struct {
		name      string
		got, want string
	}{
		{"title", l.Title, "Стартер SAT ST-A0051513901"},
		{"price", l.Price, "4 810 ₽"},
		{"city", l.City, "Барнаул"},
		{"source", l.SourceURL, source},
	}
	for _, c := range checks {
		if c.got != c.want {
			t.Errorf("%s = %q, want %q", c.name, c.got, c.want)
		}
	}

	if len(l.Photos) != 1 {
		t.Fatalf("len(photos) = %d, want 1", len(l.Photos))
	}
	const wantPhoto = "https://static.baza.drom.ru/drom/1729130741896_bulletin"
	if l.Photos[0] != wantPhoto {
		t.Errorf("photos[0] = %q, want %q", l.Photos[0], wantPhoto)
	}

	for _, want := range []struct{ key, value string }{
		{"Состояние", "Новый"},
		{"Производитель", "SAT"},
		{"Номер запчасти", "STA0051513901"},
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
		if n := utf8.RuneCountInString(s.Value); n > 120 {
			t.Errorf("spec %q = %d runes, want <= 120 (в карточке нечитаемо)", s.Key, n)
		}
	}

	if !strings.Contains(l.Description, "Применимость: Mercedes-Benz C-Class") {
		t.Errorf("description = %q", l.Description)
	}
	if strings.Contains(l.Description, "\u2060") {
		t.Error("description keeps soft-hyphen separators")
	}
}

// TestBazaPhotosLimit проверяет отбор снимков: без дублей и не больше лимита.
func TestBazaPhotosLimit(t *testing.T) {
	var images []string
	for i := range 12 {
		img := fmt.Sprintf("https://static.baza.drom.ru/drom/17884457%05d_bulletin", i)
		images = append(images, img, img) // дубли, как в разметке галереи
	}

	photos := bazaPhotos(bazaProduct{images: images}, nil)
	if len(photos) != 10 {
		t.Fatalf("len(photos) = %d, want 10", len(photos))
	}
	for i, u := range photos {
		want := fmt.Sprintf("https://static.baza.drom.ru/drom/17884457%05d_bulletin", i)
		if u != want {
			t.Errorf("photos[%d] = %q, want %q (порядок сломан)", i, u, want)
		}
	}
}

// TestBazaPhotosFromHTMLFallback — без разметки schema.org снимки берутся из DOM.
func TestBazaPhotosFromHTMLFallback(t *testing.T) {
	body := loadFixture(t, "baza.html")
	photos := bazaPhotos(bazaProduct{}, body)
	if len(photos) == 0 {
		t.Fatal("no photos from html fallback")
	}
	for _, u := range photos {
		if !strings.HasPrefix(u, "https://static.baza.drom.ru/drom/") {
			t.Errorf("photo = %q", u)
		}
	}
}

func TestParseBazaNoData(t *testing.T) {
	_, err := parseBaza([]byte("<html><body><h1>Доска объявлений</h1></body></html>"), "https://baza.drom.ru/g1.html")
	if !errors.Is(err, ErrNoData) {
		t.Fatalf("err = %v, want ErrNoData", err)
	}
}

func TestFormatRub(t *testing.T) {
	cases := map[int]string{
		400:     "400 ₽",
		4810:    "4 810 ₽",
		23000:   "23 000 ₽",
		140000:  "140 000 ₽",
		1234567: "1 234 567 ₽",
	}
	for in, want := range cases {
		if got := formatRub(in); got != want {
			t.Errorf("formatRub(%d) = %q, want %q", in, got, want)
		}
	}
}
