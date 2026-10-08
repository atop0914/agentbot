# AgentBot

[![CI](https://github.com/atop0914/agentbot/actions/workflows/ci.yml/badge.svg)](https://github.com/atop0914/agentbot/actions/workflows/ci.yml)

企业级 AI 智能体平台。部署自主运行的 AI Agent，支持任务分解、浏览器自动化、多 Agent 协作。

当前版本：**v1.0.0**（24 个业务包，`go test -short -race` 全绿）

## 技术栈

- Go 1.25+ (net/http 标准库)
- PostgreSQL + Redis（规划中，当前为内存实现）
- JWT 认证 + bcrypt 密码哈希
- 零第三方 Web 框架与日志库：依赖只有 JWT、uuid、websocket、go-redis、x/crypto

## 发布

- **v1.0.0** — [Release 页面](https://github.com/atop0914/agentbot/releases/tag/v1.0.0)，
  5 个平台二进制 + `checksums.txt`（sha256 与 GitHub 侧摘要一致）

## 项目结构

```
agentbot/
├── cmd/agentbot/main.go        # 入口：加载配置 → 装配 → HTTP 服务 + 优雅关停
├── deploy/                     # 部署配置（见 docs/deployment.md）
│   ├── docker/                 #  多阶段 Dockerfile + compose（含 .env.example）
│   ├── k8s/                    #  Deployment/Service/ConfigMap/Secret/PVC/NetworkPolicy/PDB
│   └── config.example.json     #  配置分层样例
├── internal/
│   ├── config/                 # 配置分层（默认值 → 文件 → 环境变量）+ 机密 fail-fast
│   ├── version/                # 构建期注入的版本信息（ldflags -X）
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
│   ├── network/                # 网络出口路由（策略 / 网关 / 出站流量审计）
│   ├── tenant/                 # 多租户隔离（租户 / 成员 / 资源归属边界）
│   ├── sso/                    # 企业 SSO（OIDC 授权码流 + ID Token 校验）
│   ├── admin/                  # 管理后台聚合视图 + 静态资源托管
│   └── websocket/              # 实时通信
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
- [x] 网络出口路由（出口策略 + 代理网关 + 出站流量审计）
- [x] 管理后台服务端（聚合视图 + 配置 + 静态资源托管）
- [x] 实时通信（WebSocket）
- [x] 多租户隔离 + 企业 SSO（OIDC）
- [x] 配置分层 + 机密 fail-fast 启动校验
- [x] 部署配置（distroless 镜像 / compose / K8s manifests）
- [x] CI/CD（GitHub Actions：race 测试 + vet + golangci-lint v2；tag 触发交叉编译发版）
- [x] 构建期版本注入（`make build` / release 产物均带 version/commit/date）

### 规划中

- [ ] PostgreSQL 持久化（当前为内存实现）
- [ ] Redis 缓存与分布式锁
- [ ] Agent 运行时（容器隔离，依赖 Docker）
- [ ] 管理后台前端（React 构建产物挂载到 `/admin/*`）

## 构建

```bash
make build        # 本机二进制 → bin/，注入 git 推导的版本三元组
make build-all    # 交叉编译 5 个平台 → dist/ + checksums.txt
make check        # fmt + vet + test（提交前的完整闸门）
make lint         # golangci-lint（未安装时给出安装命令）
```

版本号由 `git describe` 推导，**不由人手填** —— 手填的版本号迟早与代码分叉。
校验注入是否生效：

```bash
./bin/agentbot --version   # agentbot v1.0.0 (commit 170eb51, built ..., go1.25.6, linux/amd64)
```

### 发版

打 tag 即触发 `.github/workflows/release.yml`：跑一遍 `-short -race` 测试，
交叉编译 linux/darwin/windows（跳过 windows/arm64），生成 `checksums.txt`，
创建 Release 并自动生成 release notes。

```bash
git checkout main && git merge dev --no-ff
git tag -a v1.0.1 -m "..." && git push origin main && git push origin v1.0.1
```

## 快速开始

```bash
git clone git@github.com:atop0914/agentbot.git
cd agentbot
go test -short ./...        # 运行测试（-short 跳过依赖 Docker 的用例）

# 本地开发：显式注入一组仅用于本地的机密（生产路径不会这样跑）
export AGENTBOT_AUTH_JWT_SECRET="$(openssl rand -base64 48)"
export AGENTBOT_DATABASE_PASSWORD="$(openssl rand -base64 24)"
go run ./cmd/agentbot        # 默认 :8080，指标 :9090

# 容器编排
cd deploy/docker && cp .env.example .env   # 填入两个机密
docker compose up -d
```

### 配置

优先级：**内置默认值 → 配置文件（`--config` / `AGENTBOT_CONFIG_FILE`）→ 环境变量**。
完整说明见 [docs/deployment.md](docs/deployment.md)。

三个机密**没有可用默认值**，缺失时进程拒绝启动（非零退出码 + 可操作的错误信息）：

| 变量 | 何时必需 |
|------|----------|
| `AGENTBOT_AUTH_JWT_SECRET` | 始终（≥32 字节随机值） |
| `AGENTBOT_DATABASE_PASSWORD` | 始终 |
| `AGENTBOT_SSO_CLIENT_SECRET` | 仅在启用企业 SSO 时 |

这一条是刻意的：带默认机密的部署只会在被攻击时才暴露问题，而拒绝启动在
第一次部署时就暴露问题。

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
       /api/v1/network                 出口策略 / 网关代理 / 出站流量审计
       /api/v1/tenants                 租户 / 成员 / 资源边界自查
       /api/v1/sso                     企业 SSO 登录（authorize / callback / status）
       /api/v1/admin                   管理后台配置 + 聚合视图
       /admin/*                        管理后台前端静态资源
       /api/v1/ws                      WebSocket 实时通道
```

## 开发约定

- 存储实现遍历 map 返回列表时必须排序，保证接口输出稳定
- 存进仓库的结构体存副本，避免调用方后续修改污染已存数据
- HTTP 路径参数用 `strings.TrimPrefix` 提取，合法请求始终返回非 nil 切片
- 新增模块需在 `internal/app/app.go` 装配，并在 `internal/app/router.go` 注册路由
- 新增路由必须在 `internal/authz` 的路由权限表登记：**无尾斜杠的集合路径**
  （如 `/api/v1/network/rules`）不会被任何前缀规则覆盖，漏登记即 403
- 安全边界类模块（出口策略、权限判定、租户隔离、SSO 校验）一律 fail-closed：
  依赖不可用时拒绝而非放行
- 租户身份只能来自**签名过的 JWT claims**，禁止从请求体 / query / 自定义 header 读取；
  跨租户访问一律返回 404（与「不存在」同形），不用 403 —— 403 会泄漏 ID 是否存在
- 权限表登记后缀规则时注意命名空间：`/users/{id}/status` 与 `/tenants/{id}/status`
  尾部相同，必须用 `Namespace` 区分，否则会静默串用对方的权限动作
- **新建受租户约束的资源必须同时登记归属**：只建资源不登记归属，等于让它在
  隔离语义下「不属于任何人」—— 配额统计恒为 0、租户资源清单看不到、跨租户检查
  对所有人返回 404（连合法所有者都访问不了）。症状是「建完就查不到」。
  Agent 通过 `agent.Handler.SetCreatedHook` 在装配层接入
  （`internal/app/app.go`），新增同类资源时照此接上
- 无租户作用域的平台级操作**不**登记归属：凭空挑一个租户等于把资源塞进别人家。
  未归属的资源对所有人不可见，这是明确取舍而非遗漏
- **业务代码不得直接读环境变量**：唯一入口是 `internal/config`，值由
  `app.NewWithConfig(cfg)` 注入。散落的 `os.Getenv` 会让「这个部署实际用了哪个值」
  无法回答，也无法在启动期统一校验
- 新增配置项：在 `internal/config` 的 `Default()` 给安全默认值 → 在 `fileConfig`
  加指针字段 → 在 `applyEnv` 加环境变量 → 在 `Validate()` 加约束（若需要）
- 机密一律不设默认值，并在 `Validate()` 用 `isPlaceholderSecret` 拦住占位符

## License

MIT