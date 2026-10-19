package agent

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"time"
)

// repository_postgres.go 是 Repository 的 PostgreSQL 实现。
//
// 表结构由 storage 的 0002_agents 迁移创建（agents），本文件不建表 ——
// 「一个表只有一个归属」见 docs/persistence.md 的「分批计划与回退点」一节。
//
// 语义必须与 memory_repository.go 的实现逐条对齐，并由
// repository_contract_test.go 里同一组断言强制。四处容易走偏的地方：
//
//  1. **未命中返回 (nil, error)**，而不是 (nil, nil)。
//     agent 的 memory 版就是这么写的（GetByID 返回 fmt.Errorf("agent %s
//     not found")），与 user 仓库约定相反 —— agentService.Get 把**任何**
//     非 nil error 翻译成 404「agent not found」。若这里改成 (nil, nil)，
//     服务层会拿着一个 nil Agent 去 ToResponse() → 空指针 panic；
//     若把 404 之外的错误（如连接断开）也归成一类，调用方就分不清
//     「这个 Agent 不存在」和「数据库挂了」。
//     契约测试把这条钉死了。
//
//  2. **Store 副本**。memory 版存的是 `copy := *agent`，因为 Agent 里带
//     map（Config.Env）与 slice（Config.Tools）—— 调用方 Create 之后
//     继续改自己手里的结构体，不能改动库里的数据。Postgres 侧靠序列化
//     天然满足，但**写出去的字节必须来自一次快照**，不能边写边读。
//     本实现先 marshal 成 []byte 再写，读回来也是全新对象。
//
//  3. **Config 是 JSONB，不是若干扁平列**。Config 有 4 组子结构且会继续长
//     字段；拆成 ~15 个列意味着每加一个配置项都要写一条迁移，而且
//     Config.Tools 这种数组列用 JSONB 一次就能表达。
//
//  4. **state 列有 CHECK 约束**（见 0002_agents.sql）。写进未知状态会得到
//     一个可判定的约束错误，而不是一条读回来谁也不认识的字符串 ——
//     JSONB 与 TEXT 都不做值域校验，这一层只能靠 schema 兜。
//
// 与 user 仓库的另一个差异：agent 的 Update 走**整行覆盖**（含 state），
// 因为 SetState/ApplyUpdate 之后 service 调的就是 Update。
// UpdateState 是独立的窄路径（只改状态列），两者不可互相替代：
// 用 UpdateState 表达「改配置」会丢掉配置，用 Update 表达「只改状态」
// 会把调用方手里那份可能已陈旧的配置一起写回去。
type PostgresRepository struct {
	db *sql.DB
}

// NewPostgresRepository 创建 PostgreSQL Agent 仓库。
func NewPostgresRepository(db *sql.DB) *PostgresRepository {
	return &PostgresRepository{db: db}
}

// agentColumns 抽成常量：SELECT 与 scan 的顺序必须逐列对应。
// 分散手写迟早出现「加了列但 scan 没跟」的错位读取 —— 那种 bug 不报错，
// 只是两个字段的值悄悄对调（如 name 与 description 互换）。
const agentColumns = `id, name, description, state, container_id, config, created_at, updated_at`

// Create 创建 Agent。
//
// 与 memory 版一致：ID 重复视为错误（不是幂等 upsert）。
// 用 ON CONFLICT DO NOTHING + RowsAffected 判定而不是裸 INSERT：
// 显式的「0 行受影响」比解析 23505 错误文本更直接，也不会把其它
// 唯一约束冲突误判成「id 已存在」。
func (r *PostgresRepository) Create(ctx context.Context, agent *Agent) error {
	payload, err := encodeConfig(agent.Config)
	if err != nil {
		return err
	}

	res, err := r.db.ExecContext(ctx,
		`INSERT INTO agents (`+agentColumns+`)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
		 ON CONFLICT (id) DO NOTHING`,
		agent.ID, agent.Name, agent.Description, string(agent.State),
		agent.ContainerID, payload, agent.CreatedAt, agent.UpdatedAt,
	)
	if err != nil {
		return fmt.Errorf("failed to create agent: %w", err)
	}
	if n, err := res.RowsAffected(); err == nil && n == 0 {
		return fmt.Errorf("agent %s already exists", agent.ID)
	}
	return nil
}

