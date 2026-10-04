# AgentBot API 参考

基础路径：`/api/v1`。除 `/health` 与认证注册/登录外，所有接口都需要
`Authorization: Bearer <access_token>`。

请求与响应均为 JSON。列表接口统一返回非 `null` 的数组；分页接口返回
`total` / `limit` / `offset` / `has_more`。

---

## 健康检查

### GET /health

```json
{"status": "ok"}
```

---

## 认证

### POST /api/v1/auth/register

```json
{"email": "user@example.com", "password": "s3cret-Pass", "name": "Alice"}
```

### POST /api/v1/auth/login

```json
{"email": "user@example.com", "password": "s3cret-Pass"}
```

响应：

```json
{
  "access_token": "eyJ...",
  "refresh_token": "eyJ...",
  "expires_in": 3600,
  "user": {"id": "u_1", "email": "user@example.com", "name": "Alice"}
}
```

---

## Agent

| 方法 | 路径 | 说明 |
|------|------|------|
| GET | `/agents` | 列出 Agent |
| POST | `/agents` | 创建 Agent |
| GET | `/agents/{id}` | 查询单个 Agent |
| PUT | `/agents/{id}` | 更新 Agent |
| DELETE | `/agents/{id}` | 删除 Agent |
| POST | `/agents/{id}/start` | 启动（pending/failed → running） |
| POST | `/agents/{id}/stop` | 停止 |

Agent 状态机：`idle → running ⇄ paused → stopped`，异常进入 `error`。

---

## 云环境

| 方法 | 路径 | 说明 |
|------|------|------|
| GET/POST | `/environments` | 列出 / 创建环境 |
| GET/DELETE | `/environments/{id}` | 查询 / 销毁环境 |
| POST | `/environments/{id}/start` \| `/stop` \| `/pause` \| `/resume` | 生命周期切换 |
| POST | `/environments/{id}/exec` | 执行命令 |
| POST | `/environments/{id}/files` | 上传文件 |
| GET | `/environments/{id}/files` | 下载 / 列目录 |
| GET | `/environments/{id}/metrics` | 资源用量 |

> 容器类型环境需要宿主 Docker；无 Docker 时使用 `sandbox`/本地实现。

---

## 任务

| 方法 | 路径 | 说明 |
|------|------|------|
| GET | `/tasks` | 列表，支持 `agent_id`、`state` 过滤 |
| POST | `/tasks` | 创建任务（自动拆解为子任务） |
| GET | `/tasks/{id}` | 任务详情 |
| POST | `/tasks/{id}/start` | 启动（异步执行子任务） |
| POST | `/tasks/{id}/cancel` | 取消 |
| POST | `/tasks/{id}/retry` | 重试失败任务 |
| GET | `/tasks/{id}/progress` | 进度百分比 |

子任务状态迁移是并发安全的：异步执行结果只会在任务仍处于 `in_progress`
时写回，不会覆盖并发的 `complete` / `fail` / `cancel`。

---

## 浏览器 / 终端 / 文件系统

| 模块 | 路径前缀 | 说明 |
|------|----------|------|
| 浏览器 | `/browser/sessions` | 创建会话、导航、截图、执行脚本 |
| 终端 | `/terminal/sessions` | 创建会话、执行命令、调整窗口 |
| 文件系统 | `/filesystem/*` | 读取、写入、列目录、移动、复制、搜索 |

文件系统接口基于路径沙箱，所有路径都相对于配置的根目录解析。

---

## 应用适配器

| 方法 | 路径 | 说明 |
|------|------|------|
| GET/POST | `/adapters` | 列出 / 注册适配器 |
| POST | `/adapters/{id}/connect` \| `/disconnect` | 连接管理 |
| GET | `/adapters/{id}/actions` | 可用动作 |
| POST | `/adapters/{id}/execute` | 执行动作 |

内置适配器：`email`（send/list/read/search）、`calendar`（list/create/update/delete events、freebusy）。

---

## 记忆

