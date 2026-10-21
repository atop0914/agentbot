package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/atop0914/agentbot/internal/sandbox"
)

// Client 是一个已握手完成的 MCP server 连接。
//
// # 并发模型
//
// 一条连接上可以同时有多个在途请求：一个 reader goroutine 读 stdout，
// 按 id 把响应分发给各自的等待者。这是 MCP over stdio 的正确用法 ——
// 串行化会让「一个慢工具阻塞所有工具」。
//
// ⚠️ 请求 id 用**单调递增**而不是随机/时间戳：id 只需要在一条连接内
// 唯一，递增最省事且可读（日志里能直接看出「第 7 个请求」）。
type Client struct {
	cfg  ServerConfig
	tr   Transport
	info InitializeResult

	nextID atomic.Int64
	// pending 保存 id → 等待中的响应通道。
	pendingMu sync.Mutex
	pending   map[int64]chan *Response
	// closed 在 reader 退出**或** Close 被调用时关闭，用于唤醒所有等待者。
	//
	// ⚠️ 必须与 closeTransport 分开使用两个 sync.Once，这是实测踩到的坑：
	// 最初两处共用一个 once，于是 readLoop 退出时先把它用掉了，
	// 随后 Close() 里的 once.Do 变成 no-op —— **tr.Close() 从未执行，
	// 子进程永远不被回收**。而进程泄漏在测试里表现为「进程还在跑」，
	// 在线上表现为「每个崩溃的 server 留一个常驻进程」。
	closed     chan struct{}
	closeOne   sync.Once
	trCloseOne sync.Once

	// readErr 是 reader 退出时记录的传输错误（连接已不可用的原因）。
	readErrMu sync.Mutex
	readErr   error

	// requestTimeout 是单次请求的默认超时。
	requestTimeout time.Duration
}

// Connect 启动 server 并完成 initialize 握手。
//
// 握手失败时**必须**回收进程（defer 在错误路径上也要跑），
// 否则每次连接失败都在宿主上留一个 MCP server 进程。
func Connect(ctx context.Context, mgr *sandbox.Manager, cfg ServerConfig) (*Client, error) {
	if mgr == nil {
		return nil, errors.New("mcp: sandbox manager is required")
	}

	tr, err := newStdioTransport(ctx, mgr, cfg)
	if err != nil {
		return nil, err
	}

	timeout := cfg.StartTimeout
	if timeout <= 0 {
		timeout = DefaultStartTimeout
	}

	c := &Client{
		cfg:            cfg,
		tr:             tr,
		pending:        make(map[int64]chan *Response),
		closed:         make(chan struct{}),
		requestTimeout: cfg.RequestTimeout,
	}
	if c.requestTimeout <= 0 {
		c.requestTimeout = DefaultRequestTimeout
	}

	go c.readLoop()

	// 握手单独设超时：一个不响应 initialize 的 server 不该占住启动路径
	// 直到调用方的 ctx 过期（那可能是几分钟）。
	hctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	if err := c.handshake(hctx); err != nil {
		_ = c.Close()
		return nil, err
	}
	return c, nil
}

// handshake 执行 initialize → notifications/initialized。
func (c *Client) handshake(ctx context.Context) error {
	params, err := json.Marshal(initializeParams{
		ProtocolVersion: ProtocolVersion,
		Capabilities:    clientCapabilities{},
		ClientInfo:      implementation{Name: "agentbot", Version: "1"},
	})
	if err != nil {
		return fmt.Errorf("mcp: encode initialize params: %w", err)
	}

	raw, err := c.call(ctx, methodInitialize, params)
	if err != nil {
		return err
	}

	var res InitializeResult
	if err := json.Unmarshal(raw, &res); err != nil {
		return &ProtocolError{Reason: "initialize result is not an InitializeResult", Raw: string(raw)}
	}
	c.info = res

	// notifications/initialized 是**通知**（无 id、无响应）。
	// 它失败只记不报：规范要求客户端发，但一个没实现它的 server
	// 仍然能用（实测不少 server 会直接忽略）。
	note, _ := json.Marshal(map[string]any{"jsonrpc": JSONRPCVersion, "method": methodInitialized})
	if err := c.tr.Send(ctx, note); err != nil {
		return err
	}
	return nil
}

// ServerInfo 返回握手时 server 声明的实现信息。
func (c *Client) ServerInfo() implementation { return c.info.ServerInfo }

// ProtocolVersion 返回协商后的协议版本。
func (c *Client) ProtocolVersion() string {
	if c.info.ProtocolVersion == "" {
		return ProtocolVersion
	}
	return c.info.ProtocolVersion
}

// ToolsSupported 报告 server 是否声明了 tools 能力。
func (c *Client) ToolsSupported() bool { return c.info.ToolsSupported() }

