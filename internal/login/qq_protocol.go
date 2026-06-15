package login

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/chromedp/cdproto/network"

	"right-signin/internal/browser"
)

type qqQRProtocolContext struct {
	PollURL    string
	RefererURL string
	AppID      string
	DAID       string
	ThirdAID   string
	TargetURL  string
	Lang       string
	UIStyle    string
	JSVersion  string
	LoginSig   string
	PTDRVS     string
	O1vID      string
}

type qqQRProtocolPollResult struct {
	Code        string
	Message     string
	RedirectURL string
	Raw         string
	QRSig       string
	PTQRToken   int
	PollURL     string
}

func (s *QQAuthenticator) pollQQQRCodeProtocol(ctx context.Context, sess *browser.Session, qrInfo QRCodeInfo) (qqQRProtocolPollResult, error) {
	protocolCtx, err := buildQQQRProtocolContext(qrInfo)
	if err != nil {
		return qqQRProtocolPollResult{}, err
	}
	cks, err := sess.FindCookies()
	if err != nil {
		return qqQRProtocolPollResult{}, err
	}
	qrsig, cookieHeader := buildQQProtocolCookieHeader(cks)
	if qrsig == "" {
		return qqQRProtocolPollResult{}, fmt.Errorf("未找到 qrsig cookie，无法进行 ptqrlogin 轮询")
	}
	protocolCtx.fillOptionalFieldsFromCookies(cks)
	ptqrtoken := hash33(qrsig)
	pollURL := protocolCtx.buildPollURL(ptqrtoken)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, pollURL, nil)
	if err != nil {
		return qqQRProtocolPollResult{}, err
	}
	req.Header.Set("User-Agent", s.cfg.UserAgent)
	req.Header.Set("Accept", "*/*")
	if strings.TrimSpace(protocolCtx.RefererURL) != "" {
		req.Header.Set("Referer", protocolCtx.RefererURL)
	}
	if strings.TrimSpace(cookieHeader) != "" {
		req.Header.Set("Cookie", cookieHeader)
	}
	resp, err := s.httpClient.Do(req)
	if err != nil {
		return qqQRProtocolPollResult{}, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return qqQRProtocolPollResult{}, err
	}
	if resp.StatusCode >= 300 {
		return qqQRProtocolPollResult{}, fmt.Errorf("ptqrlogin 轮询失败，状态码=%d body=%s", resp.StatusCode, browser.NormalizeSnippet(string(body), 240))
	}
	result := parseQQQRProtocolPollResponse(string(body))
	result.Raw = strings.TrimSpace(string(body))
	result.QRSig = qrsig
	result.PTQRToken = ptqrtoken
	result.PollURL = pollURL
	return result, nil
}

func buildQQQRProtocolContext(qrInfo QRCodeInfo) (qqQRProtocolContext, error) {
	for _, raw := range []string{qrInfo.Raw, qrInfo.IframeURL, qrInfo.IframeSrc} {
		ctx, ok := parseQQQRProtocolContextFromURL(raw)
		if ok {
			return ctx, nil
		}
	}
	return qqQRProtocolContext{}, fmt.Errorf("未能从二维码信息中解析 QQ 协议层轮询参数: raw=%s iframe=%s", browser.NormalizeSnippet(qrInfo.Raw, 180), browser.NormalizeSnippet(qrInfo.IframeURL, 180))
}

func parseQQQRProtocolContextFromURL(raw string) (qqQRProtocolContext, bool) {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || parsed.Host == "" {
		return qqQRProtocolContext{}, false
	}
	query := parsed.Query()
	appid := strings.TrimSpace(query.Get("appid"))
	if appid == "" {
		return qqQRProtocolContext{}, false
	}
	refererURL := parsed.String()
	if strings.Contains(strings.ToLower(parsed.Path), "ptqrshow") {
		refererURL = ""
	}
	return qqQRProtocolContext{
		PollURL:    (&url.URL{Scheme: firstNonEmpty(parsed.Scheme, "https"), Host: parsed.Host, Path: "/ssl/ptqrlogin"}).String(),
		RefererURL: refererURL,
		AppID:      appid,
		DAID:       strings.TrimSpace(query.Get("daid")),
		ThirdAID:   strings.TrimSpace(query.Get("pt_3rd_aid")),
		TargetURL:  firstNonEmpty(strings.TrimSpace(query.Get("u1")), strings.TrimSpace(query.Get("s_url")), "https://graph.qq.com/oauth2.0/login_jump"),
		Lang:       firstNonEmpty(strings.TrimSpace(query.Get("ptlang")), strings.TrimSpace(query.Get("lang")), "2052"),
		UIStyle:    firstNonEmpty(strings.TrimSpace(query.Get("pt_uistyle")), strings.TrimSpace(query.Get("style")), "40"),
		JSVersion:  firstNonEmpty(strings.TrimSpace(query.Get("js_ver")), "26030415"),
		LoginSig:   strings.TrimSpace(query.Get("login_sig")),
	}, true
}

