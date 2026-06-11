package browser

import (
	"context"
	"fmt"

	"github.com/chromedp/cdproto/page"
	"github.com/chromedp/chromedp"
)

const stealthScript = `(() => {
  Object.defineProperty(navigator, 'webdriver', { get: () => false, configurable: true });
  Object.defineProperty(navigator, 'languages', { get: () => ['zh-CN', 'zh'], configurable: true });
  Object.defineProperty(navigator, 'plugins', { get: () => [1,2,3,4,5], configurable: true });
  window.navigator.chrome = window.navigator.chrome || { runtime: {} };
})();`

func installStealthActions() []chromedp.Action {
	return []chromedp.Action{
		chromedp.ActionFunc(func(ctx context.Context) error {
			_, err := page.AddScriptToEvaluateOnNewDocument(stealthScript).Do(ctx)
			return err
		}),
		chromedp.Evaluate(stealthScript, nil),
	}
}

func windowSize(width, height int) string {
	return fmt.Sprintf("%d,%d", width, height)
}