| 方法 | 路径 | 说明 |
|------|------|------|
| GET/POST | `/memory` | 查询 / 写入记忆 |
| GET/DELETE | `/memory/{id}` | 读取 / 删除 |
| GET | `/memory/search` | 关键词 + 标签检索 |
| GET | `/memory/stats` | 统计 |
| POST | `/memory/cleanup` | 清理过期条目 |

---

## 通信与角色

| 方法 | 路径 | 说明 |
|------|------|------|
| POST | `/messages` | 发送 Agent 间消息 |
| GET | `/messages` | 收件箱 |
| POST | `/presence/heartbeat` | 心跳上报 |
| GET/POST | `/roles` | 列出 / 创建角色 |
| POST | `/role-assignments/{agentID}` | 分配角色 |
| POST | `/permissions/check` | 权限校验 |

---

## 模板市场

| 方法 | 路径 | 说明 |
|------|------|------|
| GET/POST | `/templates` | 列出 / 创建工作流模板 |
| POST | `/templates/recordings` | 录制工作流 |
| GET | `/marketplace/templates` | 浏览共享模板 |
| POST | `/marketplace/templates/{id}/install` | 安装模板 |
| GET/POST | `/marketplace/templates/{id}/reviews` | 查看 / 提交评价 |

---

## 健康监控

| 方法 | 路径 | 说明 |
|------|------|------|
| GET | `/monitor/agents` | 所有 Agent 状态 + 健康分 |
| POST | `/monitor/agents` | 上报状态（心跳） |
| GET | `/monitor/agents/{id}` | 单 Agent 状态 |
| GET | `/monitor/agents/{id}/metrics?duration=1h` | 资源采样（`aggregate=1` 返回聚合） |
| GET | `/monitor/alerts` | 告警列表（`status=firing\|acknowledged\|resolved` 过滤，`rules=1` 查看规则） |
| POST | `/monitor/alerts` | 创建告警规则 |
| POST | `/monitor/alerts/{id}/ack` | 认领告警（firing → acknowledged） |
| POST | `/monitor/alerts/{id}/resolve` | 解决告警（→ resolved） |
| POST | `/monitor/alerts/{id}/reopen` | 重新打开告警（resolved → firing） |
| GET | `/monitor/alerts/{id}/dispositions` | 查看该告警的处置记录（时间正序） |
| DELETE | `/monitor/alerts/{id}` | 删除告警规则 |
| GET | `/monitor/dashboard` | 集群总览 |

### 监控时间序列（Day 24）

| 方法 | 路径 | 说明 |
|------|------|------|
| GET | `/monitor/series/{agentID}?duration=30m` | 按 1 分钟桶归并的时间序列 |
| GET | `/monitor/series/{agentID}?start=…&end=…` | 显式窗口（RFC3339 或 Unix 秒） |
| POST | `/monitor/task-outcomes` | 上报任务结果（用于计算成功率） |
| GET | `/monitor/alert-dispositions?agent_id=&limit=` | 告警处置进度汇总 |

时间序列的每个点包含资源均值与**峰值**（均值会掩盖短时尖刺）以及该桶内的
任务成功率（无任务时为 `-1`）。窗口内没有采样的桶也会返回并标记 `empty=true`，
前端无需自行对齐时间轴。单次查询最多返回 1440 个桶（24 小时），
超出时自动收窄为「最近 1440 分钟」，`start` 字段反映实际起点。

### 告警处置（Day 24）

状态机：`firing → acknowledged → resolved`，任意状态可 `reopen` 回到 `firing`。
每次流转都会追加一条**不可变**的处置记录（操作人 + 备注 + 状态迁移边界），
并同步写入审计日志（动作名 `alert.acknowledged` / `alert.resolved` / `alert.reopened`，
资源 ID 为该告警所属的 Agent）。

处置人取自 JWT claims；请求体中的 `operator` 仅作为显式覆盖
（自动化与测试用），**不接受自定义 header 自报身份**。
审计写入失败不会让处置失败（处置已真实发生），响应中通过 `record_error` 暴露。

