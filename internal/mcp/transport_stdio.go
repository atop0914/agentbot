package mcp

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/atop0914/agentbot/internal/sandbox"
)

// Transport 是一条到一个 MCP server 的连接。
//
// 只有两个动作：把一条消息写出去、把消息读进来。刻意**不**在这里做
// 请求/响应配对（那是 Client 的职责）—— 传输层做配对会让「重连」
// 变成一件需要重放未完成请求的事，而重放对工具调用并不安全
// （同一个工具可能被调用两次）。
type Transport interface {
	// Send 写入一条消息（会补换行）。
	Send(ctx context.Context, msg []byte) error
	// Receive 读取一条消息。返回原始字节（已去掉换行）。
	Receive(ctx context.Context) ([]byte, error)
	// Close 关闭连接并回收子进程。
	Close() error
	// Err 返回连接关闭的原因（未关闭时为 nil）。
	Err() error
}

// DefaultRequestTimeout 是单次请求的默认超时。
//
// 30s 的选择：工具调用是外部命令（git、npm、查询），比 HTTP 慢得多；
// 而超过 30s 的同步调用在交互场景里已经不可接受了（客户端早就该看到
// 「正在执行」而不是一个卡住的请求）。长跑工具应当由 server 侧异步化，
// 而不是让客户端无限等。
const DefaultRequestTimeout = 30 * time.Second

// DefaultStartTimeout 是 initialize 握手的超时。
//
// 握手只是两次 JSON 往返，10s 足够；给太多会让「server 根本起不来」
// 这种错误拖到用户面前才暴露。
const DefaultStartTimeout = 10 * time.Second

// errTransportClosed 是连接关闭后 Send/Receive 返回的哨兵。
var errTransportClosed = errors.New("mcp: transport is closed")

// stdioTransport 通过子进程的 stdin/stdout 逐行收发 JSON。
//
// # 为什么 stdout 只能给协议用
//
// MCP stdio 传输规定：stdout **只**承载协议消息，日志走 stderr。
// 一个往 stdout 打调试信息的 server 会让客户端读到非 JSON 行。
// 本实现的处置是**跳过并记录**非 JSON 行（不是立刻失败）：
// 直接失败会让一个只是多打了一行 banner 的 server 完全不可用，
// 而跳过只是容忍 —— 但容忍必须有上限，见 maxNonJSONLines。
const maxNonJSONLines = 100

type stdioTransport struct {
	sup    *sandbox.Supervisor
	stdin  io.WriteCloser
	stdout *bufio.Reader
	// stderrPipe 是子进程 stderr 的读端，由 drainStderr 持续读走。
	stderrPipe io.Reader
	// stderr 尾部留作排障证据：server 崩溃时最有用的信息在这里。
	stderr *tailBuffer

	writeMu sync.Mutex

	closeOnce sync.Once
	closeErr  error
	errMu     sync.Mutex
	err       error

	// skipped 记录被跳过的非 JSON 行数（供日志与测试断言）。
	skippedMu    sync.Mutex
	skippedLines int
}

// newStdioTransport 启动子进程并建立管道。
//
// 进程由 sandbox.Manager 监管（rlimit/cgroup + 进程组回收），
// 而不是裸 os/exec —— 见 sandbox.Supervisor 的注释：
// 第二份进程管理的复制品必然漏掉某一步。
func newStdioTransport(ctx context.Context, mgr *sandbox.Manager, cfg ServerConfig) (*stdioTransport, error) {
	if mgr == nil {
		return nil, errors.New("mcp: sandbox manager is required")
	}
	if cfg.Command == "" {
		return nil, errors.New("mcp: server command is required")
	}

	agentID := cfg.AgentID
	if agentID == "" {
		// 没给 AgentID 时用 server 名 —— sandbox.Prepare 要求非空，
		// 而这里凭空返回错误只会让调用方多写一行样板。
		agentID = "mcp-" + cfg.Name
	}

	sup, err := mgr.Start(ctx, agentID, cfg.WorkDir, cfg.Command, cfg.Args,
		sandbox.WithPipes(), sandbox.WithEnv(childEnv(cfg.Env)))
	if err != nil {
		return nil, &TransportError{Op: "start", Err: err}
	}

	stdin, err := sup.StdinPipe()
	if err != nil {
		_ = sup.Stop()
		return nil, &TransportError{Op: "stdin pipe", Err: err}
	}
	stdout, err := sup.StdoutPipe()
	if err != nil {
		_ = sup.Stop()
		return nil, &TransportError{Op: "stdout pipe", Err: err}
	}
	stderr, err := sup.StderrPipe()
	if err != nil {
		_ = sup.Stop()
		return nil, &TransportError{Op: "stderr pipe", Err: err}
	}

	t := &stdioTransport{
		sup:        sup,
		stdin:      stdin,
		stdout:     bufio.NewReaderSize(stdout, 256*1024),
		stderrPipe: stderr,
		stderr:     newTailBuffer(8 * 1024),
	}
	go t.drainStderr()
	return t, nil
}

