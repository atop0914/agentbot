# MCP 工具接入

> **什么时候读**：要接一个外部工具 server / 排查「工具调用失败但不知道失败在哪一层」/
> 想知道这个实现与完整 MCP 规范的差距。
>
> **读完能回答**：支持哪些 MCP 方法、四类错误怎么区分、server 进程由谁管、
> 安全边界在哪、为什么不用官方 SDK。

---

## 1. 为什么做这个

第 1-3 轮把平台做扎实了（19 个业务模块、安全基线、可观测性），但一个 Agent
仍然只能执行「被拆好的本地任务」，**接不上任何外部工具生态**。用户想让它查
Jira、读数据库、调内部 API，只能自己写一个 `internal/adapter` 适配器。

MCP（Model Context Protocol）已经是事实标准：Anthropic 2024 年提出，
2025 年 OpenAI / Google 跟进。不接 MCP 就等于不能接任何现成工具。

## 2. 支持的协议子集

刻意**只**实现 `tools`。规范里的 resources / prompts / sampling / roots
在本平台没有任何消费方，而第 3 轮的教训是：一个「没人消费的能力」被消费的那一刻，
它的错误才会显形。先把 tools 做对，比把一个没人用的规范搬全更有价值。

| 方法 | 方向 | 说明 |
|------|------|------|
| `initialize` | 客户端 → server | 握手，协商协议版本与能力 |
| `notifications/initialized` | 客户端 → server | 握手完成通知（无 id、无响应） |
| `tools/list` | 客户端 → server | 拉取工具列表（**单页**，见下方边界） |
| `tools/call` | 客户端 → server | 调用工具 |
| `ping` | 客户端 → server | 探活 |

**传输**：stdio —— 子进程的 stdin/stdout，每行一条 JSON。

**stdout 只能承载协议消息**。日志必须走 stderr。本实现对「往 stdout 打日志」
的 server 采取**跳过并计数**的策略（不是立刻判死）：一个只是多打了一行 banner
的 server 不该完全不可用。但容忍有上限（100 行），超过即判定为协议错误。

## 3. 四类错误必须可区分

这是本模块最重要的设计。四类都用 `errors.As` 分别识别：

```go
var toolErr      *mcp.ToolError
var rpcErr       *mcp.RPCError
var protoErr     *mcp.ProtocolError
var transportErr *mcp.TransportError

switch {
case errors.As(err, &toolErr):
    // 工具自己报告失败（命令退出码非 0、文件不存在）
    // → 重试通常无用，把工具的 stderr 给用户看
case errors.As(err, &rpcErr):
    // 协议层拒绝：方法不存在、参数类型错
    // → 换参数可能就成，值得修调用方
case errors.As(err, &protoErr):
    // 进程活着但说胡话：非 JSON、缺协议字段、result 与 error 并存
    // → 查 server 实现，重启无用
case errors.As(err, &transportErr):
    // 管道断了：进程崩溃、EOF
    // → 这条连接以后也不能用了，必须重连
}
```

**为什么不能合并成一个「调用失败」**：第 3 轮的登出案例里，「token 已过期」
被报成「state 不匹配（CSRF）」，于是所有人往 CSRF 方向查了两天。
错的分类不只是不够细，它会把排障送到完全错误的方向。

一个具体的对照：`ToolError` 时**不应该**重连（server 明明工作正常，
只是工具返回了业务失败），而 `TransportError` 时**必须**重连。
合并成一类就只能瞎猜。

## 4. 超时是第四种、也是独立的一条

超时**不**归入 `TransportError`：

```go
if errors.Is(err, context.DeadlineExceeded) { /* 可重试 / 可调大超时 */ }
```

| 层级 | 默认值 | 覆盖方式 |
|------|--------|----------|
| 单次请求 | 30s | `ServerConfig.RequestTimeout` |
| initialize 握手 | 10s | `ServerConfig.StartTimeout` |

30s 的取值理由：工具调用是外部命令（git、npm、查询），比 HTTP 慢得多；
而超过 30s 的同步调用在交互场景里客户端早就该看到「正在执行」而不是一个卡住的请求。
长跑工具应当由 server 侧异步化，而不是让客户端无限等。

## 5. 进程由谁管

MCP server 是被本进程启动的**子进程**，由 `internal/sandbox` 监管：

```
sandbox.Manager.Start(ctx, agentID, workDir, command, args, WithPipes(), WithEnv(env))
        ↓
sandbox.Supervisor
        ├─ Attach：rlimit（子进程内 ulimit 引导）+ 进程组（Setpgid）
        ├─ Track：缓存 pgid（晚了就问不到了）+ cgroup attach
        └─ Stop：按 -pgid 杀整棵树 → 等 exitCh → 回收 cgroup
```