健康分 = 资源占用（70%）+ 错误扣分（严重 15 / 一般 5）+ 活跃度。
告警规则类型：`cpu_high`、`memory_high`、`disk_high`、`inactive`、
`unresponsive`、`error_rate`、`task_failed`。

---

## 网络出口路由（Day 26）

Agent 能连上什么，就是它能泄露什么。出口层把「策略 + 网关 + 流量审计」
做成一条链路：出站请求必须经网关转发，网关在转发**之前**做判定，
未获允许的目标直接阻断（不建立连接）并落审计。

| 方法 | 路径 | 说明 |
|------|------|------|
| GET | `/network/rules` | 列出出口策略 |
| POST | `/network/rules` | 新增策略 |
| DELETE | `/network/rules/{id}` | 删除策略 |
| POST | `/network/evaluate` | 出站前预检（只判定，不发请求；允许与否都返回 200） |
| POST | `/network/proxy` | 经网关转发一次出站请求 |
| GET | `/network/traffic` | 查询出站流量记录 |
| POST | `/network/traffic/purge` | 清理流量记录（默认 `dry_run=true`） |
| GET | `/network/stats?window=24h` | 出站流量聚合统计 |
| GET | `/network/summary` | 后台「网络」分区摘要 |

### 策略条目

```json
{
  "id": "egress-deny-cloud-metadata",
  "agent_id": "",
  "target": "169.254.169.254",
  "effect": "deny",
  "methods": ["GET", "POST"],
  "priority": -100,
  "description": "云元数据服务：Agent 读到实例凭据即可横向移动到整个云账号"
}
```

`target` 支持三种写法：**精确域名**（`api.example.com`，含其子域）、
**通配子域**（`*.example.com`，不匹配 apex 本身）、**任意**（`*`）。
带 scheme / 端口 / 路径的输入会被**拒绝**（400）——
那通常意味着作者以为写的是 URL，静默接受会让策略匹配不到任何流量。

`effect` 必须是 `allow` / `deny`；**缺省为 `deny`**（缺省 allow 会是一条静默的口子）。

### 判定顺序

1. **Agent 维度优先于全局维度**——为某个 Agent 单独收紧出口，
   不会被一条宽泛的全局 allow 抵消；
2. 同维度内**具体度高的先判定**：精确域名 > 最长通配后缀 > 更短后缀 > `*`；
3. 具体度相同时 **deny 优先**（fail-closed）；`priority` 数值小的先判定；
4. 方法不匹配的条目跳过（`methods` 为空表示不限方法）；
5. **一条都没命中 → 默认拒绝**。

### 阻断与放行的响应差异

未获策略允许时返回 **403**，且响应体带 `decision` 与 `record`，
让「被哪条规则拦下」可诊断：

```json
{
  "error": "network: egress blocked by policy: domain=c2.evil.xyz",
  "decision": {
    "allowed": false,
    "scope": "global",
    "reason": "default-deny: no egress policy rule allows this destination"
  },
  "record": {"domain": "c2.evil.xyz", "allowed": false, "reason": "…"}
}
```

放行路径返回 200，body 中是下游的状态码与响应体：

```json
{"domain": "api.example.com", "status": 200, "body": "…", "decision": {...}, "record": {...}}
```

**放行与阻断都会写流量记录和审计**（`egress.allowed` / `egress.blocked` /
`egress.failed`）。只记放行等于给「被拦下的攻击尝试」留盲区 ——
而那恰恰是最有价值的信号。

### 默认策略

服务启动时会种三条 deny，让最常见的高危外联有**可读的拒绝理由**：

| ID | Target | 理由 |
|----|--------|------|
| `egress-deny-cloud-metadata` | `169.254.169.254` | 读到实例凭据即可横向移动整个云账号 |
| `egress-deny-localhost` | `localhost` | 出站回到平台自身会绕过出口审计的初衷 |
| `egress-deny-aws-internal` | `*.internal` | 生产内网不应由 Agent 直接访问 |

这**不是白名单**：没有命中任何 allow 条目依然拒绝。三条默认规则的价值在于
把「默认拒绝」细化成可解释、可统计的具体规则，并防止将来一条宽泛的 allow
把内网地址捎带放开。

