package cmd

import "testing"

func TestDesktopURL(t *testing.T) {
	tests := []struct {
		host string
		port int
		want string
	}{
		{"", 8080, "http://127.0.0.1:8080"},
		{"0.0.0.0", 8080, "http://127.0.0.1:8080"},
		{"::", 8080, "http://127.0.0.1:8080"},
		{"[::]", 8080, "http://127.0.0.1:8080"},
		{"127.0.0.1", 9000, "http://127.0.0.1:9000"},
		{"192.168.1.5", 8080, "http://192.168.1.5:8080"},
		{"::1", 8080, "http://[::1]:8080"},
	}
	for _, tt := range tests {
		if got := desktopURL(tt.host, tt.port); got != tt.want {
			t.Errorf("desktopURL(%q, %d) = %q, want %q", tt.host, tt.port, got, tt.want)
		}
	}
}
