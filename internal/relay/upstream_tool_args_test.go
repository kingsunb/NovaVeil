package relay

import (
	"strings"
	"testing"
)

// TestMergeOfficialToolArgumentsIncremental 增量片段不得被前缀判定吞掉。
func TestMergeOfficialToolArgumentsIncremental(t *testing.T) {
	var builder strings.Builder
	mergeOfficialToolArguments(&builder, `{"content": [{"`)
	mergeOfficialToolArguments(&builder, `startId": "b112"`)
	want := `{"content": [{"startId": "b112"`
	if builder.String() != want {
		t.Fatalf("增量拼接 = %q, 期望 %q", builder.String(), want)
	}
}

// TestMergeOfficialToolArgumentsCumulative 累积重发按严格超集替换。
func TestMergeOfficialToolArgumentsCumulative(t *testing.T) {
	var builder strings.Builder
	mergeOfficialToolArguments(&builder, `{"content"`)
	mergeOfficialToolArguments(&builder, `{"content": [{"startId"`)
	want := `{"content": [{"startId"`
	if builder.String() != want {
		t.Fatalf("累积替换 = %q, 期望 %q", builder.String(), want)
	}
}

// TestMergeOfficialToolArgumentsDuplicateIgnored 完全相同的片段不重复追加。
func TestMergeOfficialToolArgumentsDuplicateIgnored(t *testing.T) {
	var builder strings.Builder
	mergeOfficialToolArguments(&builder, `{"a":1}`)
	mergeOfficialToolArguments(&builder, `{"a":1}`)
	if got := builder.String(); got != `{"a":1}` {
		t.Fatalf("重复片段被追加: %q", got)
	}
}

// TestMergeOfficialToolArgumentsPrefixKept 子串增量不被当成重复丢弃。
func TestMergeOfficialToolArgumentsPrefixKept(t *testing.T) {
	var builder strings.Builder
	mergeOfficialToolArguments(&builder, `{"a":1}`)
	mergeOfficialToolArguments(&builder, `{"`)
	got := builder.String()
	if got != `{"a":1}{"` {
		t.Fatalf("前缀增量被吞: %q", got)
	}
}
