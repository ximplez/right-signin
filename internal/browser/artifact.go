package browser

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/chromedp/chromedp"
)

func (s *Session) SaveScreenshot(path string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("创建截图目录失败: %w", err)
	}
	var buf []byte
	if err := s.Run(chromedp.FullScreenshot(&buf, 90)); err != nil {
		return fmt.Errorf("截图失败: %w", err)
	}
	return os.WriteFile(path, buf, 0o644)
}

func (s *Session) SaveHTML(path string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("创建 HTML 目录失败: %w", err)
	}
	html, err := s.HTML()
	if err != nil {
		return fmt.Errorf("提取 HTML 失败: %w", err)
	}
	return os.WriteFile(path, []byte(html), 0o644)
}

func (s *Session) Snapshot(basePath string) (string, string, error) {
	screenshot := basePath + ".png"
	html := basePath + ".html"
	if err := s.SaveScreenshot(screenshot); err != nil {
		return "", "", err
	}
	if err := s.SaveHTML(html); err != nil {
		return screenshot, "", err
	}
	return screenshot, html, nil
}
