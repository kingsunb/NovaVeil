package handlers

import (
	"testing"

	"github.com/kingsunb/NovaVeil/internal/model"
)

func TestSetModelContextWindow(t *testing.T) {
	maxOutput := 4096
	original := map[string]model.ChannelModelLimit{
		"keep": {MaxOutput: &maxOutput},
	}

	defined := setModelContextWindow(original, "target", 128_000)
	if limit, ok := defined["target"]; !ok || limit.ContextWindow == nil || *limit.ContextWindow != 128_000 {
		t.Fatalf("定义窗口后 target = %+v, want context_window=128000", defined["target"])
	}
	if _, ok := original["target"]; ok {
		t.Fatal("不得改动入参 map")
	}

	overwritten := setModelContextWindow(defined, "target", 1_000_000)
	if got := *overwritten["target"].ContextWindow; got != 1_000_000 {
		t.Fatalf("覆盖窗口 = %d, want 1000000", got)
	}

	cleared := setModelContextWindow(overwritten, "target", 0)
	if _, ok := cleared["target"]; ok {
		t.Fatal("只含窗口的条目在清除后应删除")
	}

	kept := setModelContextWindow(defined, "keep", 64_000)
	limit, ok := kept["keep"]
	if !ok || limit.ContextWindow == nil || *limit.ContextWindow != 64_000 {
		t.Fatalf("保留 max_output 并写入窗口失败: %+v", limit)
	}
	if limit.MaxOutput == nil || *limit.MaxOutput != maxOutput {
		t.Fatalf("写入窗口不得清掉 max_output: %+v", limit)
	}

	clearedKeep := setModelContextWindow(kept, "keep", 0)
	limit, ok = clearedKeep["keep"]
	if !ok {
		t.Fatal("仍有 max_output 的条目不能被删除")
	}
	if limit.ContextWindow != nil {
		t.Fatalf("清除后 context_window 应为 nil: %+v", limit)
	}
	if limit.MaxOutput == nil || *limit.MaxOutput != maxOutput {
		t.Fatalf("清除窗口不得影响 max_output: %+v", limit)
	}
}
