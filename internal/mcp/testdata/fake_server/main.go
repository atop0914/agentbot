// fake_server 是一个只用于测试的 MCP server。
//
// 它位于 testdata/ 下 —— Go 工具链不会把 testdata 目录纳入
// `go build ./...`，所以它不会成为生产二进制的一部分；但它仍是
// 仓库里正常的、可编译、会被 lint 扫到的 Go 代码。
//
// 由测试在 t.TempDir() 里现编译出来：这样测试不依赖任何外部安装，
// 也不需要一个「已经存在的 server 二进制」这种隐含前提。
//
// 故障形态通过环境变量选择（FAKE_MCP_MODE）：
//
//	（空）/ normal  — 正常：initialize、tools/list、tools/call 全部可用
//	crash           — 收到 tools/call 后直接 os.Exit(3)（模拟 server 崩溃）
//	slow            — tools/call 睡 5s 再回（模拟超时）
//	malformed       — tools/call 回一条非 JSON 的行（模拟畸形输出）
//	badjson         — tools/call 回一行合法 JSON 但不是 JSON-RPC 响应
//	noisy           — 每条响应前先往 stdout 打一行 banner（模拟不守规矩的 server）
//	error_result    — tools/call 返回 isError=true（模拟工具级失败）
//	rpc_error       — tools/call 返回 JSON-RPC error（模拟协议层失败，方法不存在）
//	no_tools        — initialize 不声明 tools 能力
//	hang_init       — 收到 initialize 后什么都不回（模拟起不来的 server）
package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"time"
)

type request struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      *int64          `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

func main() {
	mode := os.Getenv("FAKE_MCP_MODE")
	fmt.Fprintf(os.Stderr, "fake-mcp-server starting in mode %q\n", mode)

	in := bufio.NewScanner(os.Stdin)
	in.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)
	out := bufio.NewWriter(os.Stdout)

	for in.Scan() {
		line := in.Bytes()
		if len(line) == 0 {
			continue
		}
		var req request
		if err := json.Unmarshal(line, &req); err != nil {
			// 输入坏了：协议错误
			writeResp(out, nil, nil, &rpcErr{Code: -32700, Message: "parse error"})
			continue
		}
		if req.ID == nil {
			// 通知：不回复
			continue
		}

		switch req.Method {
		case "initialize":
			if mode == "hang_init" {
				// 永不回复。刻意不 select，直接阻塞在这个循环里什么都不做。
				for {
					time.Sleep(time.Hour)
				}
			}
			caps := map[string]any{"tools": map[string]any{}}
			if mode == "no_tools" {
				caps = map[string]any{}
			}
			writeResp(out, req.ID, map[string]any{
				"protocolVersion": "2024-11-05",
				"capabilities":    caps,
				"serverInfo":      map[string]any{"name": "fake-mcp", "version": "0.1.0"},
			}, nil)

		case "tools/list":
			writeResp(out, req.ID, map[string]any{
				"tools": []map[string]any{
					{
						"name":        "echo",
						"description": "echoes the given text back",
						"inputSchema": map[string]any{
							"type":       "object",
							"properties": map[string]any{"text": map[string]any{"type": "string"}},
							"required":   []string{"text"},
						},
					},
					{
						"name":        "fail",
						"description": "always reports a tool-level error",
						"inputSchema": map[string]any{"type": "object"},
					},
					{
						"name":        "slow",
						"description": "sleeps before answering",
						"inputSchema": map[string]any{"type": "object"},
					},
				},
			}, nil)

		case "tools/call":
			handleCall(out, req, mode)

		case "ping":
			writeResp(out, req.ID, map[string]any{}, nil)

		default:
			writeResp(out, req.ID, nil, &rpcErr{Code: -32601, Message: "method not found: " + req.Method})
		}
	}
	fmt.Fprintln(os.Stderr, "fake-mcp-server stdin closed, exiting")
}

func handleCall(out *bufio.Writer, req request, mode string) {
	var p struct {
		Name string `json:"name"`
	}
	_ = json.Unmarshal(req.Params, &p)

	switch mode {
	case "crash":
		fmt.Fprintln(os.Stderr, "fake-mcp-server crashing on purpose")
		os.Exit(3)
	case "slow":
		time.Sleep(5 * time.Second)
	case "malformed":
		// 非 JSON 行：客户端会跳过并继续等（一个只是打日志的 server
		// 不该被判死）。随后本进程退出，客户端读到 EOF —— 那是正确的
		// 兜底：server 输出的东西无法解析、且它已经死了。
		fmt.Fprintln(out, "this is not json at all")
		_ = out.Flush()
		os.Exit(0)
	case "badjson":
		// 合法 JSON、但不含协议必需字段，且**继续运行**。
		// 这条是真协议错误：进程活着但在说胡话，客户端必须立刻
		// 把它报成 ProtocolError，而不是等请求超时。
		fmt.Fprintln(out, `{"hello":"world"}`)
		_ = out.Flush()
		// 继续读 stdin，保持进程活着
		for {
			time.Sleep(time.Hour)
		}
	case "rpc_error":
		writeResp(out, req.ID, nil, &rpcErr{Code: -32601, Message: "tool not found"})
		return
	}

	switch p.Name {
	case "echo":
		args := parseArgs(req.Params)
		writeResp(out, req.ID, map[string]any{
			"content": []map[string]any{{"type": "text", "text": "echo: " + args.Text}},
		}, nil)

	case "fail", "slow":
		if mode == "error_result" || p.Name == "fail" {
			writeResp(out, req.ID, map[string]any{
				"content": []map[string]any{{"type": "text", "text": "boom: the tool refuses to work"}},
				"isError": true,
			}, nil)
			return
		}
		writeResp(out, req.ID, map[string]any{
			"content": []map[string]any{{"type": "text", "text": "slept 5s"}},
		}, nil)

	default:
		writeResp(out, req.ID, nil, &rpcErr{Code: -32602, Message: "unknown tool: " + p.Name})
	}
}

// callArgs 是 tools/call 参数里我们会用到的那部分。
type callArgs struct {
	Text string `json:"text"`
}

// parseArgs 从 tools/call 的参数里取出 arguments 对象再解一层。
// 两段都需要解析：外层是 {name, arguments}，内层才是工具自己的参数。
func parseArgs(params json.RawMessage) callArgs {
	var wrapper struct {
		Arguments json.RawMessage `json:"arguments"`
	}
	_ = json.Unmarshal(params, &wrapper)
	var args callArgs
	_ = json.Unmarshal(wrapper.Arguments, &args)
	return args
}

type rpcErr struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func writeResp(out *bufio.Writer, id *int64, result any, e *rpcErr) {
	resp := map[string]any{"jsonrpc": "2.0"}
	if id != nil {
		resp["id"] = *id
	}
	if e != nil {
		resp["error"] = e
	} else {
		resp["result"] = result
	}
	// noisy 模式：在响应前打一行 banner。MCP 规定 stdout 只承载协议消息，
	// 一个不守规矩的 server 会让客户端读到非 JSON 行 —— 客户端必须跳过它
	// 而不是判定整条连接损坏。
	if os.Getenv("FAKE_MCP_MODE") == "noisy" {
		fmt.Fprintln(out, "[fake-mcp] responding now")
	}
	b, _ := json.Marshal(resp)
	fmt.Fprintln(out, string(b))
	_ = out.Flush()
}
