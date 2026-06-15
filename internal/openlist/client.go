package openlist

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"right-signin/internal/browser"
	"right-signin/internal/httputil"
)

type Client struct {
	baseURL    string
	token      string
	uploadDir  string
	apiClient  *http.Client
	uploadHTTP *http.Client
}

type basicResponse struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type listResponse struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    struct {
		Content []struct {
			Name string `json:"name"`
		} `json:"content"`
	} `json:"data"`
}

type linkResponse struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    struct {
		URL string `json:"url"`
	} `json:"data"`
}

func New(baseURL, token, uploadDir string) *Client {
	return &Client{
		baseURL:    strings.TrimRight(strings.TrimSpace(baseURL), "/"),
		token:      strings.TrimSpace(token),
		uploadDir:  normalizeUploadDir(uploadDir),
		apiClient:  httputil.NewClient(30 * time.Second),
		uploadHTTP: httputil.NewClient(60 * time.Second),
	}
}

func (c *Client) Enabled() bool {
	return c != nil && c.baseURL != "" && c.token != ""
}

func (c *Client) UploadDir() string {
	if c == nil {
		return "/"
	}
	return c.uploadDir
}

func (c *Client) UploadFileAndGetLink(ctx context.Context, localPath, remotePath string) (string, error) {
	if !c.Enabled() || strings.TrimSpace(localPath) == "" {
		return "", nil
	}
	if err := c.EnsureDir(ctx); err != nil {
		return "", err
	}
	if err := c.UploadFile(ctx, localPath, remotePath); err != nil {
		return "", err
	}
	return c.FileLink(ctx, remotePath)
}

func (c *Client) CleanupUploadDir(ctx context.Context) error {
	if !c.Enabled() {
		return nil
	}
	names, err := c.ListDirNames(ctx, c.uploadDir)
	if err != nil {
		return err
	}
	if len(names) == 0 {
		return nil
	}
	if err := c.RemoveNames(ctx, c.uploadDir, names); err != nil {
		return err
	}
	log.Printf("已清空 OpenList 上传目录: dir=%s removed=%d", c.uploadDir, len(names))
	return nil
}

func (c *Client) EnsureDir(ctx context.Context) error {
	if !c.Enabled() || c.uploadDir == "/" {
		return nil
	}
	req, err := c.newJSONRequest(ctx, http.MethodPost, "/api/fs/mkdir", map[string]string{"path": c.uploadDir})
	if err != nil {
		return err
	}
	resp, err := c.apiClient.Do(req)
	if err != nil {
		return err
	}
	body, err := httputil.ReadBody(resp)
	if err != nil {
		return err
	}
	if resp.StatusCode >= 300 {
		return fmt.Errorf("OpenList 创建目录失败，状态码=%d body=%s", resp.StatusCode, browser.NormalizeSnippet(string(body), 240))
	}
	if strings.TrimSpace(string(body)) == "" {
		return nil
	}
	var result basicResponse
	if err := parseJSON(body, &result); err == nil && result.Code != 0 && result.Code != 200 {
		if !strings.Contains(strings.ToLower(result.Message), "exists") {
			return fmt.Errorf("OpenList 创建目录失败，code=%d message=%s", result.Code, result.Message)
		}
	}
	return nil
}

func (c *Client) ListDirNames(ctx context.Context, dir string) ([]string, error) {
	req, err := c.newJSONRequest(ctx, http.MethodPost, "/api/fs/list", map[string]any{
		"path":     normalizeUploadDir(dir),
		"password": "",
		"page":     1,
		"per_page": 1000,
		"refresh":  true,
	})
	if err != nil {
		return nil, err
	}
	resp, err := c.apiClient.Do(req)
	if err != nil {
		return nil, err
	}
	body, err := httputil.ReadBody(resp)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode >= 300 {
		return nil, fmt.Errorf("OpenList 列目录失败，状态码=%d body=%s", resp.StatusCode, browser.NormalizeSnippet(string(body), 240))
	}
	var result listResponse
	if err := parseJSON(body, &result); err != nil {
		return nil, err
	}
	if result.Code != 0 && result.Code != 200 {
		return nil, fmt.Errorf("OpenList 列目录失败，code=%d message=%s", result.Code, result.Message)
	}
	names := make([]string, 0, len(result.Data.Content))
	for _, item := range result.Data.Content {
		name := strings.TrimSpace(item.Name)
		if name != "" {
			names = append(names, name)
		}
	}
	return names, nil
}

