package login

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"right-signin/internal/browser"
	"right-signin/internal/classify"
	"right-signin/internal/config"
	"right-signin/internal/model"
	"right-signin/internal/notify"
)

type Service struct {
	cfg      *config.Config
	notifier notify.Notifier
}

type QRCodeInfo struct {
	Raw         string `json:"raw"`
	Kind        string `json:"kind"`
	PreviewURL  string `json:"preview_url"`
	ImagePath   string `json:"image_path,omitempty"`
	MetaPath    string `json:"meta_path,omitempty"`
	PageShot    string `json:"page_shot,omitempty"`
	CurrentURL  string `json:"current_url,omitempty"`
	CurrentPage string `json:"current_page_title,omitempty"`
}

func New(cfg *config.Config, notifier notify.Notifier) *Service {
	return &Service{cfg: cfg, notifier: notifier}
}

func (s *Service) IsLoggedIn(sess *browser.Session) (bool, string, error) {
	summary := sess.Summary()
	log.Printf("登录态检查摘要: url=%s title=%s cookies=%v localStorage=%v sessionStorage=%v body=%s", summary.URL, summary.Title, summary.CookieNames, summary.LocalStorageKeys, summary.SessionStoreKeys, summary.BodySnippet)
	currentURL, err := sess.CurrentURL()
	if err != nil {
		return false, "", err
	}
	body, err := sess.BodyText()
	if err != nil {
		return false, "", err
	}
	status, reason := classify.DetectStatus(body)
	if status == model.StatusNeedLogin {
		return false, reason, nil
	}
	if strings.Contains(currentURL, "connect.php") || strings.Contains(currentURL, "ptlogin2") || strings.Contains(currentURL, "xui.ptlogin2") {
		return false, "当前仍处于第三方登录流程", nil
	}
	if strings.Contains(body, "退出") || strings.Contains(body, "个人中心") || strings.Contains(body, "欢迎您回来") {
		return true, "页面出现已登录特征", nil
	}
	if strings.Contains(currentURL, "right.com.cn") && !strings.Contains(body, "请先登录") && !strings.Contains(body, "立即登录") {
		return true, "目标站点页面未出现登录提示", nil
	}
	names, err := sess.FindCookieNames()
	if err == nil {
		for _, name := range names {
			if strings.Contains(strings.ToLower(name), "auth") || strings.Contains(strings.ToLower(name), "saltkey") || strings.Contains(strings.ToLower(name), "uid") {
				return true, "检测到站点会话 cookie", nil
			}
		}
	}
	return false, "未发现可靠的已登录特征", nil
}

func (s *Service) EnsureLoggedIn(ctx context.Context, sess *browser.Session) (model.Result, error) {
	loggedIn, reason, err := s.IsLoggedIn(sess)
	if err != nil {
		return model.Result{Status: model.StatusFailure, Message: "登录态检测失败"}, err
	}
	if loggedIn {
		return model.Result{Status: model.StatusSuccess, Message: "已复用登录态: " + reason}, nil
	}

	log.Printf("检测到未登录，准备进入 QQ 登录流程: %s", reason)
	if err := s.openLoginEntry(sess); err != nil {
		return model.Result{Status: model.StatusNeedLogin, Message: "打开登录入口失败"}, err
	}
	qqURL, err := s.resolveQQLoginURL(sess)
	if err != nil {
		return model.Result{Status: model.StatusNeedLogin, Message: "未找到 QQ 登录入口"}, err
	}
	if err := sess.Navigate(qqURL); err != nil {
		return model.Result{Status: model.StatusNeedLogin, Message: "跳转 QQ 登录页失败", URL: qqURL}, err
	}
	if err := sess.SleepRandom(800*time.Millisecond, 1500*time.Millisecond); err != nil {
		return model.Result{Status: model.StatusNeedLogin, Message: "等待登录页渲染失败", URL: qqURL}, err
	}
	qqSummary := sess.Summary()
	log.Printf("已跳转 QQ 登录页: url=%s title=%s cookies=%v localStorage=%v sessionStorage=%v body=%s", qqSummary.URL, qqSummary.Title, qqSummary.CookieNames, qqSummary.LocalStorageKeys, qqSummary.SessionStoreKeys, qqSummary.BodySnippet)
	if iframeSrc, err := sess.Attribute("#ptlogin_iframe", "src"); err == nil && iframeSrc != "" {
		iframeURL := sess.ResolveURL(qqSummary.URL, iframeSrc)
		log.Printf("发现 QQ 登录 iframe，准备直接进入 iframe 页面: %s", iframeURL)
		if err := sess.Navigate(iframeURL); err != nil {
			log.Printf("跳转 iframe 登录页失败，保留当前 graph.qq.com 页面继续尝试: %v", err)
		} else {
			iframeSummary := sess.Summary()
			log.Printf("已进入 iframe 登录页: url=%s title=%s cookies=%v localStorage=%v sessionStorage=%v body=%s", iframeSummary.URL, iframeSummary.Title, iframeSummary.CookieNames, iframeSummary.LocalStorageKeys, iframeSummary.SessionStoreKeys, iframeSummary.BodySnippet)
		}
	}
	return s.waitForQRCodeAndLogin(ctx, sess, qqURL)
}

