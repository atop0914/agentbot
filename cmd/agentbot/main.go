package main

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/atop0914/agentbot/internal/app"
)

func main() {
	// Initialize application (all in-memory services)
	a := app.New()
	logger := a.Logger

	logger.Info("Starting AgentBot server...")

	// Build HTTP server
	server := &http.Server{
		Addr:         ":8080",
		Handler:      app.NewRouter(a),
		ReadTimeout:  30 * time.Second,
		WriteTimeout: 30 * time.Second,
		IdleTimeout:  120 * time.Second,
	}

	// Start server
	go func() {
		logger.Info("Server starting", "addr", server.Addr)
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			logger.Error("Server failed", "error", err)
			os.Exit(1)
		}
	}()

	// Start metrics stub (placeholder for future Prometheus)
	go func() {
		metricsMux := http.NewServeMux()
		metricsMux.HandleFunc("/metrics", func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
			fmt.Fprintln(w, "# AgentBot metrics placeholder")
		})
		metricsServer := &http.Server{Addr: ":9090", Handler: metricsMux}
		logger.Info("Metrics server starting", "addr", ":9090")
		if err := metricsServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			logger.Warn("Metrics server stopped", "error", err)
		}
	}()

	// Wait for shutdown signal
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit

	logger.Info("Shutting down server...")

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	if err := server.Shutdown(ctx); err != nil {
		logger.Error("Server forced to shutdown", "error", err)
	}

	// Stop WebSocket hub
	a.WSHub.Stop()

	// Log connected users (for observability)
	if n := a.WSHub.ConnCount(); n > 0 {
		logger.Warn("shutdown with active connections", "count", n)
	}

	logger.Info("Server exited properly")
}

// initLogger is no longer needed — app.New() configures slog internally.
// Kept as a note for future customisation.
func initLogger(level string) *slog.Logger {
	var lvl slog.Level
	switch level {
	case "debug":
		lvl = slog.LevelDebug
	case "warn":
		lvl = slog.LevelWarn
	case "error":
		lvl = slog.LevelError
	default:
		lvl = slog.LevelInfo
	}
	return slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: lvl}))
}
