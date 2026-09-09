# AgentBot

企业级 AI 智能体平台。部署自主运行的 AI Agent，支持任务分解、浏览器自动化、多 Agent 协作。

## 技术栈

- Go 1.25+ (net/http 标准库)
- PostgreSQL + Redis
- JWT 认证 + bcrypt 密码哈希

## 项目结构

```
agentbot/
├── cmd/agentbot/main.go        # 入口，HTTP 服务器 + 优雅关停
├── internal/
│   ├── auth/                   # 认证模块（已实现）
│   │   ├── handler.go          # 注册/登录接口
│   │   ├── service.go          # 认证业务逻辑
│   │   ├── middleware.go       # JWT 认证中间件
│   │   ├── jwt.go              # Token 生成/验证
│   │   └── password.go         # bcrypt 哈希
│   ├── user/                   # 用户模块（已实现）
│   │   ├── model.go
│   │   ├── repository.go
│   │   └── service.go
│   ├── agent/                  # Agent 核心（类型定义）
│   ├── task/                   # 任务管理（类型定义）
│   ├── browser/                # 浏览器自动化（类型定义）
│   ├── cloud/                  # 云环境隔离（类型定义）
│   ├── communication/          # 多 Agent 通信（类型定义）
│   ├── template/               # 工作流模板（类型定义）
│   ├── monitor/                # 监控指标（类型定义）
│   └── audit/                  # 审计日志（类型定义）
├── pkg/config/                 # 配置加载
└── internal/pkg/errors/        # 错误类型
```

## 当前进度

- [x] 项目骨架 + 分层架构
- [x] 用户注册/登录（JWT + bcrypt）
- [x] 认证中间件
- [x] 各模块接口定义
- [ ] 数据库连接（PostgreSQL）
- [ ] Redis 缓存
- [ ] Agent 运行时（Docker 容器隔离）
- [ ] 任务分解引擎
- [ ] 浏览器自动化（Playwright）
- [ ] 多 Agent 通信（NATS）
- [ ] 监控 + 告警

## 快速开始

```bash
git clone git@github.com:atop0914/agentbot.git
cd agentbot
cp config.example.json config.json
# 编辑 config.json 填入数据库和 Redis 配置
go run cmd/agentbot/main.go
```

## API

```
GET  /health                  # 健康检查
POST /api/v1/auth/register    # 注册
POST /api/v1/auth/login       # 登录
GET  /api/v1/agents           # Agent 列表（待实现）
POST /api/v1/agents           # 创建 Agent（待实现）
POST /api/v1/tasks            # 创建任务（待实现）
```

## License

MIT
