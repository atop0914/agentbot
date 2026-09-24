package app

import (
	"log/slog"
	"net/http"
	"os"
	"time"

	"github.com/atop0914/agentbot/internal/agent"
	"github.com/atop0914/agentbot/internal/auth"
	"github.com/atop0914/agentbot/internal/browser"
	"github.com/atop0914/agentbot/internal/cloud"
	"github.com/atop0914/agentbot/internal/communication"
	"github.com/atop0914/agentbot/internal/executor"
	"github.com/atop0914/agentbot/internal/adapter"
	"github.com/atop0914/agentbot/internal/filesystem"
	"github.com/atop0914/agentbot/internal/memory"
	"github.com/atop0914/agentbot/internal/role"
	"github.com/atop0914/agentbot/internal/terminal"
	"github.com/atop0914/agentbot/internal/user"
	"github.com/atop0914/agentbot/internal/template"
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
	Presence    *communication.PresenceManager
	Messenger   *communication.AgentMessengerImpl
	WSHub       *websocket.Hub
	WSHandler   *websocket.Handler
	BrowserH    *browser.Handler
	TerminalH   *terminal.Handler
	FileSystemH *filesystem.Handler
	AdapterH    *adapter.Handler
	AdapterSvc  *adapter.Service
	MemoryH     *memory.Handler
	RoleSvc        role.Service
	RoleH          *role.Handler
	TemplateH      *template.Handler
	MarketplaceH   *template.MarketplaceHandler
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

	// Presence manager (30s heartbeat timeout)
	presence := communication.NewPresenceManager(30 * time.Second)

	// Agent messenger with request-response protocol
	messengerCfg := communication.DefaultMessengerConfig()
	messenger := communication.NewAgentMessenger(messengerCfg, commBus, presence, commRepo)

	// Browser automation
	browserRepo := browser.NewMemoryRepository()
	browserSvc := browser.NewLocalService(browserRepo)
	browserH := browser.NewHandler(browserSvc)

	// Terminal execution
	terminalMgr := terminal.NewLocalManager()
	terminalRepo := terminal.NewMemoryRepository()
	terminalSvc := terminal.NewService(terminalMgr, terminalRepo)
	terminalH := terminal.NewHandler(terminalSvc)

	// Filesystem
	fsMgr, _ := filesystem.NewLocalManager("/tmp/agentbot-fs")
	fsRepo := filesystem.NewMemoryRepository()
	fsSvc := filesystem.NewService(fsMgr, fsRepo)
	fsH := filesystem.NewHandler(fsSvc)

	// Application adapters
	adapterRegistry := adapter.NewMemoryRegistry()
	adapterRegistry.RegisterFactory(adapter.AdapterTypeEmail, adapter.NewEmailAdapter)
	adapterRegistry.RegisterFactory(adapter.AdapterTypeCalendar, adapter.NewCalendarAdapter)
	adapterSvc := adapter.NewService(adapterRegistry)
	adapterH := adapter.NewHandler(adapterSvc)

	// Memory system
	memoryStore := memory.NewMemoryStore()
	memorySvc := memory.NewMemoryService(memoryStore)
	memoryH := memory.NewHandler(memorySvc)

	// Role system
	roleRepo := role.NewMemoryRepository()
	roleSvc := role.NewService(roleRepo)
	roleH := role.NewHandler(roleSvc)

	// Template system
	tmplRepo := template.NewMemoryRepository()
	tmplExecRepo := template.NewMemoryExecRepository()
	tmplSvc := template.NewService(tmplRepo, tmplExecRepo)
	tmplRecorder := template.NewInMemoryRecorder(tmplRepo)
	tmplH := template.NewHandler(tmplSvc, tmplRecorder)

	// Marketplace
	marketplaceRepo := template.NewInMemoryMarketplaceRepo()
	reviewRepo := template.NewInMemoryReviewRepo()
	marketplaceSvc := template.NewMarketplaceService(marketplaceRepo, reviewRepo, tmplRepo)
	marketplaceH := template.NewMarketplaceHandler(marketplaceSvc)

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
		Presence:    presence,
		Messenger:   messenger,
		WSHub:       wsHub,
		WSHandler:   wsHandler,
		BrowserH:    browserH,
		TerminalH:   terminalH,
		FileSystemH: fsH,
		AdapterH:    adapterH,
		AdapterSvc:  adapterSvc,
		MemoryH:     memoryH,
		RoleSvc:     roleSvc,
		RoleH:          roleH,
		TemplateH:      tmplH,
		MarketplaceH:   marketplaceH,
	}
}