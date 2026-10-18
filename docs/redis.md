# Redis：用途、键设计与降级行为

> 本文档回答三个问题：**为什么要 Redis**、**键长什么样**、
> **连不上时会怎样**。第三点最重要 —— 它决定了故障时的行为是否可接受。

## 1. 为什么需要 Redis（两个实测确认的缺口）

引入 Redis 不是因为「企业平台应该有 Redis」，而是因为两个**可复现的安全缺口**。

### 缺口 1：凭据吊销在多副本下失效

`internal/auth/service.go` 原先用进程内 `map[string]time.Time` 做 token 黑名单
（其注释本身就写着「生产环境应使用 Redis」）。

**症状**：多副本部署时，用户在副本 A 登出后，副本 B **仍然接受**
同一个 access token，直到它自然过期（默认 1h）。登出接口返回 200，
用户以为已失效。

**为什么危险**：这是一个**静默**的安全失效 —— 没有报错、没有日志、
没有指标。唯一的信号是「被盗的 token 在登出后还能用」，
而那通常是在事故发生之后才被发现的。

**可执行的证据**（`internal/app/redis_revocation_e2e_test.go`）：

| 后端 | 副本 A 登出后，副本 B 用同一 token 请求 |
|------|----------------------------------------|
| 内存（改前） | **200** ← 缺口 |
| Redis（改后） | **401** |

对照组测试 `TestRedisRevocation_MemoryBackendDoesNotShareAcrossReplicas`
**主动断言内存后端返回 200**。这条测试不是"证明修复生效"，
而是把「缺口确实存在」写成可执行的事实 —— 否则上面那条断言
可能只是在测别的东西。

### 缺口 2：登录接口没有暴力破解防护

`grep -rn "429|StatusTooManyRequests" internal/` 在改动前**零命中** ——
平台此前没有任何限流。`POST /api/v1/auth/login` 可被无限次尝试。

进程内计数不能解决这个问题：多副本下它把攻击者的可用尝试次数
放大 N 倍（N = 副本数），且各副本独立重置 —— 轮询副本即可绕过。

> **⚠️ 已知的探测差异**：其他 gRPC 指标相关的 API 端点若无法探测到，
> 不影响本节的结论；本节的两条缺口都由真实 HTTP 请求验证。

## 2. 键设计

所有键都带 `agentbot:` 前缀。同一个 Redis 常被多个应用/环境共用，
不带前缀时另一个应用的同名键会与本项目的键互相覆盖 ——
症状是「别人改了配置，这边的人突然登出」。

| 用途 | 键格式 | TTL | 说明 |
|------|--------|-----|------|
| 凭据吊销 | `agentbot:revoked:<userID>:<sha256>` | = token 剩余有效期 | 存**指纹**，不存原始 token |
| 登录限流（账号） | `agentbot:rl:login:email:<email>:<slot>` | 窗口 + 1s | slot = unix秒 / 窗口秒 |
| 登录限流（IP） | `agentbot:rl:login:ip:<ip>:<slot>` | 窗口 + 1s | 同上 |

### 为什么存指纹而不是原始 token

原始 access token 是 bearer 凭据本身。把它写进 Redis 等于把
「可直接使用的凭据」复制到了另一个存储上 —— 一次 Redis 泄露
就变成一次全量凭据泄露。sha256 指纹足以做等值判定，
且泄露后无法反推出 token。

### 为什么 TTL 取「凭据剩余有效期」而不是固定值

凭据一旦自然过期，吊销记录就再无意义。让 TTL 跟着凭据走，
可以省掉任何清理任务，也不会让表无限增长。
`Revoke` 因此**拒绝** ttl <= 0：一条永不过期的吊销记录
会永久占用存储，而它保护的凭据早已过期。

### 为什么限流键里含「窗口序号」

固定窗口的核心：窗口一过，键名自然变化，旧键靠 TTL 回收，
**不需要任何清理逻辑**。
用窗口序号而不是窗口起始时间戳，是为了让键长度恒定且可读。

## 3. 降级行为（决策表）

`internal/app/redis_wiring.go` 的决策表，与持久化后端同构：

| 配置 | 可达性 | 行为 | 启动日志 |
|------|--------|------|----------|
| 未配 Redis | — | 内存吊销表 + **不限流** | `redis.backend=memory` |
| 配了 | 可达 | Redis 吊销表 + 限流 | `redis.backend=redis` |
| 配了 | 不可达 | **拒绝启动**（默认） | 退出码非零 |
| 配了 | 不可达 + 开了回退 | 内存吊销表 + **不限流** | `redis.backend=memory` + **WARN** |

### 为什么默认拒绝启动

Redis 承载的是**跨副本安全状态**。静默退回内存实现的后果不是
「功能少一点」，而是「登出只在命中的那个副本生效」——
用户看到 200，安全动作却没有生效，且没有任何外部信号。

这类失败必须在启动期响亮地失败。回退到内存时日志降级为
**WARN**：这是一个安全性下降的状态，不该与正常部署长得一样。

