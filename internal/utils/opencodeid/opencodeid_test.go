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
