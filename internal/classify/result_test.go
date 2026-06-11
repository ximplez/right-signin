package classify

import (
	"os"
	"path/filepath"
	"testing"

	"right-signin/internal/model"
)

func TestDetectStatusFromFixtures(t *testing.T) {
	tests := []struct {
		name   string
		file   string
		status model.Status
	}{
		{name: "need login", file: "need_login.html", status: model.StatusNeedLogin},
		{name: "already signed", file: "already_signed.html", status: model.StatusAlreadySigned},
		{name: "success", file: "success.html", status: model.StatusSuccess},
		{name: "risk", file: "risk.html", status: model.StatusRiskControl},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			data, err := os.ReadFile(filepath.Join("..", "..", "testdata", "html-fixtures", tt.file))
			if err != nil {
				t.Fatalf("读取 fixture 失败: %v", err)
			}
			status, _ := DetectStatus(string(data))
			if status != tt.status {
				t.Fatalf("状态不匹配: got=%s want=%s", status, tt.status)
			}
		})
	}
}
