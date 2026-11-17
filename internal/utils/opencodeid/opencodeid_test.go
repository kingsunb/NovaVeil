package opencodeid

import (
	"strings"
	"testing"
)

func TestProjectIDForLane(t *testing.T) {
	commit := "0123456789abcdef0123456789abcdef01234567"
	cases := []struct {
		signal string
		want   string
	}{
		{"global", "global"},
		{commit, commit},
		{"", "global"},
		{"prj_abc", "global"},
		{strings.ToUpper(commit), "global"},
		{"global ", "global"},
	}
	for _, v1 := range []bool{true, false} {
		for _, tc := range cases {
			if got := ProjectIDForLane(tc.signal, v1); got != tc.want {
				t.Fatalf("ProjectIDForLane(%q, v1=%v) = %q, 期望 %q", tc.signal, v1, got, tc.want)
			}
		}
	}
}

// TestDerivedProjectID 锁定按渠道派生的 project 号: 40 位小写 hex 且稳定。
func TestDerivedProjectID(t *testing.T) {
	first := DerivedProjectID(7)
	if len(first) != 40 {
		t.Fatalf("project 号应为 40 位, 实际 %q", first)
	}
	if strings.Trim(first, "0123456789abcdef") != "" {
		t.Fatalf("project 号应为小写 hex, 实际 %q", first)
	}
	if again := DerivedProjectID(7); again != first {
		t.Fatalf("同渠道应稳定: %q != %q", first, again)
	}
	if other := DerivedProjectID(8); other == first {
		t.Fatalf("不同渠道应不同: %q", other)
	}
}
