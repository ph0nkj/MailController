// Command mailcontroller starts the MailController web service.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/ph0nkj/mailcontroller/internal/agent"
	"github.com/ph0nkj/mailcontroller/internal/auth"
	"github.com/ph0nkj/mailcontroller/internal/config"
	mcrypto "github.com/ph0nkj/mailcontroller/internal/crypto"
	"github.com/ph0nkj/mailcontroller/internal/gmail"
	"github.com/ph0nkj/mailcontroller/internal/healthcheck"
	"github.com/ph0nkj/mailcontroller/internal/httpapi"
	"github.com/ph0nkj/mailcontroller/internal/jobs"
	"github.com/ph0nkj/mailcontroller/internal/llm"
	"github.com/ph0nkj/mailcontroller/internal/mailer"
	"github.com/ph0nkj/mailcontroller/internal/store"
)

func main() {
	if len(os.Args) == 2 && os.Args[1] == "-healthcheck" {
		if err := runHealthcheck(); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		return
	}
	if err := run(); err != nil {
		slog.Error("mailcontroller stopped", "error", err)
		os.Exit(1)
	}
}

func runHealthcheck() error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	addr := os.Getenv("MC_ADDR")
	if addr == "" {
		addr = config.DefaultAddr
	}
	return healthcheck.Run(ctx, addr)
}

func run() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}

	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: cfg.LogLevel}))
	slog.SetDefault(logger)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	st, err := store.Open(ctx, cfg.DBPath)
	if err != nil {
		return fmt.Errorf("open store: %w", err)
	}
	defer func() {
		if closeErr := st.Close(); closeErr != nil {
			logger.Error("close store", "error", closeErr)
		}
	}()

	authService, err := auth.New(ctx, st.Settings(), cfg.AdminPassword, cfg.CookieSecure, logger)
	if err != nil {
		return fmt.Errorf("initialise auth: %w", err)
	}

	secretCipher, err := mcrypto.New(cfg.SecretKey)
	if err != nil {
		return fmt.Errorf("initialise secret cipher: %w", err)
	}

	gmailService, err := gmail.NewService(
		st.Settings(),
		st.GoogleAccount(),
		secretCipher,
		logger,
		gmail.Options{
			BaseURL:      cfg.BaseURL,
			ClientID:     cfg.GoogleClientID,
			ClientSecret: cfg.GoogleClientSecret,
		},
	)
	if err != nil {
		return fmt.Errorf("initialise gmail service: %w", err)
	}

	llmService, err := llm.NewService(
		st.Settings(),
		secretCipher,
		gmailService,
		st.Audit(),
		logger,
		llm.Options{
			MaxMessages: cfg.LLMMaxMessages,
			BodyChars:   cfg.LLMBodyChars,
		},
	)
	if err != nil {
		return fmt.Errorf("initialise llm service: %w", err)
	}

	runner := jobs.NewRunner(50, 30*time.Minute)
	agentService, err := agent.NewService(
		agent.NewLLMPlanner(llmService, cfg.AgentMaxSteps),
		gmailService,
		st.AgentRuns(),
		runner,
		st.Audit(),
		logger,
		agent.Options{
			MaxMessages: cfg.AgentMaxMessages,
			MaxSteps:    cfg.AgentMaxSteps,
		},
	)
	if err != nil {
		return fmt.Errorf("initialise agent service: %w", err)
	}
	if recovered, recoverErr := agentService.RecoverInterrupted(ctx); recoverErr != nil {
		logger.Warn("recover agent runs", "error", recoverErr)
	} else if recovered > 0 {
		logger.Info("recovered interrupted agent runs", "count", recovered)
	}

	campaignOptions := []mailer.Option{
		mailer.WithSendCaps(cfg.SendRatePerMin, cfg.DailySendLimit),
	}
	if cfg.UnsubscribeEnabled {
		campaignOptions = append(campaignOptions, mailer.WithUnsubscribe(cfg.UnsubscribeEmail))
	}
	campaignEngine, err := mailer.NewEngine(
		logger,
		st.Campaigns(),
		func(ctx context.Context) (mailer.Sender, error) {
			client, clientErr := gmailService.Client(ctx)
			if clientErr != nil {
				return nil, clientErr
			}
			return client, nil
		},
		runner,
		st.Audit(),
		campaignOptions...,
	)
	if err != nil {
		return fmt.Errorf("initialise campaign engine: %w", err)
	}
	if _, err := campaignEngine.RecoverRunning(ctx); err != nil {
		logger.Warn("recover running campaigns", "error", err)
	}

	api := httpapi.New(logger, cfg, httpapi.Deps{
		Auth:           authService,
		Google:         gmailService,
		Mailbox:        gmailService,
		Audit:          st.Audit(),
		Jobs:           runner,
		Campaigns:      st.Campaigns(),
		CampaignRunner: campaignEngine,
		LLM:            llmService,
		Agent:          agentService,
	})
	server := &http.Server{
		Addr:              cfg.Addr,
		Handler:           api.Router(),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       60 * time.Second,
		WriteTimeout:      120 * time.Second,
		IdleTimeout:       120 * time.Second,
	}

	serverErr := make(chan error, 1)
	go func() {
		logger.Info("mailcontroller listening", "addr", cfg.Addr, "base_url", cfg.BaseURL)
		serverErr <- server.ListenAndServe()
	}()

	select {
	case <-ctx.Done():
		logger.Info("shutdown requested")
	case err := <-serverErr:
		if !errors.Is(err, http.ErrServerClosed) {
			return fmt.Errorf("http server: %w", err)
		}
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := server.Shutdown(shutdownCtx); err != nil {
		return fmt.Errorf("shutdown server: %w", err)
	}
	return nil
}
