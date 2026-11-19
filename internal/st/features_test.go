package st_test

import (
	"os"
	"testing"

	"softplc/internal/st"
)

// 特性示例程序必须通过完整加载检查。
func TestFeaturesProgramLoads(t *testing.T) {
	b, err := os.ReadFile("../../programs/features.st")
	if err != nil {
		t.Skip("特性文件不存在")
	}
	if _, err := st.Load("CTRL", map[string]string{"features.st": string(b)}); err != nil {
		t.Fatalf("features.st 加载失败: %v", err)
	}
}
