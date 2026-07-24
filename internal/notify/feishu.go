package notify

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"right-signin/internal/httputil"
)

const (
	DefaultRightSigninAppName  = "恩山论坛自动签到"
	DefaultRightSigninURL      = "https://www.right.com.cn/forum/erling_qd-sign_in.html"
	DefaultProgressNotifyEvery = time.Minute
	NotificationConfigEnv      = "NOTIFICATION_CONFIG_JSON"
)

type CardConfig struct {
	Enabled             bool
	ConfigError         string
	GatewayBaseURL      string
	GatewayAuthToken    string
	AppID               string
	TemplateID          string
	TemplateVersionName string
	ReceiveIDType       string
	ReceiveID           string
	AppName             string
	OpenID              string
	DefaultURL          string
	ProgressNotifyEvery time.Duration
}

type FeishuCardNotifier struct {
	config         CardConfig
	client         *http.Client
	messageID      string
	lastProgressAt time.Time
	disabledLogged bool
	mu             sync.Mutex
}

type CardMessage struct {
	AppName            string
	Title              string
	SubTitle           string
	TitleStyle         string
	Content            string
	Foot               string
	MainButtonText     string
	MainButtonDisabled bool
	MainButtonEvent    any
	SubButtonText      string
	SubButtonDisabled  bool
	SubButtonURL       string
	OpenID             string
	Status             string
	Action             string
	Timestamp          time.Time
}

type SigninCardState struct {
	RunID          string
	Stage          string
	Status         string
	Message        string
	SiteURL        string
	CurrentURL     string
	QRCodeURL      string
	QRCodeKind     string
	ScreenshotPath string
	HTMLPath       string
	RefreshCount   int
	CookieCount    int
	Duration       time.Duration
	DryRun         bool
}

type RightSigninStatus string

const (
	RightSigninStatusStarting        RightSigninStatus = "starting"
	RightSigninStatusChecking        RightSigninStatus = "checking"
	RightSigninStatusLoginRequired   RightSigninStatus = "login_required"
	RightSigninStatusLoginWaiting    RightSigninStatus = "login_waiting_confirm"
	RightSigninStatusLoginSuccess    RightSigninStatus = "login_success"
	RightSigninStatusSigning         RightSigninStatus = "signing"
	RightSigninStatusProgressWarning RightSigninStatus = "progress_warning"
	RightSigninStatusFailed          RightSigninStatus = "failed"
	RightSigninStatusFinished        RightSigninStatus = "finished"
)

type sendCardRequest struct {
	AppID               string         `json:"appId"`
	ReceiveIDType       string         `json:"receiveIdType,omitempty"`
	ReceiveID           string         `json:"receiveId,omitempty"`
	MessageID           string         `json:"messageId,omitempty"`
	TemplateID          string         `json:"templateId"`
	TemplateVersionName string         `json:"templateVersionName,omitempty"`
	TemplateVariable    map[string]any `json:"templateVariable"`
}

type gatewayCardResponse struct {
	Success bool   `json:"success"`
	Error   string `json:"error"`
	Data    struct {
		MessageID      string `json:"messageId"`
		MessageIDSnake string `json:"message_id"`
	} `json:"data"`
}

type notificationConfigJSON struct {
	Enabled             *bool                `json:"enabled"`
	GatewayBaseURL      string               `json:"gatewayBaseUrl"`
	GatewayAuthToken    string               `json:"gatewayAuthToken"`
	AppID               string               `json:"appId"`
	TemplateID          string               `json:"templateId"`
	TemplateVersionName string               `json:"templateVersionName"`
	ReceiveIDType       string               `json:"receiveIdType"`
	ReceiveID           string               `json:"receiveId"`
	AppName             string               `json:"appName"`
	OpenID              string               `json:"openId"`
	DefaultURL          string               `json:"defaultUrl"`
	ProgressSeconds     any                  `json:"progressNotifySeconds"`
	Card                notificationCardJSON `json:"card"`
}

type notificationCardJSON struct {
	OpenID       string `json:"openId"`
	SubButtonURL string `json:"subButtonUrl"`
}