// GetByID 根据 ID 获取 Agent。未命中返回包装了 not found 的错误，
// 与 memory 版一致（理由见文件头第 1 条）。
func (r *PostgresRepository) GetByID(ctx context.Context, id string) (*Agent, error) {
	a, err := scanAgent(r.db.QueryRowContext(ctx,
		`SELECT `+agentColumns+` FROM agents WHERE id = $1`, id))
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, fmt.Errorf("agent %s not found", id)
		}
		return nil, err
	}
	return a, nil
}

// Update 整行覆盖 Agent（含状态与配置）。
//
// 更新不存在的行返回错误而不是静默成功：0 行受影响通常意味着调用方
// 拿着一个已删除 Agent 的 ID 在改，静默通过会让「改完看不到变化」
// 成为最难查的一类问题。
func (r *PostgresRepository) Update(ctx context.Context, agent *Agent) error {
	payload, err := encodeConfig(agent.Config)
	if err != nil {
		return err
	}

	res, err := r.db.ExecContext(ctx,
		`UPDATE agents SET name = $1, description = $2, state = $3,
			container_id = $4, config = $5, updated_at = $6
		 WHERE id = $7`,
		agent.Name, agent.Description, string(agent.State),
		agent.ContainerID, payload, agent.UpdatedAt, agent.ID,
	)
	if err != nil {
		return fmt.Errorf("failed to update agent: %w", err)
	}
	if n, err := res.RowsAffected(); err == nil && n == 0 {
		return fmt.Errorf("agent %s not found", agent.ID)
	}
	return nil
}

// Delete 删除 Agent。
func (r *PostgresRepository) Delete(ctx context.Context, id string) error {
	res, err := r.db.ExecContext(ctx, `DELETE FROM agents WHERE id = $1`, id)
	if err != nil {
		return fmt.Errorf("failed to delete agent: %w", err)
	}
	if n, err := res.RowsAffected(); err == nil && n == 0 {
		return fmt.Errorf("agent %s not found", id)
	}
	return nil
}