func (c *Client) RemoveNames(ctx context.Context, dir string, names []string) error {
	if len(names) == 0 {
		return nil
	}
	req, err := c.newJSONRequest(ctx, http.MethodPost, "/api/fs/remove", map[string]any{
		"dir":   normalizeUploadDir(dir),
		"names": names,
	})
	if err != nil {
		return err
	}
	resp, err := c.apiClient.Do(req)
	if err != nil {
		return err
	}
	body, err := httputil.ReadBody(resp)
	if err != nil {
		return err
	}
	if resp.StatusCode >= 300 {
		return fmt.Errorf("OpenList 删除目录内容失败，状态码=%d body=%s", resp.StatusCode, browser.NormalizeSnippet(string(body), 240))
	}
	if strings.TrimSpace(string(body)) == "" {
		return nil
	}
	var result basicResponse
	if err := parseJSON(body, &result); err == nil && result.Code != 0 && result.Code != 200 {
		return fmt.Errorf("OpenList 删除目录内容失败，code=%d message=%s", result.Code, result.Message)
	}
	return nil
}

func (c *Client) UploadFile(ctx context.Context, localPath, remotePath string) error {
	file, err := os.Open(localPath)
	if err != nil {
		return err
	}
	defer file.Close()
	req, err := httputil.NewRequestWithContext(ctx, http.MethodPut, c.apiURL("/api/fs/put"), file)
	if err != nil {
		return err
	}
	c.applyAuth(req)
	req.Header.Set("File-Path", strings.TrimSpace(remotePath))
	req.Header.Set("As-Task", "false")
	req.Header.Set("Content-Type", "application/octet-stream")
	resp, err := c.uploadHTTP.Do(req)
	if err != nil {
		return err
	}
	body, err := httputil.ReadBody(resp)
	if err != nil {
		return err
	}
	if resp.StatusCode >= 300 {
		return fmt.Errorf("OpenList 上传失败，状态码=%d body=%s", resp.StatusCode, browser.NormalizeSnippet(string(body), 240))
	}
	if strings.TrimSpace(string(body)) == "" {
		return nil
	}
	var result basicResponse
	if err := parseJSON(body, &result); err == nil && result.Code != 0 && result.Code != 200 {
		return fmt.Errorf("OpenList 上传失败，code=%d message=%s", result.Code, result.Message)
	}
	return nil
}

func (c *Client) FileLink(ctx context.Context, remotePath string) (string, error) {
	req, err := c.newJSONRequest(ctx, http.MethodPost, "/api/fs/link", map[string]string{
		"path":     strings.TrimSpace(remotePath),
		"password": "",
	})
	if err != nil {
		return "", err
	}
	resp, err := c.apiClient.Do(req)
	if err != nil {
		return "", err
	}
	body, err := httputil.ReadBody(resp)
	if err != nil {
		return "", err
	}
	if resp.StatusCode >= 300 {
		return "", fmt.Errorf("OpenList 获取文件链接失败，状态码=%d body=%s", resp.StatusCode, browser.NormalizeSnippet(string(body), 240))
	}
	var result linkResponse
	if err := parseJSON(body, &result); err != nil {
		return "", err
	}
	if result.Code != 0 && result.Code != 200 {
		return "", fmt.Errorf("OpenList 获取文件链接失败，code=%d message=%s", result.Code, result.Message)
	}
	return c.NormalizeDirectLink(strings.TrimSpace(result.Data.URL)), nil
}

func (c *Client) NormalizeDirectLink(rawURL string) string {
	rawURL = strings.TrimSpace(rawURL)
	if rawURL == "" || c == nil || c.baseURL == "" {
		return rawURL
	}
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return rawURL
	}
	base, err := url.Parse(c.baseURL)
	if err != nil {
		return rawURL
	}
	if strings.HasPrefix(parsed.Path, "/p/") {
		parsed.Path = "/d/" + strings.TrimPrefix(parsed.Path, "/p/")
	}
	parsed.Scheme = base.Scheme
	parsed.Host = base.Host
	return parsed.String()
}

func (c *Client) newJSONRequest(ctx context.Context, method string, apiPath string, payload any) (*http.Request, error) {
	req, err := httputil.NewJSONRequest(ctx, method, c.apiURL(apiPath), payload)
	if err != nil {
		return nil, err
	}
	c.applyAuth(req)
	return req, nil
}

func (c *Client) applyAuth(req *http.Request) {
	if req == nil {
		return
	}
	req.Header.Set("Authorization", c.token)
}

func (c *Client) apiURL(apiPath string) string {
	return c.baseURL + apiPath
}

func normalizeUploadDir(dir string) string {
	dir = strings.TrimSpace(strings.TrimRight(dir, "/"))
	if dir == "" {
		return "/"
	}
	if !strings.HasPrefix(dir, "/") {
		return "/" + dir
	}
	return dir
}

func parseJSON(body []byte, out any) error {
	if strings.TrimSpace(string(body)) == "" {
		return nil
	}
	return json.Unmarshal(body, out)
}
