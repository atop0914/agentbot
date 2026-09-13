package app

import (
	"log/slog"
	"net/http"
	"os"

	"github.com/atop0914/agentbot/internal/agent"
	"github.com/atop0914/agentbot/internal/auth"
	"github.com/atop0914/agentbot/internal/cloud"
	"github.com/atop0914/agentbot/internal/communication"
	"github.com/atop0914/agentbot/internal/executor"
	"github.com/atop0914/agentbot/internal/user"
	"github.com/atop0914/agentbot/internal/websocket"
)

// App holds all application dependencies
type App struct {
	Logger      *slog.Logger
	Auth        *auth.Service
	AuthHandler *auth.Handler
	AuthMW      *auth.Middleware
	Agent       agent.Service
	AgentH      *agent.Handler
	Cloud       *cloud.Service
	CloudH      *cloud.Handler
	TaskMgr     *executor.Handler
	Comm        *communication.CommService
	CommH       *communication.Handler
	WSHub       *websocket.Hub
	WSHandler   *websocket.Handler
}

// New creates a new App with all in-memory services wired up.
// This is the standalone bootstrap for development/testing.
func New() *App {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))

	// User service (in-memory)
	userRepo := user.NewMemoryRepository()
	userSvc := user.NewUserService(userRepo)

	// Auth service
	jwtCfg := auth.TokenConfig{
		Secret:        "dev-secret-change-in-production",
		AccessExpiry:  3600e9,   // 1 hour in nanoseconds
		RefreshExpiry: 604800e9, // 7 days
		Issuer:        "agentbot",
	}
	jwtMgr := auth.NewJWTManager(jwtCfg)
	oauthCfgs := make(map[string]*auth.OAuthConfig)
	authSvc := auth.NewService(jwtMgr, userSvc, oauthCfgs)
	authHandler := auth.NewHandler(authSvc)
	authMW := auth.NewMiddleware(authSvc)

	// Agent service
	agentRepo := agent.NewMemoryRepository()
	agentSvc := agent.NewService(agentRepo)
	agentH := agent.NewHandler(agentSvc)

	// Cloud service
	cloudRepo := cloud.NewMemoryRepository()
	cloudLocal, _ := cloud.NewLocalManager("")
	cloudSvc := cloud.NewService(cloudLocal, cloudRepo)
	cloudH := cloud.NewHandler(cloudSvc)

	// Task executor
	taskRepo := executor.NewMemoryRepository()
	taskSvc := executor.NewService(taskRepo)
	taskH := executor.NewHandler(taskSvc)

	// Communication
	commRepo := communication.NewMemoryRepository()
	commBus := communication.NewMemoryBus()
	commSvc := communication.NewCommService(commRepo, commBus)
	commH := communication.NewHandler(commSvc, logger)

	// WebSocket
	wsHub := websocket.NewHub(logger)
	go wsHub.Run()
	wsHandler := websocket.NewHandler(wsHub, logger)

	// Wire auth for WebSocket (optional auth via query token)
	wsHandler.AuthFunc = func(r *http.Request) (string, error) {
		token := r.URL.Query().Get("token")
		if token == "" {
			return "", nil
		}
		claims, err := authSvc.ValidateAccessToken(token)
		if err != nil {
			return "", err
		}
		return claims.UserID, nil
	}

	return &App{
		Logger:      logger,
		Auth:        authSvc,
		AuthHandler: authHandler,
		AuthMW:      authMW,
		Agent:       agentSvc,
		AgentH:      agentH,
		Cloud:       cloudSvc,
		CloudH:      cloudH,
		TaskMgr:     taskH,
		Comm:        commSvc,
		CommH:       commH,
		WSHub:       wsHub,
		WSHandler:   wsHandler,
	}
}
