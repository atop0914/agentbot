// Package mcp 实现 Model Context Protocol 的**客户端**子集。
//
// # 为什么是「子集」而不是完整协议
//
// 完整 MCP 规范包含 resources / prompts / sampling / roots 等能力，但
// AgentBot 的 Agent 真正需要的只有一件事：**调用外部工具**。把资源与提示词
// 一起搬进来会得到一个「文档很全、但没有消费方」的实现 —— 第 3 轮的教训是
// 「没人消费的能力」一旦被消费，它的错误才会显形。因此本包只实现 tools。
//
// # 为什么不用官方 SDK
//
// 零新增第三方依赖是本仓库的基线（见 README 的开发约定）。MCP 的 stdio
// 传输层只是「stdin/stdout 逐行 JSON-RPC 2.0」，用标准库 200 行就能说清楚；
// 而引入 SDK 会把依赖树扩大一圈，却仍要自己实现进程生命周期与进程组回收
// （SDK 不负责这些）。权衡下来自己实现子集更划算，边界也更容易讲清楚。
//
// # 安全边界
//
// server 是被本进程**启动的子进程**，通过 sandbox.Manager 监管：
// 有 rlimit / cgroup 限额与进程组回收，但**不是安全边界**（共享内核，
// 同 uid 可发信号、读 /proc）。与 docs/sandbox.md 叙述的能力边界一致。
package mcp

import (
	"encoding/json"
	"fmt"
	"time"
)

// JSONRPCVersion 是协议版本号。JSON-RPC 2.0 要求请求与响应都带此字段。
const JSONRPCVersion = "2.0"

// ProtocolVersion 是本次实现的 MCP 协议版本。
//
// 协商规则（见 Client.initialize）：server 若返回一个**它自己支持的**
// 版本，客户端接受并记录下来；若返回空则退回本常量。刻意**不**因为版本
// 不同而拒绝连接 —— 版本差异在 tools 这一层是向后兼容的，拒绝连接
// 只会让一个本来能用的 server 用不了。
const ProtocolVersion = "2024-11-05"

// 本标准方法名。全部用小写下划线，与规范一致（不是 REST 的短横线风格）。
const (
	methodInitialize  = "initialize"
	methodInitialized = "notifications/initialized"
	methodListTools   = "tools/list"
	methodCallTool    = "tools/call"
	methodPing        = "ping"
)

// Request 是一条 JSON-RPC 2.0 请求。
//
// 用 `*int64` 表示 id：通知（notification）在协议里就是**没有 id 字段**，
// 而不是 id=0。用零值 int64 会在序列化时发出 "id":0，那是一条请求，
// server 会回一条响应 —— 通知语义当场丢失。
type Request struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      *int64          `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

// Response 是一条 JSON-RPC 2.0 响应。
//
// Error 与 Result 互斥：协议规定二者只能出现一个。这里不做强制校验，
// 而是让区分逻辑集中在 decodeResponse —— 一个地方判定，避免两处各写一份。
type Response struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      *int64          `json:"id,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *RPCError       `json:"error,omitempty"`
	// Method 非空表示这是一条来自 server 的**通知**（无 id）。
	//
	// ⚠️ 加这个字段不是为了处理通知内容，而是为了**把通知与畸形消息区分开**：
	// 只有 id 为空时无法判断「合法的通知」还是「随便一个 JSON 对象」，
	// 而后者静默跳过会让一个说胡话的 server 表现为「每次调用都超时」。
	Method string `json:"method,omitempty"`
}

// RPCError 是协议层的错误对象（JSON-RPC 2.0 §5.1）。
//
// ⚠️ 它与 ToolError 是**两种东西**，绝不能合并：
//   - RPCError 表示「这次调用没完成」（方法不存在、参数类型错、server 内部异常）
//   - ToolError 表示「调用完成了，但工具自己报告失败」（如命令退出码非 0）
//
// 混在一起的后果见 docs/mcp.md：运维会把「工具业务失败」当成「协议坏了」
// 去查传输层，方向一开始就错。
type RPCError struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data,omitempty"`
}

// Error 实现 error。带上 code 是有意的 —— 排障时 code 比 message 稳定。
func (e *RPCError) Error() string {
	if e == nil {
		return "<nil>"
	}
	if e.Data != nil && len(e.Data) > 0 {
		return fmt.Sprintf("mcp: rpc error %d: %s (data=%s)", e.Code, e.Message, string(e.Data))
	}
	return fmt.Sprintf("mcp: rpc error %d: %s", e.Code, e.Message)
}

// JSON-RPC 2.0 的预定义错误码（§5.1）。
const (
	CodeParseError     = -32700
	CodeInvalidRequest = -32600
	CodeMethodNotFound = -32601
	CodeInvalidParams  = -32602
	CodeInternalError  = -32603
)

// 实现能力（capabilities）。
//
// 客户端只声明它真的会做的事：tools。声明不实现的 capability 会让 server
// 发出客户端无法处理的请求 —— 那是一个「配置文件写着支持、实际不支持」的
// 假前提。
type clientCapabilities struct {
	Roots *struct{} `json:"roots,omitempty"`
}

// implementation 是协议里的「名称 + 版本」二元组。
type implementation struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

// initializeParams 是 initialize 请求的参数。
type initializeParams struct {
	ProtocolVersion string             `json:"protocolVersion"`
	Capabilities    clientCapabilities `json:"capabilities"`
	ClientInfo      implementation     `json:"clientInfo"`
}

