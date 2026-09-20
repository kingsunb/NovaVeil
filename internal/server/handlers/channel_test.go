package handlers

import "testing"

func TestChannelKeyMaskedLeavesLast4Runes(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"", ""},
		{"  ", ""},
		{"abcd", "****"},
		{"sk-secret-key-1234", "****1234"},
	}
	for _, tc := range cases {
		if got := channelKeyMasked(tc.in); got != tc.want {
			t.Fatalf("channelKeyMasked(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}
