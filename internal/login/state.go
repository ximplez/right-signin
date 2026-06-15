package login

import (
	"fmt"
	"log"
	"regexp"
	"strings"

	"right-signin/internal/browser"
	"right-signin/internal/classify"
	"right-signin/internal/model"
)

type LoginStateDetector struct{}

type loginSignals struct {
	InlineUserID      string
	CookiePrefix      string
	AuthCookieName    string
	SaltkeyCookieName string
	HasAuthCookie     bool
	HasSaltkeyCookie  bool
	HasBindCookie     bool
	HasUINCookie      bool
	HasClientToken    bool
	Status            model.Status
	StatusReason      string
}

var (
	inlineUserIDPatterns = []*regexp.Regexp{
		regexp.MustCompile(`discuz_uid\s*=\s*['"]([^'"]+)['"]`),
		regexp.MustCompile(`(?i)(?:user|member)_?id\s*[:=]\s*['"]([^'"]+)['"]`),
	}
	cookiePrefixPatterns = []*regexp.Regexp{
		regexp.MustCompile(`cookiepre\s*=\s*['"]([^'"]*)['"]`),
	}
)

func NewLoginStateDetector() *LoginStateDetector {
	return &LoginStateDetector{}
}

func (s *QQAuthenticator) IsLoggedIn(sess *browser.Session) (bool, string, error) {
	if s.stateDetector == nil {
		s.stateDetector = NewLoginStateDetector()
	}
	return s.stateDetector.Detect(sess)
}

func (d *LoginStateDetector) Detect(sess *browser.Session) (bool, string, error) {
	summary := sess.Summary()
	log.Printf("登录态检查摘要: url=%s title=%s cookies=%v localStorage=%v sessionStorage=%v body=%s", summary.URL, summary.Title, summary.CookieNames, summary.LocalStorageKeys, summary.SessionStoreKeys, summary.BodySnippet)
	currentURL, err := sess.CurrentURL()
	if err != nil {
		return false, "", err
	}
	if strings.Contains(currentURL, "connect.php") || strings.Contains(currentURL, "ptlogin2") || strings.Contains(currentURL, "xui.ptlogin2") {
		return false, "当前仍处于第三方登录流程", nil
	}
	body, err := sess.BodyText()
	if err != nil {
		return false, "", err
	}
	html, err := sess.HTML()
	if err != nil {
		log.Printf("读取页面 HTML 失败，登录态检测将跳过内联脚本锚点: %v", err)
	}
	cookieNames, err := sess.FindCookieNames()
	if err != nil {
		log.Printf("读取 cookies 失败，登录态检测将仅依赖页面锚点: %v", err)
		cookieNames = summary.CookieNames
	}
	signals := detectLoginSignals(html, cookieNames, body)
	log.Printf("登录态信号: inline_user_id=%q cookie_prefix=%q auth=%t saltkey=%t bind=%t uin=%t client_token=%t status=%s reason=%s", signals.InlineUserID, signals.CookiePrefix, signals.HasAuthCookie, signals.HasSaltkeyCookie, signals.HasBindCookie, signals.HasUINCookie, signals.HasClientToken, signals.Status, signals.StatusReason)

	if signals.HasAuthCookie && signals.HasSaltkeyCookie {
		return true, "命中站点会话强锚点: auth/saltkey 会话 cookie 同时存在", nil
	}
	if hasPositiveInlineUserID(signals.InlineUserID) && (signals.HasAuthCookie || signals.HasSaltkeyCookie) {
		return true, fmt.Sprintf("命中站点会话锚点: 页面内用户 ID=%s 且存在会话 cookie", signals.InlineUserID), nil
	}
	if signals.Status == model.StatusNeedLogin && !signals.HasAuthCookie && !signals.HasSaltkeyCookie && !hasPositiveInlineUserID(signals.InlineUserID) {
		return false, signals.StatusReason, nil
	}
	if isLoggedOutInlineUserID(signals.InlineUserID) && !signals.HasAuthCookie && !signals.HasSaltkeyCookie {
		return false, "命中页面内未登录锚点: user_id=0 且不存在站点会话 cookie", nil
	}
	if strings.Contains(body, "退出") || strings.Contains(body, "个人中心") || strings.Contains(body, "欢迎您回来") {
		return true, "命中文本回退登录特征", nil
	}
	if signals.Status != model.StatusNeedLogin && !containsLoginPrompt(body) && (signals.HasAuthCookie || signals.HasSaltkeyCookie || hasPositiveInlineUserID(signals.InlineUserID)) {
		return true, "命中弱会话锚点: 未出现登录提示且存在页面/会话登录信号", nil
	}
	return false, "未发现可靠的已登录特征", nil
}

