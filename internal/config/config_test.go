package config

import (
	"os"
	"path/filepath"
	"testing"
)

func writeTempConfig(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

const validConfig = `
data_dir: ./data
smtp:
  listen: ":2525"
  max_message_size: 5MB
upstream:
  host: smtp.provider.com
  port: 2587
  username: apikey
  password: ${TEST_UPSTREAM_KEY}
  tls: starttls
public:
  listen: ":8080"
  base_url: https://mail.example.com
admin:
  listen: ":8081"
log_retention_days: 30
`

func TestLoad(t *testing.T) {
	t.Setenv("TEST_UPSTREAM_KEY", "secret-key-value")

	cfg, err := Load(writeTempConfig(t, validConfig))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	if cfg.DataDir != "./data" {
		t.Errorf("DataDir = %q", cfg.DataDir)
	}
	if cfg.SMTP.Listen != ":2525" {
		t.Errorf("SMTP.Listen = %q", cfg.SMTP.Listen)
	}
	if cfg.SMTP.MaxMessageSize != 5*1024*1024 {
		t.Errorf("SMTP.MaxMessageSize = %d", cfg.SMTP.MaxMessageSize)
	}
	if cfg.Upstream.Password != "secret-key-value" {
		t.Errorf("Upstream.Password not expanded from env, got %q", cfg.Upstream.Password)
	}
	if cfg.LogRetentionDays != 30 {
		t.Errorf("LogRetentionDays = %d", cfg.LogRetentionDays)
	}
}

func TestLoadDefaults(t *testing.T) {
	cfg, err := Load(writeTempConfig(t, `
upstream:
  host: smtp.provider.com
  username: apikey
  password: pw
public:
  base_url: https://mail.example.com
`))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	if cfg.DataDir != "./data" {
		t.Errorf("DataDir default = %q", cfg.DataDir)
	}
	if cfg.SMTP.Listen != ":587" {
		t.Errorf("SMTP.Listen default = %q", cfg.SMTP.Listen)
	}
	if cfg.SMTP.MaxMessageSize != 10*1024*1024 {
		t.Errorf("SMTP.MaxMessageSize default = %d", cfg.SMTP.MaxMessageSize)
	}
	if cfg.Public.Listen != ":8080" || cfg.Admin.Listen != ":8081" {
		t.Errorf("listen defaults = %q / %q", cfg.Public.Listen, cfg.Admin.Listen)
	}
	if cfg.Upstream.TLS != "starttls" {
		t.Errorf("Upstream.TLS default = %q", cfg.Upstream.TLS)
	}
	if cfg.LogRetentionDays != 90 {
		t.Errorf("LogRetentionDays default = %d", cfg.LogRetentionDays)
	}
}

func TestLoadExampleConfig(t *testing.T) {
	t.Setenv("UPSTREAM_KEY", "dummy")
	t.Setenv("UNSUB_SECRET", "")

	_, err := Load("../../config.example.yaml")
	if err != nil {
		t.Fatalf("Load(config.example.yaml): %v", err)
	}
}

func TestLoadMissingEnv(t *testing.T) {
	t.Setenv("TEST_UPSTREAM_KEY", "")

	_, err := Load(writeTempConfig(t, validConfig))
	if err == nil {
		t.Fatal("expected error for missing env var, got nil")
	}
}

func TestLoadUnknownField(t *testing.T) {
	t.Setenv("TEST_UPSTREAM_KEY", "x")
	_, err := Load(writeTempConfig(t, validConfig+"\nunknown_field: 1\n"))
	if err == nil {
		t.Fatal("expected error for unknown field, got nil")
	}
}

func TestValidate(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(*Config)
		wantErr bool
	}{
		{"valid", func(*Config) {}, false},
		{"missing host", func(c *Config) { c.Upstream.Host = "" }, true},
		{"missing password", func(c *Config) { c.Upstream.Password = "" }, true},
		{"bad tls mode", func(c *Config) { c.Upstream.TLS = "ssl" }, true},
		{"http base url", func(c *Config) { c.Public.BaseURL = "http://mail.example.com" }, true},
		{"garbage base url", func(c *Config) { c.Public.BaseURL = "mail.example.com" }, true},
		{"cert without key", func(c *Config) { c.SMTP.TLSCert = "/tmp/cert.pem" }, true},
		{"negative retention", func(c *Config) { c.LogRetentionDays = -1 }, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &Config{
				Upstream: Upstream{Host: "smtp.provider.com", Port: 587, Username: "k", Password: "pw", TLS: "starttls"},
				Public:   Public{BaseURL: "https://mail.example.com"},
			}
			tt.mutate(cfg)
			err := cfg.Validate()
			if (err != nil) != tt.wantErr {
				t.Fatalf("Validate() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestBannerDomain(t *testing.T) {
	cfg := &Config{Public: Public{BaseURL: "https://mail.example.com/"}}
	if got := cfg.BannerDomain(); got != "mail.example.com" {
		t.Errorf("BannerDomain = %q", got)
	}
	cfg.Public.BaseURL = ""
	if got := cfg.BannerDomain(); got != "relayward" {
		t.Errorf("BannerDomain fallback = %q", got)
	}
}

func TestParseSize(t *testing.T) {
	tests := []struct {
		in      string
		want    int64
		wantErr bool
	}{
		{"10MB", 10 * 1024 * 1024, false},
		{"10mb", 10 * 1024 * 1024, false},
		{"512KB", 512 * 1024, false},
		{"1G", 1 << 30, false},
		{"2048", 2048, false},
		{" 10 MB ", 10 * 1024 * 1024, false},
		{"", 0, true},
		{"MB", 0, true},
		{"-5MB", 0, true},
		{"10TB", 0, true},
	}
	for _, tt := range tests {
		got, err := parseSize(tt.in)
		if (err != nil) != tt.wantErr {
			t.Errorf("parseSize(%q) error = %v, wantErr %v", tt.in, err, tt.wantErr)
			continue
		}
		if err == nil && got != tt.want {
			t.Errorf("parseSize(%q) = %d, want %d", tt.in, got, tt.want)
		}
	}
}
