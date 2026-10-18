package app

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"net/http"
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
	"github.com/atop0914/agentbot/internal/config"
	"github.com/atop0914/agentbot/internal/executor"
	"github.com/atop0914/agentbot/internal/filesystem"
	"github.com/atop0914/agentbot/internal/memory"
	"github.com/atop0914/agentbot/internal/metrics"
	"github.com/atop0914/agentbot/internal/monitor"
	"github.com/atop0914/agentbot/internal/network"
	"github.com/atop0914/agentbot/internal/redis"
	"github.com/atop0914/agentbot/internal/role"
	"github.com/atop0914/agentbot/internal/sso"
	"github.com/atop0914/agentbot/internal/storage"
	"github.com/atop0914/agentbot/internal/template"
	"github.com/atop0914/agentbot/internal/tenant"
	"github.com/atop0914/agentbot/internal/terminal"
	"github.com/atop0914/agentbot/internal/user"
	"github.com/atop0914/agentbot/internal/version"
	"github.com/atop0914/agentbot/internal/websocket"
)

// 管理控制台的前端产物落点改由配置提供（admin.console_dir）。
// 容器镜像里是 /srv/agentbot/web，本地开发是 web/admin/dist；
// 目录不存在时后台返回占位页，不影响服务启动。

// App holds all application dependencies
type App struct {
	Logger *slog.Logger
	// Config 是本次装配使用的完整配置。挂在这里是为了可观测：
	// 排障时可以问「这个进程实际用了哪个值」，而不是去猜环境变量。
	Config       config.Config
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
	EgressSvc    network.Service
	EgressH      *network.Handler
	EgressPolicy *network.MemoryPolicyStore
	// 多租户隔离（Day 27）
	TenantSvc tenant.Service
	TenantH   *tenant.Handler
	// 企业 SSO / OIDC（Day 27）
	SSOSvc sso.Service
	SSOH   *sso.Handler
	// 持久化后端（Day 3-3）。Storage 为 nil 表示内存后端。
	//
	// 两个字段而不是一个：Storage 是连接句柄（没有数据库时为 nil），
	// StorageBackend 是**标识**（永远是 postgres 或 memory 之一）。
	// 合成一个字段的话，调用方就无法区分「后端是内存」与「字段没赋值」。
	Storage        *storage.DB
	StorageBackend string
	// DB 与 Storage 指向同一句柄，别名保留给「取底层 *sql.DB」的调用点。
	DB *storage.DB
	// 可观测性（Day 3-7）。Metrics 是零依赖的指标注册表，
	// HTTP 层的打点由装配层完成（见 metrics_wiring.go）。
	//
	// 注册表挂在这里的理由与 Config 相同：排障时要能问
	// 「这个进程到底注册了哪些指标」，而不是去 grep 源码。
	Metrics *metrics.Registry
	// MetricsH 是 /metrics 的 handler。为什么也挂出来：
	// 单测需要在不启动真实监听的情况下直接打这个 handler。
	MetricsH http.Handler
	// httpMetrics 是 HTTP 层指标的持有者。不导出：它是装配细节，
	// 外部只应通过 /metrics 的文本观察，而不是直接摸计数器对象。
	httpMetrics *httpMetrics

	// Redis 是 Redis 客户端；未配置 Redis（或回退到内存）时为 nil。
	// 挂出来的理由与 Storage 相同：排障时要能问「这个进程到底连了没有」。
	Redis *redis.Client
	// RedisBackend 是 Redis 后端标识（memory / redis），用于日志与诊断。
	RedisBackend string
	// RateLimiter 是跨副本限流器；回退到内存时为 nil（此时不限流）。
	//
	// nil 表示「不限制」而不是「限制为零」：把 nil 当成零额度
	// 会让一个未配置 Redis 的部署变成全站 429。
	RateLimiter *redis.RateLimiter
}

