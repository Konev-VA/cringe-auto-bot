// Command bot слушает чаты и отвечает карточками объявлений drom.ru и avito.ru.
package main

import (
	"context"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/mymmrac/telego"
	"github.com/mymmrac/telego/telegohandler"

	"autobot/internal/config"
	"autobot/internal/extract"
	"autobot/internal/handler"
	"autobot/internal/listing"
	"autobot/internal/tgnet"
)

const (
	// Короткий long polling: на этом канале запросы обрываются, а Telegram
	// держит оборванный запрос до истечения таймаута. С длинным опросом
	// следующий getUpdates приходит к «зависшему» и получает 409 Conflict.
	pollTimeout = 30 * time.Second
	// Короткая пауза перед повтором: 409 значит, что предыдущий запрос уже
	// снят сервером, и следующий сразу проходит. С паузой в 8–15 с бот в это
	// время просто не слышит чат.
	pollRetryTimeout = 2 * time.Second
	shutdownTimeout  = 10 * time.Second
	// photoTimeout — предел на скачивание одного снимка перед отправкой.
	photoTimeout = 20 * time.Second
)

func main() {
	if err := run(); err != nil {
		slog.Error("bot stopped with error", "error", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}

	log := newLogger(cfg.LogLevel)
	slog.SetDefault(log)

	bot, err := telego.NewBot(cfg.BotToken,
		telego.WithDefaultLogger(false, true),
		telego.WithHTTPClient(&http.Client{Transport: tgnet.NewTransport(cfg.TelegramIPs)}),
	)
	if err != nil {
		return err
	}

	// Проверка связи на старте: в блокирующих сетях иначе видно только
	// бесконечные ошибки long polling.
	probeCtx, probeCancel := context.WithTimeout(context.Background(), 20*time.Second)
	me, err := bot.GetMe(probeCtx)
	probeCancel()
	if err != nil {
		log.Error("telegram unreachable", "error", tgnet.RedactToken(err.Error(), cfg.BotToken), "api_ips", cfg.TelegramIPs)
	} else {
		log.Info("telegram connected", "bot", "@"+me.Username)
	}

	client := extract.NewHTTPClient(cfg.AvitoProxy, cfg.FetchTimeout)
	browser := extract.NewBrowser(cfg.ChromePath, cfg.AvitoProxy, cfg.ChromeHeadless)
	defer browser.Close()

	h := handler.New(bot, map[listing.Site]extract.Extractor{
		listing.SiteDrom:     extract.NewDrom(client),
		listing.SiteDromBaza: extract.NewBaza(client, browser),
		listing.SiteAvito:    extract.NewAvito(browser),
	}, &http.Client{Timeout: photoTimeout}, cfg.FetchTimeout, log)

	pollCtx, pollCancel := context.WithCancel(context.Background())
	defer pollCancel()

	updates, err := bot.UpdatesViaLongPolling(pollCtx, &telego.GetUpdatesParams{
		Timeout:        int(pollTimeout.Seconds()),
		AllowedUpdates: []string{telego.MessageUpdates},
	}, telego.WithLongPollingRetryTimeout(pollRetryTimeout))
	if err != nil {
		return err
	}

	bh, err := telegohandler.NewBotHandler(bot, updates, telegohandler.WithErrorHandler(
		func(_ *telegohandler.Context, update telego.Update, err error) {
			log.Error("handler failed", "update_id", update.UpdateID, "error", tgnet.RedactToken(err.Error(), cfg.BotToken))
		},
	))
	if err != nil {
		return err
	}

	bh.HandleMessage(h.HandleMessage)

	signalCtx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	startErr := make(chan error, 1)
	go func() { startErr <- bh.Start() }()
	log.Info("bot started",
		"chrome_path", cfg.ChromePath,
		"chrome_headless", cfg.ChromeHeadless,
		"avito_proxy", cfg.AvitoProxy != "",
		"fetch_timeout", cfg.FetchTimeout.String(),
		"api_ips", cfg.TelegramIPs,
	)

	select {
	case err := <-startErr:
		return err
	case <-signalCtx.Done():
	}

	log.Info("shutdown requested")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()

	stopErr := bh.StopWithContext(shutdownCtx)
	pollCancel()
	if stopErr != nil {
		log.Error("stop bot handler", "error", stopErr)
	}
	log.Info("bot stopped")
	return nil
}

func newLogger(level string) *slog.Logger {
	var lvl slog.Level
	if err := lvl.UnmarshalText([]byte(level)); err != nil {
		lvl = slog.LevelInfo
	}
	return slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: lvl}))
}
