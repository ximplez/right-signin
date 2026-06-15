package config

import (
	"flag"
	"fmt"
	"math/rand/v2"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const (
	defaultSignInURL = "https://www.right.com.cn/forum/erling_qd-sign_in.html"
	defaultAppName   = "恩山论坛自动签到"
)

type Config struct {
	AppName             string
	SignInURL           string
	CookiesEnv          string
	GitHubToken         string
	GitHubRepo          string
	GitHubSecretName    string
	Debug               bool
	DryRun              bool
	NotifySuccess       bool
	FeishuWebhook       string
	OpenListBaseURL     string
	OpenListToken       string
	OpenListUploadDir   string
	Timeout             time.Duration
	PageTimeout         time.Duration
	LoginWaitTimeout    time.Duration
	LoginPollInterval   time.Duration
	QRCodeCheckInterval time.Duration
	MaxQRRefresh        int
	NavigationRetries   int
	ActionRetries       int
	UserDataDir         string
	CookiesPath         string
	ArtifactsRoot       string
	UserAgent           string
	BrowserLang         string
	WindowWidth         int
	WindowHeight        int
	RunID               string
	RunArtifactDir      string
}

func Load() (*Config, error) {
	wd, err := os.Getwd()
	if err != nil {
		return nil, fmt.Errorf("获取工作目录失败: %w", err)
	}

	defaultRuntime := filepath.Join(wd, "runtime")
	defaultProfileDir := filepath.Join(defaultRuntime, "profile")
	defaultArtifacts := filepath.Join(defaultRuntime, "artifacts")
	defaultCookies := filepath.Join(defaultRuntime, "cookies.json")

	var cfg Config
	flag.StringVar(&cfg.SignInURL, "sign-url", getenv("RIGHT_SIGNIN_URL", defaultSignInURL), "签到页 URL")
	flag.BoolVar(&cfg.Debug, "debug", getenvBool("RIGHT_SIGNIN_DEBUG", false), "开启有头调试")
	flag.BoolVar(&cfg.DryRun, "dry-run", getenvBool("RIGHT_SIGNIN_DRY_RUN", false), "只探测，不真正点击签到")
	flag.BoolVar(&cfg.NotifySuccess, "notify-success", getenvBool("RIGHT_SIGNIN_NOTIFY_SUCCESS", true), "成功与已签到是否通知")
	flag.DurationVar(&cfg.Timeout, "timeout", getenvDuration("RIGHT_SIGNIN_TIMEOUT", 8*time.Minute), "总超时时间")
	flag.DurationVar(&cfg.PageTimeout, "page-timeout", getenvDuration("RIGHT_SIGNIN_PAGE_TIMEOUT", 25*time.Second), "页面加载超时时间")
	flag.DurationVar(&cfg.LoginWaitTimeout, "login-timeout", getenvDuration("RIGHT_SIGNIN_LOGIN_TIMEOUT", 5*time.Minute), "登录等待超时时间")
	flag.DurationVar(&cfg.LoginPollInterval, "login-poll", getenvDuration("RIGHT_SIGNIN_LOGIN_POLL", 5*time.Second), "登录轮询间隔")
	flag.DurationVar(&cfg.QRCodeCheckInterval, "qr-check", getenvDuration("RIGHT_SIGNIN_QR_CHECK", 15*time.Second), "二维码过期检查间隔")
	flag.IntVar(&cfg.MaxQRRefresh, "qr-refresh-max", getenvInt("RIGHT_SIGNIN_QR_REFRESH_MAX", 6), "二维码最多刷新次数")
	flag.IntVar(&cfg.NavigationRetries, "nav-retries", getenvInt("RIGHT_SIGNIN_NAV_RETRIES", 2), "页面导航重试次数")
	flag.IntVar(&cfg.ActionRetries, "action-retries", getenvInt("RIGHT_SIGNIN_ACTION_RETRIES", 1), "关键动作重试次数")
	flag.StringVar(&cfg.UserDataDir, "profile-dir", getenv("RIGHT_SIGNIN_PROFILE_DIR", defaultProfileDir), "固定浏览器 profile 目录")
	flag.StringVar(&cfg.CookiesPath, "cookies-path", getenv("RIGHT_SIGNIN_COOKIES_PATH", defaultCookies), "cookies 快照文件")
	flag.StringVar(&cfg.ArtifactsRoot, "artifacts-dir", getenv("RIGHT_SIGNIN_ARTIFACTS_DIR", defaultArtifacts), "运行产物根目录")
	flag.StringVar(&cfg.UserAgent, "user-agent", getenv("RIGHT_SIGNIN_USER_AGENT", "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/136.0.0.0 Safari/537.36"), "浏览器 User-Agent")
	flag.StringVar(&cfg.BrowserLang, "lang", getenv("RIGHT_SIGNIN_LANG", "zh-CN"), "浏览器语言")
	flag.IntVar(&cfg.WindowWidth, "window-width", getenvInt("RIGHT_SIGNIN_WINDOW_WIDTH", 1365), "浏览器宽度")
	flag.IntVar(&cfg.WindowHeight, "window-height", getenvInt("RIGHT_SIGNIN_WINDOW_HEIGHT", 900), "浏览器高度")
	flag.Parse()

	cfg.CookiesEnv = strings.TrimSpace(os.Getenv("COOKIES"))
	cfg.GitHubToken = strings.TrimSpace(os.Getenv("GITHUB_TOKEN"))
	cfg.GitHubRepo = getenv("RIGHT_SIGNIN_GITHUB_REPO", getenv("GITHUB_REPOSITORY", ""))
	cfg.GitHubSecretName = getenv("RIGHT_SIGNIN_GITHUB_SECRET_NAME", "COOKIES")
	cfg.FeishuWebhook = strings.TrimSpace(os.Getenv("FEISHU_BOT_URL"))
	cfg.OpenListBaseURL = strings.TrimRight(getenv("RIGHT_SIGNIN_OPENLIST_BASE_URL", "/"), "/")
	cfg.OpenListToken = strings.TrimSpace(os.Getenv("RIGHT_SIGNIN_OPENLIST_TOKEN"))
	cfg.OpenListUploadDir = ensureLeadingSlash(getenv("RIGHT_SIGNIN_OPENLIST_UPLOAD_DIR", "/right-signin"))
	cfg.AppName = getenv("RIGHT_SIGNIN_APP_NAME", defaultAppName)
	cfg.RunID = buildRunID()
	cfg.RunArtifactDir = filepath.Join(cfg.ArtifactsRoot, cfg.RunID)

	if cfg.SignInURL == "" {
		return nil, fmt.Errorf("sign-url 不能为空")
	}
	if cfg.Timeout <= 0 || cfg.PageTimeout <= 0 || cfg.LoginWaitTimeout <= 0 || cfg.LoginPollInterval <= 0 || cfg.QRCodeCheckInterval <= 0 {
		return nil, fmt.Errorf("超时配置必须大于 0")
	}
	if cfg.MaxQRRefresh < 0 || cfg.NavigationRetries < 0 || cfg.ActionRetries < 0 {
		return nil, fmt.Errorf("重试次数不能小于 0")
	}
	return &cfg, nil
}

func buildRunID() string {
	return fmt.Sprintf("%s-%04d", time.Now().Format("20060102-150405"), rand.IntN(10000))
}

func getenv(key, fallback string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return fallback
}

func ensureLeadingSlash(path string) string {
	path = strings.TrimSpace(path)
	if path == "" || path == "/" {
		return ""
	}
	path = "/" + strings.Trim(path, "/")
	return path
}

func getenvBool(key string, fallback bool) bool {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		return fallback
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		return fallback
	}
	return b
}

func getenvInt(key string, fallback int) int {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		return fallback
	}
	i, err := strconv.Atoi(v)
	if err != nil {
		return fallback
	}
	return i
}

func getenvDuration(key string, fallback time.Duration) time.Duration {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		return fallback
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		return fallback
	}
	return d
}