func detectLoginSignals(html string, cookieNames []string, body string) loginSignals {
	status, reason := classify.DetectStatus(body)
	signals := loginSignals{
		InlineUserID: extractInlineValue(html, inlineUserIDPatterns...),
		CookiePrefix: extractInlineValue(html, cookiePrefixPatterns...),
		Status:       status,
		StatusReason: reason,
	}
	signals.AuthCookieName = cookieNameFromPrefix(signals.CookiePrefix, "auth")
	signals.SaltkeyCookieName = cookieNameFromPrefix(signals.CookiePrefix, "saltkey")
	signals.HasAuthCookie = hasCookieExactOrSuffix(cookieNames, signals.AuthCookieName, "_auth")
	signals.HasSaltkeyCookie = hasCookieExactOrSuffix(cookieNames, signals.SaltkeyCookieName, "_saltkey")
	signals.HasBindCookie = hasCookieExactOrSuffix(cookieNames, cookieNameFromPrefix(signals.CookiePrefix, "connect_is_bind"), "_connect_is_bind")
	signals.HasUINCookie = hasCookieExactOrSuffix(cookieNames, cookieNameFromPrefix(signals.CookiePrefix, "connect_uin"), "_connect_uin")
	signals.HasClientToken = hasCookieExactOrSuffix(cookieNames, cookieNameFromPrefix(signals.CookiePrefix, "client_token"), "_client_token")
	return signals
}

func extractInlineValue(html string, patterns ...*regexp.Regexp) string {
	if html == "" {
		return ""
	}
	for _, pattern := range patterns {
		if pattern == nil {
			continue
		}
		matches := pattern.FindStringSubmatch(html)
		if len(matches) >= 2 {
			return strings.TrimSpace(matches[1])
		}
	}
	return ""
}

func cookieNameFromPrefix(prefix, suffix string) string {
	if prefix == "" || suffix == "" {
		return ""
	}
	return prefix + suffix
}

func hasCookieExactOrSuffix(cookieNames []string, exact string, suffix string) bool {
	exact = strings.ToLower(strings.TrimSpace(exact))
	suffix = strings.ToLower(strings.TrimSpace(suffix))
	for _, name := range cookieNames {
		lower := strings.ToLower(strings.TrimSpace(name))
		if lower == "" {
			continue
		}
		if exact != "" && lower == exact {
			return true
		}
		if suffix != "" && strings.HasSuffix(lower, suffix) {
			return true
		}
	}
	return false
}

func hasPositiveInlineUserID(uid string) bool {
	uid = strings.TrimSpace(uid)
	if uid == "" || uid == "0" {
		return false
	}
	for _, ch := range uid {
		if ch < '0' || ch > '9' {
			return false
		}
	}
	return true
}

func isLoggedOutInlineUserID(uid string) bool {
	return strings.TrimSpace(uid) == "0"
}

func containsLoginPrompt(body string) bool {
	normalized := classify.NormalizeText(body)
	for _, keyword := range []string{"请先登录", "立即登录", "QQ登录", "QQ 登录"} {
		if strings.Contains(normalized, keyword) {
			return true
		}
	}
	return false
}
