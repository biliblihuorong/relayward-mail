package main

import (
	"context"
	"crypto/tls"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"golang.org/x/sync/errgroup"

	smtp "github.com/emersion/go-smtp"

	"relayward-mail/internal/api"
	"relayward-mail/internal/config"
	"relayward-mail/internal/proxyproto"
	"relayward-mail/internal/ratelimit"
	"relayward-mail/internal/relay"
	"relayward-mail/internal/smtpd"
	"relayward-mail/internal/store"
	"relayward-mail/internal/unsub"
	"relayward-mail/internal/web"
)

// dbFileName is the SQLite database file inside the configured data_dir.
const dbFileName = "relayward.db"

func serve(args []string) error {
	fs := flag.NewFlagSet("serve", flag.ExitOnError)
	configPath := fs.String("config", "config.yaml", "path to the configuration file")
	if err := fs.Parse(args); err != nil {
		return err
	}

	cfg, err := config.Load(*configPath)
	if err != nil {
		return err
	}
	logger := newLogger()
	logger.Info("starting relayward",
		slog.String("version", version),
		slog.String("smtp_listen", cfg.SMTP.Listen),
		slog.String("public_listen", cfg.Public.Listen),
		slog.String("admin_listen", cfg.Admin.Listen),
		slog.String("data_dir", cfg.DataDir))

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := os.MkdirAll(cfg.DataDir, 0o700); err != nil {
		return fmt.Errorf("create data dir: %w", err)
	}
	st, err := store.Open(filepath.Join(cfg.DataDir, dbFileName))
	if err != nil {
		return err
	}
	defer st.Close()

	if err := ensureInitialAdminToken(ctx, st, cfg.DataDir, logger); err != nil {
		return err
	}

	unsubSecret, err := ensureUnsubscribeSecret(cfg, logger)
	if err != nil {
		return err
	}
	unsubManager, err := unsub.NewManager(unsubSecret)
	if err != nil {
		return fmt.Errorf("init unsubscribe manager: %w", err)
	}

	upstream := relay.New(relay.Options{
		Host:        cfg.Upstream.Host,
		Port:        cfg.Upstream.Port,
		Username:    cfg.Upstream.Username,
		Password:    cfg.Upstream.Password,
		TLSMode:     cfg.Upstream.TLS,
		HelloDomain: cfg.BannerDomain(),
	})
	monitor := api.NewUpstreamMonitor(upstream, time.Minute, logger)

	limiter := ratelimit.NewLimiter()
	lockout := ratelimit.NewLockout(
		ratelimit.DefaultFailureLimit,
		ratelimit.DefaultFailureWindow,
		ratelimit.DefaultBanDuration)

	backend := smtpd.NewBackend(st, upstream, limiter, lockout, logger, unsubManager, cfg.Public.BaseURL, cfg.Unsubscribe.FooterText)
	smtpServer, err := newSMTPServer(cfg, backend)
	if err != nil {
		return err
	}
	smtpListener, err := net.Listen("tcp", cfg.SMTP.Listen)
	if err != nil {
		return fmt.Errorf("listen smtp: %w", err)
	}
	trusted, err := proxyproto.ParseTrusted(cfg.SMTP.ProxyProtocolTrusted)
	if err != nil {
		return fmt.Errorf("smtp.proxy_protocol_trusted: %w", err)
	}
	// Closing the wrapper closes the underlying listener too.
	smtpListener = proxyproto.Listener(smtpListener, trusted)

	startedAt := time.Now()
	apiSrv := api.New(api.Options{
		Store:       st,
		Monitor:     monitor,
		Limiter:     limiter,
		Lockout:     lockout,
		DataDir:     cfg.DataDir,
		Version:     version,
		StartedAt:   startedAt,
		Logger:      logger,
		IPAllowlist: cfg.Admin.IPAllowlist,

		TurnstileSiteKey:   cfg.Admin.Turnstile.SiteKey,
		TurnstileSecretKey: cfg.Admin.Turnstile.SecretKey,
		CookieSecure:       cfg.Admin.CookieSecure,
	})
	apiServer := &http.Server{
		Addr:              cfg.Admin.Listen,
		Handler:           apiSrv.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       120 * time.Second,
	}
	adminListener, err := net.Listen("tcp", cfg.Admin.Listen)
	if err != nil {
		return fmt.Errorf("listen admin: %w", err)
	}

	publicMux := http.NewServeMux()
	publicMux.Handle("GET /healthz", apiSrv.HealthzHandler())
	publicMux.Handle("/u/", web.New(web.Options{Store: st, Unsub: unsubManager, Logger: logger}))
	publicServer := &http.Server{
		Addr:              cfg.Public.Listen,
		Handler:           publicMux,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       120 * time.Second,
	}
	publicListener, err := net.Listen("tcp", cfg.Public.Listen)
	if err != nil {
		return fmt.Errorf("listen public: %w", err)
	}

	group, groupCtx := errgroup.WithContext(ctx)
	group.Go(func() error { return monitor.Run(groupCtx) })
	group.Go(func() error { return runRetention(groupCtx, st, cfg.LogRetentionDays, logger) })
	group.Go(func() error {
		if err := smtpServer.Serve(smtpListener); err != nil && !errors.Is(err, smtp.ErrServerClosed) && !errors.Is(err, net.ErrClosed) {
			return fmt.Errorf("smtp server: %w", err)
		}
		return nil
	})
	group.Go(func() error {
		if err := apiServer.Serve(adminListener); err != nil && !errors.Is(err, http.ErrServerClosed) {
			return fmt.Errorf("admin http server: %w", err)
		}
		return nil
	})
	group.Go(func() error {
		if err := publicServer.Serve(publicListener); err != nil && !errors.Is(err, http.ErrServerClosed) {
			return fmt.Errorf("public http server: %w", err)
		}
		return nil
	})
	group.Go(func() error {
		<-groupCtx.Done()
		logger.Info("shutting down")
		shutdown(smtpListener, smtpServer, backend, apiServer, publicServer, logger)
		return nil
	})

	if err := group.Wait(); err != nil && !errors.Is(err, context.Canceled) {
		return err
	}
	logger.Info("relayward stopped")
	return nil
}

