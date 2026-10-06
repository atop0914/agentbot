package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/atop0914/agentbot/internal/app"
	"github.com/atop0914/agentbot/internal/config"
	"github.com/atop0914/agentbot/internal/version"
)

func main() {
	os.Exit(run(os.Args[1:]))
}

// run 是 main 的可测形态：返回退出码而不是直接 os.Exit，
// 让「配置缺失时以非零码退出」这件事可以被测试断言。
//
// 这也是本轮部署加固的核心：进程的启动条件从「能编译就能跑」变成了
// 「必需机密齐备才能跑」。之前 app.New() 里写死了一个 dev 密钥，
// 忘了注入密钥的部署会安静地带着公开可伪造的密钥对外服务。
func run(args []string) int {
	fs := flag.NewFlagSet("agentbot", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	configPath := fs.String("config", "", "path to a JSON configuration file (optional)")
	showVersion := fs.Bool("version", false, "print version information and exit")
	if err := fs.Parse(args); err != nil {
		return 2
	}

	if *showVersion {
		v := version.Get()
		fmt.Printf("agentbot %s (commit %s, built %s, %s, %s)\n",
			v.Version, v.GitCommit, v.BuildDate, v.GoVersion, v.Platform)
		return 0
	}

	// config.Load 是**唯一**读取环境变量的地方，fail-fast 也发生在这里。
	cfg, err := config.Load(*configPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "agentbot: configuration error: %v\n\n", err)
		fmt.Fprintf(os.Stderr, "Required secrets (there is no usable default):\n")
		fmt.Fprintf(os.Stderr, "  %sAUTH_JWT_SECRET    at least %d bytes of randomness\n",
			config.EnvPrefix, config.MinJWTSecretLength)
		fmt.Fprintf(os.Stderr, "  %sDATABASE_PASSWORD  database password\n", config.EnvPrefix)
		fmt.Fprintf(os.Stderr, "  %sSSO_CLIENT_SECRET  only required when SSO is configured\n", config.EnvPrefix)
		fmt.Fprintf(os.Stderr, "Precedence: defaults -> config file -> environment. See docs/deployment.md.\n")
		return 1
	}

	application, err := app.NewWithConfig(cfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "agentbot: cannot assemble application: %v\n", err)
		return 1
	}
	logger := application.Logger

	v := version.Get()
	logger.Info("starting agentbot",
		"version", v.Version,
		"commit", v.GitCommit,
		"built", v.BuildDate,
		"go", v.GoVersion,
		"log_level", cfg.Log.Level,
		"sso_configured", cfg.SSO.Configured(),
	)

	server := &http.Server{
		Addr:         cfg.Server.Addr(),
		Handler:      app.NewRouter(application),
		ReadTimeout:  cfg.Server.ReadTimeout,
		WriteTimeout: cfg.Server.WriteTimeout,
		IdleTimeout:  cfg.Server.IdleTimeout,
	}

	serverErr := make(chan error, 1)
	go func() {
		logger.Info("http server listening", "addr", server.Addr)
		if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			serverErr <- err
		}
	}()

	// 指标端点：端口与路径都来自配置。硬编码 :9090 在多实例部署里会撞端口，
	// 而那种失败发生在启动数秒之后，表现为「服务莫名退出」，最难与配置关联。
	var metricsServer *http.Server
	if cfg.Metrics.Enabled {
		metricsMux := http.NewServeMux()
		metricsMux.HandleFunc(cfg.Metrics.Path, func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
			fmt.Fprint(w, "# AgentBot metrics (stub)\n")
			fmt.Fprintf(w, "agentbot_build_info{version=%q,commit=%q} 1\n", v.Version, v.GitCommit)
		})
		metricsServer = &http.Server{
			Addr:              fmt.Sprintf(":%d", cfg.Metrics.Port),
			Handler:           metricsMux,
			ReadHeaderTimeout: 5 * time.Second,
		}
		go func() {
			logger.Info("metrics server listening",
				"addr", metricsServer.Addr, "path", cfg.Metrics.Path)
			if err := metricsServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
				// 指标端点起不来不该拖垮主服务：它是可观测性辅助，不是业务路径。
				logger.Error("metrics server stopped", "error", err)
			}
		}()
	}

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)

	exitCode := 0
	select {
	case <-quit:
		logger.Info("shutdown signal received")
	case err := <-serverErr:
		logger.Error("http server failed", "error", err)
		exitCode = 1
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	if err := server.Shutdown(ctx); err != nil {
		logger.Error("server forced to shutdown", "error", err)
	}
	if metricsServer != nil {
		if err := metricsServer.Shutdown(ctx); err != nil {
			logger.Warn("metrics server shutdown failed", "error", err)
		}
	}

	application.WSHub.Stop()
	if n := application.WSHub.ConnCount(); n > 0 {
		logger.Warn("shutdown with active connections", "count", n)
	}

	logger.Info("server exited properly")
	return exitCode
}
