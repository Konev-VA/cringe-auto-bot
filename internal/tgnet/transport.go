// Package tgnet собирает HTTP-транспорт для Bot API: обход заблокированных
// DNS-адресов Telegram и повторные попытки, когда соединение срывается.
package tgnet

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strings"
	"time"
)

const (
	dialTimeout = 4 * time.Second
	attempts    = 3
	backoff     = 150 * time.Millisecond
)

// NewTransport возвращает транспорт для http.Client, который:
//   - соединяется с api.telegram.org по перечисленным IP по очереди (если список
//     непустой), потому что DNS в блокирующих сетях отдаёт мёртвые адреса;
//   - повторяет запрос, если TCP-соединение не установилось (в блокирующих
//     сетях такие обрывы идут пачками).
//
// Повтор безопасен: запрос на этом этапе ещё не ушёл на сервер.
func NewTransport(ips []string) http.RoundTripper {
	dial := &net.Dialer{Timeout: dialTimeout, KeepAlive: 30 * time.Second}
	return &retryTransport{
		base: &http.Transport{
			Proxy:                 http.ProxyFromEnvironment,
			DialContext:           dialFunc(dial, ips),
			ForceAttemptHTTP2:     true,
			MaxIdleConns:          10,
			IdleConnTimeout:       90 * time.Second,
			TLSHandshakeTimeout:   dialTimeout,
			ExpectContinueTimeout: time.Second,
		},
	}
}

// dialFunc перебирает IP из списка, сохраняя исходный адрес и порт из запроса.
func dialFunc(dial *net.Dialer, ips []string) func(ctx context.Context, network, addr string) (net.Conn, error) {
	return func(ctx context.Context, network, addr string) (net.Conn, error) {
		if len(ips) == 0 {
			return dial.DialContext(ctx, network, addr)
		}
		_, port, err := net.SplitHostPort(addr)
		if err != nil {
			return nil, fmt.Errorf("tgnet: %w", err)
		}

		var lastErr error
		for _, ip := range ips {
			conn, err := dial.DialContext(ctx, network, net.JoinHostPort(ip, port))
			if err == nil {
				return conn, nil
			}
			lastErr = err
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
		}
		return nil, fmt.Errorf("tgnet: dial %s: all %d candidate IPs failed: %w", addr, len(ips), lastErr)
	}
}

type retryTransport struct {
	base http.RoundTripper
}

func (t *retryTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	for attempt := 1; ; attempt++ {
		resp, err := t.base.RoundTrip(req)
		if err == nil || attempt >= attempts || req.GetBody == nil || !IsDialError(err) {
			return resp, err
		}

		body, bodyErr := req.GetBody()
		if bodyErr != nil {
			return resp, err
		}
		retry := req.Clone(req.Context())
		retry.Body = body
		req = retry

		select {
		case <-req.Context().Done():
			return nil, req.Context().Err()
		case <-time.After(backoff * time.Duration(attempt)):
		}
	}
}

// RedactToken убирает токен бота из текста: telego вкладывает в ошибку URL
// запроса вида https://api.telegram.org/bot<token>/..., и он попадал бы в логи.
func RedactToken(text, token string) string {
	if token == "" {
		return text
	}
	return strings.ReplaceAll(text, token, "<token>")
}

// IsDialError сообщает, что запрос не дошёл до сервера: не удалось открыть
// TCP-соединение. Только такие ошибки можно повторять.
func IsDialError(err error) bool {
	var opErr *net.OpError
	return errors.As(err, &opErr) && opErr.Op == "dial"
}
