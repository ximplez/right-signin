package notify

import "context"

type Message struct {
	App       string `json:"app"`
	Title     string `json:"title"`
	Msg       string `json:"msg"`
	TargetURL string `json:"targetUrl,omitempty"`
}

type Notifier interface {
	Notify(ctx context.Context, msg Message) error
}

type noopNotifier struct{}

func (noopNotifier) Notify(context.Context, Message) error { return nil }

func NewNoop() Notifier { return noopNotifier{} }
