package browser

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"math/rand/v2"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/chromedp/cdproto/network"
	"github.com/chromedp/chromedp"

	"right-signin/internal/config"
)

type Session struct {
	cfg         *config.Config
	ctx         context.Context
	allocCancel context.CancelFunc
	browserStop context.CancelFunc
}

type DOMRect struct {
	X       float64 `json:"x"`
	Y       float64 `json:"y"`
	Width   float64 `json:"width"`
	Height  float64 `json:"height"`
	ScrollX float64 `json:"scrollX"`
	ScrollY float64 `json:"scrollY"`
	DPR     float64 `json:"dpr"`
}

func New(parent context.Context, cfg *config.Config) (*Session, error) {
	headlessMode := any(false)
	if !cfg.Debug {
		headlessMode = "new"
	}
	allocOpts := append(chromedp.DefaultExecAllocatorOptions[:],
		chromedp.Flag("headless", headlessMode),
		chromedp.Flag("disable-gpu", true),
		chromedp.Flag("no-sandbox", true),
		chromedp.Flag("disable-dev-shm-usage", true),
		chromedp.Flag("disable-blink-features", "AutomationControlled"),
		chromedp.Flag("enable-automation", false),
		chromedp.Flag("useAutomationExtension", false),
		chromedp.Flag("disable-background-timer-throttling", true),
		chromedp.Flag("disable-backgrounding-occluded-windows", true),
		chromedp.Flag("disable-renderer-backgrounding", true),
		chromedp.Flag("password-store", "basic"),
		chromedp.Flag("mute-audio", true),
		chromedp.Flag("lang", cfg.BrowserLang),
		chromedp.Flag("window-size", windowSize(cfg.WindowWidth, cfg.WindowHeight)),
		chromedp.UserAgent(cfg.UserAgent),
		chromedp.UserDataDir(cfg.UserDataDir),
	)
	allocCtx, allocCancel := chromedp.NewExecAllocator(parent, allocOpts...)
	browserCtx, browserCancel := chromedp.NewContext(allocCtx, chromedp.WithLogf(log.Printf))

	s := &Session{cfg: cfg, ctx: browserCtx, allocCancel: allocCancel, browserStop: browserCancel}
	actions := []chromedp.Action{network.Enable()}
	actions = append(actions, installStealthActions()...)
	if err := chromedp.Run(browserCtx, actions...); err != nil {
		browserCancel()
		allocCancel()
		return nil, fmt.Errorf("初始化浏览器失败: %w", err)
	}
	return s, nil
}

func (s *Session) Close() {
	if s.browserStop != nil {
		s.browserStop()
	}
	if s.allocCancel != nil {
		s.allocCancel()
	}
}

func (s *Session) Ctx() context.Context { return s.ctx }

func (s *Session) Run(actions ...chromedp.Action) error {
	ctx, cancel := context.WithTimeout(s.ctx, s.cfg.PageTimeout)
	defer cancel()
	return chromedp.Run(ctx, actions...)
}

func (s *Session) Navigate(rawURL string) error {
	var lastErr error
	for attempt := 0; attempt <= s.cfg.NavigationRetries; attempt++ {
		ctx, cancel := context.WithTimeout(s.ctx, s.cfg.PageTimeout)
		lastErr = chromedp.Run(ctx,
			chromedp.Navigate(rawURL),
			chromedp.WaitReady("body", chromedp.ByQuery),
			chromedp.Sleep(1200*time.Millisecond),
		)
		cancel()
		if lastErr == nil {
			return nil
		}
		_ = s.SleepRandom(400*time.Millisecond, 900*time.Millisecond)
	}
	return fmt.Errorf("导航失败: %w", lastErr)
}

func (s *Session) SleepRandom(min, max time.Duration) error {
	if max <= min {
		return chromedp.Sleep(min).Do(s.ctx)
	}
	delta := max - min
	return chromedp.Sleep(min + time.Duration(rand.Int64N(int64(delta)))).Do(s.ctx)
}

func (s *Session) CurrentURL() (string, error) {
	var current string
	err := s.Run(chromedp.Evaluate(`window.location.href`, &current))
	return current, err
}

