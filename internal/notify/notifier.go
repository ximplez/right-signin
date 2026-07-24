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

type CardNotifier interface {
	Notifier
	Upsert(ctx context.Context, msg CardMessage) error
	NotifyProgress(ctx context.Context, msg CardMessage) error
}

type noopNotifier struct{}

func (noopNotifier) Notify(context.Context, Message) error             { return nil }
func (noopNotifier) Upsert(context.Context, CardMessage) error         { return nil }
func (noopNotifier) NotifyProgress(context.Context, CardMessage) error { return nil }

func NewNoop() Notifier { return noopNotifier{} }
