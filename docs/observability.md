# 可观测性（Observability）

> **一句话**：指标、日志分级、请求追踪三件套怎么用，以及「接口 403 但 handler 看着能过」怎么查。
> **适用读者**：排障的人、新加中间件或新加端点的人。
> **相关**：[文档索引](index.md) · [性能基线](performance.md) · [API 参考](api.md)

## 1. 指标端点 `/metrics`

### 1.1 暴露面与鉴权

| 项 | 值 |
|----|-----|
| 路径 | `metrics.path`，默认 `/metrics`（可改，启动日志会写出实际值） |
| 方法 | `GET` / `HEAD` |
| 鉴权 | **匿名可达**（与 `/health` 同级） |
| Content-Type | `text/plain; version=0.0.4; charset=utf-8` |
| 缓存 | 响应带 `Cache-Control: no-store` |

**为什么匿名**：抓取端（Prometheus、容器探针）通常不持有本平台的 JWT。
要求认证会让指标形同虚设。而导出的内容**只有计数、延迟分布与模块规模**，
不含任何用户名、令牌或 Agent 配置 —— 这是它与 `/api/v1/admin/snapshot`
的关键区别（后者分区里含用户列表，所以必须鉴权）。

若某次部署要求指标也需鉴权，正确做法是**在网关层把该路径限制为内网可达**，
而不是给 `/metrics` 加一个「抓取端需要先登录」的认证。

> ⚠️ 这条「匿名」是**契约的一部分**，不是疏漏：`/metrics` 在
> `internal/authz/middleware.go` 的 `DefaultRouteTable` 里显式登记为
> `Public`。中间件对**未登记**路径的默认行为是 403 —— 路由存在、
> handler 正确、直接调 handler 也返回 200，但走完整链路就是 403。
> 症状极具误导性：Prometheus 静默抓不到（403 不触发「抓取失败」告警，
> 只表现为仪表盘空）。

### 1.2 指标清单

#### HTTP 层

| 指标 | 类型 | 标签 | 说明 |
|------|------|------|------|
| `agentbot_http_requests_total` | counter | `method`, `route`, `status` | 请求总数 |
| `agentbot_http_request_duration_seconds` | histogram | `method`, `route` | 请求延迟（11 个桶） |
| `agentbot_http_requests_in_flight` | gauge | — | 当前在途请求数 |
| `agentbot_http_responses_by_status_class_total` | counter | `class` (2xx/3xx/4xx/5xx) | 按状态类聚合 |
| `agentbot_http_panics_total` | counter | `route` | 被 recover 的 panic |

#### 存储与进程

| 指标 | 类型 | 标签 | 说明 |
|------|------|------|------|
| `agentbot_storage_backend_memory` | gauge | — | 1 = 内存后端（重启丢数据） |
| `agentbot_storage_backend_postgres` | gauge | — | 1 = Postgres 后端 |
| `agentbot_storage_memory_fallback_allowed` | gauge | — | 1 = 允许无库启动 |
| `process_uptime_seconds` | gauge | — | 进程运行时长 |

### 1.3 三条必须知道的读取纪律

**① `route` 是路由模板，不是真实路径。**
`NormalizePath` 把 UUID / 长数字 / 长十六进制段替换成 `{id}`：

```
/api/v1/agents/3f2504e0-4f89-11d3-9a0c-0305e82c3301/start
  → /api/v1/agents/{id}/start
```

本项目的路由大量是 `/agents/{id}/{action}` 形态，**ID 在中间** ——
只归一末段毫无用处。若真实 ID 进了标签值，几万个 Agent 就能让
Prometheus 内存爆掉。这是指标系统最常见的事故面。

**② 错误率不会恒为 0 —— 这是修过的 bug。**
`metricsMiddleware` 挂在中间件链的**最外层**。第一版紧贴 mux（最内层），
结果是：authz 对未授权请求**直接写 401/403 并 return，不调用 next**
（fail-closed 短路），内层中间件永远看不到这些请求 ——
实测 `route="/api/v1/agents"` 在导出文本里**完全不存在**。

