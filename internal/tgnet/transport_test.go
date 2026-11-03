package tgnet

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// closedPort возвращает адрес на localhost, где никто не слушает.
func closedPort(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	addr := l.Addr().String()
	if err := l.Close(); err != nil {
		t.Fatalf("close listener: %v", err)
	}
	return addr
}

func TestDialFailsOverToWorkingIP(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("ok"))
	}))
	defer srv.Close()

	_, port, err := net.SplitHostPort(strings.TrimPrefix(srv.URL, "http://"))
	if err != nil {
		t.Fatalf("split: %v", err)
	}

	// Первый кандидат — закрытый порт (мгновенный отказ), второй — живой сервер.
	deadIP, _, err := net.SplitHostPort(closedPort(t))
	if err != nil {
		t.Fatalf("split dead: %v", err)
	}

	client := &http.Client{Transport: NewTransport([]string{deadIP, "127.0.0.1"})}
	resp, err := client.Get("http://api.telegram.org:" + port + "/")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	if string(body) != "ok" {
		t.Errorf("body = %q, want ok", body)
	}
}

func TestDialAllCandidatesFail(t *testing.T) {
	_, port, err := net.SplitHostPort(closedPort(t))
	if err != nil {
		t.Fatalf("split: %v", err)
	}

	client := &http.Client{Transport: NewTransport([]string{"127.0.0.1", "127.0.0.1"})}
	_, err = client.Get("http://api.telegram.org:" + port + "/")
	if err == nil {
		t.Fatal("expected error when every candidate IP is closed")
	}
	if !IsDialError(err) {
		t.Fatalf("err = %v, want dial error", err)
	}
}

type flakyTransport struct {
	failures int32
	calls    atomic.Int32
	seen     []string
}

func (f *flakyTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	f.calls.Add(1)
	if body, err := io.ReadAll(req.Body); err == nil {
		f.seen = append(f.seen, string(body))
		_ = req.Body.Close()
	}
	if f.calls.Load() <= f.failures {
		return nil, &net.OpError{Op: "dial", Net: "tcp", Err: errors.New("connection refused")}
	}
	return &http.Response{
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(strings.NewReader("ok")),
		Header:     make(http.Header),
		Request:    req,
	}, nil
}

func TestRedactToken(t *testing.T) {
	const token = "123456:AAtest"
	msg := "getMe: Post \"https://api.telegram.org/bot" + token + "/getMe\": timeout"

	got := RedactToken(msg, token)
	if strings.Contains(got, token) {
		t.Fatalf("token leaked: %q", got)
	}
	if !strings.Contains(got, "api.telegram.org/bot<token>/getMe") {
		t.Errorf("unexpected redaction: %q", got)
	}
	if got := RedactToken(msg, ""); got != msg {
		t.Errorf("empty token must not change text: %q", got)
	}
}

func TestRetryOnDialErrorResendsBody(t *testing.T) {
	base := &flakyTransport{failures: 2}
	transport := &retryTransport{base: base}

	req, err := http.NewRequest(http.MethodPost, "http://api.telegram.org/bot/sendMediaGroup", strings.NewReader(`{"media":[]}`))
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	resp, err := transport.RoundTrip(req)
	if err != nil {
		t.Fatalf("round trip: %v", err)
	}
	defer resp.Body.Close()

	if got := base.calls.Load(); got != 3 {
		t.Errorf("attempts = %d, want 3", got)
	}
	for i, body := range base.seen {
		if body != `{"media":[]}` {
			t.Errorf("attempt %d sent body %q, want original payload", i+1, body)
		}
	}
}

func TestNoRetryForNonDialErrors(t *testing.T) {
	base := &errTransport{err: io.ErrUnexpectedEOF}
	transport := &retryTransport{base: base}

	req, _ := http.NewRequest(http.MethodPost, "http://api.telegram.org/bot/sendMessage", strings.NewReader("{}"))
	if _, err := transport.RoundTrip(req); err == nil {
		t.Fatal("expected error")
	}
	if got := base.calls.Load(); got != 1 {
		t.Errorf("attempts = %d, want 1 (не-dial ошибки не повторяются)", got)
	}
}

func TestNoRetryWithoutBodyReplay(t *testing.T) {
	base := &errTransport{err: &net.OpError{Op: "dial", Err: errors.New("refused")}}
	transport := &retryTransport{base: base}

	body := io.NopCloser(strings.NewReader("{}"))
	req := &http.Request{Method: http.MethodPost, URL: mustURL(t), Body: body, Header: make(http.Header)}
	if _, err := transport.RoundTrip(req); err == nil {
		t.Fatal("expected error")
	}
	if got := base.calls.Load(); got != 1 {
		t.Errorf("attempts = %d, want 1 (тело нельзя переиграть)", got)
	}
}

func TestRetryStopsOnContextCancel(t *testing.T) {
	base := &errTransport{err: &net.OpError{Op: "dial", Err: errors.New("refused")}}
	transport := &retryTransport{base: base}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, "http://api.telegram.org/bot/sendMessage", strings.NewReader("{}"))

	_, err := transport.RoundTrip(req)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	if got := base.calls.Load(); got > 2 {
		t.Errorf("attempts = %d, want retries stopped by cancel", got)
	}
}

type errTransport struct {
	err   error
	calls atomic.Int32
}

func (e *errTransport) RoundTrip(*http.Request) (*http.Response, error) {
	e.calls.Add(1)
	time.Sleep(5 * time.Millisecond)
	return nil, e.err
}

func mustURL(t *testing.T) *url.URL {
	t.Helper()
	u, err := url.Parse("http://api.telegram.org/bot/sendMessage")
	if err != nil {
		t.Fatalf("parse url: %v", err)
	}
	return u
}
