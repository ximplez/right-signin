package signin

import (
	"fmt"
	"regexp"
	"strings"
	"time"

	"right-signin/internal/browser"
	"right-signin/internal/classify"
	"right-signin/internal/config"
	"right-signin/internal/model"
)

type Service struct {
	cfg *config.Config
}

type signPageAnchorStatus string

const (
	signAnchorUnknown       signPageAnchorStatus = "unknown"
	signAnchorReadyToSign   signPageAnchorStatus = "ready_to_sign"
	signAnchorAlreadySigned signPageAnchorStatus = "already_signed"
)

var (
	signNavLinkPattern = regexp.MustCompile(`(?is)<a\b[^>]*href=["'][^"']*erling_qd-sign_in\.html[^"']*["'][^>]*>(.*?)</a>`)
	signButtonPattern  = regexp.MustCompile(`(?is)<button\b([^>]*)>(.*?)</button>`)
	tagPattern         = regexp.MustCompile(`<[^>]+>`)
	signStateSelectors = []string{
		"a[href*='erling_qd-sign_in.html']",
		"#signin-btn",
		"#signin-checkin-btn",
		"button.erqd-checkin-btn",
		"button.erqd-checkin-btn2",
		"button[id*='signin'][class*='checkin']",
		"button[id*='signin']",
	}
	signButtonSelectors = []string{
		"a[href*='erling_qd-sign_in.html']",
		"#signin-btn",
		"#signin-checkin-btn",
		"button.erqd-checkin-btn",
		"button.erqd-checkin-btn2",
		"button[id*='signin'][class*='checkin']",
		"button[id*='signin']",
	}
)

func New(cfg *config.Config) *Service {
	return &Service{cfg: cfg}
}

func (s *Service) Inspect(sess *browser.Session) (model.Result, error) {
	currentURL, _ := sess.CurrentURL()
	if states, result, err := s.waitForSignPageStable(sess, 6*time.Second); err == nil {
		if result.Status != model.StatusUnknown {
			result.URL = currentURL
			return result, nil
		}
		_ = states
	}

	body, err := sess.BodyText()
	if err != nil {
		return model.Result{Status: model.StatusNetworkError, Message: "读取页面文本失败"}, err
	}
	status, reason := classify.DetectStatus(body)
	if status == model.StatusRiskControl || status == model.StatusNetworkError || status == model.StatusNeedLogin {
		return model.Result{Status: status, Message: reason, URL: currentURL}, nil
	}
	if status == model.StatusSuccess || status == model.StatusFailure {
		if status == model.StatusSuccess {
			// 进入页面即出现成功关键词，大概率是签到页成功态，按已签到处理更稳妥
			status = model.StatusAlreadySigned
		}
		return model.Result{Status: status, Message: reason, URL: currentURL}, nil
	}
	if html, htmlErr := sess.HTML(); htmlErr == nil {
		anchorStatus, anchorReason := detectSignPageAnchors(html)
		switch anchorStatus {
		case signAnchorAlreadySigned:
			return model.Result{Status: model.StatusAlreadySigned, Message: anchorReason, URL: currentURL}, nil
		case signAnchorReadyToSign:
			return model.Result{Status: model.StatusReadyToSign, Message: anchorReason, URL: currentURL}, nil
		}
	}
	if states, err := s.querySignElementStates(sess); err == nil {
		if status, reason, ok := classifySignElementStates(states); ok {
			return model.Result{Status: status, Message: reason, URL: currentURL}, nil
		}
	}
	if status == model.StatusUnknown {
		ready, err := s.hasSignButton(sess)
		if err == nil && ready {
			return model.Result{Status: model.StatusReadyToSign, Message: "检测到可点击签到按钮", URL: currentURL}, nil
		}
		return model.Result{Status: model.StatusPageChanged, Message: "未识别到签到状态: " + reason, URL: currentURL}, nil
	}
	return model.Result{Status: status, Message: reason, URL: currentURL}, nil
}

func (s *Service) waitForSignPageStable(sess *browser.Session, timeout time.Duration) ([]browser.ElementState, model.Result, error) {
	if timeout <= 0 {
		states, err := s.querySignElementStates(sess)
		return states, model.Result{Status: model.StatusUnknown, Message: "未启用签到页稳定等待"}, err
	}
	deadline := time.Now().Add(timeout)
	var lastStates []browser.ElementState
	var lastResult model.Result
	for {
		states, result, err := s.inspectSignIndicators(sess)
		if err == nil {
			lastStates = states
			lastResult = result
			if result.Status != model.StatusUnknown {
				return states, result, nil
			}
		}
		if !time.Now().Before(deadline) {
			if err != nil {
				return lastStates, lastResult, err
			}
			return lastStates, lastResult, nil
		}
		_ = sess.SleepRandom(400*time.Millisecond, 900*time.Millisecond)
	}
}