// drainStderr 持续读走 stderr。
//
// ⚠️ 必须读走而不是留着不管：管道缓冲写满后，server 的下一次 stderr
// 写入会阻塞，整个 server 就卡死了 —— 症状是「工具调用无故超时」，
// 而真正的原因在 stderr 上（一个写了很多日志的 server 必然触发）。
func (t *stdioTransport) drainStderr() {
	_, _ = io.Copy(t.stderr, t.stderrPipe)
}

// Send 写入一条消息。
//
// 通道写失败时把连接标记为已损坏：一个写不进去的管道意味着 server
// 已经不再读取，后续请求必然也失败 —— 让第一个失败成为唯一一个失败，
// 而不是让每个在途请求各超时一次。
func (t *stdioTransport) Send(ctx context.Context, msg []byte) error {
	if err := t.ctxErr(); err != nil {
		return err
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}

	buf := make([]byte, 0, len(msg)+1)
	buf = append(buf, msg...)
	buf = append(buf, '\n')

	t.writeMu.Lock()
	defer t.writeMu.Unlock()
	if _, err := t.stdin.Write(buf); err != nil {
		terr := &TransportError{Op: "write", Err: err}
		t.setErr(terr)
		return terr
	}
	return nil
}

// Receive 读取一条消息。
//
// 跳过空行与非 JSON 行（见 maxNonJSONLines）。用一个**预读**判断
// 「这一行是不是 JSON」而不是交给 json.Unmarshal 报错：后者无法区分
// 「不是 JSON」与「是 JSON 但结构不对」，而这两者对排障的含义完全不同
// （前者是 server 打日志，后者是 server 实现了错的协议）。
func (t *stdioTransport) Receive(ctx context.Context) ([]byte, error) {
	if err := t.ctxErr(); err != nil {
		return nil, err
	}

	type readResult struct {
		line []byte
		err  error
	}
	for {
		// 在 goroutine 里读：bufio 的 ReadBytes 不可取消，
		// 而 Receive 必须能被 ctx 打断（否则一次卡住的 server 会
		// 占住调用方直到进程被整个杀掉）。
		ch := make(chan readResult, 1)
		go func() {
			line, err := t.stdout.ReadBytes('\n')
			ch <- readResult{line: line, err: err}
		}()

		var res readResult
		select {
		case res = <-ch:
		case <-ctx.Done():
			// goroutine 仍会读到数据并写入带缓冲的通道（不会泄漏），
			// 只是这次调用不再等它。
			return nil, ctx.Err()
		}

		if res.err != nil {
			if errors.Is(res.err, io.EOF) {
				terr := &TransportError{Op: "read", Err: io.EOF}
				t.setErr(terr)
				return nil, terr
			}
			terr := &TransportError{Op: "read", Err: res.err}
			t.setErr(terr)
			return nil, terr
		}

		line := trimEOL(res.line)
		if len(line) == 0 {
			continue
		}
		if !looksLikeJSON(line) {
			t.skippedMu.Lock()
			t.skippedLines++
			n := t.skippedLines
			t.skippedMu.Unlock()
			if n > maxNonJSONLines {
				return nil, &ProtocolError{
					Reason: fmt.Sprintf("server wrote more than %d non-JSON lines to stdout", maxNonJSONLines),
					Raw:    string(line),
				}
			}
			continue
		}
		return line, nil
	}
}

// Close 关闭管道并回收子进程。
func (t *stdioTransport) Close() error {
	t.closeOnce.Do(func() {
		t.setErr(errTransportClosed)
		// 先关 stdin：一个行为良好的 server 看到 stdin EOF 会自行退出，
		// 这比直接 SIGKILL 干净（它有机会 flush / 清理临时文件）。
		_ = t.stdin.Close()
		if err := t.sup.Stop(); err != nil {
			t.closeErr = err
		}
	})
	return t.closeErr
}

// Err 返回连接关闭的原因。
func (t *stdioTransport) Err() error {
	t.errMu.Lock()
	defer t.errMu.Unlock()
	return t.err
}