func NewCardConfigFromEnv(getEnv func(string) string) CardConfig {
	configJSON := strings.TrimSpace(getEnv(NotificationConfigEnv))
	if configJSON == "" {
		return CardConfig{
			Enabled: false,
		}.normalize()
	}

	var raw notificationConfigJSON
	decoder := json.NewDecoder(strings.NewReader(configJSON))
	decoder.UseNumber()
	if err := decoder.Decode(&raw); err != nil {
		return CardConfig{
			Enabled:     true,
			ConfigError: fmt.Sprintf("%s invalid JSON: %v", NotificationConfigEnv, err),
		}.normalize()
	}

	enabled := true
	if raw.Enabled != nil {
		enabled = *raw.Enabled
	}
	cfg := CardConfig{
		Enabled:             enabled,
		GatewayBaseURL:      raw.GatewayBaseURL,
		GatewayAuthToken:    raw.GatewayAuthToken,
		AppID:               raw.AppID,
		TemplateID:          raw.TemplateID,
		TemplateVersionName: raw.TemplateVersionName,
		ReceiveIDType:       raw.ReceiveIDType,
		ReceiveID:           raw.ReceiveID,
		AppName:             firstNonEmpty(raw.AppName, DefaultRightSigninAppName),
		OpenID:              firstNonEmpty(raw.OpenID, raw.Card.OpenID),
		DefaultURL:          firstNonEmpty(raw.DefaultURL, raw.Card.SubButtonURL, DefaultRightSigninURL),
		ProgressNotifyEvery: parseProgressNotifyEvery(raw.ProgressSeconds),
	}
	return cfg.normalize()
}

func NewFeishuCardNotifier(config CardConfig) *FeishuCardNotifier {
	return &FeishuCardNotifier{
		config: config.normalize(),
		client: httputil.NewClient(10 * time.Second),
	}
}

func (n *FeishuCardNotifier) Notify(ctx context.Context, msg Message) error {
	if n == nil {
		return nil
	}
	card := CardMessage{
		AppName:            firstNonEmpty(msg.App, DefaultRightSigninAppName),
		Title:              plainText(msg.Title),
		SubTitle:           "运行状态更新",
		TitleStyle:         "blue",
		Content:            msg.Msg,
		Foot:               "后续状态会继续更新在这张卡片。",
		MainButtonText:     "自动执行中",
		MainButtonDisabled: true,
		SubButtonText:      "打开链接",
		SubButtonURL:       msg.TargetURL,
		Status:             "message",
		Action:             "right-signin",
		Timestamp:          time.Now(),
	}
	return n.Upsert(ctx, card)
}

func (n *FeishuCardNotifier) Upsert(ctx context.Context, message CardMessage) error {
	if n == nil {
		return nil
	}
	n.mu.Lock()
	defer n.mu.Unlock()

	if !n.config.Enabled {
		return nil
	}
	if err := n.config.validate(); err != nil {
		n.logDisabledOnce(err)
		return nil
	}

	message = n.config.applyMessageDefaults(message)
	if n.messageID != "" {
		if _, err := n.callGateway(ctx, n.messageID, message); err == nil {
			return nil
		} else {
			log.Printf("FeishuCardNotify update err:%v, message_id:%s", err, n.messageID)
			n.messageID = ""
		}
	}

	messageID, err := n.callGateway(ctx, "", message)
	if err != nil {
		log.Printf("FeishuCardNotify send err:%v, title:%s", err, message.Title)
		return nil
	}
	n.messageID = messageID
	return nil
}

func (n *FeishuCardNotifier) NotifyProgress(ctx context.Context, message CardMessage) error {
	if n == nil {
		return nil
	}
	now := time.Now()
	n.mu.Lock()
	notifyEvery := n.config.ProgressNotifyEvery
	if notifyEvery <= 0 {
		notifyEvery = DefaultProgressNotifyEvery
	}
	if !n.lastProgressAt.IsZero() && now.Sub(n.lastProgressAt) < notifyEvery {
		n.mu.Unlock()
		return nil
	}
	n.lastProgressAt = now
	n.mu.Unlock()

	return n.Upsert(ctx, message)
}