### 流量查询参数

| 参数 | 说明 |
|------|------|
| `agent_id` | 按 Agent 过滤 |
| `domain` | 按域名过滤（含子域） |
| `allowed` | `true` / `false` |
| `suspicious` | `true` 只看可疑外联 |
| `since` / `until` | RFC3339 或 Unix 秒 |
| `limit` | 返回上限（默认 200）；超限时保留**最近**的记录 |

### 可疑外联启发式

`/network/stats` 与 `/network/summary` 会标记可疑外联，并给出**理由列表**
（而非一个孤立的布尔位，便于判断误报与调阈值）：

- `high-risk tld: .xyz` —— 高风险 TLD（`.tk` `.ml` `.xyz` `.zip` 等迁移常用域）；
- `high frequency egress: N requests in window` —— 同域名请求数超过阈值（默认 200）；
- `direct ip literal destination` —— 直连 IP 字面量，绕过了正常的域名解析路径。

---

## 审计日志

用于后台的审计查询与操作回放。所有写操作都会补全 `id`、`timestamp`、
`status`（默认 `success`）与 `actor_type`（默认 `user`）。

### 事件结构

```json
{
  "id": "aud_...",
  "timestamp": "2026-09-28T13:00:00Z",
  "actor": "u_1",
  "actor_type": "user",
  "action": "user.login",
  "resource": "user",
  "resource_id": "u_1",
  "details": {"ip_region": "cn-hangzhou"},
  "ip_address": "10.0.0.1",
  "user_agent": "Mozilla/5.0",
  "status": "success",
  "error": "",
  "seq": 42
}
```

`seq` 是单调递增的写入序号。同一毫秒内写入的事件按 `seq` 排序，
保证分页与导出结果稳定可复现。

### 接口

| 方法 | 路径 | 说明 |
|------|------|------|
| GET | `/audit/events` | 查询事件（过滤 + 分页 + 排序） |
| POST | `/audit/events` | 写入事件 |
| GET | `/audit/events/{id}` | 查看单条事件 |
| GET | `/audit/stats` | 按维度聚合 |
| GET | `/audit/distinct` | 维度去重取值（筛选器选项） |
| GET | `/audit/export` | 导出（`format=json` \| `csv`） |
| DELETE | `/audit/purge` | 删除早于 `before` 的事件 |

### 查询参数

| 参数 | 说明 |
|------|------|
| `actor` | 操作者 ID |
| `actor_type` | `user` \| `agent` \| `system` |
| `event_type` / `action` | 事件类型，两者等价 |
| `resource` | 资源类型：`agent`、`task`、`user`、`message`、`permission`、`template`、`system` |
| `resource_id` | 资源 ID |
| `status` | `success` \| `failure` |
| `ip_address` | 来源 IP |
| `start_time` / `end_time` | RFC3339 或 Unix 秒 |
| `limit` / `offset` | 分页，默认 50，上限 1000 |
| `sort_by` | `timestamp`（默认）\| `action` |
| `sort_order` | `desc`（默认）\| `asc` |

示例：

```bash
# 查询某用户今天的失败登录
curl -H "Authorization: Bearer $TOKEN" \
  "$BASE/audit/events?actor=u_1&status=failure&start_time=2026-09-28T00:00:00Z"

# 按操作类型聚合
curl -H "Authorization: Bearer $TOKEN" "$BASE/audit/stats?dimension=action"

# 导出 CSV
curl -H "Authorization: Bearer $TOKEN" "$BASE/audit/export?format=csv" -o audit.csv

# 清理 30 天前的事件
curl -X DELETE -H "Authorization: Bearer $TOKEN" \
  "$BASE/audit/purge?before=2026-08-29T00:00:00Z"
```

### 在代码中打点