// InitializeResult 是 initialize 的响应。
//
// Capabilities 保留为 RawMessage：不同 server 会声明 tools/resources/prompts
// 等不同组合，逐字段建模会得到一个「结构体永远多几个 null」的脆弱契约。
// 我们只消费 ToolsSupported()。
type InitializeResult struct {
	ProtocolVersion string          `json:"protocolVersion"`
	Capabilities    json.RawMessage `json:"capabilities"`
	ServerInfo      implementation  `json:"serverInfo"`
}

// ToolsSupported 报告 server 是否声明了 tools 能力。
//
// ⚠️ 这是「能不能调用工具」的唯一判据。没有这条判定就去 list_tools，
// 会得到一个 RPCError(method not found)，而调用方会把「这个 server
// 本来就不提供工具」误读成「server 坏了」。
func (r *InitializeResult) ToolsSupported() bool {
	if len(r.Capabilities) == 0 {
		return false
	}
	var caps struct {
		Tools *json.RawMessage `json:"tools"`
	}
	if err := json.Unmarshal(r.Capabilities, &caps); err != nil {
		return false
	}
	return caps.Tools != nil
}

// Tool 是一个工具的描述。
//
// InputSchema 保留为 RawMessage：JSON Schema 有几个版本，逐字段建模等于
// 把某一版规范钉死进类型里。第 4 轮的校验只做子集（见 validateArgs），
// 需要的正是「原始的、未经解释的」schema。
type Tool struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	InputSchema json.RawMessage `json:"inputSchema,omitempty"`
}

// ListToolsResult 是 tools/list 的响应。
//
// NextCursor 为空表示没有更多页。本实现**不**自动翻页（见 Service），
// 但把它记录下来，让调用方能区分「只有这些」与「还有更多」。
type ListToolsResult struct {
	Tools      []Tool `json:"tools"`
	NextCursor string `json:"nextCursor,omitempty"`
}

// Content 是工具结果里的一段内容。MCP 定义 text/image/resource 等类型。
type Content struct {
	Type string `json:"type"`
	Text string `json:"text,omitempty"`
	// 其余字段（data / mimeType / uri）按需透传，不做建模。
}

// CallToolResult 是 tools/call 的响应。
//
// ⚠️ IsError 是**工具级**失败，不是协议失败：server 成功处理了请求，
// 只是工具自己报告出错（命令返回非 0、文件不存在等）。它与 RPCError
// 必须可区分 —— 见 RPCError 的注释。
type CallToolResult struct {
	Content []Content `json:"content"`
	IsError bool      `json:"isError,omitempty"`
}

// ToolError 表示「工具调用完成，但工具报告失败」。
//
// 与 RPCError、TransportError 一起构成三类可区分的错误（第 4 轮 4-1 的
// 硬性要求）。三者都实现 error，但调用方应当用 errors.As 分别识别：
//
//	switch {
//	case errors.As(err, &mcp.ToolError{}):   // 工具的错，重试通常无用
//	case errors.As(err, &mcp.RPCError{}):     // 协议/方法层，可能换个参数就成
//	case errors.As(err, &mcp.TransportError{}): // 传输坏了，server 可能已死
//	}
type ToolError struct {
	Tool    string
	Message string
}

func (e *ToolError) Error() string {
	return fmt.Sprintf("mcp: tool %q reported an error: %s", e.Tool, e.Message)
}

// TransportError 表示「这条连接已经不可用」：进程崩溃、管道断开、
// 写入失败。它与 RPCError 的区别是**这条连接以后也不能用了**。
type TransportError struct {
	Op  string
	Err error
}

func (e *TransportError) Error() string {
	return fmt.Sprintf("mcp: transport %s failed: %v", e.Op, e.Err)
}

// Unwrap 让 errors.Is/As 能穿透到底层错误（如 io.EOF、os.ErrClosed）。
func (e *TransportError) Unwrap() error { return e.Err }

// ProtocolError 表示「server 回了一条不合法的消息」：不是 JSON、缺 jsonrpc
// 字段、id 对不上、error 与 result 同时出现。
//
// 单独一类而不并进 TransportError：协议错误说明**进程还活着但说胡话**，
// 处置方式不同（记录原始字节排查 server 实现，而不是重启 server）。
type ProtocolError struct {
	Reason string
	Raw    string
}

func (e *ProtocolError) Error() string {
	raw := e.Raw
	if len(raw) > 200 {
		raw = raw[:200] + "..."
	}
	return fmt.Sprintf("mcp: protocol error: %s (raw=%q)", e.Reason, raw)
}

// ServerConfig 描述一个待启动的 MCP server。
type ServerConfig struct {
	// Name 是逻辑名，同时用作工具命名空间前缀（见 4-2）。
	Name string
	// Command 是可执行文件路径，Args 是参数。两者直接传给 exec，不经 shell。
	Command string
	Args    []string
	// Env 是追加给子进程的环境变量（KEY=VALUE）。
	//
	// ⚠️ 刻意**不**继承父进程环境再覆盖，而是显式追加：MCP server 会读到
	// AgentBot 的全部环境变量（含数据库密码、JWT 密钥）。子进程需要什么
	// 就给它什么，比「默认全给、需要时再删」安全得多 —— 后者只要忘删一个
	// 变量就是一次凭据泄漏。
	Env []string
	// WorkDir 是子进程的工作目录（空表示继承）。
	WorkDir string
	// AgentID 是用于沙箱限额与进程组隔离的标识。
	AgentID string
	// RequestTimeout 是单次请求的默认超时（0 表示用 DefaultRequestTimeout）。
	RequestTimeout time.Duration
	// StartTimeout 是 initialize 握手的超时（0 表示用 DefaultStartTimeout）。
	StartTimeout time.Duration
}
