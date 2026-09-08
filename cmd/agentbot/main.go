package main

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/atop0914/agentbot/pkg/config"
	"github.com/redis/go-redis/v9"
)

// Services holds all service dependencies
type Services struct {
	// TODO: Add service fields
}

func main() {
	// Load config
	cfg, err := config.Load("config.json")
	if err != nil {
		slog.Error("Failed to load config", "error", err)
		os.Exit(1)
	}

	// Initialize logger
	logger := initLogger(cfg.Log)
	logger.Info("Starting AgentBot server...")

	// Initialize database
	db, err := initDatabase(cfg.Database)
	if err != nil {
		logger.Error("Failed to initialize database", "error", err)
		os.Exit(1)
	}
	defer db.Close()

	// Initialize Redis
	rdb, err := initRedis(cfg.Redis)
	if err != nil {
		logger.Error("Failed to initialize Redis", "error", err)
		os.Exit(1)
	}
	defer rdb.Close()

	// Initialize services
	services := initServices(db, rdb, cfg)

	// Initialize HTTP server
	server := &http.Server{
		Addr:         fmt.Sprintf("%s:%d", cfg.Server.Host, cfg.Server.Port),
		Handler:      initRouter(services),
		ReadTimeout:  cfg.Server.ReadTimeout,
		WriteTimeout: cfg.Server.WriteTimeout,
		IdleTimeout:  cfg.Server.IdleTimeout,
	}

	// Start server
	go func() {
		logger.Info("Server starting", "addr", server.Addr)
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			logger.Error("Server failed", "error", err)
			os.Exit(1)
		}
	}()

	// Start metrics server if enabled
	if cfg.Metrics.Enabled {
		go func() {
			metricsAddr := fmt.Sprintf(":%d", cfg.Metrics.Port)
			logger.Info("Metrics server starting", "addr", metricsAddr)
			http.Handle(cfg.Metrics.Path, metricsHandler())
			if err := http.ListenAndServe(metricsAddr, nil); err != nil {
				logger.Error("Metrics server failed", "error", err)
			}
		}()
	}

	// Start background workers
	startWorkers(services, logger)

	// Wait for shutdown signal
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit

	logger.Info("Shutting down server...")

	// Graceful shutdown
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	if err := server.Shutdown(ctx); err != nil {
		logger.Error("Server forced to shutdown", "error", err)
	}

	// Cleanup resources
	cleanup(services, logger)

	logger.Info("Server exited properly")
}

func initLogger(cfg config.LogConfig) *slog.Logger {
	// TODO: Initialize structured logger with level and format
	return slog.Default()
}

func initDatabase(cfg config.DatabaseConfig) (*sql.DB, error) {
	// TODO: Initialize PostgreSQL connection
	// dsn := fmt.Sprintf("host=%s port=%d user=%s password=%s dbname=%s sslmode=%s",
	//     cfg.Host, cfg.Port, cfg.User, cfg.Password, cfg.DBName, cfg.SSLMode)
	// return sql.Open("postgres", dsn)
	return nil, nil
}

func initRedis(cfg config.RedisConfig) (*redis.Client, error) {
	// TODO: Initialize Redis connection
	// rdb := redis.NewClient(&redis.Options{
	//     Addr:     fmt.Sprintf("%s:%d", cfg.Host, cfg.Port),
	//     Password: cfg.Password,
	//     DB:       cfg.DB,
	// })
	// return rdb, nil
	return nil, nil
}

func initServices(db *sql.DB, rdb *redis.Client, cfg *config.Config) *Services {
	// TODO: Initialize all services
	return &Services{}
}

func initRouter(services *Services) http.Handler {
	// TODO: Initialize HTTP router with all endpoints
	mux := http.NewServeMux()
	
	// Health check
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("OK"))
	})
	
	// API v1
	mux.HandleFunc("/api/v1/agents", handleAgents)
	mux.HandleFunc("/api/v1/tasks", handleTasks)
	mux.HandleFunc("/api/v1/templates", handleTemplates)
	mux.HandleFunc("/api/v1/messages", handleMessages)
	
	return mux
}

func startWorkers(services *Services, logger *slog.Logger) {
	// TODO: Start background workers
	// - Task executor worker
	// - Metrics collector worker
	// - Health check worker
	// - Cleanup worker
}

func cleanup(services *Services, logger *slog.Logger) {
	// TODO: Cleanup resources
}

func metricsHandler() http.Handler {
	// TODO: Return Prometheus metrics handler
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
}

func handleAgents(w http.ResponseWriter, r *http.Request) {
	// TODO: Implement agent CRUD endpoints
	switch r.Method {
	case http.MethodGet:
		// List agents or get agent by ID
	case http.MethodPost:
		// Create agent
	case http.MethodPut:
		// Update agent
	case http.MethodDelete:
		// Delete agent
	default:
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
	}
}

func handleTasks(w http.ResponseWriter, r *http.Request) {
	// TODO: Implement task management endpoints
	switch r.Method {
	case http.MethodGet:
		// List tasks or get task by ID
	case http.MethodPost:
		// Create task
	case http.MethodPut:
		// Update task
	case http.MethodDelete:
		// Cancel task
	default:
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
	}
}

func handleTemplates(w http.ResponseWriter, r *http.Request) {
	// TODO: Implement template management endpoints
	switch r.Method {
	case http.MethodGet:
		// List templates or get template by ID
	case http.MethodPost:
		// Create template
	case http.MethodPut:
		// Update template
	case http.MethodDelete:
		// Delete template
	default:
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
	}
}

func handleMessages(w http.ResponseWriter, r *http.Request) {
	// TODO: Implement message endpoints
	switch r.Method {
	case http.MethodGet:
		// Get messages
	case http.MethodPost:
		// Send message
	default:
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
	}
}