```go
// 直接写事件
auditRec.RecordAgentEvent(ctx, audit.EventAgentCreated, "agent-1",
    map[string]interface{}{"name": "demo"})

// 包装一段业务逻辑，自动记录成功/失败
audited := audit.Middleware(auditSvc, audit.BusinessEvent{
    Action: "user.login",
    Resource: audit.ResourceUser,
    Actor: userID,
})
err := audited(ctx, func(ctx context.Context) error { return doLogin(ctx) })
```

审计写入失败不会中断主流程：包装函数始终把原始错误原样返回。

---

## 管理后台

管理后台的服务端部分：把各业务模块的统计聚合成控制台首屏需要的单一视图，
并托管将来的 React 构建产物。本仓库不含前端工具链，前端产物放在
`web/admin/dist`，缺失时控制台入口返回占位页而非 404。

### 接口

| 方法 | 路径 | 说明 |
|------|------|------|
| GET | `/api/v1/admin/config` | 读取控制台配置（标题、版本、功能开关） |
| PUT | `/api/v1/admin/config` | 更新配置（部分更新，未提供的字段保持原值） |
| GET | `/api/v1/admin/snapshot` | 聚合视图（控制台首屏） |
| GET | `/api/v1/admin/static` | 静态资源挂载状态 |
| GET | `/admin/*` | 前端静态资源（产物缺失时返回占位页） |

### 配置

`feature_flags` 为**整体替换**语义：请求中带上该字段即覆盖全部开关，
传空对象表示清空。默认只开启只读展示类开关，`agent_control`、
`task_control`、`user_admin` 默认关闭，避免开发期误操作真实 Agent。

```bash
curl -X PUT localhost:8080/api/v1/admin/config \
  -H 'Content-Type: application/json' \
  -d '{"title":"AgentBot 运维台","feature_flags":{"show_audit":true,"show_monitor":true}}'
```

标题会被 trim，为空或超过 120 字符返回 400；开关名不得含空白字符。

### 聚合视图

### 查询参数

| 参数 | 默认值 | 说明 |
|------|--------|------|
| `sections` | 全部 | 逗号分隔的分区名，见下表；未知分区返回 400 |
| `recent_limit` | 10 | “最近”类列表长度（上限 200），超限自动收敛 |
| `audit_window` | 不限 | 审计事件统计时间窗，Go duration 语法（如 `24h`） |

| 分区 | 内容 |
|------|------|
| `overview` | 概览计数（Agent / 任务 / 未处理告警 / 审计事件） |
| `agents` | Agent 状态分布 + 最近更新的若干个 |
| `tasks` | 任务状态分布 |
| `alerts` | 未处理告警（按严重级别聚合 + 最近若干条） |
| `audit` | 最近审计事件 + 按动作聚合 |
| `network` | 网络出口：策略计数（allow/deny）+ Top 外联目标 + 可疑外联 |
| `plugins` | 功能开关当前取值 |
| `static` | 静态资源挂载状态 |

**分区独立降级**：某个下游模块出错时，只有该分区缺失并记入
`errors`，同时 `overview.degraded` 置为 `true`，其余分区照常渲染。
因此控制台在部分模块未装配时依然可用。

```bash
# 只要概览和 Agent 分布，最近列表取 5 条
curl 'localhost:8080/api/v1/admin/snapshot?sections=overview,agents&recent_limit=5'
```

响应示例：

```json
{
  "title": "AgentBot Admin",
  "version": "v1.0.0",
  "generated_at": "2026-09-29T12:00:00Z",
  "overview": {
    "total_agents": 2,
    "running_agents": 1,
    "failed_agents": 0,
    "total_tasks": 1,
    "open_alerts": 0,
    "audit_events": 3,
    "degraded": false
  },
  "agents": {
    "total": 2,
    "by_state": {"idle": 1, "running": 1},
    "recently": [{"id": "a_...", "name": "agent-1", "state": "running", "updated_at": "..."}]
  }
}
```

`by_state` / `by_severity` / `by_action` 在无数据时返回空对象而非 `null`，
保证前端无需做额外判空。

---

## WebSocket

`GET /api/v1/ws?token=<access_token>` —— 省略 `token` 时以匿名连接建立，
加入后可通过消息体进行 Agent 事件订阅。
