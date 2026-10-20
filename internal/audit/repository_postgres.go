package audit

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
)

// repository_postgres.go 是 Repository 的 PostgreSQL 实现。
//
// 表结构不是本文件建的 —— 它由 storage 的 0001_baseline 迁移创建
// （audit_events）。这里只做读写，理由见 docs/persistence.md 的「两套实现如何共存」一节：
// 「一个表只有一个归属」，迁移与仓库分成两处会产出「谁该建表」的分歧。
//
// 语义必须与 repository.go 的 memory 实现**逐条对齐**。这不是「尽量一致」，
// 而是被 postgres_contract_test.go 里的同一组断言强制的契约 ——
// 两套实现各测各的，无法证明可以互换，而「换后端」正是这次改造的全部意义。
//
// 三处容易走偏的地方，在实现里逐一标注：
//  1. 脱敏必须在**写入前**做（fail-closed），且与 memory 版共用 RedactDetails。
//  2. Seq 由 BIGSERIAL 分配并用 RETURNING 取回，不能由 Go 侧生成 ——
//     多副本部署下 Go 侧的本地计数器会在实例之间重复。
//  3. List 的排序必须是 (created_at DESC, seq DESC) 且**全序**，
//     否则同毫秒事件的翻页会出现「第一页与第二页各出现一次同一行」。
type PostgresRepository struct {
	db *sql.DB
}

// NewPostgresRepository 创建 PostgreSQL 审计仓库。
//
// 传入 *storage.DB 而不是裸 *sql.DB 会让 audit 包依赖 storage 包，
// 但 storage 是被所有业务包共用的底座，且这里只需要一个 *sql.DB ——
// 保持依赖方向为「业务 → 标准库」，底座可以从容替换。
func NewPostgresRepository(db *sql.DB) *PostgresRepository {
	return &PostgresRepository{db: db}
}

// 列清单抽成常量：SELECT 与 scan 的顺序必须严格一致，
// 分散在四处手写迟早会出现「schemas 改了但 scan 没改」的错位读取
// —— 那种 bug 不会报错，只会让两个字段的值悄悄对调。
const auditColumns = `id, seq, event_type, actor_id, actor_type, action,
	resource, resource_id, status, error, ip_address, user_agent, details, created_at`

// auditInsertColumns 是 INSERT 用的列清单：与 SELECT 的 auditColumns 相比
// **去掉了 seq** —— 它由 BIGSERIAL 分配，不出现在值表达式里。
//
// 两份清单必须分开维护：列清单的长度与值表达式个数必须严格相等，
// 若图省事复用含 seq 的那份，就会报
// "INSERT has more target columns than expressions"。
const auditInsertColumns = `id, event_type, actor_id, actor_type, action,
	resource, resource_id, status, error, ip_address, user_agent, details, created_at`

