package extract

import (
	"context"
	"sync"

	"github.com/chromedp/chromedp"
)

// Browser — один общий Chrome на процесс: площадки, которым нужен настоящий
// браузер (avito.ru) и страницы, которые отдают проверку на обычный запрос
// (baza.drom.ru, farpost.ru), ходят в него через вкладки.
type Browser struct {
	chromePath string
	proxy      string
	headless   bool

	once    sync.Once
	alloc   context.Context
	release context.CancelFunc
}

// NewBrowser настраивает браузер. Он поднимается при первом обращении.
func NewBrowser(chromePath, proxy string, headless bool) *Browser {
	return &Browser{chromePath: chromePath, proxy: proxy, headless: headless}
}

// Close останавливает браузер и все его вкладки.
func (b *Browser) Close() {
	if b == nil {
		return
	}
	if b.release != nil {
		b.release()
	}
}

// newTab открывает новую вкладку; вызывающий обязан закрыть её отменой.
func (b *Browser) newTab() (context.Context, context.CancelFunc) {
	b.once.Do(func() {
		opts := append(chromedp.DefaultExecAllocatorOptions[:],
			chromedp.ExecPath(b.chromePath),
			chromedp.Flag("no-sandbox", true),
			chromedp.Flag("disable-dev-shm-usage", true),
			chromedp.Flag("disable-gpu", true),
			// UA не подменяем: Chrome сам присылает согласованные UA, Client
			// Hints и платформу, а подмена на Windows-строку при Linux —
			// лишний признак автоматизации.
			// Blink-фича убирает navigator.webdriver, который выставляет chromedp.
			chromedp.Flag("disable-blink-features", "AutomationControlled"),
			chromedp.Flag("exclude-switches", "enable-automation"),
			// Qrator на avito.ru сверяет язык страницы с адресом: без
			// --accept-lang navigator.languages остаётся en-US.
			chromedp.Flag("accept-lang", "ru-RU,ru"),
			chromedp.Flag("lang", "ru-RU"),
			chromedp.Flag("window-size", "1920,1080"),
		)
		// DefaultExecAllocatorOptions уже включает headless: для окна флаг
		// нужно снять целиком, а не добавлять ещё один.
		if !b.headless {
			opts = append(opts, chromedp.VisibleWindow)
		}
		if b.proxy != "" {
			opts = append(opts, chromedp.Flag("proxy-server", b.proxy))
		}
		b.alloc, b.release = chromedp.NewExecAllocator(context.Background(), opts...)
	})
	return chromedp.NewContext(b.alloc)
}