**为什么不直接 `os/exec`**：那等于把「进程组回收」与「墙钟超时」两条纪律
复制出第二份。本仓库在 executor/cloud 上已经踩过一次 —— 复制品的第二份
必然漏掉某一步，而漏掉的是 `Track` 或 `Close`。

### 环境变量：显式给，不继承

⚠️ 子进程**只**拿到最小集合（`PATH`、`HOME`）+ 调用方显式声明的变量，
**不继承** AgentBot 的环境。理由：AgentBot 的环境里有数据库密码、JWT 密钥，
而第三方 MCP server 是别人写的代码。「默认全给、需要时再删」只要忘删一个
就是一次凭据泄漏 —— 这不是风险，是必然。

### stderr 必须被持续读走

不读的后果不是「丢日志」，而是**进程卡死**：管道缓冲（通常 64KB）写满后，
server 的下一次 stderr 写入会阻塞，整个 server 就停在那里。
症状是「工具调用无故超时」，而真正的原因在一个没人读的管道上。

本实现保留 stderr 的**尾部 8KB** 作为排障证据（`StderrTail()`）——
server 崩溃时真正的原因（缺依赖、权限不足、参数错）都写在这里，
而不是在传输层错误里。

## 6. 安全边界

与 [sandbox.md](sandbox.md) 叙述的边界一致，这里再明确一次：

| 做到了 | **没**做到 |
|--------|-----------|
| CPU / 内存 / 文件大小 / 打开文件的 rlimit | 不是安全边界：共享内核 |
| cgroup v2 限额（linux，可写时） | 同 uid 进程可互相发信号、读 `/proc` |
| 进程组回收（不留孤儿） | 无法阻止恶意 server 主动逃逸 |
| 不继承父进程环境变量 | 无法限制网络访问 |

**结论**：可以跑「可信但可能写错」的 server，**不能**跑不信任的第三方代码。
后者需要真正的容器或 gVisor 级别的隔离。

## 7. 为什么不用官方 SDK

1. **零新增第三方依赖是仓库基线**（见 README 的开发约定）。
2. stdio 传输层只是「stdin/stdout 逐行 JSON-RPC 2.0」，用标准库能说清楚。
3. SDK **不负责**进程生命周期与进程组回收 —— 那恰恰是本模块真正的难点，
   仍要自己写。引入 SDK 只会把依赖树扩大一圈，难点一个都没省。

## 8. 已知边界（当前批次）

- **`tools/list` 不自动翻页**：`NextCursor` 被记录但不跟随。一个工具超过
  一页的 server 只会暴露第一页 —— 4-2 处理。
- **不处理 server-initiated 请求**：sampling、roots、elicitation 之类一律忽略。
  本客户端不声明这些能力，所以合规的 server 不该发；不发的实现就够用了。
- **没有重连**：`TransportError` 之后必须由上层重建 `Client`。
  连接上重放未完成请求对工具调用并不安全（同一个工具可能被执行两次）。
- **无 HTTP 接口**：`/api/v1/mcp/*` 在 4-4。
- **协议版本固定为 `2024-11-05`**：server 返回别的版本时接受并记录，
  不因版本不同拒绝连接（版本差异在 tools 这层是向后兼容的，
  拒绝只会让一个本来能用的 server 用不了）。

## 9. 测试怎么写的

`internal/mcp/testdata/fake_server/` 是一个假 MCP server，测试在 `t.TempDir()`
里**现编译**它，不依赖任何外部安装。

现编译而不是预编译入库：入库一个二进制意味着它会在某个时刻与源码不同步
（改了行为没重新编译，测试仍然绿），那是最难查的一类「测试撒谎」。

故障形态通过 `FAKE_MCP_MODE` 选择：

| 模式 | 模拟什么 | 期望的错误类型 |
|------|----------|---------------|
| （空） | 正常 | 无 |
| `crash` | 调用时 `os.Exit(3)` | `TransportError` |
| `slow` | 睡 5s 再回 | `context.DeadlineExceeded` |
| `malformed` | stdout 输出非 JSON 后退出 | `TransportError`（EOF） |
| `badjson` | 输出合法 JSON 但缺协议字段，且进程活着 | `ProtocolError` |
| `noisy` | 每条响应前打一行 banner | 无（行被跳过并计数） |
| `error_result` | `isError: true` | `ToolError` |
| `rpc_error` | JSON-RPC error 对象 | `RPCError` |
| `no_tools` | initialize 不声明 tools | `ToolsSupported() == false` |
| `hang_init` | 收到 initialize 后不回 | `context.DeadlineExceeded`（握手超时） |

**断言落在副作用上**，不是配置标识。例：握手失败时「进程被回收」这条，
断言方式是扫 `/proc/*/cmdline` 找那个可执行文件路径，确认它不存在 ——
而不是断言某个 `closed` 布尔值。
