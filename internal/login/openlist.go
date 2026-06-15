package login

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
)

func (s *QQAuthenticator) uploadOAuthScreenshot(ctx context.Context, localPath string, refreshCount int) (string, error) {
	if strings.TrimSpace(localPath) == "" || s.openList == nil || !s.openList.Enabled() {
		return "", nil
	}
	remotePath := s.openListRemotePath(localPath, refreshCount)
	return s.openList.UploadFileAndGetLink(ctx, localPath, remotePath)
}

func (s *QQAuthenticator) CleanupOpenListUploadDir(ctx context.Context) error {
	if s.openList == nil {
		return nil
	}
	return s.openList.CleanupUploadDir(ctx)
}

func (s *QQAuthenticator) openListRemotePath(localPath string, refreshCount int) string {
	ext := strings.ToLower(filepath.Ext(localPath))
	if ext == "" {
		ext = ".png"
	}
	name := fmt.Sprintf("oauth-%s-qr-%02d%s", s.cfg.RunID, refreshCount, ext)
	return strings.TrimRight(s.openList.UploadDir(), "/") + "/" + name
}
