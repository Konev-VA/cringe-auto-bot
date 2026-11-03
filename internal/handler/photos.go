package handler

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/mymmrac/telego"
)

const (
	// maxPhotoBytes — предел на один снимок; страницы Drom/Avito отдают ~0.2–0.5 МБ.
	maxPhotoBytes = 12 << 20
	// photoDownloads — сколько снимков качаем параллельно.
	photoDownloads = 4
	// photoTimeout — предел на скачивание одного снимка по умолчанию.
	photoTimeout   = 20 * time.Second
	photoUserAgent = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36"
)

// photoFile — скачанный снимок: имя нужно Telegram для multipart-загрузки.
type photoFile struct {
	name string
	data []byte
	off  int
}

func (p *photoFile) Read(b []byte) (int, error) {
	if p.off >= len(p.data) {
		return 0, io.EOF
	}
	n := copy(b, p.data[p.off:])
	p.off += n
	return n, nil
}

func (p *photoFile) Name() string { return p.name }

// inputFile отдаёт свежий reader: тело запроса вычитывается целиком, а на
// повторной попытке отправки нужен новый.
func (p *photoFile) inputFile() telego.InputFile {
	return telego.InputFile{File: &photoFile{name: p.name, data: p.data}}
}

// downloadPhotos скачивает снимки по ссылкам, сохраняя порядок. Сбой отдельного
// снимка не фатален: он просто выпадает из альбома.
//
// Загружать байты, а не отдавать Telegram ссылки, приходится из-за
// WEBPAGE_CURL_FAILED: серверы Telegram не могут вытянуть снимок с CDN
// объявлений, и без этого падает вся отправка.
func (h *Handler) downloadPhotos(ctx context.Context, urls []string) []*photoFile {
	files := make([]*photoFile, len(urls))

	var wg sync.WaitGroup
	sem := make(chan struct{}, photoDownloads)
	for i, raw := range urls {
		wg.Add(1)
		sem <- struct{}{}
		go func() {
			defer wg.Done()
			defer func() { <-sem }()

			file, err := h.fetchPhoto(ctx, raw, i)
			if err != nil {
				h.log.Warn("photo download failed", "url", raw, "error", h.redact(err))
				return
			}
			files[i] = file
		}()
	}
	wg.Wait()

	kept := make([]*photoFile, 0, len(files))
	for _, f := range files {
		if f != nil {
			kept = append(kept, f)
		}
	}
	return kept
}

func (h *Handler) fetchPhoto(ctx context.Context, raw string, idx int) (*photoFile, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, raw, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", photoUserAgent)
	req.Header.Set("Accept", "image/*,*/*;q=0.8")

	resp, err := h.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("photo %s: status %d", raw, resp.StatusCode)
	}

	data, err := io.ReadAll(io.LimitReader(resp.Body, maxPhotoBytes+1))
	if err != nil {
		return nil, err
	}
	if len(data) == 0 {
		return nil, fmt.Errorf("photo %s: empty body", raw)
	}
	if len(data) > maxPhotoBytes {
		return nil, fmt.Errorf("photo %s: larger than %d bytes", raw, maxPhotoBytes)
	}
	if kind := http.DetectContentType(data); !strings.HasPrefix(kind, "image/") {
		return nil, fmt.Errorf("photo %s: not an image (%s)", raw, kind)
	}

	return &photoFile{name: fmt.Sprintf("photo-%d.jpg", idx+1), data: data}, nil
}