func (s *Service) openLoginEntry(sess *browser.Session) error {
	_ = sess.SleepRandom(300*time.Millisecond, 800*time.Millisecond)
	if err := sess.ClickFirstByText([]string{"立即登录", "登录"}); err != nil {
		return err
	}
	return sess.SleepRandom(800*time.Millisecond, 1400*time.Millisecond)
}

func (s *Service) resolveQQLoginURL(sess *browser.Session) (string, error) {
	currentURL, _ := sess.CurrentURL()
	href, err := sess.FindFirstLink([]string{"QQ登录", "QQ 登录", "QQ"}, []string{"connect.php?mod=login", "qq.com"})
	if err != nil {
		return "", err
	}
	if href == "" {
		href, err = sess.FindFirstLink(nil, []string{"connect.php?mod=login"})
		if err != nil {
			return "", err
		}
	}
	if href == "" {
		return "", fmt.Errorf("页面中未找到 QQ 登录链接")
	}
	return sess.ResolveURL(currentURL, href), nil
}

func (s *Service) waitForQRCodeAndLogin(ctx context.Context, sess *browser.Session, qqAuthURL string) (model.Result, error) {
	qrInfo, err := s.fetchQRCode(sess, 0)
	if err != nil {
		return model.Result{Status: model.StatusNeedLogin, Message: "未提取到登录二维码"}, err
	}
	refreshCount := 0
	resumedAfterScan := false
	if err := s.sendQRCode(ctx, qrInfo, refreshCount); err != nil {
		log.Printf("发送二维码通知失败: %v", err)
	}
	log.Printf("二维码信息: kind=%s raw=%s image=%s meta=%s pageShot=%s", qrInfo.Kind, browser.NormalizeSnippet(qrInfo.Raw, 180), qrInfo.ImagePath, qrInfo.MetaPath, qrInfo.PageShot)
	start := time.Now()
	for time.Since(start) < s.cfg.LoginWaitTimeout {
		if resumed, err := s.resumeQQOAuthIfNeeded(sess, qqAuthURL, resumedAfterScan); err != nil {
			log.Printf("恢复 QQ OAuth 流程失败: %v", err)
		} else if resumed {
			resumedAfterScan = true
		}
		loggedIn, reason, err := s.IsLoggedIn(sess)
		if err == nil && loggedIn {
			return model.Result{Status: model.StatusSuccess, Message: "扫码登录成功: " + reason, QRCodeURL: qrInfo.PreviewURL, QRCodeKind: qrInfo.Kind, QRCodeFilePath: chooseFile(qrInfo.ImagePath, qrInfo.PageShot), RefreshCount: refreshCount}, nil
		}
		if err != nil {
			log.Printf("登录态轮询失败: %v", err)
		}
		if risk, reason, err := s.detectRisk(sess); err == nil && risk {
			return model.Result{Status: model.StatusRiskControl, Message: reason, QRCodeURL: qrInfo.PreviewURL, QRCodeKind: qrInfo.Kind, QRCodeFilePath: chooseFile(qrInfo.ImagePath, qrInfo.PageShot), RefreshCount: refreshCount}, nil
		}
		invalid, reason, err := s.isQRCodeInvalid(sess)
		if err != nil {
			log.Printf("二维码失效检查失败: %v", err)
		} else if invalid {
			if refreshCount >= s.cfg.MaxQRRefresh {
				return model.Result{Status: model.StatusQRCodeExpiredTooMany, Message: "二维码刷新次数超限: " + reason, QRCodeURL: qrInfo.PreviewURL, QRCodeKind: qrInfo.Kind, QRCodeFilePath: chooseFile(qrInfo.ImagePath, qrInfo.PageShot), RefreshCount: refreshCount}, nil
			}
			if err := s.refreshQRCode(sess); err != nil {
				return model.Result{Status: model.StatusFailure, Message: "刷新二维码失败", QRCodeURL: qrInfo.PreviewURL, QRCodeKind: qrInfo.Kind, QRCodeFilePath: chooseFile(qrInfo.ImagePath, qrInfo.PageShot), RefreshCount: refreshCount}, err
			}
			refreshCount++
			qrInfo, err = s.fetchQRCode(sess, refreshCount)
			if err != nil {
				return model.Result{Status: model.StatusFailure, Message: "刷新后重新抓取二维码失败", RefreshCount: refreshCount}, err
			}
			log.Printf("二维码刷新成功: kind=%s raw=%s image=%s meta=%s pageShot=%s", qrInfo.Kind, browser.NormalizeSnippet(qrInfo.Raw, 180), qrInfo.ImagePath, qrInfo.MetaPath, qrInfo.PageShot)
			_ = s.sendQRCode(ctx, qrInfo, refreshCount)
		}
		select {
		case <-ctx.Done():
			return model.Result{Status: model.StatusLoginTimeout, Message: "上下文取消或超时", QRCodeURL: qrInfo.PreviewURL, QRCodeKind: qrInfo.Kind, QRCodeFilePath: chooseFile(qrInfo.ImagePath, qrInfo.PageShot), RefreshCount: refreshCount}, ctx.Err()
		case <-time.After(s.cfg.LoginPollInterval):
		}
	}
	return model.Result{Status: model.StatusLoginTimeout, Message: "等待扫码登录超时", QRCodeURL: qrInfo.PreviewURL, QRCodeKind: qrInfo.Kind, QRCodeFilePath: chooseFile(qrInfo.ImagePath, qrInfo.PageShot), RefreshCount: refreshCount}, nil
}

