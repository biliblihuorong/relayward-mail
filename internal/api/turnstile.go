package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// siteverifyURL is Cloudflare's Turnstile verification endpoint; tests point
// it at a stub.
var siteverifyURL = "https://challenges.cloudflare.com/turnstile/v0/siteverify"

// errCaptchaFailed marks a verdict from Cloudflare itself: the widget token
// was missing, reused, expired or solved wrong. Every other error means the
// check could not be performed (Cloudflare unreachable), which also fails
// closed.
var errCaptchaFailed = errors.New("captcha: verification failed")

// turnstileVerifier checks Turnstile widget tokens against the siteverify
// API. It exists only when the captcha is enabled.
type turnstileVerifier struct {
	secret string
	client *http.Client
}

func newTurnstileVerifier(secret string) *turnstileVerifier {
	return &turnstileVerifier{secret: secret, client: &http.Client{Timeout: 10 * time.Second}}
}

// verify exchanges one widget response token for a verdict. Cloudflare
// tokens are single-use and bound to the site key, so no replay or
// cross-site check is needed here.
func (t *turnstileVerifier) verify(ctx context.Context, response, remoteIP string) error {
	response = strings.TrimSpace(response)
	if response == "" {
		return errCaptchaFailed
	}

	form := url.Values{"secret": {t.secret}, "response": {response}}
	if remoteIP != "" {
		form.Set("remoteip", remoteIP)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, siteverifyURL, strings.NewReader(form.Encode()))
	if err != nil {
		return fmt.Errorf("captcha: build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := t.client.Do(req)
	if err != nil {
		return fmt.Errorf("captcha: siteverify unreachable: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("captcha: siteverify status %d", resp.StatusCode)
	}

	var out struct {
		Success    bool     `json:"success"`
		ErrorCodes []string `json:"error-codes"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 64<<10)).Decode(&out); err != nil {
		return fmt.Errorf("captcha: siteverify response: %w", err)
	}
	if !out.Success {
		return errCaptchaFailed
	}
	return nil
}
