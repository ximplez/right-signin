package model

import "time"

type Status string

const (
	StatusUnknown                Status = "unknown"
	StatusReadyToSign            Status = "ready_to_sign"
	StatusSuccess                Status = "success"
	StatusAlreadySigned          Status = "already_signed"
	StatusNeedLogin              Status = "need_login"
	StatusLoginTimeout           Status = "login_timeout"
	StatusQRCodeExpiredTooMany   Status = "qrcode_expired_too_many_times"
	StatusRiskControl            Status = "risk_control"
	StatusNetworkError           Status = "network_error"
	StatusPageChanged            Status = "page_changed"
	StatusFailure                Status = "failure"
	StatusDryRun                 Status = "dry_run"
)

type Result struct {
	Status         Status
	Message        string
	URL            string
	QRCodeURL      string
	QRCodeKind     string
	QRCodeFilePath string
	ArtifactDir    string
	ScreenshotPath string
	HTMLPath       string
	RefreshCount   int
	Duration       time.Duration
}

func (r Result) SuccessLike() bool {
	return r.Status == StatusSuccess || r.Status == StatusAlreadySigned || r.Status == StatusDryRun
}
