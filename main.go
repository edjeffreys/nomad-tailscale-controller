package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"github.com/edjeffreys/nomad-tailscale-controller/internal/config"
	"github.com/edjeffreys/nomad-tailscale-controller/internal/tailscale"
	"github.com/edjeffreys/nomad-tailscale-controller/internal/watcher"
	"go.uber.org/zap"
)

func main() {
	logCfg := zap.NewProductionConfig()
	if os.Getenv("LOG_LEVEL") == "debug" {
		logCfg.Level = zap.NewAtomicLevelAt(zap.DebugLevel)
	}
	logger, _ := logCfg.Build()
	defer logger.Sync()

	cfg, err := config.FromEnv()
	if err != nil {
		logger.Fatal("failed to load config", zap.Error(err))
	}

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	tsClient := tailscale.NewClient(cfg.TailscaleSocket, logger)
	w := watcher.NewWatcher(cfg, tsClient, logger)

	logger.Info("starting nomad-tailscale-controller",
		zap.String("consul_addr", cfg.ConsulAddr),
		zap.String("tailnet", cfg.Tailnet),
		zap.Duration("poll_interval", cfg.PollInterval),
	)

	if err := w.Run(ctx); err != nil {
		logger.Fatal("controller exited with error", zap.Error(err))
	}

	logger.Info("controller stopped")
}
