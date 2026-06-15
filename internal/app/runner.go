package app

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"right-signin/internal/browser"
	"right-signin/internal/config"
	"right-signin/internal/login"
	"right-signin/internal/model"
	"right-signin/internal/notify"
	"right-signin/internal/session"
	"right-signin/internal/signin"
)

type Runner struct {
	cfg          *config.Config
	notifier     notify.Notifier
	store        session.Store
	authProvider login.Provider
	signinSvc    *signin.Service
}

func NewRunner(cfg *config.Config) (*Runner, error) {
	store := session.Store{
		ProfileDir:  cfg.UserDataDir,
		CookiesPath: cfg.CookiesPath,
		CookiesEnv:  cfg.CookiesEnv,
		GitHubToken: cfg.GitHubToken,
		GitHubRepo:  cfg.GitHubRepo,
		SecretName:  cfg.GitHubSecretName,
	}
	if err := store.Ensure(); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(cfg.RunArtifactDir, 0o755); err != nil {
		return nil, fmt.Errorf("创建运行产物目录失败: %w", err)
	}
	notifier := notify.NewFeishu(cfg.FeishuWebhook)
	return &Runner{
		cfg:          cfg,
		notifier:     notifier,
		store:        store,
		authProvider: login.New(cfg, notifier),
		signinSvc:    signin.New(cfg),
	}, nil
}

func (r *Runner) Run(parent context.Context) error {
	ctx, cancel := context.WithTimeout(parent, r.cfg.Timeout)
	defer cancel()

	start := time.Now()
	log.Printf("开始执行 %s, runID=%s", r.cfg.AppName, r.cfg.RunID)
	sess, err := browser.New(ctx, r.cfg)
	if err != nil {
		return err
	}
	defer sess.Close()
	persistCookies := func(stage string) {
		if err := r.store.SaveCookies(sess.Ctx()); err != nil {
			log.Printf("在 %s 保存 cookies 快照失败: %v", stage, err)
			return
		}
		log.Printf("在 %s 保存 cookies 快照成功: %s", stage, r.cfg.CookiesPath)
	}

	finalResult := model.Result{Status: model.StatusUnknown, ArtifactDir: r.cfg.RunArtifactDir}
	defer func() {
		persistCookies("run-defer")
		if err := r.authProvider.Cleanup(context.Background()); err != nil {
			log.Printf("清空 OpenList 上传目录失败: %v", err)
		}
		finalResult.Duration = time.Since(start)
	}()

	if count, err := r.store.LoadCookies(sess.Ctx()); err != nil {
		log.Printf("加载 cookies 快照失败: %v", err)
	} else if count > 0 {
		log.Printf("已从 %s 加载 %d 条 cookies", r.cfg.CookiesPath, count)
	}

	if err := sess.Navigate(r.cfg.SignInURL); err != nil {
		finalResult.Status = model.StatusNetworkError
		finalResult.Message = "打开签到页失败"
		_ = r.captureAndNotify(ctx, sess, &finalResult, "open-signin-page")
		return err
	}

	inspect, err := r.signinSvc.Inspect(sess)
	if err != nil {
		finalResult.Status = model.StatusFailure
		finalResult.Message = "初始页面识别失败"
		_ = r.captureAndNotify(ctx, sess, &finalResult, "initial-inspect")
		return err
	}
	if inspect.Status == model.StatusNeedLogin {
		loginResult, err := r.authProvider.EnsureLoggedIn(ctx, sess)
		finalResult = loginResult
		if err != nil {
			_ = r.captureAndNotify(ctx, sess, &finalResult, "login-failed")
			return err
		}
		if loginResult.Status == model.StatusRiskControl || loginResult.Status == model.StatusLoginTimeout || loginResult.Status == model.StatusQRCodeExpiredTooMany {
			_ = r.captureAndNotify(ctx, sess, &finalResult, "login-stopped")
			return errors.New(loginResult.Message)
		}
		if err := sess.Navigate(r.cfg.SignInURL); err != nil {
			finalResult.Status = model.StatusNetworkError
			finalResult.Message = "登录成功后回到签到页失败"
			_ = r.captureAndNotify(ctx, sess, &finalResult, "navigate-back")
			return err
		}
		persistCookies("post-login-navigate")
	}

	result, err := r.signinSvc.Execute(sess, r.cfg.DryRun)
	finalResult = result
	persistCookies("post-signin")
	if err != nil {
		_ = r.captureAndNotify(ctx, sess, &finalResult, "signin-failed")
		return err
	}

	if finalResult.Status == model.StatusFailure || finalResult.Status == model.StatusPageChanged || finalResult.Status == model.StatusRiskControl || finalResult.Status == model.StatusNetworkError {
		_ = r.captureAndNotify(ctx, sess, &finalResult, "final-error")
		return errors.New(finalResult.Message)
	}

	if finalResult.Status == model.StatusUnknown {
		finalResult.Status = model.StatusPageChanged
		finalResult.Message = "最终状态未知，按页面变化处理"
		_ = r.captureAndNotify(ctx, sess, &finalResult, "unknown")
		return errors.New(finalResult.Message)
	}

	if r.cfg.NotifySuccess && finalResult.SuccessLike() {
		_ = r.captureAndNotify(ctx, sess, &finalResult, "success")
	}
	log.Printf("执行结束，状态=%s, message=%s", finalResult.Status, finalResult.Message)
	return nil
}

func (r *Runner) captureAndNotify(ctx context.Context, sess *browser.Session, result *model.Result, prefix string) error {
	base := filepath.Join(r.cfg.RunArtifactDir, prefix)
	screenshot, html, err := sess.Snapshot(base)
	if err != nil {
		log.Printf("保存运行留证失败: %v", err)
	} else {
		result.ScreenshotPath = screenshot
		result.HTMLPath = html
	}
	currentURL, _ := sess.CurrentURL()
	if result.URL == "" {
		result.URL = currentURL
	}
	msg := notify.Message{
		App:   r.cfg.AppName,
		Title: titleForStatus(result.Status),
		Msg: strings.Join([]string{
			"运行ID: " + r.cfg.RunID,
			"阶段: " + prefix,
			"状态: " + string(result.Status),
			"原因: " + result.Message,
			optionalLine("二维码类型", result.QRCodeKind),
			optionalLine("二维码刷新次数", fmt.Sprintf("%d", result.RefreshCount)),
		}, "\n"),
	}
	return r.notifier.Notify(ctx, msg)
}

func titleForStatus(status model.Status) string {
	switch status {
	case model.StatusSuccess:
		return "签到成功"
	case model.StatusAlreadySigned:
		return "今日已签到"
	case model.StatusDryRun:
		return "dry-run 检测完成"
	case model.StatusRiskControl:
		return "检测到风控/验证码"
	case model.StatusLoginTimeout, model.StatusQRCodeExpiredTooMany:
		return "登录失败"
	case model.StatusNetworkError:
		return "网络异常"
	case model.StatusPageChanged:
		return "页面结构变化"
	default:
		return "签到失败"
	}
}

func optionalLine(label, value string) string {
	if value == "" {
		return ""
	}
	return label + ": " + value
}

func chooseTarget(qrURL, url string) string {
	if qrURL != "" {
		return qrURL
	}
	return url
}
