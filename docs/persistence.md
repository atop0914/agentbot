# 持久化边界与迁移策略

> **一句话**：数据在内存还是 Postgres、两套实现如何共存、迁移到哪一步了、回退点在哪。
> **适用读者**：接数据库的人、怀疑「数据没落库」的人、要加快捷仓储实现的人。
> **相关**：[文档索引](index.md) · [部署配置](deployment.md) · [性能基线](performance.md)

## 1. 现状：内存仓库清单与耦合度评估

全仓库共 **14 个** 持久化接口（`Repository` / `Store`）。逐一评估如下
（LOC 为模块总行数，用于衡量改造成本；「外部依赖」指该仓库的数据本身是否
引用进程外资源）：

| 模块 | 接口 | LOC | 数据来源 | 迁移难度 | 排序 |
|------|------|-----|----------|----------|------|
| `audit` | `Repository` | 4259 | 纯自产事件 | 低：追加型、无外部引用、字段固定 | **第 1 批** |
| `user` | `Repository` | 1509 | 纯自产用户 | 低：`internal/user/repository_postgres.go` 已有 SQL 实现雏形 | **第 1 批** |
| `agent` | `Repository` | 1358 | 纯自产 Agent | 低：CRUD + 状态机，字段稳定 | **第 2 批** |
| `role` / `authz` | `Repository` / `Store` | 1433 / 2140 | 纯自产授权数据 | 中：授权链读多写少，必须与权限判定同事务 | **第 2 批** |
| `template` | `Repository` | 2747 | 纯自产模板 | 中：模板市场含共享/导入语义 | 第 3 批 |
| `task` / `executor` | `Repository` | 2367 / 1949 | 自产任务 + 执行状态 | 中：与 goroutine 执行器耦合，需先稳定状态机 | 第 3 批 |
| `monitor` | `Repository` | 4003 | 时间序列 | **高**：写入量大、天然分片，属于 TSDB 而非 OLTP | 第 4 批（换存储引擎） |
| `tenant` | `Store` | 2078 | 租户归属 | 中：与 audit 联动的边界数据，建议随 audit 一起 | 第 2 批 |
| `network` | `PolicyStore` | 3235 | 出口策略 | 低-中：策略是配置型数据，也可走配置文件 | 第 3 批 |
| `memory` | `Store` | 1166 | Agent 记忆 | **高**：需要向量/全文检索，不是普通行存 | 第 4 批（换存储引擎） |
| `cloud` / `terminal` / `filesystem` / `browser` | `Repository` | 1796 / 1249 / 1292 / 1437 | **外部资源句柄** | **不适合迁**：存的是本地进程/路径状态 | 不迁 |
| `communication` | 内存态 | 1896 | 在线会话/群组 | **不适合迁**：实时在线状态，重启即失效是正确语义 | 不迁 |

### 为什么不一次性全迁

1. **耦合度不均匀**。上表的「迁移难度」跨度极大：`audit` 是纯追加表，
   `cloud` 存的是「某个本地进程的句柄」。把两者放进同一个迭代，
   真正的工作量会被后者吃掉，而后者**根本不该持久化** ——
   它是进程级资源，重启后句柄必然失效，落库只会存下一批死引用。
2. **回退面**。一次性替换 14 个仓库意味着任一处 SQL 有误都要全量回滚。
   分批则每一批都有独立的回退边界（换回内存实现即可）。
3. **可验证性**。`audit` 与新加的 `storage` 包能写「同一条断言跑两套实现」的
   契约测试；`memory` 的语义（相似度检索）连断言都还没定下来，
   强行迁等于先写一个性质不明的表。

### 明确判定为「不迁移」的部分及理由

这些不是「以后再说」，而是**结论就是不该迁**：

- `cloud` / `terminal` / `filesystem` / `browser`：持久化的是**进程外资源句柄**
  （本地工作目录、PTY 会话、浏览器上下文）。重启后句柄失效，
  存进数据库只会让下一次启动读到一批不可用的引用。正确做法是
  启动时标记为失效并让调用方重建 —— 见 Day 3-8 的进程级沙箱。
- `communication`：在线状态、群组成员实时视图。**重启即清空是正确的语义**；
  落库反而会让「谁在线」出现幽灵条目。

---

## 2. 驱动选型：`database/sql` + `pgx v5`（stdlib 模式）

本轮 Day 3-1 刚做过一次死依赖减法（模块图 19 → 9），所以加任何依赖都要给出理由。

**候选**：

