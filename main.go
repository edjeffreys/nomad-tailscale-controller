package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"github.com/yourorg/nomad-tailscale-controller/internal/config"
	"github.com/yourorg/nomad-tailscale-controller/internal/nomad"
	"github.com/yourorg/nomad-tailscale-controller/internal/tailscale"
	"go.uber.org/zap"
)

func main() {
	logger, _ := zap.NewProduction()
	defer logger.Sync()

	cfg, err := config.FromEnv()
	if err != nil {
		logger.Fatal("failed to load config", zap.Error(err))
	}

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	tsClient := tailscale.NewClient(cfg.TailscaleSocket, logger)
	nomadWatcher := nomad.NewWatcher(cfg, tsClient, logger)

	logger.Info("starting nomad-tailscale-controller",
		zap.String("nomad_addr", cfg.NomadAddr),
		zap.String("tailnet", cfg.Tailnet),
		zap.Duration("poll_interval", cfg.PollInterval),
	)

	if err := nomadWatcher.Run(ctx); err != nil {
		logger.Fatal("controller exited with error", zap.Error(err))
	}

	logger.Info("controller stopped")
}