后果是**错误率恒为 0**：一个永远看不见失败的监控比没有监控更危险，
它会让运维确信「没有 5xx」。

> **一般规律**：任何「短路返回错误而不调用 next」的中间件，
> 它的内层都观测不到它。需要观测全量请求的东西必须挂最外层。

**③ 某次抓取看不到自己。**
metrics 在最外层，它在 handler 写完响应体**之后**才记数。
因此第 N 次抓取只能看到第 1..N-1 次抓取的计数。
这是刻意保留的（要让抓取看到自己，就得在生成响应体前污染计数）。

反向地，抓取 `/metrics` **期间** `in_flight` 为 1 —— 这是正确的，
不要据此判断「有泄漏」。判断泄漏应直接读注册表（`App.Metrics.Expose()`），
那时没有在途请求。

### 1.4 建议的告警规则

```promql
# 内存后端被启用 —— 数据重启即丢，这是最该报的一条
agentbot_storage_backend_memory > 0

# 5xx 出现（不刷屏，任何一条都值得看）
rate(agentbot_http_responses_by_status_class_total{class="5xx"}[5m]) > 0

# 发生 panic
increase(agentbot_http_panics_total[10m]) > 0

# P95 延迟超过 2 秒
histogram_quantile(0.95,
  sum by (le, route) (rate(agentbot_http_request_duration_seconds_bucket[5m]))) > 2
```

### 1.5 为什么自绘而不用 `prometheus/client_golang`

1. 它的传递依赖 `github.com/munnerz/goautoutils` 仓库**已不可访问**，
   `go mod tidy` 会直接失败（本仓库 research 里有记载）；
2. 本项目只需要「计数器 + 直方图 + 导出文本」，用不上服务发现与推送网关；
3. **零新增第三方依赖是本仓库的默认基线**。

实现要点（每条都有测试兜着）：

- 直方图**必须**补 `+Inf` 桶且令其等于 `_count`：缺这一行
  `histogram_quantile()` 返回 `NaN`；
- 标签值转义 `\`、`"`、换行：漏转义会让抓取端**整轮失败**（不是丢一行）；
- 标签值用 `\x00` 分隔拼 key：用逗号分隔会让 `("x,y", "z")` 与
  `("x", "y,z")` 撞成同一个 series（静默串指标）；
- 同名指标被两组不同标签注册 → **注册期报错**（装配失败拒绝启动）；
  合并会让导出的同一 series 出现两种标签集，抓取端整轮失败；
- 指标名在**加命名空间前缀之前**校验：否则空名会变成 `ns_`、
  `1leading_digit` 前缀化后首位不再是数字，两者都会「注册成功但语义错」。

---

## 2. 日志

### 2.1 级别配置

| 配置 | 环境变量 | 取值 | 默认 |
|------|----------|------|------|
| `log.level` | `AGENTBOT_LOG_LEVEL` | `debug` / `info` / `warn` / `error` | `info` |
| `log.format` | `AGENTBOT_LOG_FORMAT` | `json` / `text` | `json` |
| `log.output` | `AGENTBOT_LOG_OUTPUT` | `stdout` / `file` | `stdout` |

非法取值会**拒绝启动**（fail-fast），不会静默退回 info ——
静默降级会让「我配了 warn 但日志还是那么多」变成一次无声的调试。

> `log.output=file` 目前**退化成 stdout 并告警**。容器化部署里
> 日志写文件是反模式（应由运行时收集）；静默忽略会让一个自以为
> 「日志在文件里」的部署在排障时找不到日志，所以这里明确告警。

### 2.2 各级别打什么

| 场景 | 级别 |
|------|------|
| 启动、装配、后端选择、指标端点注册 | `INFO` |
| **成功请求的访问日志** | `DEBUG` |
| **4xx 请求** | `WARN` |
| **5xx 请求** | `ERROR` |
| panic（含堆栈） | `ERROR` |
| `log.output=file` 降级、开发配置告警 | `WARN` |

