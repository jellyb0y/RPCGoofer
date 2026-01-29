package main

import (
	"context"
	"flag"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/rs/zerolog"

	"rpcgofer/internal/config"
	"rpcgofer/internal/server"
)

func main() {
	// Parse flags
	configPath := flag.String("config", "config.json", "path to config file")
	flag.Parse()

	// Load config
	cfg, err := config.LoadWithDefaults(*configPath)
	if err != nil {
		// Basic logger for startup errors
		log := zerolog.New(os.Stderr).With().Timestamp().Logger()
		log.Fatal().Err(err).Msg("failed to load config")
	}

	// Setup logger
	logger := setupLogger(cfg.LogLevel)
	logger.Info().
		Str("config", *configPath).
		Str("host", cfg.Host).
		Int("rpcPort", cfg.RPCPort).
		Int("wsPort", cfg.WSPort).
		Int("groups", len(cfg.Groups)).
		Msg("starting RPCGofer")

	// Create server
	srv, err := server.New(cfg, logger)
	if err != nil {
		logger.Fatal().Err(err).Msg("failed to create server")
	}

	// Add groups
	for _, groupCfg := range cfg.Groups {
		srv.AddGroup(groupCfg)
	}

	// Start server
	if err := srv.Start(); err != nil {
		logger.Fatal().Err(err).Msg("failed to start server")
	}

	// Wait for shutdown signal
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	sig := <-quit

	logger.Info().Str("signal", sig.String()).Msg("received shutdown signal")

	// Graceful shutdown with timeout
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	if err := srv.Stop(ctx); err != nil {
		logger.Error().Err(err).Msg("error during shutdown")
	}
}

// setupLogger configures the zerolog logger
func setupLogger(level string) zerolog.Logger {
	// Set log level
	var logLevel zerolog.Level
	switch level {
	case "debug":
		logLevel = zerolog.DebugLevel
	case "info":
		logLevel = zerolog.InfoLevel
	case "warn":
		logLevel = zerolog.WarnLevel
	case "error":
		logLevel = zerolog.ErrorLevel
	default:
		logLevel = zerolog.InfoLevel
	}

	zerolog.SetGlobalLevel(logLevel)

	// Configure output
	output := zerolog.ConsoleWriter{
		Out:        os.Stdout,
		TimeFormat: time.RFC3339,
	}

	return zerolog.New(output).With().Timestamp().Logger()
}