func (n *FeishuCardNotifier) callGateway(ctx context.Context, messageID string, message CardMessage) (string, error) {
	payload := sendCardRequest{
		AppID:               n.config.AppID,
		ReceiveIDType:       n.config.ReceiveIDType,
		ReceiveID:           n.config.ReceiveID,
		MessageID:           messageID,
		TemplateID:          n.config.TemplateID,
		TemplateVersionName: n.config.TemplateVersionName,
		TemplateVariable:    message.toTemplateVariable(),
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, n.config.gatewayEndpoint(), bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+n.config.GatewayAuthToken)
	req.Header.Set("Content-Type", "application/json")

	resp, err := n.client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return "", err
	}
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return "", fmt.Errorf("gateway status %d: %s", resp.StatusCode, strings.TrimSpace(string(respBody)))
	}

	var gatewayResp gatewayCardResponse
	if err := json.Unmarshal(respBody, &gatewayResp); err != nil {
		return "", fmt.Errorf("decode gateway response: %w, body:%s", err, strings.TrimSpace(string(respBody)))
	}
	if !gatewayResp.Success {
		return "", fmt.Errorf("gateway rejected card: %s", gatewayResp.Error)
	}
	if messageID != "" {
		return messageID, nil
	}
	messageID = firstNonEmpty(gatewayResp.Data.MessageID, gatewayResp.Data.MessageIDSnake)
	if messageID == "" {
		return "", fmt.Errorf("gateway response missing messageId")
	}
	return messageID, nil
}

func (n *FeishuCardNotifier) logDisabledOnce(err error) {
	if n.disabledLogged {
		return
	}
	n.disabledLogged = true
	if n.config.Enabled || n.config.hasAnyConfig() {
		log.Printf("FeishuCardNotify disabled: %v", err)
	}
}

func BuildRightSigninCard(status RightSigninStatus, state SigninCardState) CardMessage {
	state = state.normalize()
	card := CardMessage{
		AppName:            DefaultRightSigninAppName,
		Title:              "签到任务准备开始",
		SubTitle:           "正在启动浏览器",
		TitleStyle:         "blue",
		Content:            buildRightSigninContent(state),
		Foot:               "后续登录、签到和异常留证都会持续更新在这张卡片。",
		MainButtonText:     "自动执行中",
		MainButtonDisabled: true,
		SubButtonText:      "打开恩山论坛",
		SubButtonURL:       state.SiteURL,
		Status:             string(status),
		Action:             "right-signin",
		Timestamp:          time.Now(),
	}

	switch status {
	case RightSigninStatusChecking:
		card.Title = "正在检查登录态"
		card.SubTitle = "正在恢复 Cookie 并识别页面状态"
		card.TitleStyle = "blue"
		card.Foot = "如果登录态失效，会自动推送 QQ 扫码入口并继续更新这张卡片。"
	case RightSigninStatusLoginRequired:
		card.Title = "需要扫码登录"
		if state.RefreshCount > 0 {
			card.Title = "登录二维码已刷新"
		}
		card.SubTitle = "请打开二维码完成 QQ 登录"
		card.TitleStyle = "orange"
		card.Foot = "二维码失效后会自动刷新并更新这张卡片，扫码完成后任务会继续执行。"
		card.MainButtonText = "等待扫码"
		card.SubButtonText = "打开登录二维码"
		card.SubButtonURL = state.QRCodeURL
		card.SubButtonDisabled = state.QRCodeURL == ""
	case RightSigninStatusLoginWaiting:
		card.Title = "已扫码，等待确认"
		card.SubTitle = "请在 QQ 客户端确认授权"
		card.TitleStyle = "wathet"
		card.Foot = "确认完成后会自动回到签到页继续执行；如果二维码失效，卡片会刷新为新的扫码入口。"
		card.MainButtonText = "等待确认"
		card.SubButtonText = "打开登录二维码"
		card.SubButtonURL = state.QRCodeURL
		card.SubButtonDisabled = state.QRCodeURL == ""
	case RightSigninStatusLoginSuccess:
		card.Title = "登录成功"
		card.SubTitle = "正在回到签到页"
		card.TitleStyle = "turquoise"
		card.Foot = "登录态已确认，正在继续执行签到流程。"
	case RightSigninStatusSigning:
		card.Title = "正在执行签到"
		card.SubTitle = "已进入签到页"
		card.TitleStyle = "wathet"
		card.Foot = "正在识别签到按钮和页面状态，异常时会自动截图留证。"
	case RightSigninStatusProgressWarning:
		card.Title = "签到进度异常"
		card.SubTitle = "已记录当前页面状态"
		card.TitleStyle = "orange"
		card.Foot = "程序会继续收敛到明确结果；当前阶段和页面信息已保留在卡片中。"
		card.MainButtonText = "检查中"
	case RightSigninStatusFailed:
		card.Title = "签到失败"
		card.SubTitle = resultSubtitle(state)
		card.TitleStyle = "red"
		card.Foot = "请检查登录态、页面结构、网络和 GitHub Secrets 配置。"
		card.MainButtonText = "已失败"
	case RightSigninStatusFinished:
		card.Title = resultTitle(state)
		card.SubTitle = resultSubtitle(state)
		card.TitleStyle = resultTitleStyle(state)
		card.Foot = "本次任务已结束，卡片不会继续更新。"
		card.MainButtonText = "已完成"
	}
	return card
}

