package signin

import (
	"fmt"
	"log"
	"net/url"
	"path/filepath"
	"regexp"
	"sort"
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

type signClickCandidate struct {
	State  browser.ElementState
	Score  int
	Reason string
}

type postClickOutcome struct {
	Result  model.Result
	Changed bool
	Detail  string
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
		"#signin-btn",
		"#signin-checkin-btn",
		"button.erqd-checkin-btn",
		"button.erqd-checkin-btn2",
		"button[id*='signin'][class*='checkin']",
		"button[id*='signin']",
		"a[href*='plugin.php'][href*='sign']",
		"a[href*='erling_qd-sign_in.html']",
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
	_ = s.captureSignArtifact(sess, "pre-sign-click")

	maxAttempts := s.cfg.ActionRetries + 1
	if maxAttempts <= 0 {
		maxAttempts = 1
	}
	var lastOutcome postClickOutcome
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		if attempt > 1 {
			current, err = s.Inspect(sess)
			if err != nil {
				return current, err
			}
			if current.Status == model.StatusAlreadySigned {
				return normalizeSignedResult(current), nil
			}
			if current.Status != model.StatusReadyToSign {
				return current, nil
			}
		}
		if err := sess.SleepRandom(300*time.Millisecond, 800*time.Millisecond); err != nil {
			return model.Result{Status: model.StatusFailure, Message: fmt.Sprintf("签到前等待失败: attempt=%d", attempt), URL: current.URL}, err
		}
		candidate, err := s.pickSignClickCandidate(sess)
		if err != nil {
			return model.Result{Status: model.StatusFailure, Message: "未找到可靠的签到点击目标", URL: current.URL}, err
		}
		preClickURL, _ := sess.CurrentURL()
		preClickBody, _ := sess.BodyText()
		_ = s.captureSignArtifact(sess, fmt.Sprintf("pre-sign-click-attempt-%d", attempt))
		if err := sess.ClickBySelector(candidate.State.Selector); err != nil {
			return model.Result{Status: model.StatusFailure, Message: fmt.Sprintf("点击签到失败: attempt=%d target=%s", attempt, summarizeSignElement(candidate.State)), URL: current.URL}, err
		}
		_ = s.captureSignArtifact(sess, fmt.Sprintf("post-sign-click-attempt-%d", attempt))

		outcome, waitErr := s.waitForPostClickOutcome(sess, candidate, attempt, preClickURL, browser.NormalizeSnippet(preClickBody, 180))
		lastOutcome = outcome
		result := normalizeSignedResult(outcome.Result)
		if waitErr == nil && (result.Status == model.StatusSuccess || result.Status == model.StatusAlreadySigned || result.Status == model.StatusRiskControl || result.Status == model.StatusNeedLogin || result.Status == model.StatusNetworkError) {
			return result, nil
		}
		if !s.shouldRetrySignClick(outcome, attempt, maxAttempts) {
			if result.Status == model.StatusUnknown || result.Status == model.StatusPageChanged || result.Status == model.StatusReadyToSign {
				if outcome.Changed {
					result.Status = model.StatusFailure
					result.Message = mergeDetail("点击签到后页面有变化，但未出现成功或已签到状态", outcome.Detail)
				} else {
					result.Status = model.StatusFailure
					result.Message = mergeDetail("点击签到后页面无明显变化，可能点到了导航或无效元素", outcome.Detail)
				}
			}
			if result.Status == model.StatusFailure {
				_ = s.captureSignArtifact(sess, "sign-timeout")
			}
			if waitErr != nil {
				return result, waitErr
			}
			return result, nil
		}
		log.Printf("签到点击结果未稳定，准备重试: attempt=%d/%d detail=%s", attempt, maxAttempts, outcome.Detail)
	}

	result := normalizeSignedResult(lastOutcome.Result)
	if result.Status == model.StatusUnknown || result.Status == model.StatusPageChanged || result.Status == model.StatusReadyToSign {
		if lastOutcome.Changed {
			result.Status = model.StatusFailure
			result.Message = mergeDetail("点击签到后页面有变化，但未出现成功或已签到状态", lastOutcome.Detail)
		} else {
			result.Status = model.StatusFailure
			result.Message = mergeDetail("点击签到后页面无明显变化，可能点到了导航或无效元素", lastOutcome.Detail)
		}
	}
	_ = s.captureSignArtifact(sess, "sign-timeout")
	return result, nil
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

func (s *Service) pickSignClickCandidate(sess *browser.Session) (signClickCandidate, error) {
	currentURL, _ := sess.CurrentURL()
	candidates := make([]signClickCandidate, 0, len(signButtonSelectors))
	for _, selector := range signButtonSelectors {
		state, err := sess.ElementState(selector)
		if err != nil {
			return signClickCandidate{}, err
		}
		candidate, ok := scoreSignClickCandidate(currentURL, state)
		if ok {
			candidates = append(candidates, candidate)
		}
	}
	if len(candidates) == 0 {
		return signClickCandidate{}, fmt.Errorf("未找到可靠的签到点击候选，selectors=%v", signButtonSelectors)
	}
	sort.SliceStable(candidates, func(i, j int) bool {
		if candidates[i].Score == candidates[j].Score {
			return candidates[i].State.Selector < candidates[j].State.Selector
		}
		return candidates[i].Score > candidates[j].Score
	})
	best := candidates[0]
	log.Printf("已选择签到点击目标: selector=%s score=%d reason=%s state=%s", best.State.Selector, best.Score, best.Reason, summarizeSignElement(best.State))
	return best, nil
}

func scoreSignClickCandidate(currentURL string, state browser.ElementState) (signClickCandidate, bool) {
	if !state.Exists || !state.Visible || !state.Interactable {
		return signClickCandidate{}, false
	}
	if state.Disabled || strings.EqualFold(state.AriaDisabled, "true") {
		return signClickCandidate{}, false
	}
	text := normalizeAnchorText(state.Text)
	if text == "已签到" || strings.Contains(text, "今日已签到") {
		return signClickCandidate{}, false
	}
	attrs := strings.ToLower(strings.Join([]string{state.Class, state.ID, state.Name, state.Role, state.Type, state.Href}, " "))
	score := 0
	reasons := make([]string, 0, 8)
	if state.TagName == "button" {
		score += 120
		reasons = append(reasons, "button 元素")
	}
	if state.TagName == "input" {
		score += 90
		reasons = append(reasons, "input 按钮")
	}
	if state.Role == "button" {
		score += 60
		reasons = append(reasons, "role=button")
	}
	if state.ID == "signin-btn" || state.ID == "signin-checkin-btn" {
		score += 160
		reasons = append(reasons, "命中固定签到 id")
	}
	if strings.Contains(attrs, "erqd-checkin-btn") {
		score += 140
		reasons = append(reasons, "命中签到 class")
	}
	if strings.Contains(attrs, "signin") || strings.Contains(attrs, "checkin") {
		score += 70
		reasons = append(reasons, "属性含 signin/checkin")
	}
	if state.Type == "submit" || state.Type == "button" {
		score += 40
		reasons = append(reasons, "显式按钮 type")
	}
	switch text {
	case "立即签到":
		score += 90
		reasons = append(reasons, "文案=立即签到")
	case "签到领奖":
		score += 80
		reasons = append(reasons, "文案=签到领奖")
	case "签到":
		score += 60
		reasons = append(reasons, "文案=签到")
	default:
		if strings.Contains(text, "签到") {
			score += 45
			reasons = append(reasons, "文案包含签到")
		}
	}
	if state.TagName == "a" {
		score -= 45
		reasons = append(reasons, "anchor 降权")
	}
	if strings.Contains(strings.ToLower(state.Href), "plugin.php") && strings.Contains(strings.ToLower(state.Href), "sign") {
		score += 60
		reasons = append(reasons, "href 指向签到提交")
	}
	if isLikelySignNavigation(currentURL, state.Href) {
		score -= 90
		reasons = append(reasons, "疑似仅用于打开签到页的导航链接")
	}
	if !state.InViewport {
		score -= 5
	}
	if score <= 0 {
		return signClickCandidate{}, false
	}
	return signClickCandidate{State: state, Score: score, Reason: strings.Join(reasons, ", ")}, true
}

func (s *Service) waitForPostClickOutcome(sess *browser.Session, candidate signClickCandidate, attempt int, baselineURL, baselineSnippet string) (postClickOutcome, error) {
	baselineState := candidate.State
	timeout := 8 * time.Second
	if s.cfg.PageTimeout > 0 && s.cfg.PageTimeout < timeout {
		timeout = s.cfg.PageTimeout
	}
	if timeout < 4*time.Second {
		timeout = 4 * time.Second
	}
	deadline := time.Now().Add(timeout)
	changed := false
	changeNotes := make([]string, 0, 4)
	lastResult := model.Result{Status: model.StatusUnknown, Message: "等待签到结果"}
	lastURL := baselineURL

	for {
		currentURL, _ := sess.CurrentURL()
		lastURL = currentURL
		currentBody, _ := sess.BodyText()
		currentSnippet := browser.NormalizeSnippet(currentBody, 180)
		currentState, _ := sess.ElementState(candidate.State.Selector)
		if currentURL != baselineURL {
			changed = true
			changeNotes = appendUnique(changeNotes, "URL 已变化")
		}
		if currentSnippet != "" && currentSnippet != baselineSnippet {
			changed = true
			changeNotes = appendUnique(changeNotes, "页面摘要已变化")
		}
		if signElementChanged(baselineState, currentState) {
			changed = true
			changeNotes = appendUnique(changeNotes, "签到元素状态已变化")
		}

		_, result, inspectErr := s.inspectSignIndicators(sess)
		if inspectErr == nil {
			lastResult = result
			switch result.Status {
			case model.StatusAlreadySigned, model.StatusSuccess, model.StatusRiskControl, model.StatusNeedLogin, model.StatusNetworkError, model.StatusFailure:
				detail := buildOutcomeDetail(attempt, candidate, changed, changeNotes, baselineURL, currentURL, currentState)
				result.Message = mergeDetail(result.Message, detail)
				return postClickOutcome{Result: result, Changed: changed, Detail: detail}, nil
			}
		}

		if !time.Now().Before(deadline) {
			break
		}
		_ = sess.SleepRandom(350*time.Millisecond, 650*time.Millisecond)
	}

	final, err := s.Inspect(sess)
	if err == nil {
		lastResult = final
	}
	finalState, _ := sess.ElementState(candidate.State.Selector)
	detail := buildOutcomeDetail(attempt, candidate, changed, changeNotes, baselineURL, lastURL, finalState)
	lastResult.Message = mergeDetail(lastResult.Message, detail)
	return postClickOutcome{Result: lastResult, Changed: changed, Detail: detail}, err
}

func (s *Service) shouldRetrySignClick(outcome postClickOutcome, attempt, maxAttempts int) bool {
	if attempt >= maxAttempts {
		return false
	}
	switch outcome.Result.Status {
	case model.StatusSuccess, model.StatusAlreadySigned, model.StatusRiskControl, model.StatusNeedLogin, model.StatusNetworkError:
		return false
	}
	return !outcome.Changed
}

func (s *Service) captureSignArtifact(sess *browser.Session, prefix string) error {
	if s.cfg == nil || strings.TrimSpace(s.cfg.RunArtifactDir) == "" || strings.TrimSpace(prefix) == "" {
		return nil
	}
	base := filepath.Join(s.cfg.RunArtifactDir, prefix)
	shot, html, err := sess.Snapshot(base)
	if err != nil {
		log.Printf("保存签到调试留证失败: prefix=%s err=%v", prefix, err)
		return err
	}
	log.Printf("已保存签到调试留证: prefix=%s screenshot=%s html=%s", prefix, shot, html)
	return nil
}

func normalizeSignedResult(result model.Result) model.Result {
	if result.Status == model.StatusAlreadySigned {
		result.Status = model.StatusSuccess
		if result.Message == "" {
			result.Message = "签到成功后页面进入已签到状态"
		}
	}
	return result
}

func summarizeSignElement(state browser.ElementState) string {
	return fmt.Sprintf("selector=%s tag=%s text=%s href=%s class=%s id=%s disabled=%t interactable=%t", state.Selector, state.TagName, normalizeAnchorText(state.Text), strings.TrimSpace(state.Href), strings.TrimSpace(state.Class), strings.TrimSpace(state.ID), state.Disabled || strings.EqualFold(state.AriaDisabled, "true"), state.Interactable)
}

func signElementChanged(before, after browser.ElementState) bool {
	if before.Exists != after.Exists || before.Visible != after.Visible || before.Interactable != after.Interactable || before.Disabled != after.Disabled || !strings.EqualFold(before.AriaDisabled, after.AriaDisabled) {
		return true
	}
	if normalizeAnchorText(before.Text) != normalizeAnchorText(after.Text) {
		return true
	}
	if strings.TrimSpace(before.Class) != strings.TrimSpace(after.Class) || strings.TrimSpace(before.Href) != strings.TrimSpace(after.Href) {
		return true
	}
	return false
}

func isLikelySignNavigation(currentURL, href string) bool {
	href = strings.TrimSpace(strings.ToLower(href))
	if href == "" {
		return false
	}
	if strings.Contains(href, "plugin.php") && strings.Contains(href, "sign") {
		return false
	}
	if !strings.Contains(href, "erling_qd-sign_in.html") {
		return false
	}
	if strings.Contains(href, "sign=") || strings.Contains(href, "handlekey") || strings.Contains(href, "infloat") {
		return false
	}
	base, err := url.Parse(currentURL)
	if err != nil {
		return true
	}
	ref, err := url.Parse(href)
	if err != nil {
		return true
	}
	resolved := base.ResolveReference(ref)
	return strings.EqualFold(strings.TrimSpace(base.Path), strings.TrimSpace(resolved.Path))
}

func buildOutcomeDetail(attempt int, candidate signClickCandidate, changed bool, changeNotes []string, baselineURL, finalURL string, finalState browser.ElementState) string {
	return fmt.Sprintf("attempt=%d candidate={%s} score=%d candidate_reason=%s changed=%t change_notes=%s baseline_url=%s final_url=%s final_state={%s}", attempt, summarizeSignElement(candidate.State), candidate.Score, candidate.Reason, changed, strings.Join(changeNotes, "; "), baselineURL, finalURL, summarizeSignElement(finalState))
}

func mergeDetail(message, detail string) string {
	message = strings.TrimSpace(message)
	detail = strings.TrimSpace(detail)
	if detail == "" {
		return message
	}
	if message == "" {
		return detail
	}
	if strings.Contains(message, detail) {
		return message
	}
	return message + " | " + detail
}

func appendUnique(items []string, value string) []string {
	value = strings.TrimSpace(value)
	if value == "" {
		return items
	}
	for _, item := range items {
		if item == value {
			return items
		}
	}
	return append(items, value)
}
