package notify

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestFeishuCardNotifierUpsertSendsThenUpdates(t *testing.T) {
	var requests []sendCardRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/send_card" {
			t.Fatalf("path = %s, want /send_card", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer token" {
			t.Fatalf("Authorization = %s", got)
		}
		var req sendCardRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		requests = append(requests, req)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"success": true,
			"data": map[string]any{
				"messageId": "om_test",
			},
		})
	}))
	defer server.Close()

	notifier := NewFeishuCardNotifier(CardConfig{
		Enabled:          true,
		GatewayBaseURL:   server.URL,
		GatewayAuthToken: "token",
		AppID:            "cli_test",
		TemplateID:       "ctp_test",
		AppName:          "测试签到",
	})
	err := notifier.Upsert(t.Context(), BuildRightSigninCard(RightSigninStatusStarting, SigninCardState{
		RunID:   "run-1",
		Stage:   "start",
		Status:  "unknown",
		Message: "任务已启动",
	}))
	if err != nil {
		t.Fatalf("first Upsert err = %v", err)
	}
	err = notifier.Upsert(t.Context(), BuildRightSigninCard(RightSigninStatusFinished, SigninCardState{
		RunID:    "run-1",
		Stage:    "success",
		Status:   "success",
		Message:  "签到成功",
		Duration: time.Minute,
	}))
	if err != nil {
		t.Fatalf("second Upsert err = %v", err)
	}

	if len(requests) != 2 {
		t.Fatalf("request count = %d, want 2", len(requests))
	}
	if requests[0].MessageID != "" {
		t.Fatalf("first request messageId = %s, want empty", requests[0].MessageID)
	}
	if requests[1].MessageID != "om_test" {
		t.Fatalf("second request messageId = %s, want om_test", requests[1].MessageID)
	}
	if got := requests[1].TemplateVariable["app_name"]; got != "测试签到" {
		t.Fatalf("app_name = %v, want 测试签到", got)
	}
	if got := requests[1].TemplateVariable["title"]; got != "签到成功" {
		t.Fatalf("title = %v, want 签到成功", got)
	}
}

func TestBuildRightSigninLoginCardUsesQRCodeButton(t *testing.T) {
	card := BuildRightSigninCard(RightSigninStatusLoginRequired, SigninCardState{
		RunID:        "run-1",
		Stage:        "qq-login",
		Status:       "need_login",
		Message:      "需要扫码",
		QRCodeURL:    "https://example.com/qrcode",
		QRCodeKind:   "image",
		RefreshCount: 1,
	})
	vars := card.toTemplateVariable()

	if got := vars["title"]; got != "登录二维码已刷新" {
		t.Fatalf("title = %v, want 登录二维码已刷新", got)
	}
	if got := vars["sub_button_text"]; got != "打开登录二维码" {
		t.Fatalf("sub_button_text = %v, want 打开登录二维码", got)
	}
	if got := vars["sub_button_url"]; got != "https://example.com/qrcode" {
		t.Fatalf("sub_button_url = %v, want qrcode URL", got)
	}
	if got := vars["title_style"]; got != "orange" {
		t.Fatalf("title_style = %v, want orange", got)
	}
	if got := vars["sub_button"]; got != false {
		t.Fatalf("sub_button = %v, want false", got)
	}
}

func TestBuildRightSigninFinishedCardIncludesSummary(t *testing.T) {
	card := BuildRightSigninCard(RightSigninStatusFinished, SigninCardState{
		RunID:          "run-1",
		Stage:          "success",
		Status:         "success",
		Message:        "签到成功",
		ScreenshotPath: "runtime/artifacts/success.png",
		HTMLPath:       "runtime/artifacts/success.html",
		Duration:       75 * time.Second,
	})

	for _, want := range []string{
		"**运行ID**：run-1",
		"**阶段**：success",
		"**当前状态**：success",
		"**说明**：签到成功",
		"**运行耗时**：1分15秒",
		"**截图留证**：`runtime/artifacts/success.png`",
		"**HTML 留证**：`runtime/artifacts/success.html`",
	} {
		if !strings.Contains(card.Content, want) {
			t.Fatalf("card.Content missing %q:\n%s", want, card.Content)
		}
	}
}

func TestNewCardConfigFromEnvParsesNotificationConfigJSON(t *testing.T) {
	env := map[string]string{
		NotificationConfigEnv: `{
			"gatewayBaseUrl": "https://gateway.example.com",
			"gatewayAuthToken": "token",
			"appId": "cli_test",
			"templateId": "ctp_test",
			"templateVersionName": "1.0.0",
			"receiveIdType": "email",
			"receiveId": "name@example.com",
			"appName": "恩山论坛自动签到",
			"openId": "ou_test",
			"defaultUrl": "https://www.right.com.cn/forum/erling_qd-sign_in.html",
			"progressNotifySeconds": "30"
		}`,
	}

	cfg := NewCardConfigFromEnv(func(key string) string {
		return env[key]
	})

	if !cfg.Enabled {
		t.Fatal("Enabled = false, want true")
	}
	if cfg.GatewayBaseURL != "https://gateway.example.com" {
		t.Fatalf("GatewayBaseURL = %s", cfg.GatewayBaseURL)
	}
	if cfg.ProgressNotifyEvery != 30*time.Second {
		t.Fatalf("ProgressNotifyEvery = %s, want 30s", cfg.ProgressNotifyEvery)
	}
}

func TestTemplateVariableNormalizesEmptyMainButtonEvent(t *testing.T) {
	card := BuildRightSigninCard(RightSigninStatusStarting, SigninCardState{
		RunID:   "run-1",
		Stage:   "start",
		Status:  "unknown",
		Message: "任务已启动",
	})
	vars := card.toTemplateVariable()

	event, ok := vars["main_button_event"].(map[string]any)
	if !ok {
		t.Fatalf("main_button_event = %T, want object", vars["main_button_event"])
	}
	if event["action"] != "noop" {
		t.Fatalf("main_button_event.action = %v, want noop", event["action"])
	}
	if event["source"] != "right-signin" {
		t.Fatalf("main_button_event.source = %v, want right-signin", event["source"])
	}
}
