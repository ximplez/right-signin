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
	card := notify.BuildRightSigninCard(notify.RightSigninStatusLoginRequired, notify.SigninCardState{
		RunID:           s.cfg.RunID,
		Stage:           "qq-login",
		Status:          "need_login",
		Message:         loginQRCodeMessage(targetURL),
		SiteURL:         s.cfg.SignInURL,
		CurrentURL:      qrInfo.CurrentURL,
		QRCodeURL:       targetURL,
		QRCodeKind:      qrInfo.Kind,
		QRCodeImagePath: chooseQRCodeImageFile(qrInfo),
		RefreshCount:    refreshCount,
	})
	if cardNotifier, ok := s.notifier.(notify.CardNotifier); ok {
		if err := cardNotifier.Upsert(ctx, card); err != nil {
			return err
		}
	} else {
		msg := notify.Message{
			App:       s.cfg.AppName,
			Title:     notify.BoldText(notify.BlueText(card.Title)),
			Msg:       card.Content,
			TargetURL: targetURL,
		}
		if err := s.notifier.Notify(ctx, msg); err != nil {
			return err
		}
	}
	log.Printf("已发送二维码通知: refresh=%d kind=%s viewerURL=%s oauth=%s preview=%s", refreshCount, qrInfo.Kind, browser.NormalizeSnippet(targetURL, 180), browser.NormalizeSnippet(qrInfo.CurrentURL, 180), browser.NormalizeSnippet(qrInfo.PreviewURL, 180))
	return nil
}

func (s *QQAuthenticator) updateLoginProgress(ctx context.Context, status notify.RightSigninStatus, qrInfo QRCodeInfo, refreshCount int, message string) {
	cardNotifier, ok := s.notifier.(notify.CardNotifier)
	if !ok {
		return
	}
	targetURL := firstNonEmpty(qrInfo.ViewerURL, qrInfo.PreviewURL)
	_ = cardNotifier.NotifyProgress(ctx, notify.BuildRightSigninCard(status, notify.SigninCardState{
		RunID:           s.cfg.RunID,
		Stage:           "qq-login",
		Status:          string(status),
		Message:         message,
		SiteURL:         s.cfg.SignInURL,
		CurrentURL:      qrInfo.CurrentURL,
		QRCodeURL:       targetURL,
		QRCodeKind:      qrInfo.Kind,
		QRCodeImagePath: chooseQRCodeImageFile(qrInfo),
		RefreshCount:    refreshCount,
	}))
}

func loginQRCodeMessage(targetURL string) string {
	if targetURL == "" {
		return "当前尚未生成可访问二维码 URL，请等待下一次刷新。"
	}
	return "请打开二维码完成 QQ 登录；二维码刷新后这张卡片会自动更新为最新入口。"
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
