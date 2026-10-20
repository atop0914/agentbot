// Package redis 提供一个零第三方依赖的 Redis 客户端。
//
// 为什么不引入 go-redis：Redis 协议（RESP2）是一个行协议，客户端核心
// 只有「写命令 + 读回复」两件事。本仓库已有先例（3-7 手写 Prometheus
// text exposition format 而非引入 client_golang），且 go-redis 曾因
// 「声明了但从未 import」在 3-1 被移除。为一个行协议引入一棵依赖树
// 不划算，所以这里自实现。
//
// 本包只实现平台真实用到的命令（GET/SET/DEL/EXISTS/INCR/EXPIRE/TTL/PING/
// EVAL），不做通用封装 —— 用不到的命令不该出现在代码里。
package redis

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"sync"
	"time"
)

// 协议常量。
const (
	// replyOK 是简单字符串回复。
	replyOK = '+'
	// replyError 是错误回复。
	replyError = '-'
	// replyInteger 是整数回复。
	replyInteger = ':'
	// replyBulk 是批量字符串回复。
	replyBulk = '$'
	// replyArray 是数组回复。
	replyArray = '*'
	// replyNil 是 RESP2 的 null bulk string（$-1）。
	replyNil = -1
)

// ErrClosed 表示客户端已关闭。
var ErrClosed = errors.New("redis: client closed")

// ErrNil 表示键不存在（对应 Redis 的 nil 回复）。
//
// 单独定义而不是返回 ("", nil)，是为了让调用方能区分
// 「键不存在」与「键存在但值为空字符串」—— 这两者在 Redis 里
// 是不同状态，混用会让「已吊销」判定把空值当成未吊销。
var ErrNil = errors.New("redis: nil")

// Config 是客户端配置。
type Config struct {
	// Addr 是 host:port。
	Addr string
	// Password 为空表示不认证。
	Password string
	// DB 是逻辑库编号。
	DB int
	// DialTimeout 是建连超时，零值时用 defaultDialTimeout。
	DialTimeout time.Duration
	// ReadTimeout 是单次读超时，零值时用 defaultReadTimeout。
	ReadTimeout time.Duration
	// WriteTimeout 是单次写超时，零值时用 defaultWriteTimeout。
	WriteTimeout time.Duration
	// MaxIdle 是空闲连接池上限，零值时用 defaultMaxIdle。
	MaxIdle int
}

const (
	defaultDialTimeout  = 3 * time.Second
	defaultReadTimeout  = 3 * time.Second
	defaultWriteTimeout = 3 * time.Second
	defaultMaxIdle      = 8
)

// Client 是一个带连接池的 Redis 客户端。
//
// 并发安全：Pool 内部用互斥锁保护空闲连接列表。单条连接不被
// 多 goroutine 共享 —— 借出期间由调用方独占，归还后才可被再次借出。
type Client struct {
	cfg  Config
	pool *pool
}

// pool 是极简的空闲连接池。
//
// 刻意不做「最大连接数」上限：本平台的用途是 token 吊销与限流计数，
// 单次操作是微秒级往返，并发压力远低于需要排队的地步。加一个上限
// 反而引入「排队/超时」这类必须调优的参数，而它解决的不是真实问题。
type pool struct {
	mu      sync.Mutex
	idle    []*conn
	closed  bool
	maxIdle int
}

// conn 是一条连接及其读缓冲。
type conn struct {
	nc  net.Conn
	r   *bufio.Reader
	cfg Config
}

// 编译期断言：确保 time 包被使用（DialTimeout 等在 Config 里）。
var _ = time.Second

