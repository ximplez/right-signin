package login

import (
	"net/url"
	"os"
	"path/filepath"
	"testing"

	"right-signin/internal/model"
)

func TestDetectLoginAnchorsFromLoggedInFixture(t *testing.T) {
	html := `<html><head><script>var STYLEID = '1', discuz_uid = '1060472', cookiepre = 'rHEX_2132_', cookiedomain = '', cookiepath = '/';</script></head><body>今日已签到，欢迎回来</body></html>`
	body := "今日已签到，欢迎回来"
	cookies := []string{
		"rHEX_2132_auth",
		"rHEX_2132_saltkey",
		"rHEX_2132_connect_is_bind",
		"rHEX_2132_client_token",
	}

	signals := detectLoginSignals(html, cookies, body)
	if signals.InlineUserID != "1060472" {
		t.Fatalf("inline user id 提取失败: got=%q", signals.InlineUserID)
	}
	if signals.CookiePrefix != "rHEX_2132_" {
		t.Fatalf("cookie prefix 提取失败: got=%q", signals.CookiePrefix)
	}
	if !signals.HasAuthCookie || !signals.HasSaltkeyCookie {
		t.Fatalf("未识别到关键登录 cookie: %+v", signals)
	}
	if signals.Status != model.StatusAlreadySigned {
		t.Fatalf("页面状态识别错误: got=%s", signals.Status)
	}
	if !hasPositiveInlineUserID(signals.InlineUserID) {
		t.Fatalf("inline user id 应被识别为已登录 uid: %q", signals.InlineUserID)
	}
}

func TestDetectLoginAnchorsFromNeedLoginFixture(t *testing.T) {
	html, err := os.ReadFile(filepath.Join("..", "..", "testdata", "html-fixtures", "need_login.html"))
	if err != nil {
		t.Fatalf("读取 fixture 失败: %v", err)
	}
	signals := detectLoginSignals(string(html), nil, string(html))
	if signals.HasAuthCookie || signals.HasSaltkeyCookie {
		t.Fatalf("未登录页不应识别出登录 cookie: %+v", signals)
	}
	if signals.Status != model.StatusNeedLogin {
		t.Fatalf("未登录页状态识别错误: got=%s", signals.Status)
	}
	if hasPositiveInlineUserID(signals.InlineUserID) {
		t.Fatalf("未登录页不应识别出正 uid: %q", signals.InlineUserID)
	}
}

func TestDetectLoginSignalsTreatsInlineUserIDZeroAsLoggedOut(t *testing.T) {
	html := `<html><head><script>var discuz_uid = '0', cookiepre = 'rHEX_2132_';</script></head><body>请先登录</body></html>`
	signals := detectLoginSignals(html, []string{"rHEX_2132_lastvisit"}, "请先登录")
	if !isLoggedOutInlineUserID(signals.InlineUserID) {
		t.Fatalf("inline user id=0 未被识别: %+v", signals)
	}
	if signals.HasAuthCookie || signals.HasSaltkeyCookie {
		t.Fatalf("inline user id=0 场景不应误判为登录: %+v", signals)
	}
	if signals.Status != model.StatusNeedLogin {
		t.Fatalf("状态识别错误: got=%s", signals.Status)
	}
}

func TestResolveMaybeRelativeURL(t *testing.T) {
	base := "https://xui.ptlogin2.qq.com/cgi-bin/xlogin?appid=1"
	tests := []struct {
		name   string
		target string
		want   string
	}{
		{name: "absolute", target: "https://xui.ptlogin2.qq.com/ssl/ptqrshow?a=1", want: "https://xui.ptlogin2.qq.com/ssl/ptqrshow?a=1"},
		{name: "protocol relative", target: "//xui.ptlogin2.qq.com/ssl/ptqrshow?a=1", want: "https://xui.ptlogin2.qq.com/ssl/ptqrshow?a=1"},
		{name: "relative path", target: "/ssl/ptqrshow?a=1", want: "https://xui.ptlogin2.qq.com/ssl/ptqrshow?a=1"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := resolveMaybeRelativeURL(base, tt.target)
			if got != tt.want {
				t.Fatalf("URL 解析错误: got=%q want=%q", got, tt.want)
			}
		})
	}
}

func TestIframeQRPattern(t *testing.T) {
	html := `<html><body><img id="qrimg" src="//xui.ptlogin2.qq.com/ssl/ptqrshow?appid=716027609&amp;e=2"></body></html>`
	matches := iframeQRPattern.FindStringSubmatch(html)
	if len(matches) < 2 {
		t.Fatalf("未匹配到 iframe 二维码图片")
	}
	got := resolveMaybeRelativeURL("https://xui.ptlogin2.qq.com/cgi-bin/xlogin?appid=716027609", matches[1])
	want := "https://xui.ptlogin2.qq.com/ssl/ptqrshow?appid=716027609&e=2"
	if got != want {
		t.Fatalf("二维码地址解析错误: got=%q want=%q", got, want)
	}
}

func TestInferQQQRCodeFromLoginURL(t *testing.T) {
	loginURL := "https://xui.ptlogin2.qq.com/cgi-bin/xlogin?appid=716027609&daid=383&style=33&target=self&s_url=https%3A%2F%2Fgraph.qq.com%2Foauth2.0%2Flogin_jump&pt_3rd_aid=310198347"
	got, err := inferQQQRCodeFromLoginURL(loginURL)
	if err != nil {
		t.Fatalf("推导二维码地址失败: %v", err)
	}
	parsed, err := url.Parse(got)
	if err != nil {
		t.Fatalf("结果不是合法 URL: %v", err)
	}
	if parsed.Scheme != "https" || parsed.Host != "xui.ptlogin2.qq.com" || parsed.Path != "/ssl/ptqrshow" {
		t.Fatalf("二维码地址目标不正确: %s", got)
	}
	query := parsed.Query()
	checks := map[string]string{
		"appid":      "716027609",
		"daid":       "383",
		"pt_3rd_aid": "310198347",
		"u1":         "https://graph.qq.com/oauth2.0/login_jump",
		"e":          "2",
		"l":          "M",
		"s":          "3",
		"d":          "72",
		"v":          "4",
	}
	for key, want := range checks {
		if got := query.Get(key); got != want {
			t.Fatalf("查询参数 %s 错误: got=%q want=%q url=%s", key, got, want, parsed.String())
		}
	}
	if query.Get("t") == "" {
		t.Fatalf("缺少随机参数 t: %s", parsed.String())
	}
}