**为什么成功请求不在 INFO**：实测一次 `go test -run TestMetrics`
就产出数十行 request 日志；真实流量下这个量级会让启动信息与真正的错误
被彻底淹没。日志的价值在于「找得到」，不在于「全都记」。

**为什么 4xx 不降到 DEBUG**：`request_id` 是这类排障的第一入口
（「哪个客户端的哪次请求」），降到 Debug 会让它在默认级别下消失。

刻意**不做**「按路径过滤」这类更细策略：那会把「哪些路径重要」
写死在中间件里，而它其实是运维视角的问题，应由 `LOG_LEVEL` 决定。

### 2.3 访问日志字段

```json
{"level":"WARN","msg":"request","method":"GET","path":"/api/v1/agents",
 "status":403,"duration_ms":0,"request_id":"b3c34f8d-...","remote":"10.0.0.5:51422"}
```

---

## 3. 请求追踪

### 3.1 ID 的流转

```
调用方（可自带 X-Request-ID）
   │
   ▼
requestIDMiddleware ── 有上游值则**沿用**，否则生成 UUIDv4
   │                   ├─ 写响应头 X-Request-ID（回传给调用方）
   │                   └─ 写 context（供业务日志使用）
   ▼
... 后续中间件与业务 handler：RequestIDFromContext(ctx)
   ▼
访问日志 / 业务日志：都带同一个 request_id
```

### 3.2 三条纪律

**① 尊重上游传入的值。** 跨服务调用时上游已生成了 ID，覆盖它会让链路
在本服务处断开 —— 上游日志里的那个 ID 在本服务查不到。

**② 取值入口只有一个：`app.RequestIDFromContext(ctx)`。**
刻意**不**提供「从 header 反查」或「从 ResponseWriter 反查」的辅助：
多来源必然产出「中间件说 A、日志说 B」这类最难查的分歧。

**③ context 必须原地赋值。**
```go
// ❌ 静默失效：编译通过、header 正常、context 里什么都没有
r.WithContext(app.WithRequestID(r.Context(), id))

// ✅
r = r.WithContext(app.WithRequestID(r.Context(), id))
```

### 3.3 业务代码怎么用

```go
import "github.com/atop0914/agentbot/internal/app"

logger.Error("创建 Agent 失败",
    "agent_id", id,
    "request_id", app.RequestIDFromContext(ctx), // 与访问日志同一个值
    "error", err)
```

拿到一个 `request_id` 后，即可在日志系统里检索该请求的**全部**经过
（访问日志 + 业务日志）。

---

## 4. 端到端排查示例

用户报告「某个请求 403」：

```bash
# 1) 拿到 request_id（响应头里就有了）
curl -i -H "Authorization: Bearer $TOKEN" https://host/api/v1/agents
#    X-Request-ID: 3f2504e0-4f89-11d3-9a0c-0305e82c3301

# 2) 按 ID 检索日志，看到这一行的 status 与 path
#    {"level":"WARN","msg":"request","status":403,"request_id":"3f2504e0-..."}

# 3) 确认这不是 5xx 潮，而是单点拒绝
curl -s https://host/metrics | grep 'by_status_class_total'

# 4) 确认路由是否被登记（403 的常见原因之一是默认拒绝）
curl -s https://host/metrics | grep 'route="/api/v1/agents"'
```

---

## 5. 相关源码

| 文件 | 内容 |
|------|------|
| `internal/metrics/types.go` | 注册表、计数器/直方图类型、转义与格式化 |
| `internal/metrics/metrics.go` | 注册与观测 API、标签集冲突检测 |
| `internal/metrics/expose.go` | Prometheus 文本导出 |
| `internal/metrics/handler.go` | `/metrics` handler 与**路径归一**（基数防线） |
| `internal/app/metrics_wiring.go` | 指标定义与中间件（装配层持有全部指标面） |
| `internal/app/tracing.go` | request_id 流转与访问日志分级 |
| `internal/app/middleware.go` | recovery（含 panic 计数）、CORS、statusWriter |
| `internal/authz/middleware.go` | 路由权限表（`/metrics` 的 Public 登记） |