// New 创建客户端并做一次认证/选库握手。
//
// 这里刻意**主动建一条连接**（而不是懒连接）：配置错误（密码错、库越界）
// 必须在启动期暴露。若懒连接，一个配错的 Redis 会表现为「服务正常启动、
// 第一次限流才报错」—— 那正是本仓库反复出现的「启动成功 ≠ 运行期可用」。
func New(cfg Config) (*Client, error) {
	if strings.TrimSpace(cfg.Addr) == "" {
		return nil, errors.New("redis: addr is required")
	}
	if cfg.DialTimeout <= 0 {
		cfg.DialTimeout = defaultDialTimeout
	}
	if cfg.ReadTimeout <= 0 {
		cfg.ReadTimeout = defaultReadTimeout
	}
	if cfg.WriteTimeout <= 0 {
		cfg.WriteTimeout = defaultWriteTimeout
	}
	if cfg.MaxIdle <= 0 {
		cfg.MaxIdle = defaultMaxIdle
	}

	c := &Client{
		cfg:  cfg,
		pool: &pool{maxIdle: cfg.MaxIdle},
	}

	// 握手：拿一条真实连接，验证可达性 + 认证 + 选库。
	cn, err := c.dial()
	if err != nil {
		return nil, err
	}
	if cfg.Password != "" {
		if _, err := cn.do("AUTH", cfg.Password); err != nil {
			cn.close()
			return nil, fmt.Errorf("redis: auth failed: %w", err)
		}
	}
	if cfg.DB != 0 {
		if _, err := cn.do("SELECT", strconv.Itoa(cfg.DB)); err != nil {
			cn.close()
			return nil, fmt.Errorf("redis: select db %d failed: %w", cfg.DB, err)
		}
	}
	c.pool.put(cn)
	return c, nil
}

// Close 关闭客户端并释放全部空闲连接。
func (c *Client) Close() error {
	c.pool.mu.Lock()
	defer c.pool.mu.Unlock()
	if c.pool.closed {
		return nil
	}
	c.pool.closed = true
	for _, cn := range c.pool.idle {
		cn.close()
	}
	c.pool.idle = nil
	return nil
}

// Ping 探活。
func (c *Client) Ping() error {
	_, err := c.Do("PING")
	return err
}

// Do 执行一条命令并返回回复。
//
// 返回值类型映射：
//
//	简单字符串 -> string
//	整数       -> int64
//	批量字符串 -> string
//	nil 回复   -> ErrNil
//	数组       -> []any
func (c *Client) Do(args ...any) (any, error) {
	if len(args) == 0 {
		return nil, errors.New("redis: command is empty")
	}
	cn, err := c.pool.get(c)
	if err != nil {
		return nil, err
	}
	// 归还前检查连接健康：网络类错误会让连接状态不可信，直接丢弃。
	reply, err := cn.do(args...)
	if err != nil && !errors.Is(err, ErrNil) && isConnError(err) {
		cn.close()
		return nil, err
	}
	c.pool.put(cn)
	return reply, err
}

// dial 建立一条新连接。
func (c *Client) dial() (*conn, error) {
	nc, err := net.DialTimeout("tcp", c.cfg.Addr, c.cfg.DialTimeout)
	if err != nil {
		return nil, fmt.Errorf("redis: dial %s: %w", c.cfg.Addr, err)
	}
	return &conn{nc: nc, r: bufio.NewReader(nc), cfg: c.cfg}, nil
}

// get 借出一条连接，池空则新建。
func (p *pool) get(c *Client) (*conn, error) {
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return nil, ErrClosed
	}
	if n := len(p.idle); n > 0 {
		cn := p.idle[n-1]
		p.idle = p.idle[:n-1]
		p.mu.Unlock()
		return cn, nil
	}
	p.mu.Unlock()
	return c.dial()
}

// put 归还连接；池满或已关闭则直接关闭连接。
func (p *pool) put(cn *conn) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed || len(p.idle) >= p.maxIdle {
		cn.close()
		return
	}
	p.idle = append(p.idle, cn)
}

// close 关闭底层连接。
func (cn *conn) close() {
	if cn.nc != nil {
		_ = cn.nc.Close()
	}
}

// do 在单条连接上写命令 + 读回复。
func (cn *conn) do(args ...any) (any, error) {
	if err := cn.write(args...); err != nil {
		return nil, err
	}
	return cn.read()
}

// write 按 RESP2 数组格式写出命令。
func (cn *conn) write(args ...any) error {
	if err := cn.nc.SetWriteDeadline(time.Now().Add(cn.cfg.WriteTimeout)); err != nil {
		return err
	}
	var b strings.Builder
	// 预留一点空间，避免小命令多次扩容。
	b.Grow(32 * len(args))
	b.WriteByte(replyArray)
	b.WriteString(strconv.Itoa(len(args)))
	b.WriteString("\r\n")
	for _, a := range args {
		s, err := argString(a)
		if err != nil {
			return err
		}
		b.WriteByte(replyBulk)
		b.WriteString(strconv.Itoa(len(s)))
		b.WriteString("\r\n")
		b.WriteString(s)
		b.WriteString("\r\n")
	}
	if _, err := io.WriteString(cn.nc, b.String()); err != nil {
		return err
	}
	return nil
}