func (c CardConfig) normalize() CardConfig {
	c.GatewayBaseURL = strings.TrimSpace(c.GatewayBaseURL)
	c.GatewayAuthToken = strings.TrimSpace(c.GatewayAuthToken)
	c.AppID = strings.TrimSpace(c.AppID)
	c.TemplateID = strings.TrimSpace(c.TemplateID)
	c.TemplateVersionName = strings.TrimSpace(c.TemplateVersionName)
	c.ReceiveIDType = strings.TrimSpace(c.ReceiveIDType)
	c.ReceiveID = strings.TrimSpace(c.ReceiveID)
	c.AppName = firstNonEmpty(c.AppName, DefaultRightSigninAppName)
	c.OpenID = strings.TrimSpace(c.OpenID)
	c.DefaultURL = firstNonEmpty(c.DefaultURL, DefaultRightSigninURL)
	if c.ProgressNotifyEvery <= 0 {
		c.ProgressNotifyEvery = DefaultProgressNotifyEvery
	}
	return c
}

func (c CardConfig) validate() error {
	if c.ConfigError != "" {
		return fmt.Errorf("%s", c.ConfigError)
	}
	var missing []string
	if c.GatewayBaseURL == "" {
		missing = append(missing, "gatewayBaseUrl")
	}
	if c.GatewayAuthToken == "" {
		missing = append(missing, "gatewayAuthToken")
	}
	if c.AppID == "" {
		missing = append(missing, "appId")
	}
	if c.TemplateID == "" {
		missing = append(missing, "templateId")
	}
	if len(missing) > 0 {
		return fmt.Errorf("%s missing %s", NotificationConfigEnv, strings.Join(missing, ", "))
	}
	return nil
}

func (c CardConfig) hasAnyConfig() bool {
	return c.ConfigError != "" || c.GatewayBaseURL != "" || c.GatewayAuthToken != "" || c.AppID != "" || c.TemplateID != ""
}

func (c CardConfig) gatewayEndpoint() string {
	base := strings.TrimRight(c.GatewayBaseURL, "/")
	if strings.HasSuffix(base, "/send_card") {
		return base
	}
	return base + "/send_card"
}

