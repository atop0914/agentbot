package app

import (
	"context"
	"fmt"
	"time"

	"github.com/atop0914/agentbot/internal/auth"
	"github.com/atop0914/agentbot/internal/config"
	"github.com/atop0914/agentbot/internal/redis"
)

// 本文件是「Redis 后端选择」的装配点，与 storage_sources.go 的持久化
// 决策表同构。之所以单独一个文件、而不是塞进 app.go：
// 决策表本身是需要被逐条测试的东西（见 redis_wiring_test.go）。
//
// 决策表：
//
//	未配置 Redis（host 为空）            → 内存吊销表      redis.backend=memory
//	配置了 + 连接成功                    → Redis 吊销表    redis.backend=redis
//	配置了 + 连接失败 + 未开回退（默认） → **拒绝启动**
//	配置了 + 连接失败 + 开了回退         → 内存吊销表
//
// 为什么默认拒绝启动而不是静默退回内存：
// Redis 在这里承载的是**跨副本安全状态**（凭据吊销、限流计数）。
// 静默退回内存实现的后果不是「功能少一点」，而是
// 「登出只在命中的那个副本生效」——用户看到 200，安全动作却没有生效。
// 这类失败没有外部信号，所以必须在启动期响亮地失败。
//
// 与数据库决策共用一个回退开关（AGENTBOT_STORAGE_ALLOW_MEMORY_FALLBACK）：
// 刻意不为 Redis 新增第二个语义相同的开关 —— 那会变成
// 「同一件事被两个组件各存一份」的又一次复现（本仓库已踩过三次）。
// 想要回退，表达的意图是「这个部署可以接受丢失进程外状态」，
// 对数据库与 Redis 是同一个意图。

// redisSelection 是 Redis 后端的选择结果。
type redisSelection struct {
	revocations auth.RevocationStore
	limiter     *redis.RateLimiter
	// backend 用于启动日志：memory 或 redis。
	backend string
	// client 在选择了 Redis 后端时非 nil，由 App 持有并负责关闭。
	client *redis.Client
}

// Redis 后端标识（写进启动日志，运维据此回答「吊销状态存在哪」）。
const (
	// redisBackendMemory 表示进程内实现。
	redisBackendMemory = "memory"
	// redisBackendRedis 表示 Redis 实现。
	redisBackendRedis = "redis"
)

// redisDialTimeout 是启动期建连超时。
//
// 取 3 秒：配置错误必须在启动期暴露，但不能因为一台不可达的 Redis
// 让启动挂住太久 —— 3 秒足够完成 TCP 握手 + AUTH + SELECT。
const redisDialTimeout = 3 * time.Second

// selectRedis 按上述决策表挑选 Redis 后端。
//
// 返回的 error 表示**应当拒绝启动**。
func selectRedis(ctx context.Context, cfg config.Config) (redisSelection, error) {
	if !cfg.Redis.Configured() {
		// 未配置：单副本/开发形态。这是合法状态，不是降级。
		return redisSelection{
			revocations: auth.NewMemoryRevocationStore(),
			backend:     redisBackendMemory,
		}, nil
	}

	client, err := redis.New(redis.Config{
		Addr:        cfg.Redis.Addr(),
		Password:    cfg.Redis.Password,
		DB:          cfg.Redis.DB,
		DialTimeout: redisDialTimeout,
	})
	if err == nil {
		// 建连成功。再做一次 PING：New 内部的握手里，无密码且 DB=0 时
		// 只剩下建 TCP 连接，一个「接受连接但拒绝命令」的代理
		// （如未就绪的 sentinel/侧车）会在此前一路通过。
		if perr := client.Ping(); perr != nil {
			_ = client.Close()
			return redisSelection{backend: redisBackendMemory},
				fmt.Errorf("app: redis reachable but not serving commands: %w", perr)
		}
		return redisSelection{
			revocations: auth.NewRedisRevocationStore(
				redis.NewRevocationStore(client, redisKeyPrefix(cfg)),
			),
			limiter: redis.NewRateLimiter(client, redisKeyPrefix(cfg)),
			backend: redisBackendRedis,
			client:  client,
		}, nil
	}

	// 到这里说明「配了 Redis 但用不了」。
	if !cfg.Storage.AllowMemoryFallback {
		// 默认路径：拒绝启动。理由见文件头 ——
		// 静默退回内存会让跨副本的登出静默失效（200 但没有效果）。
		return redisSelection{backend: redisBackendMemory}, fmt.Errorf(
			"app: redis is configured but unreachable; refusing to start "+
				"(set %sSTORAGE_ALLOW_MEMORY_FALLBACK=true only if per-replica "+
				"revocation and rate limiting are acceptable): %w",
			config.EnvPrefix, err)
	}

	// 显式开启的回退。调用方以 ERROR 级别打印。
	return redisSelection{
		revocations: auth.NewMemoryRevocationStore(),
		backend:     redisBackendMemory,
	}, nil
}

// redisKeyPrefix 返回本部署的 Redis 键前缀。
//
// 带前缀的理由：同一个 Redis 常被多个环境/多个应用共用。
// 不带前缀时，另一个应用的同名键（如 "revoked:..."）会与本项目的
// 键互相覆盖 —— 症状是「另一个环境改了配置，这边的人突然登出」。
func redisKeyPrefix(_ config.Config) string {
	// 目前不按环境再细分：部署方用 DB 编号（redis.db）做隔离，
	// 那本来就是 Redis 提供的隔离手段。前缀只用于区分应用。
	return "agentbot"
}
