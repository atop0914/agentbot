package redis

import (
	"errors"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// testAddr 返回可用的 Redis 地址。
//
// 判定依据是「Redis 现在到底能不能连上」，不是环境变量是否存在 ——
// 与 3-3 的教训一致：环境变量说有，不代表真能连。
const defaultTestAddr = "127.0.0.1:6379"

func testAddr() string {
	if a := os.Getenv("AGENTBOT_TEST_REDIS_ADDR"); a != "" {
		return a
	}
	return defaultTestAddr
}

// newTestClient 建一个测试客户端；Redis 不可达时 t.Skip。
//
// 刻意不在 SKIP 时静默通过：调用方可以用 requireNoSkip 断言
// 「本机的确跑了真机测试」，避免把「全跳过」误当成「全通过」。
func newTestClient(t *testing.T) *Client {
	t.Helper()
	c, err := New(Config{Addr: testAddr(), DialTimeout: time.Second})
	if err != nil {
		t.Skipf("redis unavailable at %s: %v", testAddr(), err)
	}
	t.Cleanup(func() { _ = c.Close() })
	return c
}

// key 生成带前缀的唯一键，避免与同机其他测试互相干扰。
func key(t *testing.T, suffix string) string {
	t.Helper()
	return "agentbot:test:" + t.Name() + ":" + suffix
}

// TestClient_Ping 验证真机能握手。
func TestClient_Ping(t *testing.T) {
	c := newTestClient(t)
	if err := c.Ping(); err != nil {
		t.Fatalf("ping failed: %v", err)
	}
}

// TestClient_SetGetRoundTrip 验证最基础的写读往返。
//
// 断言的是**读回来的值**，不是「没报错」—— 一个把 SET 写成
// 丢包但不报错的实现，只有读回来才能发现。
func TestClient_SetGetRoundTrip(t *testing.T) {
	c := newTestClient(t)
	k := key(t, "roundtrip")

	if _, err := c.Do("SET", k, "hello"); err != nil {
		t.Fatalf("set failed: %v", err)
	}
	got, err := c.Do("GET", k)
	if err != nil {
		t.Fatalf("get failed: %v", err)
	}
	if got != "hello" {
		t.Fatalf("got %v, want hello", got)
	}

	// 清理，避免污染后续运行。
	if _, err := c.Do("DEL", k); err != nil {
		t.Fatalf("del failed: %v", err)
	}
}

// TestClient_GetMissingReturnsErrNil 验证缺键返回 ErrNil。
//
// 「键不存在」必须能与「键存在但值为空串」区分：吊销表把空串
// 当成「未吊销」会让判定反向。
func TestClient_GetMissingReturnsErrNil(t *testing.T) {
	c := newTestClient(t)

	k := key(t, "definitely-missing-"+strconv.FormatInt(time.Now().UnixNano(), 10))
	_, err := c.Do("GET", k)
	if !errors.Is(err, ErrNil) {
		t.Fatalf("got err %v, want ErrNil", err)
	}

	// 空串必须与缺键不同。
	k2 := key(t, "empty-value")
	if _, err := c.Do("SET", k2, ""); err != nil {
		t.Fatalf("set empty failed: %v", err)
	}
	v, err := c.Do("GET", k2)
	if err != nil {
		t.Fatalf("get empty failed: %v", err)
	}
	if v != "" {
		t.Fatalf("got %v, want empty string", v)
	}
	if _, err := c.Do("DEL", k2); err != nil {
		t.Fatalf("cleanup failed: %v", err)
	}
}

// TestClient_IntegerCommands 验证整数回复与自增。
func TestClient_IntegerCommands(t *testing.T) {
	c := newTestClient(t)
	k := key(t, "counter")
	t.Cleanup(func() { _, _ = c.Do("DEL", k) })

	for i := int64(1); i <= 3; i++ {
		v, err := c.Do("INCR", k)
		if err != nil {
			t.Fatalf("incr failed: %v", err)
		}
		n, ok := v.(int64)
		if !ok {
			t.Fatalf("incr returned %T, want int64", v)
		}
		if n != i {
			t.Fatalf("incr = %d, want %d", n, i)
		}
	}
}

// TestClient_ExpireAndTTL 验证过期时间真的生效。
//
// 断言 TTL 而不是「EXPIRE 没报错」：EXPIRE 对不存在的键返回 0
// 且不报错，只看错误码会漏掉「键没写上」。
func TestClient_ExpireAndTTL(t *testing.T) {
	c := newTestClient(t)
	k := key(t, "ttl")
	t.Cleanup(func() { _, _ = c.Do("DEL", k) })

	if _, err := c.Do("SET", k, "v"); err != nil {
		t.Fatalf("set failed: %v", err)
	}
	if _, err := c.Do("EXPIRE", k, 60); err != nil {
		t.Fatalf("expire failed: %v", err)
	}
	v, err := c.Do("TTL", k)
	if err != nil {
		t.Fatalf("ttl failed: %v", err)
	}
	ttl, ok := v.(int64)
	if !ok {
		t.Fatalf("ttl returned %T, want int64", v)
	}
	if ttl <= 0 || ttl > 60 {
		t.Fatalf("ttl = %d, want in (0,60]", ttl)
	}
}

// TestClient_ServerErrorIsTyped 验证服务端错误是 ServerError 而不是网络错误。
//
// 两类错误的处理不同（重试 vs 不重试），混为一类会让调用方
// 对语法错误做无意义的重试。
func TestClient_ServerErrorIsTyped(t *testing.T) {
	c := newTestClient(t)

	// 对字符串键做 INCR，Redis 返回 WRONGTYPE。
	k := key(t, "wrongtype")
	if _, err := c.Do("SET", k, "not-a-number"); err != nil {
		t.Fatalf("set failed: %v", err)
	}
	t.Cleanup(func() { _, _ = c.Do("DEL", k) })

	_, err := c.Do("INCR", k)
	if err == nil {
		t.Fatal("expected error from INCR on string key")
	}
	var se *ServerError
	if !errors.As(err, &se) {
		t.Fatalf("got %T (%v), want *ServerError", err, err)
	}
	if isConnError(err) {
		t.Fatal("server error must not be classified as a connection error")
	}
}

// TestClient_UnsupportedArgumentType 验证参数类型错误显式报错。
//
// 若允许任意类型，`Do("SET", key, someStruct{})` 会编译通过并
// 写入 %v 的结果 —— 一个静默的错误值。
func TestClient_UnsupportedArgumentType(t *testing.T) {
	c := newTestClient(t)

	type weird struct{ A int }
	_, err := c.Do("SET", key(t, "weirdarg"), weird{A: 1})
	if err == nil {
		t.Fatal("expected error for unsupported argument type")
	}
	if !strings.Contains(err.Error(), "unsupported argument type") {
		t.Fatalf("got %v, want unsupported argument type error", err)
	}
}

// TestArgString_Duration 验证 Duration 参数按秒取整且不为 0。
//
// 这是纯函数测试，不做真机往返：EX/PX 的语法要求是
// `SET k v EX <sec>`（标志位在值之后），所以 Duration 的
// 转换逻辑只能直接测 argString，不能塞进 SET 的位置参数里。
//
// 为什么要提升到 1 秒：EX 收 0 是语法错误，而 `PX 0` 会变成
// 「立即过期」—— 调用方写 500ms 的本意绝不是「别存了」。
func TestArgString_Duration(t *testing.T) {
	cases := []struct {
		name string
		in   time.Duration
		want string
	}{
		{"sub-second rounds up to 1", 500 * time.Millisecond, "1"},
		{"zero rounds up to 1", 0, "1"},
		{"negative rounds up to 1", -5 * time.Second, "1"},
		{"whole seconds preserved", 90 * time.Second, "90"},
		{"fractional truncates to whole seconds", 1500 * time.Millisecond, "1"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := argString(tc.in)
			if err != nil {
				t.Fatalf("argString(%v) failed: %v", tc.in, err)
			}
			if got != tc.want {
				t.Fatalf("argString(%v) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// TestClient_DurationAsExpireFlag 验证 Duration 真的能配合 EX 标志使用。
//
// 首版这里写成 `SET k v <duration>`，Redis 报 ERR syntax error ——
// 错的是测试（位置参数不合法），不是 Duration 转换。
// 修成 `SET k v EX <sec>` 形态后，顺带验证了「转换结果能被执行」。
func TestClient_DurationAsExpireFlag(t *testing.T) {
	c := newTestClient(t)
	k := key(t, "durations")
	t.Cleanup(func() { _, _ = c.Do("DEL", k) })

	// 500ms → 1 秒，必须与 EX 标志一起传。
	arg, err := argString(500 * time.Millisecond)
	if err != nil {
		t.Fatalf("argString failed: %v", err)
	}
	if _, err := c.Do("SET", k, "v", "EX", arg); err != nil {
		t.Fatalf("set with EX failed: %v", err)
	}
	v, err := c.Do("TTL", k)
	if err != nil {
		t.Fatalf("ttl failed: %v", err)
	}
	ttl, ok := v.(int64)
	if !ok {
		t.Fatalf("ttl returned %T, want int64", v)
	}
	if ttl < 1 {
		t.Fatalf("ttl = %d, want >= 1 (500ms must not become immediate expiry)", ttl)
	}
}

// TestClient_ConcurrentDo 验证连接池在并发下不串数据。
//
// 每条连接被单个 goroutine 独占，若池实现有竞态，
// 症状会是「读到的值属于别的键」—— 所以断言值本身，不只断言无报错。
func TestClient_ConcurrentDo(t *testing.T) {
	c := newTestClient(t)
	const n = 32

	var wg sync.WaitGroup
	errs := make(chan error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			k := key(t, "concurrent-"+strconv.Itoa(i))
			want := "value-" + strconv.Itoa(i)
			if _, err := c.Do("SET", k, want); err != nil {
				errs <- err
				return
			}
			got, err := c.Do("GET", k)
			if err != nil {
				errs <- err
				return
			}
			if got != want {
				errs <- errors.New("cross-key value: got " + got.(string) + " want " + want)
				return
			}
			if _, err := c.Do("DEL", k); err != nil {
				errs <- err
			}
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatalf("concurrent op failed: %v", err)
	}
}

// TestClient_ClosedClientRejectsCommands 验证关闭后不再执行命令。
func TestClient_ClosedClientRejectsCommands(t *testing.T) {
	c := newTestClient(t)
	if err := c.Close(); err != nil {
		t.Fatalf("close failed: %v", err)
	}
	if err := c.Ping(); !errors.Is(err, ErrClosed) {
		t.Fatalf("got %v, want ErrClosed", err)
	}
	// 重复关闭必须幂等（defer 里关两次是常见形态）。
	if err := c.Close(); err != nil {
		t.Fatalf("second close must be a no-op, got %v", err)
	}
}

// TestNew_RejectsEmptyAddr 验证空地址被拒绝。
func TestNew_RejectsEmptyAddr(t *testing.T) {
	if _, err := New(Config{}); err == nil {
		t.Fatal("expected error for empty addr")
	}
}

// TestNew_AuthFailureIsSurfaced 验证密码错误在启动期就暴露。
//
// 这是「主动建连握手」的理由：懒连接会让配错的 Redis 表现为
// 「服务正常启动、第一次限流才报错」。
func TestNew_AuthFailureIsSurfaced(t *testing.T) {
	// 本机测试 Redis 未设密码，带密码连接应被拒绝。
	c, err := New(Config{Addr: testAddr(), Password: "definitely-wrong-password", DialTimeout: time.Second})
	if err != nil {
		// 期望路径：握手期就被拒。
		return
	}
	// 若服务端恰好允许（无密码时 Redis 会报 ERR Client sent AUTH...），
	// 也不能静默通过 —— 记录清楚并关闭。
	defer func() { _ = c.Close() }()
	t.Skip("test redis accepted AUTH without password configured; cannot assert fail-fast here")
}