func (c CardConfig) applyMessageDefaults(message CardMessage) CardMessage {
	if c.AppName != "" {
		message.AppName = c.AppName
	} else if message.AppName == "" {
		message.AppName = DefaultRightSigninAppName
	}
	if message.OpenID == "" {
		message.OpenID = c.OpenID
	}
	if message.SubButtonURL == "" {
		message.SubButtonURL = c.DefaultURL
	}
	if message.SubButtonText == "" {
		message.SubButtonText = "打开恩山论坛"
	}
	if message.TitleStyle == "" {
		message.TitleStyle = "blue"
	}
	if message.MainButtonText == "" {
		message.MainButtonText = "自动执行中"
	}
	if message.Timestamp.IsZero() {
		message.Timestamp = time.Now()
	}
	return message
}

func (m CardMessage) toTemplateVariable() map[string]any {
	return map[string]any{
		"app_name":          m.AppName,
		"appName":           m.AppName,
		"title":             m.Title,
		"sub_title":         m.SubTitle,
		"subTitle":          m.SubTitle,
		"title_style":       m.TitleStyle,
		"titleStyle":        m.TitleStyle,
		"content":           withMention(m.OpenID, m.Content),
		"foot":              m.Foot,
		"main_button_text":  m.MainButtonText,
		"mainButtonText":    m.MainButtonText,
		"main_button":       m.MainButtonDisabled,
		"mainButton":        m.MainButtonDisabled,
		"main_button_event": normalizeMainButtonEvent(m.MainButtonEvent),
		"mainButtonEvent":   normalizeMainButtonEvent(m.MainButtonEvent),
		"sub_button_text":   m.SubButtonText,
		"subButtonText":     m.SubButtonText,
		"sub_button":        m.SubButtonDisabled || m.SubButtonURL == "",
		"subButton":         m.SubButtonDisabled || m.SubButtonURL == "",
		"sub_button_url":    m.SubButtonURL,
		"subButtonUrl":      m.SubButtonURL,
		"open_id":           m.OpenID,
		"openId":            m.OpenID,
		"status":            m.Status,
		"action":            m.Action,
		"timestamp":         m.Timestamp.Format(time.RFC3339),
	}
}

func (s SigninCardState) normalize() SigninCardState {
	s.RunID = firstNonEmpty(s.RunID, "-")
	s.Stage = firstNonEmpty(s.Stage, "-")
	s.Status = firstNonEmpty(s.Status, "-")
	s.Message = firstNonEmpty(s.Message, "任务正在执行")
	s.SiteURL = firstNonEmpty(s.SiteURL, DefaultRightSigninURL)
	return s
}

func buildRightSigninContent(state SigninCardState) string {
	lines := []string{
		fmt.Sprintf("**运行ID**：%s", state.RunID),
		fmt.Sprintf("**阶段**：%s", state.Stage),
	}
	if state.DryRun {
		lines = append(lines, "**运行模式**：dry-run")
	}
	if state.Status != "-" {
		lines = append(lines, fmt.Sprintf("**当前状态**：%s", state.Status))
	}
	if state.Message != "" {
		lines = append(lines, fmt.Sprintf("**说明**：%s", state.Message))
	}
	if state.CookieCount > 0 {
		lines = append(lines, fmt.Sprintf("**已加载 Cookie**：%d 条", state.CookieCount))
	}
	if state.RefreshCount > 0 {
		lines = append(lines, fmt.Sprintf("**二维码刷新次数**：%d", state.RefreshCount))
	}
	if state.QRCodeKind != "" {
		lines = append(lines, fmt.Sprintf("**二维码类型**：%s", state.QRCodeKind))
	}
	if state.Duration > 0 {
		lines = append(lines, fmt.Sprintf("**运行耗时**：%s", formatDuration(state.Duration)))
	}
	if state.CurrentURL != "" && state.CurrentURL != state.SiteURL {
		lines = append(lines, fmt.Sprintf("**当前页面**：%s", truncateText(state.CurrentURL, 160)))
	}
	if state.ScreenshotPath != "" {
		lines = append(lines, fmt.Sprintf("**截图留证**：`%s`", state.ScreenshotPath))
	}
	if state.HTMLPath != "" {
		lines = append(lines, fmt.Sprintf("**HTML 留证**：`%s`", state.HTMLPath))
	}
	return strings.Join(lines, "\n")
}

