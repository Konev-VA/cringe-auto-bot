package extract

import (
	"net/http"
	"net/url"
	"time"
)

// NewHTTPClient собирает клиент для площадок. Если задан прокси (AVITO_PROXY),
// через него пойдёт весь исходящий трафик площадок: avito.ru блокирует
// зарубежные адреса, а baza.drom.ru отдаёт проверку браузера на «грязные».
func NewHTTPClient(proxy string, timeout time.Duration) *http.Client {
	transport := &http.Transport{
		Proxy:               http.ProxyFromEnvironment,
		MaxIdleConns:        10,
		MaxIdleConnsPerHost: 4,
		IdleConnTimeout:     90 * time.Second,
	}
	if proxy != "" {
		if u, err := url.Parse(proxy); err == nil {
			transport.Proxy = http.ProxyURL(u)
		}
	}
	return &http.Client{Timeout: timeout, Transport: transport}
}
