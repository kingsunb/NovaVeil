package conf

import "testing"

func TestConfigValidateRejectsBadServerAndPath(t *testing.T) {
	valid := Config{
		Server:   Server{Host: "127.0.0.1", Port: 8080},
		Database: Database{Path: "data/novaveil.db"},
	}
	if err := valid.Validate(); err != nil {
		t.Fatalf("valid config rejected: %v", err)
	}

	cases := []struct {
		name string
		mut  func(*Config)
	}{
		{"empty host", func(c *Config) { c.Server.Host = "  " }},
		{"port zero", func(c *Config) { c.Server.Port = 0 }},
		{"port too high", func(c *Config) { c.Server.Port = 70000 }},
		{"empty db path", func(c *Config) { c.Database.Path = "" }},
		{"nul in db path", func(c *Config) { c.Database.Path = "data/x\x00.db" }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := valid
			tc.mut(&c)
			if err := c.Validate(); err == nil {
				t.Fatalf("expected validation error for %s", tc.name)
			}
		})
	}
}

func TestConfigValidateAdminRateLimitThresholds(t *testing.T) {
	base := Config{
		Server:   Server{Host: "127.0.0.1", Port: 8080},
		Database: Database{Path: "data/novaveil.db"},
		Security: Security{
			AdminAPIRateLimitEnabled:   true,
			AdminAPIRateLimitPerMinute: 120,
			AdminAPIRateLimitBurst:     30,
		},
	}
	if err := base.Validate(); err != nil {
		t.Fatalf("valid admin rate limit config rejected: %v", err)
	}

	disabled := base
	disabled.Security.AdminAPIRateLimitEnabled = false
	disabled.Security.AdminAPIRateLimitPerMinute = 0
	disabled.Security.AdminAPIRateLimitBurst = 0
	if err := disabled.Validate(); err != nil {
		t.Fatalf("disabled admin rate limit must skip threshold validation: %v", err)
	}

	badMinute := base
	badMinute.Security.AdminAPIRateLimitPerMinute = 0
	if err := badMinute.Validate(); err == nil {
		t.Fatal("expected per_minute > 0 validation error")
	}

	badBurst := base
	badBurst.Security.AdminAPIRateLimitBurst = 0
	if err := badBurst.Validate(); err == nil {
		t.Fatal("expected burst >= 1 validation error")
	}
}