### 复用同一个回退开关

回退由 `AGENTBOT_STORAGE_ALLOW_MEMORY_FALLBACK` 控制，
**没有**为 Redis 新增第二个语义相同的开关。理由：想要回退，
表达的意图是「这个部署可以接受丢失进程外状态」——
对数据库与 Redis 是同一个意图。加第二个开关会变成
「同一件事被两个组件各存一份」的又一次复现（本仓库已踩过三次）。

### 未配 Redis 时「不限流」而不是退回进程内计数

刻意的选择。进程内计数在多副本下等于没限，而它会让人
**以为已经有了防护** —— 这比明确地没有防护更危险。
未配 Redis 时 `App.RateLimiter` 为 nil，handler 不挂 guard，
端到端测试断言「15 次失败不出现 429」。

## 4. 失败方向：两个组件刻意相反

这是本项目里唯一一处「两个相似组件选择了相反的失败方向」，
两者的理由都写在代码注释里：

| 组件 | 失败方向 | 失败的后果 |
|------|----------|-----------|
| 凭据吊销（`ValidateAccessToken`） | **fail-closed** | 查不到 → 认证失败。若 fail-open，一次抖动等于「所有已登出的 token 全部复活」——静默地撤销了用户的安全动作 |
| 登录限流（`LoginGuard.Check`） | **fail-open** | 查不到 → 放行并记 Warn。限流失效只是「速度变慢」；让它 fail-closed 会让一次 Redis 故障变成全站无法登录，而攻击者恰恰能靠打垮 Redis 来实现拒绝服务 |

**判定原则**：问「这个组件失效时，世界变得更危险还是更不方便」。
吊销失效 = 更危险 → fail-closed；限流失效 = 更不方便 → fail-open。

## 5. 客户端实现

`internal/redis` 是**零第三方依赖**的 RESP2 客户端。

不引入 go-redis 的理由：
1. RESP2 是行协议，核心只有「写命令 + 读回复」；
2. 已有先例：3-7 手写 Prometheus text exposition format 而非引入
   client_golang（其传递依赖 `munnerz/goautoutils` 不可访问）；
3. go-redis 曾在 3-1 因「声明了但从未 import」被移除。

实现里几个刻意的取舍：

- **`New()` 主动建连握手**，不做懒连接。配错的 Redis 必须在启动期暴露，
  否则会变成「服务正常启动、第一次限流才报错」。
- **`ErrNil` 与空串区分开**。吊销表把空串当成未吊销会让判定反向。
- **`ServerError` 与网络错误分开**：服务端错误重试无用，网络错误才值得重试。
- **参数类型白名单**：只接受 string/[]byte/整数/浮点/布尔/Duration。
  允许任意类型会让 `Do("SET", k, someStruct{})` 编译通过却写入
  `%v` 的无意义值。

只实现平台真实用到的命令（GET/SET/DEL/EXISTS/INCR/EXPIRE/TTL/PING/EVAL）——
用不到的命令不该出现在代码里。

## 6. 本机验证方式

Redis 可用 apt 非容器安装（与 PostgreSQL 同理，绕开无 Docker 限制）：

```bash
apt-get update -qq
apt-get install -y --no-install-recommends redis-server
redis-server --daemonize yes --port 6379 --save '' --appendonly no
redis-cli ping          # 期望 PONG
```

测试用独立的 DB 编号（按测试名哈希分配），避免互相污染。
限流键含 `login:ip:127.0.0.1` 这个与测试无关的固定维度，
而 httptest 的 `RemoteAddr` 恒为 127.0.0.1 —— 共用一个库时
前一个测试打满的 IP 计数会让后一个测试一开场就拿到 429。

```bash
# 运行 Redis 相关的全部测试
go test -count=1 -race -v -run 'TestRedis|TestLoginRateLimit|TestSelectRedis' ./internal/redis/ ./internal/auth/ ./internal/app/

# 确认没有被静默跳过
go test -count=1 -v -run 'TestRedis' ./internal/redis/ 2>&1 | grep -c SKIP   # 期望 0
```

## 7. 已知边界（后续批次的前提）

- 限流是**固定窗口**，窗口交界处最多允许 2 倍额度通过。
  对「防暴力破解」这个目标可接受（目标是把无限次压到几十次，
  不是精确整形）。换滑动窗口需要 Lua 或 ZSET，代价见
  `internal/redis/ratelimit.go` 的注释。
- `Retry-After` 由服务端给出，但**固定窗口的剩余时间**是从键的 TTL 读的；
  客户端不应假定该值精确。
- 限流目前只覆盖 `/api/v1/auth/login`。其它敏感端点
  （注册、密码重置、OAuth 回调）尚未接入。
- 分布式锁 / 缓存等 Redis 的其它常见用途**未实现** ——
  本项目的 Redis 只承担上述两个用途，不扩张（`Configured()`
  是全项目唯一判定入口，新增用途前先问「是否又是同一件事各存一份」）。
