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

func TestSetModelContextWindowCaseInsensitiveClear(t *testing.T) {
	window := 128_000
	maxOutput := 4096
	original := map[string]model.ChannelModelLimit{
		"GPT-4o":        {ContextWindow: &window},
		"Claude-Sonnet": {ContextWindow: &window, MaxOutput: &maxOutput, ThinkingLevel: "high"},
		"prj_abc123":    {ContextWindow: &window},
	}

	cleared := setModelContextWindow(original, "gpt-4o", 0)
	if _, ok := cleared["GPT-4o"]; ok {
		t.Fatal("大小写不同的目标键应被删除")
	}
	if _, ok := cleared["gpt-4o"]; ok {
		t.Fatal("清除不得新增不同大小写的键")
	}
	if got := cleared["prj_abc123"].ContextWindow; got == nil || *got != window {
		t.Fatalf("无关模型窗口被改动: %+v", cleared["prj_abc123"])
	}
	if got := cleared["Claude-Sonnet"].ContextWindow; got == nil || *got != window {
		t.Fatalf("清理目标不应影响其它模型: %+v", cleared["Claude-Sonnet"])
	}

	cleared = setModelContextWindow(cleared, "CLAUDE-SONNET", 0)
	limit, ok := cleared["Claude-Sonnet"]
	if !ok {
		t.Fatal("仍有其它限制的条目不能被删除")
	}
	if limit.ContextWindow != nil {
		t.Fatalf("清除后 context_window 应为 nil: %+v", limit)
	}
	if limit.MaxOutput == nil || *limit.MaxOutput != maxOutput {
		t.Fatalf("清除窗口不得影响 max_output: %+v", limit)
	}
	if limit.ThinkingLevel != "high" {
		t.Fatalf("清除窗口不得影响 thinking_level: %+v", limit)
	}

	duplicated := map[string]model.ChannelModelLimit{
		"GPT-4o": {ContextWindow: &window},
		"gpt-4o": {ContextWindow: &window, MaxOutput: &maxOutput},
	}
	cleared = setModelContextWindow(duplicated, "Gpt-4O", 0)
	if _, ok := cleared["GPT-4o"]; ok {
		t.Fatal("历史遗留的大小写重复键也应清除")
	}
	limit, ok = cleared["gpt-4o"]
	if !ok {
		t.Fatal("仍有 max_output 的重复键不能被删除")
	}
	if limit.ContextWindow != nil || limit.MaxOutput == nil || *limit.MaxOutput != maxOutput {
		t.Fatalf("清除重复键窗口应保留其它限制: %+v", limit)
	}
}

func TestSetModelContextWindowReusesExistingCaseKey(t *testing.T) {
	maxOutput := 4096
	original := map[string]model.ChannelModelLimit{
		"GPT-4o":     {MaxOutput: &maxOutput},
		"prj_abc123": {MaxOutput: &maxOutput},
	}

	updated := setModelContextWindow(original, "gpt-4o", 256_000)
	if _, ok := updated["gpt-4o"]; ok {
		t.Fatal("写入不得新增不同大小写的重复键")
	}
	limit, ok := updated["GPT-4o"]
	if !ok {
		t.Fatal("写入应复用已有的大小写不同键")
	}
	if limit.ContextWindow == nil || *limit.ContextWindow != 256_000 {
		t.Fatalf("写入窗口失败: %+v", limit)
	}
	if limit.MaxOutput == nil || *limit.MaxOutput != maxOutput {
		t.Fatalf("写入窗口不得影响 max_output: %+v", limit)
	}
	if _, ok := updated["prj_abc123"]; !ok {
		t.Fatal("无关模型键不得被删除")
	}
}
