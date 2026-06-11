package session

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/chromedp/cdproto/network"
	"github.com/chromedp/chromedp"

	"right-signin/internal/githubsync"
)

type Store struct {
	ProfileDir  string
	CookiesPath string
	CookiesEnv  string
	GitHubToken string
	GitHubRepo  string
	SecretName  string
}

type cookieSnapshot struct {
	SavedAt time.Time              `json:"saved_at,omitempty"`
	Cookies []*network.CookieParam `json:"cookies"`
}

func (s Store) Ensure() error {
	if err := os.MkdirAll(s.ProfileDir, 0o755); err != nil {
		return fmt.Errorf("创建 profile 目录失败: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(s.CookiesPath), 0o755); err != nil {
		return fmt.Errorf("创建 cookies 目录失败: %w", err)
	}
	return nil
}

func (s Store) SaveCookies(ctx context.Context) error {
	cks, err := getCookies(ctx)
	if err != nil {
		return fmt.Errorf("获取 cookies 失败: %w", err)
	}
	params, err := cookiesToSetParams(cks)
	if err != nil {
		return fmt.Errorf("转换 cookies 失败: %w", err)
	}
	b, err := json.MarshalIndent(cookieSnapshot{SavedAt: time.Now(), Cookies: params.Cookies}, "", "  ")
	if err != nil {
		return fmt.Errorf("序列化 cookies 失败: %w", err)
	}
	tmpPath := s.CookiesPath + ".tmp"
	if err := os.WriteFile(tmpPath, b, 0o644); err != nil {
		return fmt.Errorf("写入 cookies 文件失败: %w", err)
	}
	if err := os.Rename(tmpPath, s.CookiesPath); err != nil {
		return fmt.Errorf("提交 cookies 文件失败: %w", err)
	}
	if err := githubsync.CreateOrUpdateRepoSecret(s.GitHubToken, s.GitHubRepo, chooseSecretName(s.SecretName), string(b)); err != nil {
		return fmt.Errorf("上传 GitHub Secret 失败: %w", err)
	}
	return nil
}

func (s Store) LoadCookies(ctx context.Context) (int, error) {
	data, source, err := s.readCookiesSource()
	if err != nil {
		return 0, err
	}
	if len(data) == 0 {
		return 0, nil
	}
	params, err := decodeCookies(data)
	if err != nil {
		return 0, fmt.Errorf("解析 %s cookies 失败: %w", source, err)
	}
	if len(params.Cookies) == 0 {
		return 0, nil
	}
	child, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if err := chromedp.Run(child, chromedp.ActionFunc(func(runCtx context.Context) error {
		return network.SetCookies(params.Cookies).Do(runCtx)
	})); err != nil {
		return 0, fmt.Errorf("加载 cookies 失败: %w", err)
	}
	return len(params.Cookies), nil
}

func (s Store) readCookiesSource() ([]byte, string, error) {
	if env := strings.TrimSpace(s.CookiesEnv); env != "" {
		return []byte(env), "环境变量 COOKIES", nil
	}
	data, err := os.ReadFile(s.CookiesPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, "", nil
		}
		return nil, "", fmt.Errorf("读取 cookies 文件失败: %w", err)
	}
	return data, s.CookiesPath, nil
}

func getCookies(ctx context.Context) ([]*network.Cookie, error) {
	child, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	var cookies []*network.Cookie
	if err := chromedp.Run(child, chromedp.ActionFunc(func(runCtx context.Context) error {
		var err error
		cookies, err = network.GetCookies().Do(runCtx)
		return err
	})); err != nil {
		return nil, err
	}
	return cookies, nil
}

func cookiesToSetParams(cookies []*network.Cookie) (network.SetCookiesParams, error) {
	payload, err := json.Marshal(network.GetCookiesReturns{Cookies: cookies})
	if err != nil {
		return network.SetCookiesParams{}, err
	}
	var params network.SetCookiesParams
	if err := json.Unmarshal(payload, &params); err != nil {
		return network.SetCookiesParams{}, err
	}
	return params, nil
}

func decodeCookies(data []byte) (network.SetCookiesParams, error) {
	var snapshot cookieSnapshot
	if err := json.Unmarshal(data, &snapshot); err == nil && len(snapshot.Cookies) > 0 {
		return network.SetCookiesParams{Cookies: snapshot.Cookies}, nil
	}
	var params network.SetCookiesParams
	if err := json.Unmarshal(data, &params); err == nil && len(params.Cookies) > 0 {
		return params, nil
	}
	var returns network.GetCookiesReturns
	if err := json.Unmarshal(data, &returns); err == nil && len(returns.Cookies) > 0 {
		return cookiesToSetParams(returns.Cookies)
	}
	var rawCookies []*network.Cookie
	if err := json.Unmarshal(data, &rawCookies); err == nil && len(rawCookies) > 0 {
		return cookiesToSetParams(rawCookies)
	}
	return network.SetCookiesParams{}, fmt.Errorf("cookies 文件格式无法识别: %s", snipe(data, 120))
}

func snipe(data []byte, max int) string {
	text := string(data)
	if max > 0 && len(text) > max {
		return text[:max] + "..."
	}
	return text
}

func chooseSecretName(name string) string {
	if strings.TrimSpace(name) == "" {
		return "COOKIES"
	}
	return strings.TrimSpace(name)
}
