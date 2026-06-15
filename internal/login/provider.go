package login

import (
	"context"
	"net/http"
	"regexp"
	"time"

	"right-signin/internal/browser"
	"right-signin/internal/config"
	"right-signin/internal/httputil"
	"right-signin/internal/model"
	"right-signin/internal/notify"
	"right-signin/internal/openlist"
)

type Provider interface {
	EnsureLoggedIn(context.Context, *browser.Session) (model.Result, error)
	Cleanup(context.Context) error
}

type QQAuthenticator struct {
	cfg           *config.Config
	notifier      notify.Notifier
	stateDetector *LoginStateDetector
	httpClient    *http.Client
	openList      *openlist.Client
}

type QRCodeInfo struct {
	Raw         string `json:"raw"`
	Kind        string `json:"kind"`
	PreviewURL  string `json:"preview_url"`
	ViewerURL   string `json:"viewer_url,omitempty"`
	ImageURL    string `json:"image_url,omitempty"`
	IframeSrc   string `json:"iframe_src,omitempty"`
	IframeURL   string `json:"iframe_url,omitempty"`
	DisplayHash string `json:"display_hash,omitempty"`
	ImageHash   string `json:"image_hash,omitempty"`
	DisplayPath string `json:"display_path,omitempty"`
	ImagePath   string `json:"image_path,omitempty"`
	MetaPath    string `json:"meta_path,omitempty"`
	PageShot    string `json:"page_shot,omitempty"`
	CurrentURL  string `json:"current_url,omitempty"`
	CurrentPage string `json:"current_page_title,omitempty"`
}

var (
	iframeQRPattern = regexp.MustCompile(`(?i)(?:src|data-src)=["']([^"']*(?:ptqrshow|qrshow|qrcode)[^"']*)["']`)
)

func New(cfg *config.Config, notifier notify.Notifier) Provider {
	return NewQQAuthenticator(cfg, notifier)
}

func NewQQAuthenticator(cfg *config.Config, notifier notify.Notifier) *QQAuthenticator {
	return &QQAuthenticator{
		cfg:           cfg,
		notifier:      notifier,
		stateDetector: NewLoginStateDetector(),
		httpClient:    httputil.NewClient(20 * time.Second),
		openList:      openlist.New(cfg.OpenListBaseURL, cfg.OpenListToken, cfg.OpenListUploadDir),
	}
}

func (s *QQAuthenticator) Cleanup(ctx context.Context) error {
	return s.CleanupOpenListUploadDir(ctx)
}
