package login

import (
	"context"
	"fmt"
	"log"
	"net/url"
	"strings"

	"right-signin/internal/browser"
	"right-signin/internal/notify"
)

func (s *QQAuthenticator) sendQRCode(ctx context.Context, qrInfo QRCodeInfo, refreshCount int) error {
	if qrInfo.Raw == "" && qrInfo.PageShot == "" {
		return nil
	}
	targetURL := firstNonEmpty(qrInfo.ViewerURL, qrInfo.PreviewURL)
	title := notify.BoldText(notify.BlueText("QQ 登录二维码"))
	if refreshCount > 0 {
		title = notify.BoldText(notify.OrangeText("QQ 登录二维码已刷新"))
	}
	statusLine := notify.GreenText("请直接点击消息 URL 扫码；如后续收到刷新消息，请始终以最新一条为准。")
	if targetURL == "" {
		statusLine = notify.RedText("当前尚未生成可访问 URL，请等待下一条刷新通知。")
	}
	msg := notify.Message{
		App:   s.cfg.AppName,
		Title: title,
		Msg: strings.Join([]string{
			fmt.Sprintf("%s %s", notify.BoldText("运行ID:"), s.cfg.RunID),
			fmt.Sprintf("%s %s", notify.BoldText("阶段:"), notify.PurpleText("QQ 登录")),
			fmt.Sprintf("%s %d", notify.BoldText("刷新次数:"), refreshCount),
			statusLine,
		}, "\n"),
		TargetURL: targetURL,
	}
	if err := s.notifier.Notify(ctx, msg); err != nil {
		return err
	}
	log.Printf("已发送二维码通知: refresh=%d kind=%s viewerURL=%s oauth=%s preview=%s", refreshCount, qrInfo.Kind, browser.NormalizeSnippet(targetURL, 180), browser.NormalizeSnippet(qrInfo.CurrentURL, 180), browser.NormalizeSnippet(qrInfo.PreviewURL, 180))
	return nil
}

func shouldNotifyQRCodeChange(prev, next QRCodeInfo) (bool, string) {
	prevDisplayAnchor := firstNonEmpty(strings.TrimSpace(prev.DisplayHash), strings.TrimSpace(prev.ImageHash))
	nextDisplayAnchor := firstNonEmpty(strings.TrimSpace(next.DisplayHash), strings.TrimSpace(next.ImageHash))
	if prevDisplayAnchor != "" && nextDisplayAnchor != "" && prevDisplayAnchor != nextDisplayAnchor {
		return true, fmt.Sprintf("二维码图片内容发生变更: %s -> %s", browser.NormalizeSnippet(prevDisplayAnchor, 64), browser.NormalizeSnippet(nextDisplayAnchor, 64))
	}
	prevPageAnchor := normalizeQRCodeChangeURL(prev.CurrentURL)
	nextPageAnchor := normalizeQRCodeChangeURL(next.CurrentURL)
	if prevPageAnchor != nextPageAnchor {
		return true, fmt.Sprintf("QQ 登录页 URL 发生变更: %s -> %s", browser.NormalizeSnippet(prevPageAnchor, 180), browser.NormalizeSnippet(nextPageAnchor, 180))
	}
	prevIframeAnchor := firstNonEmpty(normalizeQRCodeChangeURL(prev.IframeURL), strings.TrimSpace(prev.IframeSrc))
	nextIframeAnchor := firstNonEmpty(normalizeQRCodeChangeURL(next.IframeURL), strings.TrimSpace(next.IframeSrc))
	if prevIframeAnchor != nextIframeAnchor {
		return true, fmt.Sprintf("QQ 登录 iframe 链接发生变更: %s -> %s", browser.NormalizeSnippet(prevIframeAnchor, 180), browser.NormalizeSnippet(nextIframeAnchor, 180))
	}
	prevRawAnchor := normalizeQRCodeRawAnchor(prev.Raw)
	nextRawAnchor := normalizeQRCodeRawAnchor(next.Raw)
	if prevRawAnchor != "" && nextRawAnchor != "" && prevRawAnchor != nextRawAnchor {
		return true, fmt.Sprintf("二维码图片地址发生变更: %s -> %s", browser.NormalizeSnippet(prevRawAnchor, 180), browser.NormalizeSnippet(nextRawAnchor, 180))
	}
	return false, "二维码图片内容、QQ 登录页 URL、iframe 链接及二维码图片地址均未变化"
}

func normalizeQRCodeChangeURL(rawURL string) string {
	return normalizeURLForComparison(rawURL, nil)
}

func normalizeQRCodeRawAnchor(rawURL string) string {
	if strings.HasPrefix(strings.TrimSpace(rawURL), "data:image/") {
		return strings.TrimSpace(rawURL)
	}
	return normalizeURLForComparison(rawURL, map[string]struct{}{
		"_":      {},
		"r":      {},
		"rand":   {},
		"random": {},
		"rd":     {},
		"t":      {},
		"ts":     {},
	})
}

func normalizeURLForComparison(rawURL string, ignoredQueryKeys map[string]struct{}) string {
	rawURL = strings.TrimSpace(rawURL)
	if rawURL == "" {
		return ""
	}
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return rawURL
	}
	query := parsed.Query()
	if len(query) > 0 && len(ignoredQueryKeys) > 0 {
		for key := range ignoredQueryKeys {
			query.Del(key)
		}
		parsed.RawQuery = query.Encode()
	}
	return parsed.String()
}
