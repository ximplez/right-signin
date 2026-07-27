package login

import (
	"context"
	"fmt"
	"log"
	"strings"
	"time"

	"right-signin/internal/browser"
	"right-signin/internal/classify"
	"right-signin/internal/model"
	"right-signin/internal/notify"
)

func (s *QQAuthenticator) EnsureLoggedIn(ctx context.Context, sess *browser.Session) (model.Result, error) {
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
		log.Printf("发现 QQ 登录 iframe，保留父级 OAuth 页面，仅通过 iframe 源提取二维码: %s", iframeURL)
	}
	return s.waitForQRCodeAndLogin(ctx, sess, qqURL)
}

func (s *QQAuthenticator) openLoginEntry(sess *browser.Session) error {
	_ = sess.SleepRandom(300*time.Millisecond, 800*time.Millisecond)
	if err := sess.ClickFirstByText([]string{"立即登录", "登录"}); err != nil {
		return err
	}
	return sess.SleepRandom(800*time.Millisecond, 1400*time.Millisecond)
}

func (s *QQAuthenticator) resolveQQLoginURL(sess *browser.Session) (string, error) {
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

func (s *QQAuthenticator) waitForQRCodeAndLogin(ctx context.Context, sess *browser.Session, qqAuthURL string) (model.Result, error) {
	qrInfo, err := s.fetchQRCode(ctx, sess, 0)
	if err != nil {
		return model.Result{Status: model.StatusNeedLogin, Message: "未提取到登录二维码"}, err
	}
	refreshCount := 0
	resumedAfterScan := false
	maxRefreshCount := s.allowedQRRefreshCount()
	lastProtocolSignature := ""
	nextFallbackRefreshAt := time.Now().Add(s.cfg.QRCodeCheckInterval)
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
			return model.Result{Status: model.StatusSuccess, Message: "扫码登录成功: " + reason, QRCodeURL: qrInfo.PreviewURL, QRCodeKind: qrInfo.Kind, QRCodeFilePath: chooseQRCodeImageFile(qrInfo), RefreshCount: refreshCount}, nil
		}
		if err != nil {
			log.Printf("登录态轮询失败: %v", err)
		}
		if risk, reason, err := s.detectRisk(sess); err == nil && risk {
			return model.Result{Status: model.StatusRiskControl, Message: reason, QRCodeURL: qrInfo.PreviewURL, QRCodeKind: qrInfo.Kind, QRCodeFilePath: chooseQRCodeImageFile(qrInfo), RefreshCount: refreshCount}, nil
		}
		protocolResult, protocolErr := s.pollQQQRCodeProtocol(ctx, sess, qrInfo)
		if protocolErr != nil {
			log.Printf("QQ 协议层轮询失败，将继续等待浏览器页内状态变化: %v", protocolErr)
		} else {
			nextFallbackRefreshAt = time.Now().Add(s.cfg.QRCodeCheckInterval)
			stateSignature := protocolResult.Code + "|" + strings.TrimSpace(protocolResult.Message)
			if stateSignature != lastProtocolSignature {
				log.Printf("QQ 协议层状态更新: code=%s message=%s redirect=%s ptqrtoken=%d poll=%s", protocolResult.Code, protocolResult.Message, browser.NormalizeSnippet(protocolResult.RedirectURL, 200), protocolResult.PTQRToken, browser.NormalizeSnippet(protocolResult.PollURL, 220))
				lastProtocolSignature = stateSignature
			}
			switch protocolResult.Code {
			case "0":
				log.Printf("QQ 协议层确认扫码授权完成，等待 OAuth 页面继续跳转")
				s.updateLoginProgress(ctx, notify.RightSigninStatusLoginWaiting, qrInfo, refreshCount, "QQ 已确认扫码授权，正在等待 OAuth 页面回跳。")
			case "66":
				// 二维码仍有效，继续等待。
			case "67":
				log.Printf("QQ 协议层确认二维码已被扫码，等待手机确认授权")
				s.updateLoginProgress(ctx, notify.RightSigninStatusLoginWaiting, qrInfo, refreshCount, "二维码已被扫码，请在 QQ 客户端确认授权。")
			case "65", "68":
				if refreshCount >= maxRefreshCount {
					return model.Result{Status: model.StatusQRCodeExpiredTooMany, Message: fmt.Sprintf("二维码协议层刷新次数超限: 已刷新 %d 次，最后状态=%s", refreshCount, protocolResult.Message), QRCodeURL: qrInfo.PreviewURL, QRCodeKind: qrInfo.Kind, QRCodeFilePath: chooseQRCodeImageFile(qrInfo), RefreshCount: refreshCount}, nil
				}
				if err := s.refreshQRCode(sess); err != nil {
					return model.Result{Status: model.StatusFailure, Message: "协议层检测到二维码失效后刷新失败", QRCodeURL: qrInfo.PreviewURL, QRCodeKind: qrInfo.Kind, QRCodeFilePath: chooseQRCodeImageFile(qrInfo), RefreshCount: refreshCount}, err
				}
				refreshCount++
				prevQRInfo := qrInfo
				qrInfo, err = s.fetchQRCode(ctx, sess, refreshCount)
				if err != nil {
					return model.Result{Status: model.StatusFailure, Message: "协议层刷新后重新抓取二维码失败", RefreshCount: refreshCount}, err
				}
				lastProtocolSignature = ""
				nextFallbackRefreshAt = time.Now().Add(s.cfg.QRCodeCheckInterval)
				log.Printf("协议层检测到二维码已失效并完成刷新: refresh=%d old_code=%s kind=%s raw=%s image=%s meta=%s pageShot=%s", refreshCount, protocolResult.Code, qrInfo.Kind, browser.NormalizeSnippet(qrInfo.Raw, 180), qrInfo.ImagePath, qrInfo.MetaPath, qrInfo.PageShot)
				if changed, reason := shouldNotifyQRCodeChange(prevQRInfo, qrInfo); changed {
					if err := s.sendQRCode(ctx, qrInfo, refreshCount); err != nil {
						log.Printf("发送二维码刷新通知失败: %v", err)
					}
					log.Printf("检测到二维码关键锚点变更，已发送刷新通知: refresh=%d reason=%s", refreshCount, reason)
				} else {
					log.Printf("协议层触发刷新后二维码关键锚点未变化，跳过刷新通知: refresh=%d reason=%s current=%s iframe=%s", refreshCount, reason, browser.NormalizeSnippet(qrInfo.CurrentURL, 180), browser.NormalizeSnippet(qrInfo.IframeURL, 180))
				}
			case "":
				// 轮询返回为空时忽略。
			default:
				log.Printf("QQ 协议层返回未显式处理的状态，继续观察浏览器页内流程: code=%s message=%s raw=%s", protocolResult.Code, protocolResult.Message, browser.NormalizeSnippet(protocolResult.Raw, 220))
			}
		}
		if protocolErr != nil && s.cfg.QRCodeCheckInterval > 0 && !time.Now().Before(nextFallbackRefreshAt) {
			if refreshCount >= maxRefreshCount {
				return model.Result{Status: model.StatusQRCodeExpiredTooMany, Message: fmt.Sprintf("二维码轮询异常且刷新次数超限: 已刷新 %d 次", refreshCount), QRCodeURL: qrInfo.PreviewURL, QRCodeKind: qrInfo.Kind, QRCodeFilePath: chooseQRCodeImageFile(qrInfo), RefreshCount: refreshCount}, nil
			}
			if err := s.refreshQRCode(sess); err != nil {
				return model.Result{Status: model.StatusFailure, Message: "协议层轮询异常后的兜底刷新失败", QRCodeURL: qrInfo.PreviewURL, QRCodeKind: qrInfo.Kind, QRCodeFilePath: chooseQRCodeImageFile(qrInfo), RefreshCount: refreshCount}, err
			}
			refreshCount++
			prevQRInfo := qrInfo
			qrInfo, err = s.fetchQRCode(ctx, sess, refreshCount)
			if err != nil {
				return model.Result{Status: model.StatusFailure, Message: "协议层轮询异常后的兜底刷新重新抓取二维码失败", RefreshCount: refreshCount}, err
			}
			lastProtocolSignature = ""
			nextFallbackRefreshAt = time.Now().Add(s.cfg.QRCodeCheckInterval)
			log.Printf("协议层轮询异常后已执行兜底刷新: refresh=%d kind=%s raw=%s image=%s meta=%s pageShot=%s", refreshCount, qrInfo.Kind, browser.NormalizeSnippet(qrInfo.Raw, 180), qrInfo.ImagePath, qrInfo.MetaPath, qrInfo.PageShot)
			if changed, reason := shouldNotifyQRCodeChange(prevQRInfo, qrInfo); changed {
				if err := s.sendQRCode(ctx, qrInfo, refreshCount); err != nil {
					log.Printf("发送二维码刷新通知失败: %v", err)
				}
				log.Printf("检测到二维码关键锚点变更，已发送刷新通知: refresh=%d reason=%s", refreshCount, reason)
			} else {
				log.Printf("二维码关键锚点未变化，跳过刷新通知: refresh=%d reason=%s current=%s iframe=%s", refreshCount, reason, browser.NormalizeSnippet(qrInfo.CurrentURL, 180), browser.NormalizeSnippet(qrInfo.IframeURL, 180))
			}
		}
		select {
		case <-ctx.Done():
			return model.Result{Status: model.StatusLoginTimeout, Message: "上下文取消或超时", QRCodeURL: qrInfo.PreviewURL, QRCodeKind: qrInfo.Kind, QRCodeFilePath: chooseQRCodeImageFile(qrInfo), RefreshCount: refreshCount}, ctx.Err()
		case <-time.After(s.cfg.LoginPollInterval):
		}
	}
	return model.Result{Status: model.StatusLoginTimeout, Message: "等待扫码登录超时", QRCodeURL: qrInfo.PreviewURL, QRCodeKind: qrInfo.Kind, QRCodeFilePath: chooseQRCodeImageFile(qrInfo), RefreshCount: refreshCount}, nil
}

func (s *QQAuthenticator) resumeQQOAuthIfNeeded(sess *browser.Session, qqAuthURL string, resumedAfterScan bool) (bool, error) {
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

func (s *QQAuthenticator) clickAuthorizeIfPresent(sess *browser.Session) error {
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

func (s *QQAuthenticator) detectRisk(sess *browser.Session) (bool, string, error) {
	body, err := sess.BodyText()
	if err != nil {
		return false, "", err
	}
	if classify.IsRiskText(body) {
		return true, "登录过程中出现风控/验证码挑战", nil
	}
	return false, "", nil
}

func (s *QQAuthenticator) allowedQRRefreshCount() int {
	maxRefreshCount := s.cfg.MaxQRRefresh
	if s.cfg.LoginWaitTimeout <= 0 || s.cfg.QRCodeCheckInterval <= 0 {
		return maxRefreshCount
	}
	needed := int(s.cfg.LoginWaitTimeout / s.cfg.QRCodeCheckInterval)
	if s.cfg.LoginWaitTimeout%s.cfg.QRCodeCheckInterval != 0 {
		needed++
	}
	if needed > maxRefreshCount {
		return needed
	}
	return maxRefreshCount
}