// newSMTPServer assembles the go-smtp server from configuration. STARTTLS is
// enabled only when a certificate is configured; AUTH is then required to run
// over TLS. Without a certificate (recommended only for internal networks)
// plaintext AUTH is accepted.
func newSMTPServer(cfg *config.Config, backend smtp.Backend) (*smtp.Server, error) {
	srv := smtp.NewServer(backend)
	srv.Domain = cfg.BannerDomain()
	srv.MaxMessageBytes = int64(cfg.SMTP.MaxMessageSize)
	srv.MaxRecipients = 100
	srv.ReadTimeout = 5 * time.Minute
	srv.WriteTimeout = 5 * time.Minute

	if cfg.SMTP.TLSCert != "" {
		cert, err := tls.LoadX509KeyPair(cfg.SMTP.TLSCert, cfg.SMTP.TLSKey)
		if err != nil {
			return nil, fmt.Errorf("load smtp tls certificate: %w", err)
		}
		srv.TLSConfig = &tls.Config{
			Certificates: []tls.Certificate{cert},
			MinVersion:   tls.VersionTLS12,
		}
	} else {
		srv.AllowInsecureAuth = true
	}
	return srv, nil
}

// shutdown stops accepting new work, waits for in-flight message transfers
// and requests, and finally closes everything.
func shutdown(smtpListener net.Listener, smtpServer *smtp.Server, backend *smtpd.Backend, apiServer, publicServer *http.Server, logger *slog.Logger) {
	if err := smtpListener.Close(); err != nil && !errors.Is(err, net.ErrClosed) {
		logger.Warn("close smtp listener", slog.String("err", err.Error()))
	}
	backend.Stop()

	waitCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	backend.WaitIdle(waitCtx)
	if err := smtpServer.Close(); err != nil && !errors.Is(err, smtp.ErrServerClosed) {
		logger.Warn("close smtp server", slog.String("err", err.Error()))
	}

	shutdownCtx, cancel2 := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel2()
	if err := apiServer.Shutdown(shutdownCtx); err != nil {
		logger.Warn("shutdown admin http server", slog.String("err", err.Error()))
	}
	if err := publicServer.Shutdown(shutdownCtx); err != nil {
		logger.Warn("shutdown public http server", slog.String("err", err.Error()))
	}
}

// newLogger builds the JSON logger; LOG_LEVEL (debug/info/warn/error)
// controls verbosity.
func newLogger() *slog.Logger {
	level := slog.LevelInfo
	switch strings.ToLower(os.Getenv("LOG_LEVEL")) {
	case "debug":
		level = slog.LevelDebug
	case "warn":
		level = slog.LevelWarn
	case "error":
		level = slog.LevelError
	}
	return slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: level}))
}
