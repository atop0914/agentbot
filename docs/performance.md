# 性能基线

> 本文档记录**本机实测**的关键路径开销，用于回答一个问题：
> **这次改动有没有让关键路径变慢？**
>
> ⚠️ 这些数字是**本机基线，不是承诺**。换机器、换磁盘、换内核都会变。
> 它的价值在于**同一台机器上的前后对比**，而不是绝对值。

## 1. 为什么需要它

3-9 给每一个已认证请求加了一次吊销表查询，给登录加了一次限流计数查询。
两者都在请求路径上，各自有 300ms 超时边界，但**正常情况下的额外开销**
此前没有任何数字 —— 只有「应该很快」这种不是工程结论的说法。

于是本轮（3-10）建立组件级与端到端两类基准，并在写基准的过程中
抓出了一个已上线代码里的**静默性能缺陷**（见第 5 节）。

## 2. 机器信息

```
CPU    : Intel(R) Xeon(R) Gold 6148 CPU @ 2.40GHz
Go     : go1.25.6 linux/amd64
Redis  : 7.0.15，本机 loopback（127.0.0.1:6379）
Postgres: 16.15，本机 loopback（127.0.0.1:5432），synchronous_commit=on
```

所有数字都用 `-cpu=1 -benchtime=200x`（或 100x）采集，避免并发调度噪音。
Postgres 相关基准跑在**真实数据库**上（含迁移），不是 mock。

## 3. 复现命令

```bash
cd /root/agentbot

# a. 凭据校验路径（内存储 vs Redis 吊销表）
go test -run '^$' -bench 'BenchmarkValidateAccessToken' -benchmem -cpu=1 ./internal/app/

# b. 关键仓储（memory vs postgres）
go test -run '^$' -bench 'BenchmarkAuditRepository|BenchmarkUserRepository' -benchmem -cpu=1 ./internal/app/

# c. /metrics 导出
go test -run '^$' -bench 'BenchmarkMetrics' -benchmem -cpu=1 ./internal/app/

# d. 端到端已认证请求
go test -run '^$' -bench 'BenchmarkAuthenticatedRequest' -benchmem -cpu=1 ./internal/app/

# 全部一起跑
go test -run '^$' -bench . -benchmem -cpu=1 ./internal/app/
```

依赖：Redis 与 PostgreSQL 必须可用，否则相关基准会 `b.Skip`
（**跳过而非失败** —— CI runner 上无这两个服务是常态，
让基准因环境缺失而红会把真正的失败淹没在噪音里）。

```bash
redis-cli ping                                  # 期望 PONG
pg_isready                                      # 期望 accepting connections
redis-server --daemonize yes --port 6379 --save '' --appendonly no   # 没起来时
```

## 4. 实测数字

### 4.1 凭据校验路径（3-9 引入的主要新成本）

| 基准 | ns/op | B/op | allocs/op |
|------|-------|------|-----------|
| `ValidateAccessToken` / memory 吊销表 | 17,662 | 3,920 | 76 |
| `ValidateAccessToken` / redis 吊销表 | 161,994 | 4,624 | 86 |
| `ValidateAccessToken` / redis（命中吊销） | 108,675 | 4,624 | 86 |

**读法**：Redis 吊销查询给每次凭据校验增加约 **144µs**，
约为内存实现的 **9 倍**。

**这笔开销买到的是什么**：3-9 之前，副本 A 的登出对副本 B 无效
（用户看到 200，安全动作没有生效）。内存实现「快」恰恰因为它做不到这件事。
所以 144µs 是「跨副本吊销生效」这个属性的价格，不是浪费 ——
判断值不值要看部署形态：单副本可以退回内存实现（配置不填 Redis 即可）。

「命中吊销」单独有数字，因为它走不同的分支（多一次 `ErrTokenRevoked` 构造），
而且是攻击者拿旧 token 反复重试时**能大量触发**的那条路径。

### 4.2 关键仓储（memory vs postgres）

| 基准 | memory | postgres |
|------|--------|----------|
| `AuditRepository_Append` | 1,620 ns/op | 2,954,755 ns/op |
| `AuditRepository_Query` | 48,352 ns/op | 1,380,888 ns/op |
| `UserRepository_Create` | 15,179 ns/op | 3,157,118 ns/op |
| `UserRepository_GetByEmail` | 232 ns/op | 722,092 ns/op |

**读法**：Postgres 路径比内存慢约 3 个数量级，这**不是缺陷** ——
内存实现不落盘，进程一重启数据就没了。按 `docs/persistence.md`，
Postgres 是为了让数据在重启后仍在（`TestPersistence_SurvivesRestart`
断言的正是这一点）。

`UserRepository_GetByEmail` 在**真实登录路径**上（登录先按 email 取用户
再比对密码哈希），是「登录接口有多快」的主要决定因素之一。
密码哈希校验（bcrypt）是**刻意**慢的，不在基准范围内 —— 它不该被优化掉。