func (s *Service) resumeQQOAuthIfNeeded(sess *browser.Session, qqAuthURL string, resumedAfterScan bool) (bool, error) {
	currentURL, err := sess.CurrentURL()
	if err != nil {
		return false, err
	}
	body, err := sess.BodyText()
	if err != nil {
		return false, err
	}
	body = strings.TrimSpace(body)
	if !strings.Contains(currentURL, "graph.qq.com/oauth2.0/login_jump") {
		return false, nil
	}
	if resumedAfterScan {
		if err := sess.Navigate(s.cfg.SignInURL); err != nil {
			return true, fmt.Errorf("login_jump 后回签到页失败: %w", err)
		}
		log.Printf("检测到 login_jump 停留，已主动回到签到页继续验证登录态")
		return true, nil
	}
	log.Printf("检测到 QQ 扫码后停留在 login_jump，尝试恢复 OAuth 授权页: body=%s", browser.NormalizeSnippet(body, 120))
	if qqAuthURL == "" {
		if err := sess.Navigate(s.cfg.SignInURL); err != nil {
			return true, fmt.Errorf("qqAuthURL 为空且回签到页失败: %w", err)
		}
		return true, nil
	}
	if err := sess.Navigate(qqAuthURL); err != nil {
		return true, fmt.Errorf("重新进入 QQ OAuth 页面失败: %w", err)
	}
	if err := sess.SleepRandom(1200*time.Millisecond, 2200*time.Millisecond); err != nil {
		return true, fmt.Errorf("等待 QQ OAuth 页面恢复失败: %w", err)
	}
	if err := s.clickAuthorizeIfPresent(sess); err != nil {
		log.Printf("当前 QQ OAuth 页面未发现可点击授权按钮: %v", err)
	}
	return true, nil
}