func (s *Session) Title() (string, error) {
	var title string
	err := s.Run(chromedp.Title(&title))
	return title, err
}

func (s *Session) BodyText() (string, error) {
	var body string
	err := s.Run(chromedp.Evaluate(`document.body ? (document.body.innerText || '') : ''`, &body))
	return body, err
}

func (s *Session) HTML() (string, error) {
	var html string
	err := s.Run(chromedp.Evaluate(`document.documentElement ? document.documentElement.outerHTML : ''`, &html))
	return html, err
}

func (s *Session) ElementExists(selector string) (bool, error) {
	var exists bool
	err := s.Run(chromedp.Evaluate(fmt.Sprintf(`Boolean(document.querySelector(%s))`, strconv.Quote(selector)), &exists))
	return exists, err
}

func (s *Session) Attribute(selector, attr string) (string, error) {
	var value string
	js := fmt.Sprintf(`(() => {
		const el = document.querySelector(%s);
		if (!el) return '';
		return el.getAttribute(%s) || '';
	})()`, strconv.Quote(selector), strconv.Quote(attr))
	err := s.Run(chromedp.Evaluate(js, &value))
	return strings.TrimSpace(value), err
}

func (s *Session) ElementRect(selector string) (DOMRect, error) {
	var rect DOMRect
	script := `(function(sel){
		const el = document.querySelector(sel);
		if (!el) return null;
		const r = el.getBoundingClientRect();
		return {
			x: r.x,
			y: r.y,
			width: r.width,
			height: r.height,
			scrollX: window.scrollX || 0,
			scrollY: window.scrollY || 0,
			dpr: window.devicePixelRatio || 1,
		};
	})(` + strconv.Quote(selector) + `)`
	if err := s.Run(chromedp.Evaluate(script, &rect)); err != nil {
		return DOMRect{}, err
	}
	if rect.Width <= 0 || rect.Height <= 0 {
		return DOMRect{}, fmt.Errorf("selector=%s 未命中有效元素区域", selector)
	}
	if rect.DPR <= 0 {
		rect.DPR = 1
	}
	return rect, nil
}

func (s *Session) FindFirstLink(textHints, hrefHints []string) (string, error) {
	var href string
	js := fmt.Sprintf(`(() => {
		const textHints = %s;
		const hrefHints = %s;
		const els = Array.from(document.querySelectorAll('a[href],button,[role="button"],input[type="button"],input[type="submit"]'));
		for (const el of els) {
			const text = ((el.innerText || el.value || '') + '').replace(/\s+/g, ' ').trim();
			const href = el.href || el.getAttribute('href') || '';
			const textOK = textHints.length === 0 || textHints.some(k => text.includes(k));
			const hrefOK = hrefHints.length === 0 || hrefHints.some(k => href.includes(k));
			if (textOK && hrefOK && href) return href;
		}
		return '';
	})()`, toJSArray(textHints), toJSArray(hrefHints))
	err := s.Run(chromedp.Evaluate(js, &href))
	return strings.TrimSpace(href), err
}

func (s *Session) ClickFirstByText(textHints []string) error {
	js := fmt.Sprintf(`(() => {
		const texts = %s;
		const els = Array.from(document.querySelectorAll('a,button,[role="button"],input[type="button"],input[type="submit"],span,div'));
		for (const el of els) {
			const text = ((el.innerText || el.value || '') + '').replace(/\s+/g, ' ').trim();
			if (!text) continue;
			if (texts.some(k => text.includes(k))) { el.click(); return true; }
		}
		return false;
	})()`, toJSArray(textHints))
	var ok bool
	if err := s.Run(chromedp.Evaluate(js, &ok)); err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("未找到包含指定文案的可点击元素: %v", textHints)
	}
	return nil
}

func (s *Session) ClickFirstBySelector(selectors []string) error {
	js := fmt.Sprintf(`(() => {
		const selectors = %s;
		for (const selector of selectors) {
			const el = document.querySelector(selector);
			if (el) { el.click(); return true; }
		}
		return false;
	})()`, toJSArray(selectors))
	var ok bool
	if err := s.Run(chromedp.Evaluate(js, &ok)); err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("未找到可点击 selector: %v", selectors)
	}
	return nil
}

