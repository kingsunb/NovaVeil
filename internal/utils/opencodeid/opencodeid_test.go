package opencodeid

import (
	"strings"
	"testing"
)

func TestProjectIDForLanePreservesGlobalAndGitCommit(t *testing.T) {
	commit := "0123456789abcdef0123456789abcdef01234567"
	for _, v1 := range []bool{true, false} {
		for _, signal := range []string{"global", commit} {
			if got := ProjectIDForLane(signal, v1); got != signal {
				t.Fatalf("ProjectIDForLane(%q, v1=%v) = %q, 期望原样", signal, v1, got)
			}
		}
	}
}

func TestProjectIDForLaneHashesOtherSignals(t *testing.T) {
	for _, signal := range []string{"prj_abc", "0123456789ABCDEF0123456789ABCDEF01234567"} {
		v1 := ProjectIDForLane(signal, true)
		v2 := ProjectIDForLane(signal, false)
		if v1 != v2 {
			t.Fatalf("%q 两档结果不一致: %q != %q", signal, v1, v2)
		}
		if v1 == signal || !isLowerHex40(v1) {
			t.Fatalf("%q 应哈希为 40 位小写十六进制, 实际 %q", signal, v1)
		}
	}
}

func TestProjectIDForLaneDefaultSignal(t *testing.T) {
	got := ProjectIDForLane("", false)
	if !isLowerHex40(got) {
		t.Fatalf("缺省项目号 = %q, 期望 40 位小写十六进制", got)
	}
	if got != ProjectIDForLane("", true) {
		t.Fatal("缺省项目号两档应一致")
	}
}

func isLowerHex40(value string) bool {
	if len(value) != 40 || value != strings.ToLower(value) {
		return false
	}
	for _, r := range value {
		if !strings.ContainsRune("0123456789abcdef", r) {
			return false
		}
	}
	return true
}
