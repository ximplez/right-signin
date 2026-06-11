package signin

import (
	"fmt"
	"strings"
	"time"

	"right-signin/internal/browser"
	"right-signin/internal/classify"
	"right-signin/internal/config"
	"right-signin/internal/model"
)

type Service struct {
	cfg *config.Config
}

func New(cfg *config.Config) *Service {
	return &Service{cfg: cfg}
}

func (s *Service) Inspect(sess *browser.Session) (model.Result, error) {
	body, err := sess.BodyText()
	if err != nil {
		return model.Result{Status: model.StatusNetworkError, Message: "读取页面文本失败"}, err
	}
	status, reason := classify.DetectStatus(body)
	currentURL, _ := sess.CurrentURL()
	if status == model.StatusUnknown {
		ready, err := s.hasSignButton(sess)
		if err == nil && ready {
			return model.Result{Status: model.StatusReadyToSign, Message: "检测到可点击签到按钮", URL: currentURL}, nil
		}
		return model.Result{Status: model.StatusPageChanged, Message: "未识别到签到状态: " + reason, URL: currentURL}, nil
	}
	if status == model.StatusSuccess {
		// 进入页面即出现成功关键词，大概率是签到页成功态，按已签到处理更稳妥
		status = model.StatusAlreadySigned
	}
	return model.Result{Status: status, Message: reason, URL: currentURL}, nil
}

func (s *Service) Execute(sess *browser.Session, dryRun bool) (model.Result, error) {
	current, err := s.Inspect(sess)
	if err != nil {
		return current, err
	}
	if current.Status == model.StatusAlreadySigned || current.Status == model.StatusRiskControl || current.Status == model.StatusNeedLogin || current.Status == model.StatusPageChanged {
		return current, nil
	}
	if current.Status != model.StatusReadyToSign {
		return current, nil
	}
	if dryRun {
		current.Status = model.StatusDryRun
		current.Message = "dry-run 模式下检测到可签到，但未实际点击"
		return current, nil
	}
	if err := sess.SleepRandom(300*time.Millisecond, 800*time.Millisecond); err != nil {
		return model.Result{Status: model.StatusFailure, Message: "签到前等待失败", URL: current.URL}, err
	}
	if err := s.clickSign(sess); err != nil {
		return model.Result{Status: model.StatusFailure, Message: "点击签到失败", URL: current.URL}, err
	}
	if err := sess.SleepRandom(1200*time.Millisecond, 2200*time.Millisecond); err != nil {
		return model.Result{Status: model.StatusFailure, Message: "等待签到结果失败", URL: current.URL}, err
	}
	after, err := s.Inspect(sess)
	if err != nil {
		return after, err
	}
	if after.Status == model.StatusReadyToSign {
		body, _ := sess.BodyText()
		if strings.Contains(body, "签到") {
			after.Status = model.StatusFailure
			after.Message = "点击签到后页面未出现成功或已签到状态"
		}
	}
	if after.Status == model.StatusUnknown || after.Status == model.StatusPageChanged {
		after.Status = model.StatusFailure
		after.Message = "签到后状态未知，请检查截图和 HTML"
	}
	if after.Status == model.StatusAlreadySigned {
		after.Status = model.StatusSuccess
		if after.Message == "" {
			after.Message = "签到成功后页面进入已签到状态"
		}
	}
	return after, nil
}

func (s *Service) hasSignButton(sess *browser.Session) (bool, error) {
	selectors := []string{
		"a[href*='sign']",
		"button",
		"input[type='button']",
		"input[type='submit']",
	}
	for _, selector := range selectors {
		exists, err := sess.ElementExists(selector)
		if err == nil && exists {
			body, _ := sess.BodyText()
			if strings.Contains(body, "签到") || strings.Contains(body, "立即签到") {
				return true, nil
			}
		}
	}
	return false, nil
}

func (s *Service) clickSign(sess *browser.Session) error {
	if err := sess.ClickFirstByText([]string{"立即签到", "签到领奖", "签到", "打卡"}); err != nil {
		return fmt.Errorf("点击签到入口失败: %w", err)
	}
	return nil
}
