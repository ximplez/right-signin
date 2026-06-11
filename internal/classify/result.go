package classify

import (
	"regexp"
	"strings"

	"right-signin/internal/model"
)

var tagRegexp = regexp.MustCompile(`<[^>]+>`)

var (
	successKeywords = []string{"签到成功", "签到完成", "签到奖励", "奖励", "获得"}
	alreadyKeywords = []string{"今日已签到", "已经签到", "已签到", "签到过了"}
	failureKeywords = []string{"签到失败", "系统繁忙", "提交失败", "请稍后再试", "操作失败"}
	riskKeywords    = []string{"验证码", "滑块", "人机验证", "安全验证", "异常访问", "频繁操作", "验证后继续", "拖动滑块"}
	loginKeywords   = []string{"请先登录", "立即登录", "您需要登录后才能使用签到功能", "QQ登录", "QQ 登录"}
	networkKeywords = []string{"加载失败", "连接失败", "请求超时", "网络异常", "ERR_"}
)

func NormalizeText(input string) string {
	cleaned := tagRegexp.ReplaceAllString(input, " ")
	cleaned = strings.ReplaceAll(cleaned, "\u00a0", " ")
	cleaned = strings.ReplaceAll(cleaned, "\n", " ")
	cleaned = strings.ReplaceAll(cleaned, "\t", " ")
	return strings.Join(strings.Fields(cleaned), " ")
}

func DetectStatus(text string) (model.Status, string) {
	text = NormalizeText(text)
	if text == "" {
		return model.StatusUnknown, "页面内容为空"
	}
	if hit, kw := containsAny(text, riskKeywords); hit {
		return model.StatusRiskControl, "检测到风控关键词: " + kw
	}
	if hit, kw := containsAny(text, networkKeywords); hit {
		return model.StatusNetworkError, "检测到网络异常关键词: " + kw
	}
	if hit, kw := containsAny(text, alreadyKeywords); hit {
		return model.StatusAlreadySigned, "检测到已签到关键词: " + kw
	}
	if hit, kw := containsAny(text, successKeywords); hit {
		return model.StatusSuccess, "检测到成功关键词: " + kw
	}
	if hit, kw := containsAny(text, failureKeywords); hit {
		return model.StatusFailure, "检测到失败关键词: " + kw
	}
	if hit, kw := containsAny(text, loginKeywords); hit {
		return model.StatusNeedLogin, "检测到未登录关键词: " + kw
	}
	return model.StatusUnknown, "未命中已知关键词"
}

func IsRiskText(text string) bool {
	hit, _ := containsAny(NormalizeText(text), riskKeywords)
	return hit
}

func containsAny(text string, keywords []string) (bool, string) {
	for _, kw := range keywords {
		if strings.Contains(text, kw) {
			return true, kw
		}
	}
	return false, ""
}
