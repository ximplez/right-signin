package browser

import (
	"context"
	"fmt"

	"github.com/chromedp/cdproto/page"
	"github.com/chromedp/chromedp"
)

const stealthScript = `(() => {
  const override = (obj, prop, getter) => {
    try {
      Object.defineProperty(obj, prop, { get: getter, configurable: true });
    } catch (_) {}
  };

  override(Navigator.prototype, 'webdriver', () => undefined);
  override(Navigator.prototype, 'languages', () => ['zh-CN', 'zh', 'en-US', 'en']);
  override(Navigator.prototype, 'language', () => 'zh-CN');
  override(Navigator.prototype, 'platform', () => 'MacIntel');
  override(Navigator.prototype, 'vendor', () => 'Google Inc.');
  override(Navigator.prototype, 'hardwareConcurrency', () => 8);
  override(Navigator.prototype, 'deviceMemory', () => 8);
  override(Navigator.prototype, 'maxTouchPoints', () => 0);
  override(Navigator.prototype, 'plugins', () => [1, 2, 3, 4, 5]);

  window.chrome = window.chrome || {};
  window.chrome.app = window.chrome.app || { isInstalled: false };
  window.chrome.runtime = window.chrome.runtime || {};
  window.chrome.csi = window.chrome.csi || (() => ({}));
  window.chrome.loadTimes = window.chrome.loadTimes || (() => ({}));

  const originalQuery = window.navigator.permissions && window.navigator.permissions.query;
  if (originalQuery) {
    window.navigator.permissions.query = (parameters) => (
      parameters && parameters.name === 'notifications'
        ? Promise.resolve({ state: Notification.permission })
        : originalQuery(parameters)
    );
  }

  override(Document.prototype, 'hidden', () => false);
  override(Document.prototype, 'visibilityState', () => 'visible');
  document.hasFocus = () => true;

  try {
    if (window.screen) {
      override(window.screen, 'availWidth', () => window.screen.width);
      override(window.screen, 'availHeight', () => window.screen.height);
    }
  } catch (_) {}

  const getParameter = typeof WebGLRenderingContext !== 'undefined' && WebGLRenderingContext.prototype && WebGLRenderingContext.prototype.getParameter;
  if (getParameter) {
    WebGLRenderingContext.prototype.getParameter = function(parameter) {
      if (parameter === 37445) return 'Intel Inc.';
      if (parameter === 37446) return 'Intel Iris OpenGL Engine';
      return getParameter.call(this, parameter);
    };
  }

  const getParameter2 = typeof WebGL2RenderingContext !== 'undefined' && WebGL2RenderingContext.prototype && WebGL2RenderingContext.prototype.getParameter;
  if (getParameter2) {
    WebGL2RenderingContext.prototype.getParameter = function(parameter) {
      if (parameter === 37445) return 'Intel Inc.';
      if (parameter === 37446) return 'Intel Iris OpenGL Engine';
      return getParameter2.call(this, parameter);
    };
  }

  try {
    const outerWidth = Math.max(window.innerWidth || 1365, 1365);
    const outerHeight = Math.max(window.innerHeight || 900, 900);
    override(window, 'outerWidth', () => outerWidth);
    override(window, 'outerHeight', () => outerHeight);
  } catch (_) {}
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