| 方案 | 优点 | 缺点 | 判定 |
|------|------|------|------|
| `lib/pq` | 曾经的事实标准 | 已进入维护模式，作者建议迁移；不支持 PG 新特性 | ✗ |
| `database/sql` + `pgx/v5/stdlib` | 标准接口、生态通用、驱动活跃 | 多一层 stdlib 包装，PG 专有特性（COPY、LISTEN/NOTIFY）要走 `StdlibConn` | **✓ 选它** |
| 纯 `pgx/v5`（native） | 性能最好、PG 特性全 | 业务仓库要绑 pgx 类型，换库成本高；与「仓库层只依赖 `database/sql`」的既有风格冲突 | ✗ |
| SQLite（`modernc.org/sqlite`） | 纯 Go 无 cgo，零运维 | **不是本轮的落地目标**：并发写模型与 PG 差异大，先按 PG 写、后补 SQLite 会二次返工 | 备用 |

**结论**：**`database/sql` + `github.com/jackc/pgx/v5/stdlib`**。

理由：
1. `internal/user/repository_postgres.go` **已经**按 `*sql.DB` 写好了（含建表、CRUD、
   扫描函数），只是从未被装配过。选 `database/sql` 可以让这份既有代码
   直接进入生产，而不是推倒重写。
2. 仓库层保持 `database/sql` 抽象 → 后续若确实需要 SQLite 兜底，
   同一份仓库代码配不同驱动即可（见下节「为什么今天不直接上 SQLite」）。
3. pgx v5 是当前最活跃的 PG 驱动，且提供 `stdlib` 兼容层，不牺牲 PG 特性
   （需要时可 `db.Conn(ctx).Raw(...)` 取回原生连接）。

---

## 3. 两套实现如何共存

**原则：内存实现不删除，只是不再是默认接入点。**

- 接口不变。`audit.Repository`、`user.Repository` 等接口一个字不改，
  Postgres 实现只是接口的另一个实现 —— 这是前两轮「接口先于实现」布局的回报。
- 仓库按**能力**分组，由 `internal/storage` 统一开连接，装配层决定注入哪套。
- 内存实现保留用于两类场景：单元测试（零外部依赖）、以及显式声明的
  无数据库部署（demo / CI）。它们不是遗留代码，是**受支持的降级形态**。

### 自动选择规则（装配层 `internal/app`）

```
DSN 可用（database.host + db_name + password 齐全）
  ├─ 连接 + Ping 成功  → 使用 Postgres 实现，启动日志: storage.backend=postgres
  └─ 连接 / Ping 失败
       ├─ AllowMemoryFallback=false（默认）→ 拒绝启动（fail-fast）
       └─ AllowMemoryFallback=true        → 使用内存实现 + ERROR 级告警日志
DSN 不可用（未配置数据库）
  └─ 使用内存实现，启动日志: storage.backend=memory
```

### 关键决策：连不上时是 fail-fast 还是回退内存

**默认 fail-fast。** 理由：

1. **静默降级是最坏的一种失败**。一个连着 PG 的部署如果没有回退开关却
   悄悄退到内存，结果是「用户注册成功、重启后全部消失」——
   写入方看到的是 201 Created，没有任何信号告诉他数据只在内存里。
   这比启动失败危险得多：启动失败是响亮的，数据丢失是无声的。
2. **与既定原则一致**。配置层已经确立「机密缺失 → 拒绝启动」、
   「SSO 未启用 → 入口 503 而不是放行」。持久化同理：**声明了要用数据库，
   就必须真的用上**。
3. **回退必须是显式的**。确实需要「连不上就退内存」的场景（本地演示、
   无 PG 的 CI）通过 `AGENTBOT_STORAGE_ALLOW_MEMORY_FALLBACK=true`
   显式打开，且此时启动日志必须是 ERROR 级 —— 让这条降级在日志里刺眼。
4. **不允许「连上了但某些仓库失败」的中间态**。本轮迁移的仓库要么全走
   PG，要么全走内存；混合会把「数据的一部分持久、一部分不持久」变成常态，
   而这条信息在排障时几乎无法从外部观察。

---

## 4. 连接池 / 超时 / 迁移

### 连接池

| 参数 | 取值 | 理由 |
|------|------|------|
| `SetMaxOpenConns` | 25 | 单实例默认；PG 默认 `max_connections=100`，需给多实例留余量 |
| `SetMaxIdleConns` | 5 | 够覆盖稳态并发，不空占连接 |
| `SetConnMaxLifetime` | 30m | 短于常见 LB / PG 侧的 idle 断开时间，避免拿到半死连接 |
| `SetConnMaxIdleTime` | 5m | 回收长期空闲连接 |