func (s *Service) clickAuthorizeIfPresent(sess *browser.Session) error {
	body, err := sess.BodyText()
	if err != nil {
		return err
	}
	if !strings.Contains(body, "授权") && !strings.Contains(body, "同意") && !strings.Contains(body, "登录") {
		return fmt.Errorf("页面未出现授权相关文案")
	}
	if err := sess.ClickFirstByText([]string{"授权并登录", "确认授权", "同意", "授权", "登录"}); err != nil {
		return err
	}
	if err := sess.SleepRandom(800*time.Millisecond, 1500*time.Millisecond); err != nil {
		return err
	}
	log.Printf("已尝试点击 QQ OAuth 授权按钮")
	return nil
}

func (s *Service) fetchQRCode(sess *browser.Session, refreshCount int) (QRCodeInfo, error) {
	if err := sess.SleepRandom(500*time.Millisecond, 1200*time.Millisecond); err != nil {
		return QRCodeInfo{}, err
	}
	raw, err := sess.FindImageSrcByKeywords([]string{"ptqrshow", "qrcode", "qrshow", "qr"})
	if err != nil {
		return QRCodeInfo{}, err
	}
	currentURL, _ := sess.CurrentURL()
	title, _ := sess.Title()
	info := QRCodeInfo{Raw: raw, Kind: detectQRCodeKind(raw), CurrentURL: currentURL, CurrentPage: title}
	info.PreviewURL = s.buildPreviewURL(raw, info.Kind)
	pageShot, imagePath, metaPath, err := s.persistQRCodeArtifacts(sess, info, refreshCount)
	if err != nil {
		log.Printf("保存二维码产物失败: %v", err)
	}
	info.PageShot = pageShot
	info.ImagePath = imagePath
	info.MetaPath = metaPath
	return info, nil
}

func (s *Service) sendQRCode(ctx context.Context, qrInfo QRCodeInfo, refreshCount int) error {
	if qrInfo.Raw == "" && qrInfo.PageShot == "" {
		return nil
	}
	targetURL := qrInfo.PreviewURL
	if targetURL == "" {
		targetURL = qrInfo.CurrentURL
	}
	msg := notify.Message{
		App:       s.cfg.AppName,
		Title:     "需要 QQ 扫码登录",
		Msg:       fmt.Sprintf("运行ID: %s\n阶段: 登录\n二维码刷新次数: %d\n二维码类型: %s\n二维码文件: %s\n页面截图: %s\n请尽快扫码，如二维码失效会再次通知。", s.cfg.RunID, refreshCount, qrInfo.Kind, qrInfo.ImagePath, qrInfo.PageShot),
		TargetURL: targetURL,
	}
	return s.notifier.Notify(ctx, msg)
}

func (s *Service) isQRCodeInvalid(sess *browser.Session) (bool, string, error) {
	body, err := sess.BodyText()
	if err != nil {
		return false, "", err
	}
	for _, kw := range []string{"二维码已失效", "点击刷新", "登录超时", "请点击刷新"} {
		if strings.Contains(body, kw) {
			return true, "命中关键词: " + kw, nil
		}
	}
	return false, "", nil
}

func (s *Service) refreshQRCode(sess *browser.Session) error {
	log.Printf("检测到二维码失效，尝试刷新")
	if err := sess.ClickFirstByText([]string{"点击刷新", "刷新二维码", "重新加载", "重试"}); err != nil {
		return err
	}
	return sess.SleepRandom(900*time.Millisecond, 1600*time.Millisecond)
}