// Append 追加一条审计事件。
//
// 与 memory 版一样：补全 ID / 时间戳 / 状态 / 操作者类型，并对 Details 脱敏。
// 脱敏放在仓库层（而不是服务层）是为了让绕过服务层的写入路径也拿到保护。
func (r *PostgresRepository) Append(ctx context.Context, event Event) (*EventRecord, error) {
	if event.Action == "" {
		return nil, ErrActionRequired
	}

	rec := &EventRecord{Event: event}
	if rec.ID == "" {
		// 与 memory 版的时间戳形式 ID 不同：数据库里的 ID 一旦重复就是
		// 主键冲突，而在高并发写入下纳秒时间戳仍可能撞。这里用 UUID，
		// 并保留 aud_ 前缀以便从 ID 一眼看出资源类型。
		rec.ID = "aud_" + uuid.NewString()
	}
	if rec.Timestamp.IsZero() {
		rec.Timestamp = time.Now().UTC()
	}
	if rec.Status == "" {
		rec.Status = StatusSuccess
	}
	if rec.ActorType == "" {
		rec.ActorType = ActorTypeUser
	}

	details, err := r.encodeDetails(rec.Details)
	if err != nil {
		return nil, err
	}
	rec.Details = details

	// seq 由 BIGSERIAL 分配并 RETURNING 取回：这是「同毫秒事件顺序稳定」
	// 的唯一可靠来源。不能由 Go 侧生成 —— 多副本各有一个本地计数器，
	// 分配出的 seq 会在实例间重复，List 的稳定排序随即失效。
	query := `INSERT INTO audit_events (` + auditInsertColumns + `)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13)
		RETURNING seq`

	var seq int64
	err = r.db.QueryRowContext(ctx, query,
		// 参数顺序必须与 auditInsertColumns **逐列对应**：
		//   id, event_type, actor_id, actor_type, action, resource, ...
		// event_type 与 action 都取 rec.Action，但两者不相邻
		// （event_type 在第 2 位、action 在第 5 位）—— 这是本函数最容易
		// 写错的地方：一旦错位，字符串字段会互相串门，SQL 不报错，
		// 只是过滤条件全部查不到（症状：按 actor 查 0 条、
		// Distinct(actor) 返回的是 action 的值）。
		rec.ID,
		rec.Action,    // event_type
		rec.Actor,     // actor_id
		rec.ActorType, // actor_type
		rec.Action,    // action（与 event_type 同值，见基线迁移注释）
		rec.Resource, rec.ResourceID, rec.Status, rec.Error,
		rec.IPAddress, rec.UserAgent, mustMarshal(details), rec.Timestamp,
	).Scan(&seq)
	if err != nil {
		return nil, fmt.Errorf("audit: failed to append event: %w", err)
	}
	rec.Seq = seq

	// 返回副本，避免调用方后续修改污染已保存的事件（与 memory 版一致）。
	out := *rec
	return &out, nil
}

// List 按过滤条件查询，排序与分页语义与 memory 版逐条对齐。
func (r *PostgresRepository) List(ctx context.Context, filter Filter) ([]*EventRecord, error) {
	where, args := auditWhere(filter)

	order := "DESC"
	if filter.SortOrder == SortAsc {
		order = "ASC"
	}
	// 两个排序键都必须是**全序**：created_at 相同的行靠 seq 定序。
	// 只按 created_at 排会让同毫秒事件的相对顺序在两次查询之间变化，
	// 翻页时表现为「某一行在第一页和第二页各出现一次，另一行永远看不到」。
	orderBy := "created_at " + order + ", seq " + order
	if filter.SortBy == SortByAction {
		orderBy = "action " + order + ", seq " + order
	}

	query := `SELECT ` + auditColumns + ` FROM audit_events` + where + ` ORDER BY ` + orderBy
	query, args = appendLimitOffset(query, args, filter)

	rows, err := r.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("audit: failed to list events: %w", err)
	}
	defer rows.Close()

	out := make([]*EventRecord, 0, 16)
	for rows.Next() {
		rec, err := scanEventRecord(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, rec)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("audit: failed to read events: %w", err)
	}
	return out, nil
}