func (s *Service) inspectSignIndicators(sess *browser.Session) ([]browser.ElementState, model.Result, error) {
	currentURL, _ := sess.CurrentURL()
	body, err := sess.BodyText()
	if err != nil {
		return nil, model.Result{Status: model.StatusNetworkError, Message: "读取页面文本失败", URL: currentURL}, err
	}
	status, reason := classify.DetectStatus(body)
	if status == model.StatusRiskControl || status == model.StatusNetworkError || status == model.StatusNeedLogin {
		return nil, model.Result{Status: status, Message: reason, URL: currentURL}, nil
	}
	if status == model.StatusSuccess {
		return nil, model.Result{Status: model.StatusAlreadySigned, Message: reason, URL: currentURL}, nil
	}
	if status == model.StatusFailure {
		return nil, model.Result{Status: status, Message: reason, URL: currentURL}, nil
	}
	states, err := s.querySignElementStates(sess)
	if err == nil {
		if signStatus, signReason, ok := classifySignElementStates(states); ok {
			return states, model.Result{Status: signStatus, Message: signReason, URL: currentURL}, nil
		}
	}
	if html, htmlErr := sess.HTML(); htmlErr == nil {
		anchorStatus, anchorReason := detectSignPageAnchors(html)
		switch anchorStatus {
		case signAnchorAlreadySigned:
			return states, model.Result{Status: model.StatusAlreadySigned, Message: anchorReason, URL: currentURL}, nil
		case signAnchorReadyToSign:
			return states, model.Result{Status: model.StatusReadyToSign, Message: anchorReason, URL: currentURL}, nil
		}
	}
	return states, model.Result{Status: model.StatusUnknown, Message: "等待签到模块稳定中", URL: currentURL}, nil
}

func (s *Service) querySignElementStates(sess *browser.Session) ([]browser.ElementState, error) {
	states := make([]browser.ElementState, 0, len(signStateSelectors))
	for _, selector := range signStateSelectors {
		state, err := sess.ElementState(selector)
		if err != nil {
			return nil, err
		}
		states = append(states, state)
	}
	return states, nil
}

func classifySignElementStates(states []browser.ElementState) (model.Status, string, bool) {
	for _, state := range states {
		if !state.Exists || !state.Visible {
			continue
		}
		text := normalizeAnchorText(state.Text)
		attrs := strings.ToLower(strings.Join([]string{state.Class, state.ID, state.Name, state.Href}, " "))
		isCheckinElement := strings.Contains(attrs, "signin") || strings.Contains(attrs, "checkin") || strings.Contains(attrs, "erling_qd-sign_in.html")
		if !isCheckinElement {
			continue
		}
		if text == "已签到" || strings.Contains(text, "今日已签到") || strings.Contains(attrs, "erqd-checkin-btn2") || state.Disabled || strings.EqualFold(state.AriaDisabled, "true") {
			return model.StatusAlreadySigned, fmt.Sprintf("命中元素强锚点: selector=%s text=%s disabled=%t class=%s", state.Selector, text, state.Disabled || strings.EqualFold(state.AriaDisabled, "true"), strings.TrimSpace(state.Class)), true
		}
		if text == "立即签到" || text == "签到" || strings.Contains(text, "签到") {
			return model.StatusReadyToSign, fmt.Sprintf("命中元素强锚点: selector=%s text=%s class=%s", state.Selector, text, strings.TrimSpace(state.Class)), true
		}
		if strings.Contains(attrs, "erqd-checkin-btn") || strings.Contains(attrs, "signin-btn") {
			return model.StatusReadyToSign, fmt.Sprintf("命中元素强锚点: selector=%s class=%s", state.Selector, strings.TrimSpace(state.Class)), true
		}
	}
	return model.StatusUnknown, "未命中元素强锚点", false
}