func (s *Session) FindImageSrcByKeywords(keywords []string) (string, error) {
	var src string
	js := fmt.Sprintf(`(() => {
		const keywords = %s;
		const imgs = Array.from(document.images || []);
		for (const img of imgs) {
			const src = img.currentSrc || img.src || '';
			if (keywords.some(k => src.includes(k))) return src;
		}
		if (imgs[0] && (imgs[0].currentSrc || imgs[0].src)) return imgs[0].currentSrc || imgs[0].src;
		const canvas = document.querySelector('canvas');
		if (canvas && canvas.toDataURL) return canvas.toDataURL('image/png');
		return '';
	})()`, toJSArray(keywords))
	err := s.Run(chromedp.Evaluate(js, &src))
	return strings.TrimSpace(src), err
}

func (s *Session) ResolveURL(baseURL, target string) string {
	if target == "" {
		return ""
	}
	parsed, err := url.Parse(target)
	if err == nil && parsed.IsAbs() {
		return target
	}
	base, err := url.Parse(baseURL)
	if err != nil {
		return target
	}
	ref, err := url.Parse(target)
	if err != nil {
		return target
	}
	return base.ResolveReference(ref).String()
}

func (s *Session) TextContainsAny(keywords []string) (bool, string, error) {
	body, err := s.BodyText()
	if err != nil {
		return false, "", err
	}
	for _, kw := range keywords {
		if strings.Contains(body, kw) {
			return true, kw, nil
		}
	}
	return false, "", nil
}

func (s *Session) FindCookieNames() ([]string, error) {
	cks, err := s.FindCookies()
	if err != nil {
		return nil, err
	}
	var names []string
	for _, ck := range cks {
		names = append(names, ck.Name)
	}
	sort.Strings(names)
	return names, nil
}

func (s *Session) FindCookies() ([]*network.Cookie, error) {
	ctx, cancel := context.WithTimeout(s.ctx, s.cfg.PageTimeout)
	defer cancel()
	var cks []*network.Cookie
	if err := chromedp.Run(ctx, chromedp.ActionFunc(func(runCtx context.Context) error {
		var err error
		cks, err = network.GetCookies().Do(runCtx)
		return err
	})); err != nil {
		return nil, err
	}
	return cks, nil
}

func (s *Session) StorageKeys(kind string) ([]string, error) {
	var keys []string
	js := fmt.Sprintf(`(() => {
		const store = window[%s];
		if (!store) return [];
		const res = [];
		for (let i = 0; i < store.length; i++) {
			res.push(store.key(i));
		}
		return res;
	})()`, strconv.Quote(kind))
	if err := s.Run(chromedp.Evaluate(js, &keys)); err != nil {
		return nil, err
	}
	sort.Strings(keys)
	return keys, nil
}

type PageSummary struct {
	URL              string   `json:"url"`
	Title            string   `json:"title"`
	CookieNames      []string `json:"cookie_names,omitempty"`
	LocalStorageKeys []string `json:"local_storage_keys,omitempty"`
	SessionStoreKeys []string `json:"session_storage_keys,omitempty"`
	BodySnippet      string   `json:"body_snippet,omitempty"`
}

func (s *Session) Summary() PageSummary {
	urlVal, _ := s.CurrentURL()
	title, _ := s.Title()
	body, _ := s.BodyText()
	cookies, _ := s.FindCookieNames()
	localKeys, _ := s.StorageKeys("localStorage")
	sessionKeys, _ := s.StorageKeys("sessionStorage")
	body = NormalizeSnippet(body, 240)
	return PageSummary{
		URL:              urlVal,
		Title:            title,
		CookieNames:      cookies,
		LocalStorageKeys: localKeys,
		SessionStoreKeys: sessionKeys,
		BodySnippet:      body,
	}
}

func NormalizeSnippet(text string, max int) string {
	text = strings.Join(strings.Fields(strings.TrimSpace(text)), " ")
	if max > 0 && len(text) > max {
		return text[:max] + "..."
	}
	return text
}

func toJSArray(items []string) string {
	if items == nil {
		items = []string{}
	}
	b, _ := json.Marshal(items)
	return string(b)
}
