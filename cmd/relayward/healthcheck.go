package main

import (
	"flag"
	"fmt"
	"io"
	"net/http"
	"time"

	"relayward-mail/internal/config"
)

// healthcheck GETs /healthz and exits 0 when it answers 2xx. It backs the
// Dockerfile HEALTHCHECK: the distroless runtime image has no shell or curl.
func healthcheck(args []string) error {
	fs := flag.NewFlagSet("healthcheck", flag.ExitOnError)
	configPath := fs.String("config", "config.yaml", "path to the configuration file")
	url := fs.String("url", "", "health endpoint URL (default: derived from the admin listener in the config)")
	if err := fs.Parse(args); err != nil {
		return err
	}

	target := *url
	if target == "" {
		target = "http://127.0.0.1:8081/healthz"
		if cfg, err := config.Load(*configPath); err == nil {
			target = "http://127.0.0.1" + cfg.Admin.Listen + "/healthz"
		}
	}

	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Get(target)
	if err != nil {
		return fmt.Errorf("healthcheck %s: %w", target, err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("healthcheck %s: status %d: %s", target, resp.StatusCode, body)
	}
	fmt.Printf("ok %s: %s\n", target, body)
	return nil
}
