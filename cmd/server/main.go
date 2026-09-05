package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/vivianobiako/qless/api/internal/api"
	"github.com/vivianobiako/qless/api/internal/config"
	"github.com/vivianobiako/qless/api/internal/database"
	"github.com/vivianobiako/qless/api/internal/push"
	"github.com/vivianobiako/qless/api/internal/storage"
)

func main() {
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo})))

	if err := run(); err != nil {
		slog.Error("server stopped", "error", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}

	if err := database.Migrate(cfg.DatabaseURL); err != nil {
		return err
	}
	slog.Info("migrations applied")

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	store, err := storage.New(ctx, cfg.DatabaseURL)
	if err != nil {
		return err
	}
	defer store.Close()

	sender := push.New(cfg.VAPIDPublicKey, cfg.VAPIDPrivateKey, cfg.VAPIDSubject)
	if sender.Enabled() {
		slog.Info("push notifications on", "subject", cfg.VAPIDSubject)
	} else {
		slog.Info("push notifications off: set VAPID_PUBLIC_KEY and VAPID_PRIVATE_KEY (go run ./cmd/vapid)")
	}

	server := &http.Server{
		Addr:              ":" + cfg.Port,
		Handler:           api.NewServer(store).WithPush(sender, cfg.WebOrigin()).Routes(cfg.AllowedOrigins...),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       2 * time.Minute,
	}

	errs := make(chan error, 1)
	go func() {
		slog.Info("qless api listening", "port", cfg.Port, "origins", cfg.AllowedOrigins)
		if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errs <- err
		}
	}()

	select {
	case err := <-errs:
		return err
	case <-ctx.Done():
		slog.Info("shutting down")
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return server.Shutdown(shutdownCtx)
}
