package extract

import (
	"errors"
	"os"
	"strings"
	"testing"

	"autobot/internal/listing"
)

func loadFixture(t *testing.T, name string) []byte {
	t.Helper()
	body, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatalf("read fixture %s: %v", name, err)
	}
	return body
}

func specValue(specs []listing.Spec, key string) (string, bool) {
	for _, s := range specs {
		if s.Key == key {
			return s.Value, true
		}
	}
	return "", false
}

func TestParseDrom(t *testing.T) {
	const source = "https://auto.drom.ru/novosibirsk/subaru/domingo/148849580.html"
	l, err := parseDrom(loadFixture(t, "drom.html"), source)
	if err != nil {
		t.Fatalf("parseDrom: %v", err)
	}

	checks := []struct {
		name      string
		got, want string
	}{
		{"title", l.Title, "Продажа Subaru Domingo, 1986 год в Новосибирске"},
		{"price", l.Price, "50 000 ₽"},
		{"city", l.City, "Новосибирск"},
		{"source", l.SourceURL, source},
	}
	for _, c := range checks {
		if c.got != c.want {
			t.Errorf("%s = %q, want %q", c.name, c.got, c.want)
		}
	}

	if len(l.Photos) != 6 {
		t.Fatalf("len(photos) = %d, want 6", len(l.Photos))
	}
	const wantPhoto = "/photo/v2/adnZCBLHCDhtYuDAqAl6tu7__bfZkBQa30h7i36890KdCGN9hK3SFDAZrAMB-a8M9wP-6GbqXNPzuBk2/gen1200.jpg"
	if !strings.Contains(l.Photos[0], wantPhoto) {
		t.Errorf("photos[0] = %q, want it to contain %q", l.Photos[0], wantPhoto)
	}

	wantSpecs := []listing.Spec{
		{Key: "Год выпуска", Value: "1986"},
		{Key: "Двигатель", Value: "бензин, 1.2 л"},
		{Key: "Мощность", Value: "52 л.с."},
		{Key: "Привод", Value: "4WD"},
	}
	for _, want := range wantSpecs {
		got, ok := specValue(l.Specs, want.Key)
		if !ok {
			t.Errorf("spec %q missing in %v", want.Key, l.Specs)
			continue
		}
		if got != want.Value {
			t.Errorf("spec %q = %q, want %q", want.Key, got, want.Value)
		}
	}
	if len(l.Specs) > 10 {
		t.Errorf("len(specs) = %d, want <= 10", len(l.Specs))
	}

	if !strings.Contains(l.Description, "subaru domingo 1986") {
		t.Errorf("description = %q, want it to contain the ad text", l.Description)
	}
}

func TestParseDromNoData(t *testing.T) {
	_, err := parseDrom([]byte("<html><body><h2>пусто</h2></body></html>"), "https://auto.drom.ru/a/b/c/12345.html")
	if !errors.Is(err, ErrNoData) {
		t.Fatalf("err = %v, want ErrNoData", err)
	}
}