// ListTools 拉取 server 提供的工具列表（单页；不自动翻页）。
func (c *Client) ListTools(ctx context.Context) (*ListToolsResult, error) {
	raw, err := c.call(ctx, methodListTools, nil)
	if err != nil {
		return nil, err
	}
	var res ListToolsResult
	if err := json.Unmarshal(raw, &res); err != nil {
		return nil, &ProtocolError{Reason: "tools/list result is malformed", Raw: string(raw)}
	}
	return &res, nil
}

// CallTool 调用一个工具。
//
// 返回的错误三类可区分（见 ToolError / RPCError / ProtocolError /
// TransportError 的注释）；工具自己报告失败时返回 *ToolError，
// 而**不是**一个 error 值为 nil 的"成功"—— 那会让调用方把失败的
// 工具调用记成成功。
func (c *Client) CallTool(ctx context.Context, name string, args json.RawMessage) (*CallToolResult, error) {
	if name == "" {
		return nil, errors.New("mcp: tool name is required")
	}
	params, err := json.Marshal(struct {
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments,omitempty"`
	}{Name: name, Arguments: args})
	if err != nil {
		return nil, fmt.Errorf("mcp: encode tools/call params: %w", err)
	}

	raw, err := c.call(ctx, methodCallTool, params)
	if err != nil {
		return nil, err
	}
	var res CallToolResult
	if err := json.Unmarshal(raw, &res); err != nil {
		return nil, &ProtocolError{Reason: "tools/call result is malformed", Raw: string(raw)}
	}
	if res.IsError {
		return &res, &ToolError{Tool: name, Message: summarizeContent(res.Content)}
	}
	return &res, nil
}

// Ping 探活。返回错误说明连接已不可用。
func (c *Client) Ping(ctx context.Context) error {
	_, err := c.call(ctx, methodPing, nil)
	return err
}

// DefaultRequestTimeout 返回本客户端使用的单次请求超时。
func (c *Client) DefaultRequestTimeout() time.Duration { return c.requestTimeout }

// Close 关闭连接并回收子进程。幂等。
func (c *Client) Close() error {
	var err error
	// 先唤醒等待者（让在途请求立刻返回），再关传输层（杀进程）。
	// 反过来会让在途请求在 tr.Close 期间继续等一个已经没人会写的通道。
	c.closeOne.Do(func() { close(c.closed) })
	c.trCloseOne.Do(func() { err = c.tr.Close() })
	return err
}

// call 发一条请求并等待对应 id 的响应。
func (c *Client) call(ctx context.Context, method string, params json.RawMessage) (json.RawMessage, error) {
	select {
	case <-c.closed:
		return nil, c.transportErr()
	default:
	}

	id := c.nextID.Add(1)
	ch := make(chan *Response, 1)
	c.pendingMu.Lock()
	c.pending[id] = ch
	c.pendingMu.Unlock()

	// 无论从哪条路径返回都要摘掉这个 id：留着会让 map 无限增长，
	// 而一个长期运行的客户端会累积成千上万个死条目。
	defer func() {
		c.pendingMu.Lock()
		delete(c.pending, id)
		c.pendingMu.Unlock()
	}()

	req := Request{JSONRPC: JSONRPCVersion, ID: &id, Method: method, Params: params}
	payload, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("mcp: encode request %s: %w", method, err)
	}

	if err := c.tr.Send(ctx, payload); err != nil {
		return nil, err
	}

	timeout := c.requestTimeout
	rctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	select {
	case resp := <-ch:
		return c.unwrapResponse(resp, method)
	case <-rctx.Done():
		if errors.Is(rctx.Err(), context.DeadlineExceeded) {
			// 超时错误里带上「本次连接跳过了多少非 JSON 行」：
			// 一个狂打日志的 server 会让每次调用都超时，而真正的原因
			// 不在超时上。少了这条线索，排障会从「网络慢」开始查。
			if tr, ok := c.tr.(*stdioTransport); ok {
				if n := tr.SkippedLines(); n > 0 {
					return nil, fmt.Errorf("mcp: %s timed out after %s, and %d non-JSON line(s) were skipped on stdout: %w",
						method, timeout, n, context.DeadlineExceeded)
				}
			}
			return nil, fmt.Errorf("mcp: %s timed out after %s: %w", method, timeout, context.DeadlineExceeded)
		}
		return nil, rctx.Err()
	case <-c.closed:
		return nil, c.transportErr()
	}
}