func buildQQProtocolCookieHeader(cks []*network.Cookie) (string, string) {
	if len(cks) == 0 {
		return "", ""
	}
	allowedNames := map[string]struct{}{
		"qrsig":        {},
		"pt_login_sig": {},
		"ptdrvs":       {},
		"o1vid":        {},
		"pt_clientip":  {},
		"pt_serverip":  {},
		"pt_guid_sig":  {},
		"ptcz":         {},
		"rk":           {},
		"uin":          {},
		"p_uin":        {},
		"superuin":     {},
		"supertoken":   {},
		"ptisp":        {},
	}
	parts := make([]string, 0, len(cks))
	qrsig := ""
	for _, ck := range cks {
		if ck == nil {
			continue
		}
		name := strings.TrimSpace(ck.Name)
		value := strings.TrimSpace(ck.Value)
		if name == "" || value == "" {
			continue
		}
		lowerName := strings.ToLower(name)
		if lowerName == "qrsig" {
			qrsig = value
		}
		if _, ok := allowedNames[lowerName]; !ok {
			continue
		}
		parts = append(parts, name+"="+value)
	}
	return qrsig, strings.Join(parts, "; ")
}

func (c *qqQRProtocolContext) fillOptionalFieldsFromCookies(cks []*network.Cookie) {
	if c == nil || len(cks) == 0 {
		return
	}
	for _, ck := range cks {
		if ck == nil {
			continue
		}
		name := strings.ToLower(strings.TrimSpace(ck.Name))
		value := strings.TrimSpace(ck.Value)
		if value == "" {
			continue
		}
		switch name {
		case "pt_login_sig":
			if c.LoginSig == "" {
				c.LoginSig = value
			}
		case "ptdrvs":
			if c.PTDRVS == "" {
				c.PTDRVS = value
			}
		case "o1vid":
			if c.O1vID == "" {
				c.O1vID = value
			}
		}
	}
}

func (c qqQRProtocolContext) buildPollURL(ptqrtoken int) string {
	query := url.Values{}
	query.Set("u1", firstNonEmpty(c.TargetURL, "https://graph.qq.com/oauth2.0/login_jump"))
	query.Set("ptqrtoken", strconv.Itoa(ptqrtoken))
	query.Set("ptredirect", "0")
	query.Set("h", "1")
	query.Set("t", "1")
	query.Set("g", "1")
	query.Set("from_ui", "1")
	query.Set("ptlang", firstNonEmpty(c.Lang, "2052"))
	query.Set("action", fmt.Sprintf("0-0-%d", time.Now().UnixMilli()))
	query.Set("js_ver", firstNonEmpty(c.JSVersion, "26030415"))
	query.Set("js_type", "1")
	query.Set("login_sig", c.LoginSig)
	query.Set("pt_uistyle", firstNonEmpty(c.UIStyle, "40"))
	query.Set("aid", c.AppID)
	if c.DAID != "" {
		query.Set("daid", c.DAID)
	}
	if c.ThirdAID != "" {
		query.Set("pt_3rd_aid", c.ThirdAID)
	}
	if c.PTDRVS != "" {
		query.Set("ptdrvs", c.PTDRVS)
	}
	query.Set("has_onekey", "1")
	if c.O1vID != "" {
		query.Set("o1vId", c.O1vID)
	}
	return strings.TrimRight(c.PollURL, "?") + "?" + query.Encode()
}

func hash33(s string) int {
	var hash uint32
	for i := 0; i < len(s); i++ {
		hash = hash*33 + uint32(s[i])
	}
	return int(hash & 0x7fffffff)
}

func parseQQQRProtocolPollResponse(raw string) qqQRProtocolPollResult {
	result := qqQRProtocolPollResult{Raw: strings.TrimSpace(raw)}
	matches := regexp.MustCompile(`(?s)ptuiCB\((.*?)\)`).FindStringSubmatch(raw)
	if len(matches) < 2 {
		result.Message = browser.NormalizeSnippet(strings.TrimSpace(raw), 120)
		return result
	}
	args := parseQQPTUICallbackArgs(matches[1])
	if len(args) > 0 {
		result.Code = args[0]
	}
	for _, arg := range args {
		trimmed := strings.TrimSpace(arg)
		if result.RedirectURL == "" && (strings.HasPrefix(trimmed, "http://") || strings.HasPrefix(trimmed, "https://")) {
			result.RedirectURL = trimmed
		}
	}
	if len(args) >= 5 {
		result.Message = strings.TrimSpace(args[4])
	}
	if result.Message == "" {
		switch result.Code {
		case "0":
			result.Message = "登录成功"
		case "65":
			result.Message = "二维码已失效"
		case "66":
			result.Message = "二维码未失效"
		case "67":
			result.Message = "二维码认证中"
		case "68":
			result.Message = "二维码取消认证"
		}
	}
	return result
}

func parseQQPTUICallbackArgs(raw string) []string {
	values := make([]string, 0, 6)
	var current strings.Builder
	inQuote := false
	escaped := false
	for _, r := range raw {
		switch {
		case escaped:
			current.WriteRune(r)
			escaped = false
		case r == '\\':
			escaped = true
		case r == '\'':
			inQuote = !inQuote
		case r == ',' && !inQuote:
			values = append(values, strings.TrimSpace(current.String()))
			current.Reset()
		default:
			current.WriteRune(r)
		}
	}
	if current.Len() > 0 || len(values) > 0 {
		values = append(values, strings.TrimSpace(current.String()))
	}
	for i := range values {
		values[i] = strings.TrimSpace(values[i])
	}
	return values
}