func (s *Service) detectRisk(sess *browser.Session) (bool, string, error) {
	body, err := sess.BodyText()
	if err != nil {
		return false, "", err
	}
	if classify.IsRiskText(body) {
		return true, "登录过程中出现风控/验证码挑战", nil
	}
	return false, "", nil
}

func ArtifactBase(artifactDir, prefix string) string {
	return filepath.Join(artifactDir, prefix+"-login")
}

func detectQRCodeKind(raw string) string {
	raw = strings.TrimSpace(raw)
	switch {
	case raw == "":
		return "empty"
	case strings.HasPrefix(raw, "data:image/"):
		return "data-url-image"
	case strings.HasPrefix(raw, "blob:"):
		return "blob-url"
	case strings.HasPrefix(raw, "http://") || strings.HasPrefix(raw, "https://"):
		return "remote-image-url"
	default:
		return "unknown"
	}
}

func (s *Service) buildPreviewURL(raw, kind string) string {
	raw = strings.TrimSpace(raw)
	switch kind {
	case "data-url-image":
		return "https://ximplez.github.io/base64-image-viewer/?target=" + raw
	case "remote-image-url":
		return raw
	default:
		return ""
	}
}

func (s *Service) persistQRCodeArtifacts(sess *browser.Session, info QRCodeInfo, refreshCount int) (pageShot string, imagePath string, metaPath string, err error) {
	baseName := filepath.Join(s.cfg.RunArtifactDir, fmt.Sprintf("qrcode-%02d", refreshCount))
	pageShot = baseName + "-page.png"
	if err = sess.SaveScreenshot(pageShot); err != nil {
		return pageShot, "", "", err
	}
	if strings.HasPrefix(info.Raw, "data:image/") {
		imagePath = baseName + imageExtFromDataURL(info.Raw)
		if err = writeDataURLImage(info.Raw, imagePath); err != nil {
			log.Printf("写入 data URL 二维码图片失败: %v", err)
			imagePath = ""
		}
	} else if strings.HasPrefix(info.Raw, "http://") || strings.HasPrefix(info.Raw, "https://") {
		imagePath = baseName + imageExtFromRemoteURL(info.Raw)
		if err = downloadRemoteImage(info.Raw, imagePath); err != nil {
			log.Printf("下载二维码远程图片失败: %v", err)
			imagePath = ""
		}
	}
	metaPath = baseName + ".json"
	metaBytes, _ := json.MarshalIndent(info, "", "  ")
	if writeErr := os.WriteFile(metaPath, metaBytes, 0o644); writeErr != nil {
		return pageShot, imagePath, metaPath, writeErr
	}
	return pageShot, imagePath, metaPath, nil
}

func writeDataURLImage(raw, path string) error {
	idx := strings.Index(raw, ",")
	if idx <= 0 {
		return fmt.Errorf("非法 data url")
	}
	payload := raw[idx+1:]
	data, err := base64.StdEncoding.DecodeString(payload)
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o644)
}

func imageExtFromDataURL(raw string) string {
	switch {
	case strings.HasPrefix(raw, "data:image/jpeg"):
		return ".jpg"
	case strings.HasPrefix(raw, "data:image/webp"):
		return ".webp"
	case strings.HasPrefix(raw, "data:image/gif"):
		return ".gif"
	default:
		return ".png"
	}
}

func imageExtFromRemoteURL(raw string) string {
	lower := strings.ToLower(raw)
	for _, ext := range []string{".png", ".jpg", ".jpeg", ".gif", ".webp"} {
		if strings.Contains(lower, ext) {
			return ext
		}
	}
	return ".png"
}

func downloadRemoteImage(raw, path string) error {
	client := &http.Client{Timeout: 20 * time.Second}
	resp, err := client.Get(raw)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return fmt.Errorf("下载二维码图片失败，状态码=%d", resp.StatusCode)
	}
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o644)
}

func chooseFile(v1, v2 string) string {
	if v1 != "" {
		return v1
	}
	return v2
}
