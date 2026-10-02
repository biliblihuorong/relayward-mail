// Package config loads the relayward YAML configuration file and expands
// ${VAR} environment variable references inside it.
package config

import (
	"bytes"
	"fmt"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

// Config is the top-level configuration of relayward.
type Config struct {
	DataDir          string      `yaml:"data_dir"`
	SMTP             SMTP        `yaml:"smtp"`
	Upstream         Upstream    `yaml:"upstream"`
	Public           Public      `yaml:"public"`
	Admin            Admin       `yaml:"admin"`
	Unsubscribe      Unsubscribe `yaml:"unsubscribe"`
	LogRetentionDays int         `yaml:"log_retention_days"`
}

// SMTP configures the ingress SMTP listener for client programs.
type SMTP struct {
	Listen         string `yaml:"listen"`
	TLSCert        string `yaml:"tls_cert"`
	TLSKey         string `yaml:"tls_key"`
	MaxMessageSize Size   `yaml:"max_message_size"`
}

// Upstream configures the SMTP account of the real mail provider.
type Upstream struct {
	Host     string `yaml:"host"`
	Port     int    `yaml:"port"`
	Username string `yaml:"username"`
	Password string `yaml:"password"`
	TLS      string `yaml:"tls"`
}

// Public configures the HTTP listener that serves recipients: the
// unsubscribe pages and /healthz.
type Public struct {
	Listen  string `yaml:"listen"`
	BaseURL string `yaml:"base_url"`
}

// Admin configures the HTTP listener that serves the management API.
// IPAllowlist restricts /api/* to the listed addresses or CIDR ranges; empty
// means no restriction.
type Admin struct {
	Listen      string   `yaml:"listen"`
	IPAllowlist []string `yaml:"ip_allowlist"`
}

// Unsubscribe holds the unsubscribe token secret and the footer wording used
// for body injection ({app} is replaced by the app's display name).
type Unsubscribe struct {
	Secret     string `yaml:"secret"`
	FooterText string `yaml:"footer_text"`
}

var envRef = regexp.MustCompile(`\$\{([A-Za-z_][A-Za-z0-9_]*)\}`)

// Load reads the configuration file at path, expands ${VAR} references to the
// corresponding environment variables (unset variables expand to the empty
// string) and validates the result.
func Load(path string) (*Config, error) {
	raw, err := os.ReadFile(path) // #nosec G304 -- path comes from the operator's command line
	if err != nil {
		return nil, fmt.Errorf("read config: %w", err)
	}

	dec := yaml.NewDecoder(bytes.NewReader(expandEnv(raw)))
	dec.KnownFields(true)

	var cfg Config
	if err := dec.Decode(&cfg); err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}
	cfg.applyDefaults()
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	return &cfg, nil
}

// expandEnv replaces every ${NAME} with the value of the environment variable
// NAME, or the empty string if it is unset.
func expandEnv(data []byte) []byte {
	return envRef.ReplaceAllFunc(data, func(match []byte) []byte {
		name := envRef.FindSubmatch(match)[1]
		return []byte(os.Getenv(string(name)))
	})
}

func (c *Config) applyDefaults() {
	if c.DataDir == "" {
		c.DataDir = "./data"
	}
	if c.SMTP.Listen == "" {
		c.SMTP.Listen = ":587"
	}
	if c.SMTP.MaxMessageSize == 0 {
		c.SMTP.MaxMessageSize = 10 * 1024 * 1024
	}
	if c.Upstream.Port == 0 {
		c.Upstream.Port = 587
	}
	if c.Upstream.TLS == "" {
		c.Upstream.TLS = "starttls"
	}
	if c.Public.Listen == "" {
		c.Public.Listen = ":8080"
	}
	if c.Admin.Listen == "" {
		c.Admin.Listen = ":8081"
	}
	if c.Unsubscribe.FooterText == "" {
		c.Unsubscribe.FooterText = "不想再收到此类邮件？点此退订"
	}
	if c.LogRetentionDays == 0 {
		c.LogRetentionDays = 90
	}
}

// Validate reports whether the configuration is complete enough to start.
func (c *Config) Validate() error {
	if c.Upstream.Host == "" {
		return fmt.Errorf("config: upstream.host is required")
	}
	if c.Upstream.Username == "" {
		return fmt.Errorf("config: upstream.username is required")
	}
	if c.Upstream.Password == "" {
		return fmt.Errorf("config: upstream.password is required (check that the referenced environment variable is set)")
	}
	switch c.Upstream.TLS {
	case "starttls", "none":
	default:
		return fmt.Errorf("config: upstream.tls must be %q or %q", "starttls", "none")
	}

	base, err := url.Parse(c.Public.BaseURL)
	if err != nil || base.Scheme == "" || base.Host == "" {
		return fmt.Errorf("config: public.base_url must be an absolute URL")
	}
	if base.Scheme != "https" {
		return fmt.Errorf("config: public.base_url must use https")
	}

	if (c.SMTP.TLSCert == "") != (c.SMTP.TLSKey == "") {
		return fmt.Errorf("config: smtp.tls_cert and smtp.tls_key must be set together")
	}
	if c.LogRetentionDays < 0 {
		return fmt.Errorf("config: log_retention_days must not be negative")
	}
	return nil
}

// BannerDomain returns the host name used in the SMTP greeting and as the
// EHLO name towards the upstream provider.
func (c *Config) BannerDomain() string {
	base, err := url.Parse(c.Public.BaseURL)
	if err != nil || base.Hostname() == "" {
		return "relayward"
	}
	return base.Hostname()
}

// Size is a byte count that parses from either a plain integer or a string
// with a K/KB/M/MB/G/GB suffix (1024-based).
type Size int64

// UnmarshalYAML implements yaml.Unmarshaler.
func (s *Size) UnmarshalYAML(node *yaml.Node) error {
	var raw any
	if err := node.Decode(&raw); err != nil {
		return err
	}
	switch v := raw.(type) {
	case int:
		*s = Size(v)
		return nil
	case string:
		parsed, err := parseSize(v)
		if err != nil {
			return fmt.Errorf("parse size %q: %w", v, err)
		}
		*s = Size(parsed)
		return nil
	default:
		return fmt.Errorf("size must be an integer or a string like 10MB, got %v", raw)
	}
}

var sizeUnits = map[string]int64{
	"k": 1 << 10, "kb": 1 << 10, "kib": 1 << 10,
	"m": 1 << 20, "mb": 1 << 20, "mib": 1 << 20,
	"g": 1 << 30, "gb": 1 << 30, "gib": 1 << 30,
}

func parseSize(s string) (int64, error) {
	trimmed := strings.TrimSpace(s)
	if trimmed == "" {
		return 0, fmt.Errorf("empty size")
	}
	lower := strings.ToLower(trimmed)
	for suffix, mult := range sizeUnits {
		if strings.HasSuffix(lower, suffix) {
			num := strings.TrimSpace(strings.TrimSuffix(lower, suffix))
			return parseInt64(num, mult)
		}
	}
	return parseInt64(lower, 1)
}

func parseInt64(num string, mult int64) (int64, error) {
	if num == "" {
		return 0, fmt.Errorf("missing number")
	}
	n, err := strconv.ParseInt(num, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid number %q", num)
	}
	if n < 0 {
		return 0, fmt.Errorf("size must not be negative")
	}
	return n * mult, nil
}