// argString 把命令参数转成字符串形式。
//
// 只接受 string/[]byte/整数/浮点/布尔 —— 刻意不接受任意类型，
// 因为「参数被 %v 悄悄格式化」会让 `Do("SET", key, struct{...})`
// 编译通过却写入无意义的值。让它显式报错更安全。
func argString(a any) (string, error) {
	switch v := a.(type) {
	case string:
		return v, nil
	case []byte:
		return string(v), nil
	case int:
		return strconv.Itoa(v), nil
	case int64:
		return strconv.FormatInt(v, 10), nil
	case uint64:
		return strconv.FormatUint(v, 10), nil
	case float64:
		return strconv.FormatFloat(v, 'f', -1, 64), nil
	case bool:
		if v {
			return "1", nil
		}
		return "0", nil
	case time.Duration:
		// Redis 的 EX/PX 参数都是整数，接受 Duration 时统一按秒取整，
		// 不足 1 秒按 1 秒 —— 传 0 会让 SET 变成「立即过期」，
		// 那几乎总是调用方写错了。
		sec := int64(v / time.Second)
		if sec < 1 {
			sec = 1
		}
		return strconv.FormatInt(sec, 10), nil
	default:
		return "", fmt.Errorf("redis: unsupported argument type %T", a)
	}
}

// read 解析一条回复。
func (cn *conn) read() (any, error) {
	if err := cn.nc.SetReadDeadline(time.Now().Add(cn.cfg.ReadTimeout)); err != nil {
		return nil, err
	}
	return cn.readValue()
}

// readValue 读一个 RESP2 值。
func (cn *conn) readValue() (any, error) {
	prefix, err := cn.r.ReadByte()
	if err != nil {
		return nil, err
	}
	line, err := cn.readLine()
	if err != nil {
		return nil, err
	}
	switch prefix {
	case replyOK:
		return line, nil
	case replyError:
		return nil, &ServerError{Message: line}
	case replyInteger:
		n, err := strconv.ParseInt(line, 10, 64)
		if err != nil {
			return nil, fmt.Errorf("redis: invalid integer reply %q", line)
		}
		return n, nil
	case replyBulk:
		n, err := strconv.Atoi(line)
		if err != nil {
			return nil, fmt.Errorf("redis: invalid bulk length %q", line)
		}
		if n == replyNil {
			return nil, ErrNil
		}
		if n < 0 {
			return nil, fmt.Errorf("redis: negative bulk length %d", n)
		}
		// 一次性读满 n 字节 + 结尾 CRLF。
		buf := make([]byte, n+2)
		if _, err := io.ReadFull(cn.r, buf); err != nil {
			return nil, err
		}
		if buf[n] != '\r' || buf[n+1] != '\n' {
			return nil, errors.New("redis: bulk string not terminated by CRLF")
		}
		return string(buf[:n]), nil
	case replyArray:
		n, err := strconv.Atoi(line)
		if err != nil {
			return nil, fmt.Errorf("redis: invalid array length %q", line)
		}
		if n < 0 {
			return nil, ErrNil
		}
		out := make([]any, 0, n)
		for i := 0; i < n; i++ {
			v, err := cn.readValue()
			if err != nil {
				return nil, err
			}
			out = append(out, v)
		}
		return out, nil
	default:
		return nil, fmt.Errorf("redis: unknown reply prefix %q", string(prefix))
	}
}

// readLine 读一行（不含 CRLF）。
func (cn *conn) readLine() (string, error) {
	line, err := cn.r.ReadString('\n')
	if err != nil {
		return "", err
	}
	if len(line) < 2 || !strings.HasSuffix(line, "\r\n") {
		return "", errors.New("redis: line not terminated by CRLF")
	}
	return line[:len(line)-2], nil
}

// ServerError 是 Redis 返回的错误回复（如 WRONGTYPE）。
//
// 与网络错误分开，是因为调用方的处理不同：服务端错误说明
// 「命令语法/类型错了」—— 重试无用；网络错误才值得重试。
type ServerError struct {
	Message string
}

// Error 实现 error。
func (e *ServerError) Error() string { return "redis: " + e.Message }

// isConnError 判定错误是否属于「连接不可信」类。
//
// ServerError 不算：服务端既然回了错误，说明连接是好的。
func isConnError(err error) bool {
	if err == nil {
		return false
	}
	var se *ServerError
	if errors.As(err, &se) {
		return false
	}
	return !errors.Is(err, ErrNil)
}
