# AgentBot 部署配置分层说明（Day 28）

AgentBot 的运行时配置遵循**单一覆盖链**，优先级从低到高：

```
1. 内置默认值（defaults.go）
2. 配置文件（JSON，--config / AGENTBOT_CONFIG_FILE）
3. 环境变量（AGENTBOT_*，前缀覆盖）
4. 必需机密校验（fail-fast，无默认值）
```

配置的读取入口只有一处：`internal/config.Load`。所有业务模块都不允许
直接读环境变量（`os.Getenv`），这是刻意的——散落的 `os.Getenv` 会让
「这个部署到底用了哪个值」无法回答，也无法在启动期统一校验。

## 1. 为什么机密没有默认值

`AgentBot` 的三个机密**没有**可用默认值，缺失时 `Load` 直接返回错误、
进程以非零码退出：

| 机密 | 环境变量 | 缺失后果 |
|------|----------|----------|
| JWT 签名密钥 | `AGENTBOT_AUTH_JWT_SECRET` | 拒绝启动 |
| SSO client secret | `AGENTBOT_SSO_CLIENT_SECRET` | 拒绝启动 |
| 数据库密码 | `AGENTBOT_DATABASE_PASSWORD` | 拒绝启动 |

上一版的 `app.New()` 里硬编码了 `Secret: "dev-secret-change-in-production"`。
这行字在开发期很方便，但它做过两件很糟的事：

1. 任何人拿到仓库就能伪造该部署签发的 token；
2. 忘记注入密钥的部署**不会失败**，而是带着一个公开密钥跑起来，
   看起来一切正常——这正是最难被发现的一类事故。

因此改成了显式 fail-fast：缺密码 = 起不来。启动失败是响亮的，
静默降级是无声的。

## 2. 默认值 vs 机密判定标准

判定一个配置项是否「机密」，看它泄漏后的**影响面**：

- 泄漏即导致身份伪造 / 数据访问越权 → 机密（无默认值，fail-fast）
- 泄漏只影响资源用量或观测 → 普通配置（有安全默认值，可覆盖）

## 3. 环境变量命名规则

统一 `AGENTBOT_` 前缀，分层用下划线连接：

```
AGENTBOT_SERVER_HOST          → Server.Host
AGENTBOT_SERVER_PORT          → Server.Port
AGENTBOT_AUTH_JWT_SECRET      → Auth.JWTSecret（机密，无默认）
AGENTBOT_AUTH_ACCESS_EXPIRY   → Auth.AccessExpiry（Go duration 字符串，如 1h）
AGENTBOT_LOG_LEVEL            → Log.Level
AGENTBOT_SSO_ISSUER           → SSO.Issuer
```

布尔值接受 `1/0/true/false/yes/no/on/off`（大小写不敏感）。
时间字段接受 Go 的 duration 字面量（`30s`、`5m`、`1h30m`），也接受纯整数
（按秒解释），兼容 compose/k8s 里常见的写法。

## 4. 配置文件示例

`deploy/config.example.json` 是完整的字段示例。最小可运行配置：

```json
{
  "server": { "host": "0.0.0.0", "port": 8080 },
  "auth":   { "jwt_secret": "在真实部署里请用环境变量注入" },
  "sso":    { "issuer": "https://idp.example.com", "client_id": "agentbot",
              "client_secret": "环境变量注入", "redirect_url": "https://bot.example.com/api/v1/sso/callback" }
}
```

文件里出现机密字段是允许的（部分部署用 Secret 挂载文件），但**不允许**
提交进版本库——`deploy/config.example.json` 里的值一律是占位符。

## 5. 部署形态

| 形态 | 目录 | 说明 |
|------|------|------|
| 容器镜像 | `deploy/docker/Dockerfile` | 多阶段：静态编译 → distroless，非 root |
| 本地编排 | `deploy/docker/docker-compose.yml` | 应用 + 依赖服务 + 健康检查 |
| 集群 | `deploy/k8s/` | Deployment / Service / ConfigMap / Secret |

三种形态的 securityContext 都是显式的（`runAsNonRoot`、
`readOnlyRootFilesystem`、`drop ALL`），不依赖运行时默认值。
