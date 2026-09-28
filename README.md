# AgentBot

企业级 AI 智能体平台。部署自主运行的 AI Agent，支持任务分解、浏览器自动化、多 Agent 协作。

## 技术栈

- Go 1.25+ (net/http 标准库)
- PostgreSQL + Redis（规划中，当前为内存实现）
- JWT 认证 + bcrypt 密码哈希

## 项目结构

```
agentbot/
├── cmd/agentbot/main.go        # 入口，HTTP 服务器 + 优雅关停
├── internal/
│   ├── auth/                   # 认证（JWT + bcrypt + OAuth）
│   ├── user/                   # 用户
│   ├── agent/                  # Agent 核心模型 + 状态机
│   ├── cloud/                  # 云环境生命周期管理
│   ├── executor/               # 任务执行引擎
│   ├── task/                   # 任务拆解 + 依赖图 + 并行调度
│   ├── browser/                # 浏览器自动化
│   ├── terminal/               # 终端会话
│   ├── filesystem/             # 文件系统操作
│   ├── adapter/                # 应用适配器（邮件/日历）
│   ├── memory/                 # Agent 记忆
│   ├── communication/          # 多 Agent 通信 + 在线状态
│   ├── role/                   # 角色与权限
│   ├── template/               # 工作流模板 + 模板市场
│   ├── monitor/                # 健康监控 + 告警
│   ├── audit/                  # 审计日志 + 操作回放
│   └── websocket/              # 实时通信
├── pkg/config/                 # 配置加载
└── internal/pkg/errors/        # 错误类型
```

每个模块统一分层：`types.go`（类型与接口）→ `repository.go`（存储）→ `service.go`（业务）
→ `handler.go`（HTTP）→ `<pkg>_test.go`（测试）。

## 当前进度

### 已完成

- [x] 项目骨架 + 分层架构
- [x] 用户注册/登录（JWT + bcrypt）
- [x] Agent 核心模型 + 状态机
- [x] 云环境生命周期管理
- [x] 任务拆解引擎 + 依赖图 + 并行调度
- [x] 浏览器自动化 / 终端会话 / 文件系统
- [x] 应用适配器框架（邮件、日历）
- [x] Agent 记忆系统（TTL、标签、重要性排序）
- [x] 多 Agent 通信 + 角色权限系统
- [x] 工作流模板 + 模板市场
- [x] Agent 健康监控 + 告警规则
- [x] 审计日志（查询、聚合、导出、清理）
- [x] 实时通信（WebSocket）

### 规划中

- [ ] PostgreSQL 持久化（当前为内存实现）
- [ ] Redis 缓存与分布式锁
- [ ] Agent 运行时（容器隔离，依赖 Docker）
- [ ] 管理后台前端
- [ ] 部署配置（Docker / K8s）

## 快速开始

```bash
git clone git@github.com:atop0914/agentbot.git
cd agentbot
go test -short ./...        # 运行测试（-short 跳过依赖 Docker 的用例）
go run cmd/agentbot/main.go # 启动服务，默认监听 :8080
```

## API

完整接口清单见 [docs/api.md](docs/api.md)，以下为概要：

```
GET    /health                        健康检查

POST   /api/v1/auth/register          注册
POST   /api/v1/auth/login             登录

       /api/v1/agents                  Agent CRUD
       /api/v1/environments            云环境生命周期
       /api/v1/tasks                   任务创建/启动/取消/重试
       /api/v1/browser                 浏览器会话
       /api/v1/terminal                终端会话与命令执行
       /api/v1/filesystem              文件读写/上传/搜索
       /api/v1/adapters                应用适配器
       /api/v1/memory                  Agent 记忆
       /api/v1/messages                多 Agent 消息
       /api/v1/roles                   角色与权限
       /api/v1/templates               工作流模板
       /api/v1/marketplace             模板市场
       /api/v1/monitor                 健康监控与告警
       /api/v1/audit                   审计日志查询/聚合/导出
       /api/v1/ws                      WebSocket 实时通道
```

## 开发约定

- 存储实现遍历 map 返回列表时必须排序，保证接口输出稳定
- 存进仓库的结构体存副本，避免调用方后续修改污染已存数据
- HTTP 路径参数用 `strings.TrimPrefix` 提取，合法请求始终返回非 nil 切片
- 新增模块需在 `internal/app/app.go` 装配，并在 `internal/app/router.go` 注册路由

## License

MIT
