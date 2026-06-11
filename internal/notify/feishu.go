package notify

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

type FeishuNotifier struct {
	webhook string
	client  *http.Client
}

func NewFeishu(webhook string) Notifier {
	if webhook == "" {
		return NewNoop()
	}
	return &FeishuNotifier{
		webhook: webhook,
		client:  &http.Client{Timeout: 15 * time.Second},
	}
}

func (f *FeishuNotifier) Notify(ctx context.Context, msg Message) error {
	body, err := json.Marshal(msg)
	if err != nil {
		return fmt.Errorf("序列化通知失败: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, f.webhook, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("创建通知请求失败: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := f.client.Do(req)
	if err != nil {
		return fmt.Errorf("发送通知失败: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return fmt.Errorf("发送通知失败，状态码=%d", resp.StatusCode)
	}
	return nil
}