`Ping` 超时固定 5s：这个时间是**启动期**用的，长于它说明对端不可达，
应该让部署立刻失败而不是挂住启动流程。

### 迁移（migration）

**自研轻量执行器，不引第三方迁移库。**

理由：迁移需求很小 —— 顺序执行若干条 SQL、记录已执行版本、幂等。
`golang-migrate` 会带来 schema 解析、多种驱动、CLI 等一大堆与本项目无关的面；
本轮刚做过依赖减法，不值得为此重新引入。

**取舍：内嵌 SQL（`//go:embed`）而不是外部文件目录。**

- 内嵌 → 二进制自带迁移脚本，部署时不需要额外挂载 `migrations/` 目录，
  也不会出现「代码更新了但迁移目录还是旧的」这种错配。这正是
  「单二进制交付」应有的形态。
- 代价：改迁移必须重新编译。对本次场景完全可接受（迁移本来就随版本发布）。

**幂等**：`schema_migrations(version TEXT PRIMARY KEY, applied_at TIMESTAMPTZ)`。
执行前 `SELECT version` 建集合，跳过已应用的；每条迁移在**事务内**执行
（DDL + 记录同时提交），失败整条回滚，不会留下「执行了一半」的版本。

**约定**：迁移版本号用 `0001_xxx.sql` 形式，按**字符串序**执行
（非数值序 —— 零填充保证两者等价，但字符串序对两位数以上更稳）。

---

## 5. 分批计划与回退点

| 批次 | 范围 | 回退方式 | 验收 |
|------|------|----------|------|
| 第 1 批 | `storage` 骨架 + `audit` + `user` | 删 DSN → 自动回内存 | 真实 PG 上跑通读写往返；契约测试两套实现同断言 |
| 第 2 批 | `agent` + `tenant` + `role`/`authz` | 同上 | 权限判定与归属登记在事务内一致 |
| 第 3 批 | `template` + `task`/`executor` + `network` | 同上 | 执行器状态机重启后可恢复 |
| 第 4 批 | `monitor`（TSDB）/ `memory`（检索） | 保持内存，另立方案 | 不属于 OLTP 迁移，需单独选型 |

每一批完成后 `memory` 实现**必须仍然可用且被测试覆盖**
—— 它是回退路径，不是死代码。

---

## 6. 为什么今天不直接上 SQLite

任务书把 SQLite 列为备用方案（「若 PostgreSQL 装不上」）。实测 PostgreSQL 16.15
已可用（`pg_isready` → accepting connections），所以该备用方案未启用。即便如此，
**SQLite 也不适合作为目标后端**：

1. 本项目 30 天计划的目标形态是「多实例、容器化部署」（Day 28 已产出
   Dockerfile / compose / K8s manifests）。SQLite 是单写者模型，
   多副本滚动更新时会直接冲突。
2. `user` 仓库已有代码按 PG 语义写就，SQLite 需要处理
   `DATETIME` / `?` 占位符 / `ON CONFLICT` 差异，属于二次返工。
3. 真正需要 SQLite 的场景（本地零依赖跑起来）已经有更简单的答案：
   **内存实现**。SQLite 介于两者之间，反而没有非它不可的用途。

SQLite 若日后确有需要（例如边缘单机部署），因为仓库层走 `database/sql`
抽象，届时补一个驱动即可，不需要重写业务代码。

---

## 7. 本轮（Day 3-3）实际交付

1. 本文档（设计边界与取舍）
2. `internal/storage`：`Open` / `Ping` / `Migrate` + 内嵌迁移执行器
   - `Config`：DSN、连接池参数、超时
   - `Open(ctx, cfg)` → `*DB`（包装 `*sql.DB` + backend 标识）
   - `Migrate(ctx)` → 幂等执行内嵌迁移
   - 哨兵错误：`ErrNoDSN`、`ErrUnreachable`
   - 单元测试：DSN 解析、迁移幂等（真机 PG；`testing.Short()` 短路）
3. `internal/app` 接线：DSN 非空 → 尝试连 Postgres，启动日志显式写出
   `storage.backend=postgres|memory`；两条分支都有测试覆盖
4. 真机往返验证：真实 PG 上跑一次读写并贴出命令与输出

Day 3-4 起按第 1 批把 `audit` / `user` 真正切到 Postgres 实现。
