package conf

import (
	"bytes"
	"strings"
	"testing"
)

func TestBannerColorRespectsNoColor(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	if bannerColorEnabled(true) {
		t.Fatal("NO_COLOR must disable ANSI colors even on a TTY")
	}

	var buf bytes.Buffer
	printBanner(&buf, bannerColorEnabled(true))
	if strings.Contains(buf.String(), "\x1b[") {
		t.Fatalf("NO_COLOR banner contains ANSI: %q", buf.String())
	}
}

func TestBannerColorRequiresTTY(t *testing.T) {
	t.Setenv("NO_COLOR", "")
	if bannerColorEnabled(false) {
		t.Fatal("non-TTY stdout must not use ANSI colors")
	}
	if !bannerColorEnabled(true) {
		t.Fatal("TTY stdout without NO_COLOR should use ANSI colors")
	}

	var buf bytes.Buffer
	printBanner(&buf, bannerColorEnabled(true))
	if !strings.Contains(buf.String(), "\x1b[") {
		t.Fatal("colored banner should contain ANSI")
	}
}

func TestBannerHasNoEmoji(t *testing.T) {
	var buf bytes.Buffer
	printBanner(&buf, false)
	out := buf.String()
	if strings.Contains(out, "\x1b[") {
		t.Fatalf("plain banner contains ANSI: %q", out)
	}
	for _, r := range out {
		if r >= 0x1F000 && r <= 0x1FAFF {
			t.Fatalf("banner contains emoji codepoint U+%04X", r)
		}
	}
}