// Count 统计命中条件的全量数量（忽略 Limit/Offset，与 memory 版一致）。
func (r *PostgresRepository) Count(ctx context.Context, filter Filter) (int, error) {
	countFilter := filter
	countFilter.Limit = 0
	countFilter.Offset = 0

	where, args := auditWhere(countFilter)
	var n int
	if err := r.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM audit_events`+where, args...).Scan(&n); err != nil {
		return 0, fmt.Errorf("audit: failed to count events: %w", err)
	}
	return n, nil
}

// Get 按 ID 获取单条事件。
func (r *PostgresRepository) Get(ctx context.Context, id string) (*EventRecord, error) {
	if id == "" {
		return nil, ErrIDRequired
	}
	row := r.db.QueryRowContext(ctx,
		`SELECT `+auditColumns+` FROM audit_events WHERE id = $1`, id)
	rec, err := scanEventRecord(row)
	if err != nil {
		// 翻译成与 memory 版同一个哨兵错误 —— 调用方要能对两套实现
		// 写同一段 errors.Is 判定，而不是按后端分支处理。
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrEventNotFound
		}
		return nil, err
	}
	return rec, nil
}

// Distinct 返回指定维度的去重取值。
//
// 维度到列名的映射写成白名单而不是字符串拼接：field 直接来自 HTTP 查询参数
// （handler 会先校验，但仓库层不能依赖调用方的校验 —— 这是一层纵深防御，
// 也是唯一能防住 SQL 注入的位置）。
var distinctColumns = map[string]string{
	DistinctActor:     "actor_id",
	DistinctActorType: "actor_type",
	DistinctAction:    "action",
	DistinctResource:  "resource",
	DistinctStatus:    "status",
	DistinctIPAddress: "ip_address",
}

func (r *PostgresRepository) Distinct(ctx context.Context, field string) ([]string, error) {
	col, ok := distinctColumns[field]
	if !ok {
		return nil, ErrUnknownDistinctField
	}

	// 空值不入结果：memory 版同样跳过空串，否则前端下拉框会出现一个
	// 选不中任何事件的空选项。
	query := `SELECT DISTINCT ` + col + ` FROM audit_events WHERE ` + col + ` <> '' ORDER BY ` + col
	rows, err := r.db.QueryContext(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("audit: failed to read distinct %s: %w", field, err)
	}
	defer rows.Close()

	out := make([]string, 0, 16)
	for rows.Next() {
		var v string
		if err := rows.Scan(&v); err != nil {
			return nil, fmt.Errorf("audit: failed to scan distinct %s: %w", field, err)
		}
		out = append(out, v)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("audit: failed to read distinct %s: %w", field, err)
	}
	return out, nil
}

// Purge 删除早于 cutoff 的事件并返回**真实删除行数**。
//
// 返回 rows.RowsAffected 而不是自己先 count 再 delete：先数后删之间存在
// 竞态窗口（并发写入的新事件会被多算），而受影响行数是数据库给出的权威值。
func (r *PostgresRepository) Purge(ctx context.Context, cutoff time.Time) (int, error) {
	if cutoff.IsZero() {
		return 0, ErrCutoffRequired
	}
	res, err := r.db.ExecContext(ctx, `DELETE FROM audit_events WHERE created_at < $1`, cutoff)
	if err != nil {
		return 0, fmt.Errorf("audit: failed to purge events: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("audit: failed to read purge result: %w", err)
	}
	return int(n), nil
}

// Ping 探活。用于健康检查与「本仓库是否真的连上了」的测试断言。
func (r *PostgresRepository) Ping(ctx context.Context) error {
	return r.db.PingContext(ctx)
}

// encodeDetails 补齐 nil 并做写入前脱敏，返回可直接落库的 map。
//
// 与 memory 版共用 RedactDetails —— 这是两套实现语义一致的**根本保证**：
// 若各自实现脱敏，规则一旦分叉就会出现「内存后端脱敏了、数据库后端没脱」，
// 而这种偏差在 code review 里几乎不可见（两处代码相隔 300 行）。
func (r *PostgresRepository) encodeDetails(details map[string]interface{}) (map[string]interface{}, error) {
	if details == nil {
		return map[string]interface{}{}, nil
	}
	cp := make(map[string]interface{}, len(details))
	for k, v := range details {
		cp[k] = v
	}
	redacted, paths := RedactDetails(cp)
	if len(paths) > 0 {
		redacted[RedactedKeysField] = paths
	}
	return redacted, nil
}

// scanEventRecord 从一行读取 EventRecord。
// sql.Row 与 sql.Rows 都满足这个最小接口，因此 List/Get 可以共用同一段 scan。
type rowScanner interface {
	Scan(dest ...interface{}) error
}

func scanEventRecord(row rowScanner) (*EventRecord, error) {
	var (
		rec        EventRecord
		detailsRaw []byte
	)
	// 变量个数必须与 SELECT 的 auditColumns **逐列对应**（14 列）。
	// event_type 只用来丢弃：它与 action 是同一语义的两列（见基线迁移注释），
	// Go 侧只有 Event.Action 一个字段，读 action 列即可。
	var eventTypeDiscard string
	err := row.Scan(
		&rec.ID, &rec.Seq, &eventTypeDiscard, &rec.Action, &rec.Actor, &rec.ActorType,
		&rec.Resource, &rec.ResourceID, &rec.Status, &rec.Error,
		&rec.IPAddress, &rec.UserAgent, &detailsRaw, &rec.Timestamp,
	)
	if err != nil {
		return nil, err
	}

	if len(detailsRaw) > 0 {
		var details map[string]interface{}
		if err := json.Unmarshal(detailsRaw, &details); err != nil {
			// JSONB 里的内容是写入方决定的；解析失败说明写入路径有问题，
			// 属于真问题，不能静默返回空明细（那会让审计记录看起来"没有详情"）。
			return nil, fmt.Errorf("audit: failed to decode details of event %s: %w", rec.ID, err)
		}
		rec.Details = details
	}
	if rec.Details == nil {
		rec.Details = map[string]interface{}{}
	}
	return &rec, nil
}

// auditWhere 把 Filter 翻译成 WHERE 子句与参数。
// 条件顺序与 memory 版的 match() 一一对应，便于对照检查。
func auditWhere(f Filter) (string, []interface{}) {
	var (
		conds []string
		args  []interface{}
	)
	// 占位符编号显式维护（$n 不能用参数数量简单推导，因为时间条件会各自
	// 追加一到两个参数）。pgx 的 stdlib 层支持 $n，不支持 ? 占位符。
	next := func(v interface{}) string {
		args = append(args, v)
		return fmt.Sprintf("$%d", len(args))
	}

	if f.StartTime != nil {
		conds = append(conds, "created_at >= "+next(*f.StartTime))
	}
	if f.EndTime != nil {
		conds = append(conds, "created_at <= "+next(*f.EndTime))
	}
	if f.Actor != "" {
		conds = append(conds, "actor_id = "+next(f.Actor))
	}
	if f.ActorType != "" {
		conds = append(conds, "actor_type = "+next(f.ActorType))
	}
	if f.EventType != "" {
		conds = append(conds, "action = "+next(f.EventType))
	}
	if f.Resource != "" {
		conds = append(conds, "resource = "+next(f.Resource))
	}
	if f.ResourceID != "" {
		conds = append(conds, "resource_id = "+next(f.ResourceID))
	}
	if f.Status != "" {
		conds = append(conds, "status = "+next(f.Status))
	}
	if f.IPAddress != "" {
		conds = append(conds, "ip_address = "+next(f.IPAddress))
	}

	if len(conds) == 0 {
		return "", nil
	}
	return " WHERE " + strings.Join(conds, " AND "), args
}

// appendLimitOffset 追加分页子句。Limit <= 0 表示不限制。
//
// 这里不做 offset 越界的特判：SQL 的 OFFSET 超出结果集时返回空集，
// 与 memory 版 paginate() 的行为一致（那边显式返回空切片，
// 而不是 nil —— 调用方对 nil 切片做 range 是安全的，但对 JSON 序列化
// 会产出 null 而非 []，接口契约会因此变化）。
func appendLimitOffset(query string, args []interface{}, f Filter) (string, []interface{}) {
	if f.Offset > 0 {
		args = append(args, f.Offset)
		query += fmt.Sprintf(" OFFSET $%d", len(args))
	}
	if f.Limit > 0 {
		args = append(args, f.Limit)
		query += fmt.Sprintf(" LIMIT $%d", len(args))
	}
	return query, args
}

// mustMarshal 序列化明细。这里不可能失败：明细已由 RedactDetails 处理过，
// 其中的值来自 JSON 解码（或调用方给的 Go 字面量），不含 channel / func
// 这类无法序列化的类型。保留 panic 语义（而非返回 error）是为了不把
// 一个「不可能发生」的错误路径混进 Append 的正常流程。
func mustMarshal(v map[string]interface{}) []byte {
	if v == nil {
		return []byte("{}")
	}
	b, err := json.Marshal(v)
	if err != nil {
		// 兜底而不是 panic：审计写入失败比丢掉明细更糟，
		// 落一个空对象并继续写入，至少事件本身留痕了。
		return []byte("{}")
	}
	return b
}
