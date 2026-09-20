package op

import "testing"

func TestValidateChannelBaseURL(t *testing.T) {
	if err := validateChannelBaseURL("https://api.example.com/v1"); err != nil {
		t.Fatalf("https url: %v", err)
	}
	if err := validateChannelBaseURL("http://127.0.0.1:8080"); err != nil {
		t.Fatalf("http url (test binary, syntax-only): %v", err)
	}
	if err := validateChannelBaseURL("ftp://x"); err == nil {
		t.Fatal("ftp should be rejected")
	}
	if err := validateChannelBaseURL("not-a-url"); err == nil {
		t.Fatal("bare host should be rejected")
	}
	if err := validateChannelBaseURL(""); err == nil {
		t.Fatal("empty should be rejected")
	}
	if err := validateChannelBaseURL("https://user:pass@example.com"); err == nil {
		t.Fatal("userinfo should be rejected")
	}
}

func TestValidateChannelEgressBaseURL(t *testing.T) {
	if err := ValidateChannelEgressBaseURL("http://8.8.8.8:8080/v1"); err != nil {
		t.Fatalf("public literal ip should pass: %v", err)
	}
	for _, raw := range []string{
		"http://127.0.0.1:8080",
		"http://10.0.0.1:8080",
		"http://172.16.0.1:8080",
		"http://192.168.1.1:8080",
		"http://[::1]:8080",
		"http://169.254.169.254/latest/meta-data",
		"http://0.0.0.0:8080",
	} {
		if err := ValidateChannelEgressBaseURL(raw); err == nil {
			t.Fatalf("private/loopback/link-local address should be rejected: %s", raw)
		}
	}
	if err := ValidateChannelEgressBaseURL("http://localhost:8080"); err == nil {
		t.Fatal("localhost resolving to loopback should be rejected")
	}
}
