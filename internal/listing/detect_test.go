package listing

import (
	"errors"
	"testing"

	"github.com/mymmrac/telego"
)

func TestDetect(t *testing.T) {
	tests := []struct {
		name    string
		raw     string
		site    Site
		norm    string
		wantErr bool
	}{
		{
			name: "drom listing",
			raw:  "https://auto.drom.ru/novosibirsk/subaru/domingo/148849580.html",
			site: SiteDrom,
			norm: "https://auto.drom.ru/novosibirsk/subaru/domingo/148849580.html",
		},
		{
			name: "avito mobile with query",
			raw:  "https://m.avito.ru/novosibirsk/avtomobili/toyota_carina_1.6_mt_1982_190_000_km_8470651557?context=H4sIAAAA",
			site: SiteAvito,
			norm: "https://avito.ru/novosibirsk/avtomobili/toyota_carina_1.6_mt_1982_190_000_km_8470651557?context=H4sIAAAA",
		},
		{
			name: "avito www and http and fragment",
			raw:  "http://www.avito.ru/novosibirsk/avtomobili/toyota_carina_1.6_mt_1982_190_000_km_8470651557#gallery",
			site: SiteAvito,
			norm: "https://avito.ru/novosibirsk/avtomobili/toyota_carina_1.6_mt_1982_190_000_km_8470651557",
		},
		{
			name: "avito phones is an ad too",
			raw:  "https://www.avito.ru/novosibirsk/telefony/iphone_14_128gb_1234567890",
			site: SiteAvito,
			norm: "https://avito.ru/novosibirsk/telefony/iphone_14_128gb_1234567890",
		},
		{
			name: "avito parts with utm",
			raw:  "https://www.avito.ru/kozelsk/zapchasti_i_aksessuary/litye_diski_watanabe_rs_6r14_4100_8503704612?utm_campaign=native&utm_medium=item_page_android&utm_source=soc_sharing",
			site: SiteAvito,
			norm: "https://avito.ru/kozelsk/zapchasti_i_aksessuary/litye_diski_watanabe_rs_6r14_4100_8503704612?utm_campaign=native&utm_medium=item_page_android&utm_source=soc_sharing",
		},
		{
			name:    "avito category page is not an ad",
			raw:     "https://www.avito.ru/moskva/zapchasti_i_aksessuary",
			wantErr: true,
		},
		{name: "avito root", raw: "https://avito.ru/", wantErr: true},
		{name: "drom without id", raw: "https://auto.drom.ru/novosibirsk/subaru/", wantErr: true},
		{name: "other site", raw: "https://www.youtube.com/watch?v=1", wantErr: true},
		{name: "not a url", raw: "смотри объявление", wantErr: true},
		{name: "ftp scheme", raw: "ftp://auto.drom.ru/novosibirsk/subaru/domingo/148849580.html", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			site, u, err := Detect(tt.raw)
			if tt.wantErr {
				if !errors.Is(err, ErrNotListing) {
					t.Fatalf("Detect(%q) error = %v, want ErrNotListing", tt.raw, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("Detect(%q) unexpected error: %v", tt.raw, err)
			}
			if site != tt.site {
				t.Errorf("site = %q, want %q", site, tt.site)
			}
			if got := u.String(); got != tt.norm {
				t.Errorf("normalized = %q, want %q", got, tt.norm)
			}
		})
	}
}

func TestExtractURLsFromEntities(t *testing.T) {
	const link = "https://auto.drom.ru/novosibirsk/subaru/domingo/148849580.html"
	// "🔥" занимает две единицы UTF-16, поэтому URL начинается со смещения 10,
	// а не 9 (как было бы при подсчёте рун).
	const offset = 10
	text := "🔥 смотри " + link

	got := ExtractURLs(text, []telego.MessageEntity{
		{Type: telego.EntityTypeURL, Offset: offset, Length: len(link)},
	}, 3)

	if len(got) != 1 {
		t.Fatalf("got %d urls %q, want 1", len(got), got)
	}
	if got[0] != link {
		t.Errorf("url = %q, want %q", got[0], link)
	}
}

func TestExtractURLsFromTextLink(t *testing.T) {
	const link = "https://avito.ru/novosibirsk/avtomobili/toyota_carina_1.6_mt_1982_190_000_km_8470651557"
	text := "вот объявление"

	got := ExtractURLs(text, []telego.MessageEntity{
		{Type: telego.EntityTypeTextLink, Offset: 4, Length: 10, URL: link},
	}, 3)
	if len(got) != 1 || got[0] != link {
		t.Fatalf("with url: got %q, want [%q]", got, link)
	}

	got = ExtractURLs(text, []telego.MessageEntity{
		{Type: telego.EntityTypeTextLink, Offset: 4, Length: 10},
	}, 3)
	if len(got) != 0 {
		t.Fatalf("without url: got %q, want none", got)
	}
}

func TestExtractURLsDoesNotDuplicate(t *testing.T) {
	text := "https://auto.drom.ru/novosibirsk/subaru/domingo/148849580.html и https://m.avito.ru/novosibirsk/avtomobili/toyota_carina_1.6_mt_1982_190_000_km_8470651557"
	got := ExtractURLs(text, nil, 3)
	if len(got) != 2 {
		t.Fatalf("got %q, want 2 urls", got)
	}
}

func TestExtractURLsPlainText(t *testing.T) {
	got := ExtractURLs("смотри https://auto.drom.ru/novosibirsk/subaru/domingo/148849580.html.", nil, 3)
	if len(got) != 1 {
		t.Fatalf("got %q, want 1 url without trailing dot", got)
	}
	const want = "https://auto.drom.ru/novosibirsk/subaru/domingo/148849580.html"
	if got[0] != want {
		t.Errorf("url = %q, want %q", got[0], want)
	}
}

func TestExtractURLsLimit(t *testing.T) {
	text := "https://a.drom.ru/1/x/y/12345.html https://b.drom.ru/1/x/y/12346.html https://c.drom.ru/1/x/y/12347.html https://d.drom.ru/1/x/y/12348.html"
	got := ExtractURLs(text, nil, 3)
	if len(got) != 3 {
		t.Fatalf("got %d urls, want 3", len(got))
	}
}

func TestOrderSpecs(t *testing.T) {
	specs := []Spec{
		{Key: "Цвет", Value: "серый"},
		{Key: "Привод", Value: "4WD"},
		{Key: "Неизвестно", Value: "x"},
		{Key: "Год выпуска", Value: "1986"},
	}
	got := OrderSpecs(specs, []string{"Год выпуска", "Привод"})
	want := []Spec{
		{Key: "Год выпуска", Value: "1986"},
		{Key: "Привод", Value: "4WD"},
		{Key: "Цвет", Value: "серый"},
		{Key: "Неизвестно", Value: "x"},
	}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("specs[%d] = %v, want %v", i, got[i], want[i])
		}
	}
}

func TestTrimSpecsKeepsRequired(t *testing.T) {
	specs := make([]Spec, 0, 10)
	for i := range 10 {
		specs = append(specs, Spec{Key: "K" + string(rune('0'+i))})
	}
	specs[8] = Spec{Key: "Мощность", Value: "52 л.с."}

	got := TrimSpecs(specs, 3, []string{"Год выпуска", "Мощность"})
	if len(got) != 3 {
		t.Fatalf("got %d specs, want 3", len(got))
	}
	found := false
	for _, s := range got {
		if s.Key == "Мощность" {
			found = true
		}
	}
	if !found {
		t.Errorf("Мощность dropped: %v", got)
	}
}