### 4.3 /metrics 导出（每次抓取的固定成本）

| 基准 | ns/op | B/op | allocs/op |
|------|-------|------|-----------|
| `MetricsExpose`（约 234 组 series） | 6,726,793 | 3,594,355 | 28,435 |
| `MetricsHandler`（含路径归一） | 2,284,408 | 956,627 | 7,108 |

**读法**：导出约 6.7ms，且几乎全是分配。这是**每次抓取**的成本，
不是每请求成本 —— 按 15s 抓取间隔算，它占用 0.045% 的时间。
结论：在当前规模下，抓取间隔不是一个约束。

### 4.4 端到端已认证请求

| 基准 | ns/op | B/op | allocs/op |
|------|-------|------|-----------|
| memory 吊销表 | 344,916 | 27,669 | 290 |
| redis 吊销表 | 1,142,836 | 28,451 | 301 |

**读法**：组件级基准说 Redis 查询约 144µs，放回真实请求路径里约 800µs。
同数量级，但**端到端数字才是用户实际等待的时间** ——
两者都保留，而不是只留组件级那个更好看的。

## 5. ⚠️ 本轮用一个基准抓出的真 bug：连接池从未生效

**症状**：Postgres 仓储基准报约 19ms/op。第一反应是「磁盘慢，正常」。

**实际不是**。三条证据推翻了它：

1. 在数据库内跑 `DO` 块循环插入 100 次：**0.33 ms/op** —— 磁盘没问题；
2. 在池外直接 `sqldb.ExecContext`：**2.4 ms/op** —— 池也没问题；
3. `sql.DB.Stats()`：**`Idle=0`，`MaxIdleClosed` 一路涨** —— 连接在创建后立刻被丢弃。

**根因**（`internal/storage/storage.go` 的 `withDefaults()`）：

```go
if c.MaxIdleConns < 0 {          // ❌ 守卫写错了比较符
    c.MaxIdleConns = defaultMaxIdleConns
}
```

「未指定」在这份 `Config` 里的表示**就是 0**，而 `0 < 0` 为假，
于是 `MaxIdleConns` 停在 0。`database/sql` 对 0 的语义是
**「不保留任何空闲连接」**：每次用完就关，每个查询都要重新建连
（TCP + TLS + 认证）。

**实测对比**（100 次插入，真实 PostgreSQL）：

| MaxIdleConns | ms/op | Idle | MaxIdleClosed |
|--------------|-------|------|---------------|
| 0（bug 状态） | **16.594** | 0 | 206 |
| 5（修复后） | **2.694** | 1 | 0 |

**6.2 倍**，且**完全静默**：接口全部正常、测试全绿、没有任何错误日志。
除了基准，没有任何东西能看见它。

**为什么既有测试没抓到**：默认值测试检查了 `MaxOpenConns` 与
`PingTimeout`，**唯独没有检查 `MaxIdleConns`** ——
它断言「零值配置被补齐了」，而四个字段里有两个根本没被看过。

**修复**：改为 `if c.MaxIdleConns == 0`，并用
`TestConfig_WithDefaultsClampsIdleToOpen` 补上逐个字段的断言
（该回归测试已**验证过对旧代码会失败**，否则它可能什么都没测到）。

**教训**：
- 「默认值补齐」这类测试必须**逐字段覆盖**。挑两个字段检查，
  等于给其余字段留了一个静默的后门。
- 看到一个「符合预期但也可能是环境问题」的数字，先**排除环境**
  （在数据库内测 = 0.33ms，直接 Exec = 2.4ms）再下结论。
  把它归因于「磁盘慢」就会漏掉一个 6 倍的线上缺陷。
- `sql.DB.Stats()` 是回答「池到底有没有在工作」的唯一直接证据。
  猜不如读：

  ```go
  s := db.SQL().Stats()
  // Idle=0 且 MaxIdleClosed 持续增长 => 池在丢连接
  ```

## 6. 边界与后续

- 基准**不跑在 CI 里**。理由：GitHub runner 的性能抖动远大于
  我们要捕捉的回归幅度，绝对阈值必然产生假红。
  若将来要加，只应做**「不回归」的相对比较**（如与 main 分支的
  同一基准对比，超过 2 倍才告警），而不是绝对阈值。
- `benchstat` **未安装**，也不进 `go.mod`（它是开发工具，
  不是依赖）。需要时 `go install golang.org/x/perf/cmd/benchstat@latest`。
- 未覆盖的路径：限流计数（需要真实 Redis + 登录请求）、
  executor 任务调度、WebSocket 广播。留给后续批次 ——
  与其为了凑数写不诚实的基准，不如明确列出缺口。
- 基准里的 Postgres 是**持久**库：种子数据用唯一 email/ID，
  否则第二次运行会以「email already exists」失败 ——
  那个失败与「有多快」毫无关系。