// SkippedLines 返回被跳过的非 JSON 行数（用于日志与测试断言）。
func (t *stdioTransport) SkippedLines() int {
	t.skippedMu.Lock()
	defer t.skippedMu.Unlock()
	return t.skippedLines
}

// StderrTail 返回 server stderr 的尾部内容。
//
// 这是排障的第一入口：server 崩溃时，真正的原因（缺依赖、权限不足、
// 参数错）都写在 stderr 上，而不是在传输层错误里。
func (t *stdioTransport) StderrTail() string {
	return t.stderr.String()
}

func (t *stdioTransport) setErr(err error) {
	t.errMu.Lock()
	defer t.errMu.Unlock()
	if t.err == nil {
		t.err = err
	}
}

func (t *stdioTransport) ctxErr() error {
	if err := t.Err(); err != nil {
		if errors.Is(err, errTransportClosed) {
			return errTransportClosed
		}
		return err
	}
	return nil
}

// trimEOL 去掉行尾的 \r\n 或 \n。
//
// 必须处理 \r：server 可能跑在 Windows 上（或经 cygwin 构建），
// 行尾的 \r 会让 json.Unmarshal 直接报 invalid character。
func trimEOL(b []byte) []byte {
	for len(b) > 0 && (b[len(b)-1] == '\n' || b[len(b)-1] == '\r') {
		b = b[:len(b)-1]
	}
	return b
}

// looksLikeJSON 判断一行是否**可能**是 JSON 消息。
//
// 只做形状判断，不做完整解析：完整解析要在这里解一次、在 Client 里再解一次，
// 两次解析的错误信息会不一致。
//
// ⚠️ 只看首字符是不够的。实测一个 server 的 banner 是
// `[fake-mcp] responding now` —— 以 `[` 开头，会被误判成 JSON 数组，
// 然后一路传到 readLoop，那里解不成 Response，于是把整条连接判死。
// 症状是「server 明明正常，调用却全部失败」。
//
// 因此对 `[` 额外要求第三段：真正的 JSON 数组第二个非空字符只能是
// `]`（空数组）、`"`（字符串）、数字或 `{`，绝不可能是字母。banner 形态
// `[fake-mcp]` 的第二个字符是字母，据此排除。
func looksLikeJSON(line []byte) bool {
	s := strings.TrimSpace(string(line))
	if s == "" {
		return false
	}
	switch s[0] {
	case '{':
		return true
	case '[':
		rest := strings.TrimLeft(s[1:], " \t")
		if rest == "" {
			// 只有 '['，可能是被截断的消息；交给上层判定
			return true
		}
		// ⚠️ 刻意**不**放行 't'/'f'/'n'：`[fake-mcp] ...` 这种 banner 的
		// 第二个字符正是 f，而 JSON 数组的元素实际上只会是
		// 对象/数组/字符串/数字/null —— 而 `[true]`/`[null]` 作为
		// MCP 消息本身也不成立（协议消息必须是对象）。
		c := rest[0]
		return c == ']' || c == '"' || c == '{' || c == '[' || (c >= '0' && c <= '9') || c == '-'
	default:
		return false
	}
}

// tailBuffer 保留写入内容的后 N 字节。
//
// 不用 bytes.Buffer：stderr 可能无限增长（一个打印循环的 server 能
// 在一分钟内写满内存），而我们需要的是「最近的、崩溃前的那些行」。
type tailBuffer struct {
	mu  sync.Mutex
	max int
	buf []byte
}

func newTailBuffer(max int) *tailBuffer {
	return &tailBuffer{max: max}
}

func (b *tailBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.buf = append(b.buf, p...)
	if len(b.buf) > b.max {
		b.buf = b.buf[len(b.buf)-b.max:]
	}
	return len(p), nil
}

func (b *tailBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return string(b.buf)
}

// childEnv 构造子进程的环境变量全集。
//
// ⚠️ 有意**只**给最小的可运行集合 + 调用方显式声明的变量：
// 继承父进程环境会让每个 MCP server 都拿到 AgentBot 的数据库密码与
// JWT 密钥。这不是「配置泄漏风险」，是「必然泄漏」—— 第三方 server
// 是别人写的代码。
//
// PATH 是必须的（否则 server 自己的 exec 找不到命令）；HOME 让依赖
// 用户目录的工具（git、npm）能工作。其余一概不给。
func childEnv(extra []string) []string {
	env := []string{
		"PATH=" + os.Getenv("PATH"),
	}
	if home := os.Getenv("HOME"); home != "" {
		env = append(env, "HOME="+home)
	}
	return append(env, extra...)
}