// unwrapResponse 把一条响应拆成「result 或 error」。
//
// ⚠️ 这里校验「id 对不上」这类协议问题，而不是在 readLoop 里校验 ——
// readLoop 只负责按 id 分发；一条 id 未知的响应在那里**没有等待者**，
// 写进任何一个通道都是错的（会把 A 的响应当成 B 的）。
func (c *Client) unwrapResponse(resp *Response, method string) (json.RawMessage, error) {
	if resp == nil {
		return nil, &ProtocolError{Reason: "nil response for " + method}
	}
	if resp.JSONRPC != JSONRPCVersion {
		return nil, &ProtocolError{Reason: "response jsonrpc field is not 2.0", Raw: string(mustMarshal(resp))}
	}
	if resp.Error != nil && len(resp.Result) > 0 {
		// 协议规定二者互斥。两个都给出时无法判定哪个权威 ——
		// 猜一个会让调用方以为成功或失败得毫无根据。
		return nil, &ProtocolError{Reason: "response carries both result and error", Raw: string(mustMarshal(resp))}
	}
	if resp.Error != nil {
		return nil, resp.Error
	}
	return resp.Result, nil
}

// readLoop 读 stdout 并按 id 分发响应。
//
// 退出时关闭 closed 通道，唤醒所有等待者 —— 否则一个崩溃的 server 会让
// 每个在途请求各自等到超时（30s），而真相（进程死了）早在第一刻就知道。
func (c *Client) readLoop() {
	defer func() {
		c.closeOne.Do(func() { close(c.closed) })
	}()

	for {
		line, err := c.tr.Receive(context.Background())
		if err != nil {
			c.setReadErr(err)
			return
		}

		var resp Response
		if err := json.Unmarshal(line, &resp); err != nil {
			// 能读到但解不成 Response：这是**协议错误**，不是传输问题。
			// 留着也无法继续（不知道它对应哪个请求），故记录后终止。
			//
			// ⚠️ 这里必须记成 ProtocolError 而不是让下一轮读到 EOF 时
			// 记成 TransportError。第一版的顺序反了：坏行 → continue →
			// 下一轮读到 EOF → 记 transport。于是「server 说胡话」
			// 被报成「连接断了」，而这两者的处置完全不同
			// （前者查 server 实现，后者重启进程）。**第一个错误才是真相。**
			c.setReadErr(&ProtocolError{Reason: "response is not a JSON-RPC response", Raw: string(line)})
			return
		}
		if resp.ID == nil {
			// 无 id 有两种完全不同的东西：
			//  1. server 发来的**通知**（如日志通知）—— 合法，忽略即可；
			//  2. 一条**不含协议必需字段**的畸形消息（如 `{"hello":"world"}`）。
			//
			// ⚠️ 第 2 类必须被识别出来，不能静默丢弃。实测踩过：
			// json.Unmarshal 对 Response 的字段全是可选的，一个随便的
			// JSON 对象会解出一个零值 Response（id=nil、jsonrpc=""），
			// 然后被当成「通知」跳过 —— 于是一个在说胡话的 server
			// 表现为「每次调用都超时」，真正的原因被藏起来了。
			// 判据：通知也必须有 jsonrpc 与 method。
			if resp.JSONRPC != JSONRPCVersion || resp.Method == "" {
				c.setReadErr(&ProtocolError{
					Reason: "message has neither an id (response) nor a method (notification)",
					Raw:    string(line),
				})
				return
			}
			continue
		}

		c.pendingMu.Lock()
		ch, ok := c.pending[*resp.ID]
		c.pendingMu.Unlock()
		if !ok {
			// 迟到的响应（请求已超时并清理）：丢弃。写进任何通道都会
			// 把别的请求的响应当成自己的。
			continue
		}
		// 通道带缓冲（1），不会阻塞 readLoop
		select {
		case ch <- &resp:
		default:
		}
	}
}

func (c *Client) setReadErr(err error) {
	c.readErrMu.Lock()
	defer c.readErrMu.Unlock()
	if c.readErr == nil {
		c.readErr = err
	}
}

// transportErr 返回「连接为什么不可用」。
//
// ⚠️ 必须区分「新写的传输错误」与「ctx 取消导致的关闭」：
// 一个被调用方主动取消的连接，其错误不应被记成 server 故障。
func (c *Client) transportErr() error {
	c.readErrMu.Lock()
	err := c.readErr
	c.readErrMu.Unlock()
	if err != nil {
		return err
	}
	return errTransportClosed
}

func mustMarshal(v any) []byte {
	b, _ := json.Marshal(v)
	return b
}

// summarizeContent 把工具结果的内容压成一行可读文本，用于 ToolError。
//
// 工具失败的原因几乎总在第一段 text 内容里（stderr、错误说明）；
// 不压缩会让错误信息带出整段 HTML 或几 KB 的日志。
func summarizeContent(content []Content) string {
	var out string
	for _, c := range content {
		if c.Type == "text" && c.Text != "" {
			out = c.Text
			break
		}
	}
	if out == "" {
		return "tool reported an error with no text content"
	}
	const max = 300
	if len(out) > max {
		out = out[:max] + "..."
	}
	return out
}