// List 列出 Agent（分页，按创建时间降序）。
//
// 排序键是 (created_at DESC, id DESC) 而不是只按 created_at：
// 同一毫秒创建的一批 Agent（测试里批量建、真实场景的批量导入都是）
// 若只按时间排，两次查询的相对顺序可能不同 —— 分页时会出现
// 「某一行在两页各出现一次、另一行永远看不到」。
// memory 版的 sort.Slice 同样在时间相同时按 id 兜底，两套实现必须一致，
// 否则换后端会让前端分页行为悄悄变化。
//
// 越界（offset >= 总数）返回**空切片而不是 nil**：nil 会序列化成 JSON null，
// 前端拿到 null 再去 .map 就是一个运行时错误。service 层只做参数钳制，
// 不保证非 nil —— 这条得由仓库保证。
func (r *PostgresRepository) List(ctx context.Context, offset, limit int) ([]*Agent, error) {
	if offset < 0 {
		offset = 0
	}
	if limit <= 0 {
		limit = defaultAgentListLimit
	}

	rows, err := r.db.QueryContext(ctx,
		`SELECT `+agentColumns+` FROM agents
		 ORDER BY created_at DESC, id DESC LIMIT $1 OFFSET $2`, limit, offset)
	if err != nil {
		return nil, fmt.Errorf("failed to list agents: %w", err)
	}
	defer func() { _ = rows.Close() }()

	out := make([]*Agent, 0, limit)
	for rows.Next() {
		a, err := scanAgent(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("failed to read agents: %w", err)
	}
	return out, nil
}

// defaultAgentListLimit 是 List 未指定条数时的默认页大小，
// 与 agentService.List 的默认值保持一致（20）。
const defaultAgentListLimit = 20

// UpdateState 仅更新 Agent 状态。
//
// 单独一条窄路径而不是复用 Update：service 的 Start/Stop/Pause/Resume
// 已经在内存里走完状态机校验，这里只需落状态。若改用 Update，
// 会把调用方手里那份可能已陈旧的配置一起写回去 —— 并发下表现为
// 「刚改的配置被一个 start 调用覆盖成旧值」。
func (r *PostgresRepository) UpdateState(ctx context.Context, id string, state State) error {
	res, err := r.db.ExecContext(ctx,
		`UPDATE agents SET state = $1, updated_at = $2 WHERE id = $3`,
		string(state), time.Now().UTC(), id)
	if err != nil {
		return fmt.Errorf("failed to update agent state: %w", err)
	}
	if n, err := res.RowsAffected(); err == nil && n == 0 {
		return fmt.Errorf("agent %s not found", id)
	}
	return nil
}

// Count 返回 Agent 总数。
func (r *PostgresRepository) Count(ctx context.Context) (int, error) {
	var n int
	if err := r.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM agents`).Scan(&n); err != nil {
		return 0, fmt.Errorf("failed to count agents: %w", err)
	}
	return n, nil
}

// ListByState 按状态过滤 Agent。
//
// 按 (created_at DESC, id DESC) 排序，与 List 同一口径；返回非 nil 切片。
// 这条路径是「找一台空闲的 Agent 派活」，顺序不稳定会让同一批空闲 Agent
// 的选取结果在两次调用间变化（负载分配看起来随机）。
func (r *PostgresRepository) ListByState(ctx context.Context, state State) ([]*Agent, error) {
	rows, err := r.db.QueryContext(ctx,
		`SELECT `+agentColumns+` FROM agents WHERE state = $1
		 ORDER BY created_at DESC, id DESC`, string(state))
	if err != nil {
		return nil, fmt.Errorf("failed to list agents by state: %w", err)
	}
	defer func() { _ = rows.Close() }()

	out := make([]*Agent, 0)
	for rows.Next() {
		a, err := scanAgent(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("failed to read agents: %w", err)
	}
	return out, nil
}

// Ping 探活，供健康检查与端到端断言使用。
func (r *PostgresRepository) Ping(ctx context.Context) error { return r.db.PingContext(ctx) }

// stripAgentSecrets 在写库前清掉 Config.Env 里的敏感值。
//
// 为什么必须做：Config.Env 是「给 Agent 进程注入的环境变量」，
// 里面按约定会有 API key、token 这类凭据。把它整份 JSONB 落库意味着
// 一次数据库泄露会连带泄露所有 Agent 的下游凭据 —— 而审计日志那一轮
// 已经确立过同一原则（日志脱敏由独立函数负责）。
//
// 这里保留 key、清空 value（而不是整个删掉 Env）：key 本身是配置的一部分
// （「这个 Agent 需要哪些环境变量」是可观测信息），值才是机密。
// 空 value 在读出侧表现为「这个 key 需要配值」。
//
// **这是一条有意的语义偏移**，与 memory 实现不完全一致（内存版保留原值，
// 因为它不落盘、进程结束即消失）。契约测试里对涉及 Env 的断言要避开值，
// 只断言 key 集合 —— 见 repository_contract_test.go 的说明。
func stripAgentSecrets(cfg Config) Config {
	if len(cfg.Env) == 0 {
		return cfg
	}
	sanitized := make(map[string]string, len(cfg.Env))
	keys := make([]string, 0, len(cfg.Env))
	for k := range cfg.Env {
		keys = append(keys, k)
	}
	// 排序让写入的 JSON 稳定：map 迭代顺序随机会让同一份配置
	// 每次序列化出不同的字节，diff 与测试断言都变得不可靠。
	sort.Strings(keys)
	for _, k := range keys {
		sanitized[k] = ""
	}
	out := cfg
	out.Env = sanitized
	return out
}

// encodeConfig 把 Config 序列化成 JSONB 载荷。
//
// 一次性快照：marshal 之后再写，中途调用方改 Config 也不会让
// 「写出去的字节」与「写的那一刻的结构体」不一致。
func encodeConfig(cfg Config) ([]byte, error) {
	payload, err := json.Marshal(stripAgentSecrets(cfg))
	if err != nil {
		return nil, fmt.Errorf("failed to encode agent config: %w", err)
	}
	return payload, nil
}

// rowScanner 是 sql.Row 与 sql.Rows 的最小公共接口，让单行/多行共用 scan。
type agentRowScanner interface {
	Scan(dest ...interface{}) error
}

// scanAgent 从一行读取 Agent。变量顺序必须与 agentColumns 逐列对应。
func scanAgent(row agentRowScanner) (*Agent, error) {
	var (
		a          Agent
		state      string
		configJSON []byte
	)
	err := row.Scan(&a.ID, &a.Name, &a.Description, &state, &a.ContainerID,
		&configJSON, &a.CreatedAt, &a.UpdatedAt)
	if err != nil {
		return nil, err
	}
	a.State = State(state)

	// 空载荷兜底：0002 的默认值是 '{}'，但迁移之前建的行或手工插入的行
	// 可能是 NULL/空串。直接 Unmarshal(nil) 会报 unexpected end of JSON，
	// 把一个「配置为空」的正常情况变成读取失败。
	if len(configJSON) > 0 {
		if err := json.Unmarshal(configJSON, &a.Config); err != nil {
			return nil, fmt.Errorf("failed to decode agent %s config: %w", a.ID, err)
		}
	}
	return &a, nil
}