func detectSignPageAnchors(html string) (signPageAnchorStatus, string) {
	if strings.TrimSpace(html) == "" {
		return signAnchorUnknown, "未获取到 HTML"
	}
	for _, match := range signNavLinkPattern.FindAllStringSubmatch(html, -1) {
		if len(match) < 2 {
			continue
		}
		text := normalizeAnchorText(match[1])
		switch text {
		case "已签到":
			return signAnchorAlreadySigned, "命中签到导航强锚点: erling_qd-sign_in.html 链接文案为 已签到"
		case "签到":
			return signAnchorReadyToSign, "命中签到导航强锚点: erling_qd-sign_in.html 链接文案为 签到"
		}
	}
	for _, match := range signButtonPattern.FindAllStringSubmatch(html, -1) {
		if len(match) < 3 {
			continue
		}
		attrs := strings.ToLower(match[1])
		text := normalizeAnchorText(match[2])
		isCheckinButton := strings.Contains(attrs, "checkin-btn") || strings.Contains(attrs, "signin") || strings.Contains(attrs, "erqd-checkin-btn")
		if !isCheckinButton {
			continue
		}
		if text == "已签到" || strings.Contains(attrs, "disabled") || strings.Contains(attrs, "erqd-checkin-btn2") {
			return signAnchorAlreadySigned, "命中签到按钮强锚点: 签到按钮已禁用或文案为 已签到"
		}
		if text == "立即签到" || text == "签到" || strings.Contains(text, "签到") {
			return signAnchorReadyToSign, "命中签到按钮强锚点: 存在可签到按钮"
		}
	}
	return signAnchorUnknown, "未命中签到页结构锚点"
}

func normalizeAnchorText(raw string) string {
	text := tagPattern.ReplaceAllString(raw, " ")
	text = strings.ReplaceAll(text, "&nbsp;", " ")
	text = strings.ReplaceAll(text, "&#160;", " ")
	return strings.Join(strings.Fields(strings.TrimSpace(text)), "")
}

func (s *Service) Execute(sess *browser.Session, dryRun bool) (model.Result, error) {
	current, err := s.Inspect(sess)
	if err != nil {
		return current, err
	}
	if current.Status == model.StatusAlreadySigned || current.Status == model.StatusRiskControl || current.Status == model.StatusNeedLogin || current.Status == model.StatusPageChanged {
		return current, nil
	}
	if current.Status != model.StatusReadyToSign {
		return current, nil
	}
	if dryRun {
		current.Status = model.StatusDryRun
		current.Message = "dry-run 模式下检测到可签到，但未实际点击"
		return current, nil
	}
	if err := sess.SleepRandom(300*time.Millisecond, 800*time.Millisecond); err != nil {
		return model.Result{Status: model.StatusFailure, Message: "签到前等待失败", URL: current.URL}, err
	}
	if err := s.clickSign(sess); err != nil {
		return model.Result{Status: model.StatusFailure, Message: "点击签到失败", URL: current.URL}, err
	}
	if err := sess.SleepRandom(1200*time.Millisecond, 2200*time.Millisecond); err != nil {
		return model.Result{Status: model.StatusFailure, Message: "等待签到结果失败", URL: current.URL}, err
	}
	after, err := s.Inspect(sess)
	if err != nil {
		return after, err
	}
	if after.Status == model.StatusReadyToSign {
		body, _ := sess.BodyText()
		if strings.Contains(body, "签到") {
			after.Status = model.StatusFailure
			after.Message = "点击签到后页面未出现成功或已签到状态"
		}
	}
	if after.Status == model.StatusUnknown || after.Status == model.StatusPageChanged {
		after.Status = model.StatusFailure
		after.Message = "签到后状态未知，请检查截图和 HTML"
	}
	if after.Status == model.StatusAlreadySigned {
		after.Status = model.StatusSuccess
		if after.Message == "" {
			after.Message = "签到成功后页面进入已签到状态"
		}
	}
	return after, nil
}

func (s *Service) hasSignButton(sess *browser.Session) (bool, error) {
	states, err := s.querySignElementStates(sess)
	if err != nil {
		return false, err
	}
	status, _, ok := classifySignElementStates(states)
	if ok {
		return status == model.StatusReadyToSign, nil
	}
	return false, nil
}

func (s *Service) clickSign(sess *browser.Session) error {
	if err := sess.ClickFirstBySelector(signButtonSelectors); err == nil {
		return nil
	}
	if err := sess.ClickFirstByText([]string{"立即签到", "签到领奖", "签到", "打卡"}); err != nil {
		return fmt.Errorf("点击签到入口失败: %w", err)
	}
	return nil
}
