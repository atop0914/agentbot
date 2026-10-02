package app

import (
	"log/slog"
	"net/http"
	"os"
	"time"

	"github.com/atop0914/agentbot/internal/adapter"
	"github.com/atop0914/agentbot/internal/admin"
	"github.com/atop0914/agentbot/internal/agent"
	"github.com/atop0914/agentbot/internal/audit"
	"github.com/atop0914/agentbot/internal/auth"
	"github.com/atop0914/agentbot/internal/authz"
	"github.com/atop0914/agentbot/internal/browser"
	"github.com/atop0914/agentbot/internal/cloud"
	"github.com/atop0914/agentbot/internal/communication"
	"github.com/atop0914/agentbot/internal/executor"
	"github.com/atop0914/agentbot/internal/filesystem"
	"github.com/atop0914/agentbot/internal/memory"
	"github.com/atop0914/agentbot/internal/monitor"
	"github.com/atop0914/agentbot/internal/role"
	"github.com/atop0914/agentbot/internal/template"
	"github.com/atop0914/agentbot/internal/terminal"
	"github.com/atop0914/agentbot/internal/user"
	"github.com/atop0914/agentbot/internal/websocket"
)

// adminConsoleDir 是前端构建产物的默认落点。
// 目录不存在时管理后台返回占位页，不影响服务启动。
const adminConsoleDir = "web/admin/dist"

// App holds all application dependencies
type App struct {
	Logger       *slog.Logger
	Auth         *auth.Service
	AuthHandler  *auth.Handler
	AuthMW       *auth.Middleware
	Agent        agent.Service
	AgentH       *agent.Handler
	Cloud        *cloud.Service
	CloudH       *cloud.Handler
	TaskMgr      *executor.Handler
	Comm         *communication.CommService
	CommH        *communication.Handler
	Presence     *communication.PresenceManager
	Messenger    *communication.AgentMessengerImpl
	WSHub        *websocket.Hub
	WSHandler    *websocket.Handler
	BrowserH     *browser.Handler
	TerminalH    *terminal.Handler
	FileSystemH  *filesystem.Handler
	AdapterH     *adapter.Handler
	AdapterSvc   *adapter.Service
	MemoryH      *memory.Handler
	RoleSvc      role.Service
	RoleH        *role.Handler
	AuthzSvc     authz.Service
	AuthzH       *authz.Handler
	UserAdminH   *user.AdminHandler
	TemplateH    *template.Handler
	MarketplaceH *template.MarketplaceHandler
	MonitorSvc   monitor.Service
	MonitorH     *monitor.Handler
	AuditSvc     audit.Service
	AuditH       *audit.Handler
	AuditRec     audit.Recorder
	AdminSvc     admin.Service
	AdminH       *admin.Handler
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

	// Authorization chain (user/agent -> role -> permission) — RBAC.
	//
	// 权限判定是平台的安全边界，因此它依赖的是**具体的** agent/user 服务而非接口，
	// 保证装配期就能发现缺失依赖（而不是运行时以 503 的形式暴露）。
	authzTargets := authz.NewTargetRegistry()
	registerAuthzTargets(authzTargets, agentSvc, userSvc)
	authzSvc := authz.NewService(authz.NewMemoryStore(), ensureRoleService(roleSvc), authzTargets)
	authzH := authz.NewHandler(authzSvc, nil)
	authzH.SetSubjectNameResolver(authzSubjectName(userSvc, agentSvc))
	userAdminH := user.NewAdminHandler(userSvc)
	userAdminH.SetAssignmentLister(userAssignments(authzSvc))

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

	// Monitoring (agent health & alerts)
	monitorRepo := monitor.NewMemoryRepository()

	// Audit log (需要早于监控装配：告警处置要落审计)
	auditRepo := audit.NewMemoryRepository()
	auditSvc := audit.NewService(auditRepo)
	auditH := audit.NewHandler(auditSvc)
	auditRec := audit.NewRecorder(auditSvc)

	// 把告警处置接到审计：认领/解决/重开都会留下一条 agent 维度的审计事件，
	// 明细里带处置人、备注与状态迁移边界。
	monitorSvc := monitor.NewServiceWithRecorder(monitorRepo, monitorDispositionRecorder{rec: auditRec})
	monitorH := monitor.NewHandler(monitorSvc)

	// WebSocket
	wsHub := websocket.NewHub(logger)
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

	// Admin console (aggregate read-only view over the other modules)
	adminSources := admin.Sources{
		Agents:  adminAgentSource{svc: agentSvc},
		Tasks:   adminTaskSource{mgr: taskSvc},
		Monitor: adminMonitorSource{svc: monitorSvc},
		Audit:   adminAuditSource{svc: auditSvc},
		Users:   adminUserSource{svc: userSvc},
		Authz:   adminAuthzSource{svc: authzSvc},
	}
	adminSvc := admin.NewService(adminSources, "v1.0.0")
	adminH := admin.NewHandler(adminSvc, adminConsoleDir)

	return &App{
		Logger:       logger,
		Auth:         authSvc,
		AuthHandler:  authHandler,
		AuthMW:       authMW,
		Agent:        agentSvc,
		AgentH:       agentH,
		Cloud:        cloudSvc,
		CloudH:       cloudH,
		TaskMgr:      taskH,
		Comm:         commSvc,
		CommH:        commH,
		Presence:     presence,
		Messenger:    messenger,
		WSHub:        wsHub,
		WSHandler:    wsHandler,
		BrowserH:     browserH,
		TerminalH:    terminalH,
		FileSystemH:  fsH,
		AdapterH:     adapterH,
		AdapterSvc:   adapterSvc,
		MemoryH:      memoryH,
		RoleSvc:      roleSvc,
		RoleH:        roleH,
		AuthzSvc:     authzSvc,
		AuthzH:       authzH,
		UserAdminH:   userAdminH,
		TemplateH:    tmplH,
		MarketplaceH: marketplaceH,
		MonitorSvc:   monitorSvc,
		MonitorH:     monitorH,
		AuditSvc:     auditSvc,
		AuditH:       auditH,
		AuditRec:     auditRec,
		AdminSvc:     adminSvc,
		AdminH:       adminH,
	}
}