// New creates a new App with all in-memory services wired up.
// This is the standalone bootstrap for development/testing.
// build 完成全部的组件装配。
//
// 它是一个不导出、不接受 Logger 的内部函数：日志器与配置的构造都发生在
// options.go 的 NewWithConfig（生产路径）与 New（开发路径）里。这样
// build 里不会再出现任何 os.Getenv / 硬编码机密 —— 装配过程只是接线，
// 取值全部来自外部注入。
//
// 之前的版本在 app.New() 里硬编码了 `Secret: "dev-secret-change-in-production"`，
// 那是一行「开发方便、生产致命」的代码：任何部署只要忘了注入密钥就会
// 带着一个公开可伪造的密钥跑起来，而且完全不报错。
func build(cfg config.Config, logger *slog.Logger, db *storage.DB, rsel redisSelection) (*App, error) {
	// 持久化后端在这里落地：db 为 nil 表示内存后端，否则用 Postgres 实现。
	//
	// 为什么把 *storage.DB 显式传进来，而不是在 build 里再判断一次配置：
	// 「用哪个后端」是一个**全局一次性**决策，必须在 options.go 里做完
	// 并贯彻到底。若 build 自己再读一遍配置，就会产出「选择器说内存、
	// 装配层说 postgres」这类最难查的分歧（3-3 那轮踩过：
	// 配置层与装配层各写一份判定，两边对不上）。
	//
	// Redis 的选择（rsel）走完全相同的约定，理由也一样。
	dbHandle := sqlDB(db)

	// 指标面（Day 3-7）。在任何业务组件之前建立注册表，理由：
	// 后面每个模块的装配都可能失败，而「装配失败的原因」本身也是要观测的 ——
	// 若注册表在末尾才建，早期失败就只剩一行 stderr，没有任何指标可看。
	//
	// 命名空间 agentbot_ 是固定的：同一个 Prometheus 抓多个服务时
	// 无前缀的 http_requests_total 会与其他服务撞名并被静默合并。
	metricReg := metrics.NewRegistry("agentbot")
	httpMetrics, err := newHTTPMetrics(metricReg)
	if err != nil {
		return nil, errors.Join(errMetricsSetupFailed, err)
	}
	// 存储后端的选择在 options.go 里已经做完（这里的 db 就是结论），
	// 因此这里只是把**结论**翻译成指标，不再做任何判定 ——
	// 两处各判一次会产出「指标说 postgres、实际用内存」这类分歧。
	if err := registerStorageMetrics(metricReg, backendOf(db), cfg.Storage.AllowMemoryFallback); err != nil {
		return nil, errors.Join(errMetricsSetupFailed, err)
	}

	// User service：配了数据库就用 Postgres 实现，否则内存。
	// 内存实现**不删除** —— 它是回退路径，且被契约测试持续覆盖。
	var userRepo user.Repository
	if dbHandle != nil {
		userRepo = user.NewPostgresRepository(dbHandle)
		logger.Debug("user repository backed by postgres")
	} else {
		userRepo = user.NewMemoryRepository()
	}
	userSvc := user.NewUserService(userRepo)

	// Auth service
	jwtCfg := auth.TokenConfig{
		Secret:        cfg.Auth.JWTSecret,
		AccessExpiry:  cfg.Auth.AccessExpiry,
		RefreshExpiry: cfg.Auth.RefreshExpiry,
		Issuer:        cfg.Auth.Issuer,
	}
	jwtMgr := auth.NewJWTManager(jwtCfg)
	oauthCfgs := make(map[string]*auth.OAuthConfig)
	// 吊销表由装配层注入（见 redis_wiring.go）：多副本部署下必须共享，
	// 否则一个副本上的登出对另一个副本无效 —— 用户看到 200，
	// 安全动作却没有生效，且没有任何外部信号。
	authSvc := auth.NewService(jwtMgr, userSvc, oauthCfgs, rsel.revocations)
	// 登录暴力破解防护：只有拿到共享限流器时才挂上。
	// rsel.limiter 为 nil（未配置 Redis / 已回退）时不挂 ——
	// 宁愿不限流，也不要退回一个「多副本下等于没限」的假限流
	// （那会让运维以为已经有了防护）。
	authHandler := auth.NewHandler(authSvc)
	if rsel.limiter != nil {
		// 阈值/窗口来自配置（0 表示用 auth 包的内置默认值）。
		// 可配置的理由：共享出口 IP 的办公场景更容易撞到按 IP
		// 计数的阈值，运维需要能调；写死就只能改代码。
		guard := auth.NewLoginGuard(newRedisLoginLimiter(rsel.limiter))
		if cfg.Auth.LoginRateLimit > 0 || cfg.Auth.LoginRateWindow > 0 {
			guard = guard.WithLimit(cfg.Auth.LoginRateLimit, cfg.Auth.LoginRateWindow)
		}
		authHandler.WithLoginGuard(guard, logger)
	}
	authMW := auth.NewMiddleware(authSvc)

	// Agent service：与 user/audit 同一口径 —— 配了数据库就用 Postgres 实现，
	// 否则内存。内存实现保留为回退路径且被契约测试持续覆盖。
	//
	// Agent 比用户更需要这一层：用户丢了还能重新注册，而 Agent 是资源主体，
	// 它的配置、状态机位置与容器绑定点丢了就是丢了 —— 进程重启后
	// 全部 Agent 变成 idle 且配置清空，这在生产上等同于把所有工作负载重置。
	var agentRepo agent.Repository
	if dbHandle != nil {
		agentRepo = agent.NewPostgresRepository(dbHandle)
		logger.Debug("agent repository backed by postgres")
	} else {
		agentRepo = agent.NewMemoryRepository()
	}
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
	// 沙箱根目录来自配置（容器部署下是挂载卷，默认 /var/lib/agentbot/fs）。
	// 目录创建已由 NewWithConfig 提前校验，这里的错误只可能来自权限变化。
	fsMgr, err := filesystem.NewLocalManager(cfg.FileSystem.Root)
	if err != nil {
		return nil, err
	}
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
	//
	// 审计是**最不能丢**的一类数据：它是取证依据，进程重启后
	// 内存里的审计记录会全部消失。因此这里与 user 一样优先用 Postgres。
	var auditRepo audit.Repository
	if dbHandle != nil {
		auditRepo = audit.NewPostgresRepository(dbHandle)
		logger.Debug("audit repository backed by postgres")
	} else {
		auditRepo = audit.NewMemoryRepository()
	}
	auditSvc := audit.NewService(auditRepo)
	// 留存策略：默认 90 天。挂在 handler 上后 /retention 系列接口可用。
	auditRetention := audit.NewRetentionService(auditSvc)
	auditH := audit.NewHandler(auditSvc).WithRetention(auditRetention)
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

	// 网络出口路由（Day 26）：出口策略 + 代理网关 + 出站流量审计。
	//
	// 装配顺序刻意的：策略引擎先建，网关持有同一个策略实例，
	// 保证「HTTP 层判定」与「网关判定」用的是同一份策略快照 ——
	// 两个实例会出现「后台看到的口子实际没生效」这种最难查的偏差。
	egressPolicy := network.NewMemoryPolicyStore()
	egressStore := network.NewMemoryTrafficStore()
	egressGateway := network.NewHTTPGateway(network.GatewayConfig{
		Policy:   egressPolicy,
		Store:    egressStore,
		Recorder: egressAuditRecorder{rec: auditRec},
	})
	egressSvc := network.NewService(network.ServiceConfig{
		Policy:  egressPolicy,
		Gateway: egressGateway,
		Store:   egressStore,
	})
	egressH := network.NewHandler(egressSvc)

	// 默认出口策略：先把「必须挡住」的口子显式登记出来。
	//
	// 注意这不是白名单 —— 没有匹配到任何 allow 条目**依然**拒绝。
	// 这几条 deny 的价值在于给出可读的拒绝理由（命中具体规则而不是
	// 落到「默认拒绝」），并防止将来某条宽泛的 allow 把内网地址捎带放开。
	seedDefaultEgressRules(egressSvc, logger)

	// 多租户隔离（Day 27）：租户 + 成员 + 资源归属边界。
	//
	// 装配顺序刻意的：租户服务必须早于任何「会登记资源归属」的业务动作，
	// 否则第一轮创建的 Agent 会没有归属 —— 而「没有归属」在隔离语义下
	// 等于「谁都看不到」，看起来和「创建失败」一模一样，最难排查。
	tenantStore := tenant.NewMemoryStore()
	tenantSvc := tenant.NewService(tenantStore)
	tenantH := tenant.NewHandler(tenantSvc)
	tenantH.SetIdentityResolver(tenantIdentityFromRequest)

	// 把「新建 Agent」与「登记租户归属」绑在一起（Day 29 修复的缺陷）。
	//
	// 之前 registerTenantOwnership 被定义了但**从未被调用** —— 结果是
	// 通过 HTTP 建出来的 Agent 没有任何租户归属。这不是「少登记一条数据」，
	// 而是隔离边界出现了一个洞：CountResources 永远为 0（配额形同虚设），
	// 租户资源清单看不到自己建的 Agent，而跨租户检查对所有人返回 404 ——
	// 连资源的合法所有者都访问不了它。症状是「建完就查不到」，
	// 最容易被误判成「创建失败」。现在在装配层把它接上。
	agentH.SetCreatedHook(func(ctx context.Context, agentID string) {
		scope := tenant.ScopeFromContext(ctx)
		if scope == nil {
			// 无租户作用域（平台管理员的平台级操作）不登记归属：
			// 凭空挑一个租户会把资源塞进别人家。
			return
		}
		tnID := scope.TenantID()
		if tnID == tenant.None {
			return
		}
		if err := registerTenantOwnership(ctx, tenantSvc, tnID, tenant.ResourceAgent, agentID); err != nil {
			logger.Warn("failed to register tenant ownership for agent",
				"agent_id", agentID, "tenant_id", string(tnID), "error", err)
		}
	})

	// 企业 SSO（OIDC，Day 27）。
	//
	// 默认**不配置**：没有企业 IdP 的部署应该看到 503 而不是一个
	// 「看起来能用、实际谁都能进」的登录入口。配置由部署方显式提供
	// （环境变量 / 配置文件），这里给出的是安全默认。
	ssoSvc := sso.NewService(sso.ServiceConfig{
		Users:   userDirectoryAdapter{svc: userSvc},
		Binding: sso.BindingStrict,
	})
	ssoH := sso.NewHandler(ssoSvc)
	ssoH.SetSessionIssuer(ssoSessionIssuer(authSvc, tenantSvc))
	ssoH.SetTenantBinder(func(r *http.Request, acct *sso.Account, tenantID string) error {
		return bindSSOToTenant(r.Context(), tenantSvc, acct.LocalUserID, tenantID)
	})

	// Admin console (aggregate read-only view over the other modules)
	adminSources := admin.Sources{
		Agents:  adminAgentSource{svc: agentSvc},
		Network: adminNetworkSource{svc: egressSvc},
		Tasks:   adminTaskSource{mgr: taskSvc},
		Monitor: adminMonitorSource{svc: monitorSvc},
		Audit:   adminAuditSource{svc: auditSvc},
		Users:   adminUserSource{svc: userSvc},
		Authz:   adminAuthzSource{svc: authzSvc},
	}
	// 静态产物目录必须经 WithStaticDir 传给 **Service**，不能只传给 Handler。
	//
	// 踩过的坑：原先只写了 `admin.NewHandler(adminSvc, cfg.Admin.ConsoleDir)`，
	// 看上去配置生效了 —— Handler 确实拿到了正确的目录，静态文件的读写也正常。
	// 但 Service 在自己内部硬编码了 "web/admin/dist"，而
	//   - GET /api/v1/admin/static
	//   - snapshot 的 static 分区
	// 两者读的都是 **Service** 的 staticInfo。于是容器部署
	// （console_dir=/srv/agentbot/web）时控制台会一直声称
	// 「产物尚未构建」，而页面其实是好的 —— 报表撒谎，不是页面坏了。
	//
	// WithStaticDir 会在 NewService 内部重新 probeStatic，因此
	// 「配置里写了哪个目录」与「控制台报告的状态」从此是同一份事实。
	adminSvc := admin.NewService(adminSources, version.Version, admin.WithStaticDir(cfg.Admin.ConsoleDir))
	adminH := admin.NewHandler(adminSvc, cfg.Admin.ConsoleDir)

	return &App{
		Logger:       logger,
		Config:       cfg,
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
		EgressSvc:    egressSvc,
		EgressH:      egressH,
		EgressPolicy: egressPolicy,
		TenantSvc:    tenantSvc,
		TenantH:      tenantH,
		SSOSvc:       ssoSvc,
		SSOH:         ssoH,
		Metrics:      metricReg,
		MetricsH:     metricReg.Handler(logger),
		httpMetrics:  httpMetrics,
	}, nil
}

// sqlDB 从 storage 句柄取出 *sql.DB，nil 安全。
//
// 单独抽一个函数是为了让「没有数据库」这一情形在所有仓库构造点
// 有**同一个**表达（nil *sql.DB），而不是让每处各自写
// `if db != nil { db.SQL() }` —— 一处漏判就是一次空指针崩溃。
func sqlDB(db *storage.DB) *sql.DB {
	if db == nil {
		return nil
	}
	return db.SQL()
}
