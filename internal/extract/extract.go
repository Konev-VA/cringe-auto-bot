// Package extract достаёт данные объявления с площадок.
package extract

import (
	"context"
	"errors"
	"net/url"

	"autobot/internal/listing"
)

var (
	// ErrBlocked — площадка отдала защиту вместо страницы.
	ErrBlocked = errors.New("extract: source blocked")
	// ErrNoData — страница загрузилась, но данных объявления на ней нет.
	ErrNoData = errors.New("extract: no listing data")
)

// Extractor получает объявление по нормализованному URL.
type Extractor interface {
	Extract(ctx context.Context, u *url.URL) (*listing.Listing, error)
}
