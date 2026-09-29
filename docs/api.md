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
| GET/POST | `/monitor/alerts` | 告警列表 / 创建规则（`rules=1` 查看规则） |
| POST | `/monitor/alerts/{id}/resolve` | 解决告警 |
| DELETE | `/monitor/alerts/{id}` | 删除告警 |
| GET | `/monitor/dashboard` | 集群总览 |

健康分 = 资源占用（70%）+ 错误扣分（严重 15 / 一般 5）+ 活跃度。
告警规则类型：`cpu_high`、`memory_high`、`disk_high`、`inactive`、
`unresponsive`、`error_rate`、`task_failed`。

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