func resultTitle(state SigninCardState) string {
	switch state.Status {
	case "success":
		return "签到成功"
	case "already_signed":
		return "今日已签到"
	case "dry_run":
		return "dry-run 检测完成"
	default:
		return "签到完成"
	}
}

func resultSubtitle(state SigninCardState) string {
	if state.Message != "" && state.Message != "任务正在执行" {
		return truncateText(state.Message, 60)
	}
	if state.Status != "" && state.Status != "-" {
		return state.Status
	}
	return "任务已结束"
}

func resultTitleStyle(state SigninCardState) string {
	switch state.Status {
	case "success", "already_signed":
		return "green"
	case "dry_run":
		return "purple"
	default:
		return "blue"
	}
}

func withMention(openID, content string) string {
	openID = strings.TrimSpace(openID)
	if openID == "" {
		return content
	}
	return fmt.Sprintf("<at id=\"%s\"></at>\n\n%s", openID, content)
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func normalizeMainButtonEvent(value any) any {
	if isEmptyMainButtonEvent(value) {
		return map[string]any{
			"action": "noop",
			"source": "right-signin",
		}
	}
	switch v := value.(type) {
	case json.Number, float64, bool, map[string]any, []any:
		return v
	case string:
		trimmed := strings.TrimSpace(v)
		if trimmed == "" {
			return map[string]any{
				"action": "noop",
				"source": "right-signin",
			}
		}
		var parsed any
		decoder := json.NewDecoder(strings.NewReader(trimmed))
		decoder.UseNumber()
		if err := decoder.Decode(&parsed); err == nil {
			return parsed
		}
		return map[string]any{
			"action": trimmed,
			"source": "right-signin",
		}
	default:
		return v
	}
}

func isEmptyMainButtonEvent(value any) bool {
	if value == nil {
		return true
	}
	if text, ok := value.(string); ok {
		return strings.TrimSpace(text) == ""
	}
	return false
}

func parseProgressNotifyEvery(value any) time.Duration {
	if value == nil {
		return DefaultProgressNotifyEvery
	}
	var seconds int64
	switch v := value.(type) {
	case json.Number:
		parsed, err := v.Int64()
		if err != nil {
			return DefaultProgressNotifyEvery
		}
		seconds = parsed
	case float64:
		seconds = int64(v)
	case int:
		seconds = int64(v)
	case int64:
		seconds = v
	case string:
		parsed, err := strconv.ParseInt(strings.TrimSpace(v), 10, 64)
		if err != nil {
			return DefaultProgressNotifyEvery
		}
		seconds = parsed
	default:
		return DefaultProgressNotifyEvery
	}
	if seconds <= 0 {
		return DefaultProgressNotifyEvery
	}
	return time.Duration(seconds) * time.Second
}

func formatDuration(d time.Duration) string {
	if d <= 0 {
		return "-"
	}
	d = d.Round(time.Second)
	h := int(d / time.Hour)
	d -= time.Duration(h) * time.Hour
	m := int(d / time.Minute)
	d -= time.Duration(m) * time.Minute
	s := int(d / time.Second)
	if h > 0 {
		return fmt.Sprintf("%d小时%d分%d秒", h, m, s)
	}
	if m > 0 {
		return fmt.Sprintf("%d分%d秒", m, s)
	}
	return fmt.Sprintf("%d秒", s)
}

func truncateText(text string, limit int) string {
	text = strings.TrimSpace(text)
	if limit <= 0 {
		return text
	}
	runes := []rune(text)
	if len(runes) <= limit {
		return text
	}
	return string(runes[:limit]) + "..."
}

func plainText(text string) string {
	replacer := strings.NewReplacer(
		"<font color=\"red\">", "",
		"<font color=\"green\">", "",
		"<font color=\"blue\">", "",
		"<font color=\"purple\">", "",
		"<font color=\"orange\">", "",
		"</font>", "",
		"**", "",
	)
	return strings.TrimSpace(replacer.Replace(text))
}
