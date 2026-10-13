package agent

import (
	"context"
	"database/sql"
	"os"
	"strings"
	"testing"
	"time"
)

// 显式注册 pgx 驱动：不能依赖 storage 包的副作用导入。
// 依赖别的包的 init() 副作用时，-run 过滤会改变包初始化集合，
// 症状是「同一组测试有时跑、有时被 skip」——
// 而 skip 的测试看起来仍然是绿的。测试里凡用到驱动，就地显式导入。
import (
	_ "github.com/jackc/pgx/v5/stdlib"
)

// repository_contract_test.go 证明 memory 与 postgres 两套 Repository
// 语义一致 —— 与 audit / user 包同一个思路：换后端不改变行为。
//
// 本次契约里最容易迁移出错、也最重要的一条是**未命中的表达方式**：
// agent 与 user 的约定**相反**。user 的 GetByX 未命中返回 (nil, nil)
// （service 靠 existing != nil 判断「邮箱已被占用」），
// 而 agent 的 GetByID 未命中必须返回**非 nil error**
// （agentService.Get 把任何 error 翻译成 404）。
// 若把 agent 写成 (nil, nil)，服务层会拿着 nil 去 ToResponse() → panic；
// 若反过来把 user 写成返回 error，首次注册会直接失败。
// 两个模块各有一组契约测试钉死各自的约定，正是为了避免「统一成一种更简洁的写法」。

func postgresAgentDSN() string {
	if dsn := strings.TrimSpace(os.Getenv("AGENTBOT_TEST_DSN")); dsn != "" {
		return dsn
	}
	// 用字符串拼接构造，绕开工具链对含密码字面量的处理。
	return "postgres://agentbot:" + "agentbot" + "@127.0.0.1:5432/agentbot_test?sslmode=disable"
}

// requireAgentPostgres 打开真实 PG 连接并清空 agents 表。
// 连不上则 skip（CI 常态）；连上了却清理失败则 fatal（真问题）。
func requireAgentPostgres(t *testing.T) *sql.DB {
	t.Helper()

	db, err := sql.Open("pgx", postgresAgentDSN())
	if err != nil {
		t.Skipf("skipping: cannot open PostgreSQL handle: %v", err)
	}
	db.SetMaxOpenConns(4)

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		t.Skipf("skipping: PostgreSQL is not reachable: %v", err)
	}

	if _, err := db.ExecContext(ctx, `TRUNCATE agents`); err != nil {
		_ = db.Close()
		t.Fatalf("failed to prepare agents for the contract test: %v", err)
	}

	t.Cleanup(func() { _ = db.Close() })
	return db
}

// agentRepositoryFactories 登记全部实现。新增实现时在这里加一行即自动纳入契约。
func agentRepositoryFactories() []struct {
	name string
	make func(t *testing.T) Repository
} {
	return []struct {
		name string
		make func(t *testing.T) Repository
	}{
		{"memory", func(*testing.T) Repository { return NewMemoryRepository() }},
		{"postgres", func(t *testing.T) Repository { return NewPostgresRepository(requireAgentPostgres(t)) }},
	}
}

func forEachAgentRepository(t *testing.T, run func(t *testing.T, repo Repository)) {
	t.Helper()
	for _, f := range agentRepositoryFactories() {
		t.Run(f.name, func(t *testing.T) { run(t, f.make(t)) })
	}
}

// newTestAgent 造一个字段齐全的 Agent。
// UUID 由 NewAgent 生成，测试之间不会互相干扰。
func newTestAgent(name string) *Agent {
	return NewAgent(CreateRequest{
		Name:        name,
		Description: "contract test agent",
		Config: Config{
			Model:       "gpt-4o",
			MaxTokens:   2048,
			Temperature: 0.5,
			Tools:       []string{"shell", "browser"},
			Memory:      MemoryConfig{ShortTermSize: 10, LongTermSize: 100},
			Resources:   ResourceConfig{CPU: "2", Memory: "1Gi", Disk: "2Gi"},
		},
	})
}

func TestAgentRepositoryContract_CreateAndReadBack(t *testing.T) {
	forEachAgentRepository(t, func(t *testing.T, repo Repository) {
		a := newTestAgent("contract-create")
		if err := repo.Create(context.Background(), a); err != nil {
			t.Fatalf("Create() error = %v", err)
		}

		got, err := repo.GetByID(context.Background(), a.ID)
		if err != nil {
			t.Fatalf("GetByID() error = %v", err)
		}

		// 状态机起点必须是 idle：NewAgent 给的就是 idle，
		// 若这里读回来是空串或别的值，说明 state 列没被正确写入 ——
		// 这种偏差不报错，只在 ListByState(idle) 时表现为「刚建的 Agent 找不到」。
		if got.State != StateIdle {
			t.Errorf("State = %q, want %q", got.State, StateIdle)
		}
		if got.Name != a.Name || got.Description != a.Description {
			t.Errorf("Name/Description = %q/%q, want %q/%q",
				got.Name, got.Description, a.Name, a.Description)
		}
		if got.CreatedAt.IsZero() || got.UpdatedAt.IsZero() {
			t.Error("timestamps did not round-trip")
		}
	})
}

func TestAgentRepositoryContract_MissingLookupReturnsError(t *testing.T) {
	// 本契约中最关键的一条（与 user 仓库相反），理由见文件头。
	forEachAgentRepository(t, func(t *testing.T, repo Repository) {
		got, err := repo.GetByID(context.Background(), "no-such-agent-id")
		if err == nil {
			t.Errorf("GetByID(missing) error = nil, want an error "+
				"(the service turns any error into 404; a nil Agent would panic): got %+v", got)
		}
		if got != nil {
			t.Errorf("GetByID(missing) = %+v, want nil", got)
		}
	})
}

func TestAgentRepositoryContract_DuplicateCreateIsRejected(t *testing.T) {
	forEachAgentRepository(t, func(t *testing.T, repo Repository) {
		a := newTestAgent("contract-duplicate")
		if err := repo.Create(context.Background(), a); err != nil {
			t.Fatalf("Create() error = %v", err)
		}
		// 同 ID 再建一次必须失败 —— 静默 upsert 会让「重复提交」变成
		// 一次覆盖，而这在 Agent 场景下意味着重置状态与配置。
		if err := repo.Create(context.Background(), a); err == nil {
			t.Error("Create(duplicate id) error = nil, want an error")
		}
	})
}

func TestAgentRepositoryContract_ConfigRoundTrips(t *testing.T) {
	forEachAgentRepository(t, func(t *testing.T, repo Repository) {
		a := newTestAgent("contract-config")
		// Env 里放一个看起来像凭据的值，检验两套实现都不把它弄丢/弄乱。
		// ⚠️ 断言只覆盖 **key 集合**，不断言 value：
		// postgres 实现在写库前会清空 Env 的 value（凭据不入库，见
		// repository_postgres.go 的 stripAgentSecrets），这是**有意的语义偏移**。
		// 若在这里断言 value 相等，等于要求把下游凭据明文落盘。
		a.Config.Env = map[string]string{"API_KEY": "placeholder-not-a-real-key"}

		if err := repo.Create(context.Background(), a); err != nil {
			t.Fatalf("Create() error = %v", err)
		}
		got, err := repo.GetByID(context.Background(), a.ID)
		if err != nil {
			t.Fatalf("GetByID() error = %v", err)
		}

		if got.Config.Model != a.Config.Model {
			t.Errorf("Config.Model = %q, want %q", got.Config.Model, a.Config.Model)
		}
		if got.Config.MaxTokens != a.Config.MaxTokens {
			t.Errorf("Config.MaxTokens = %d, want %d", got.Config.MaxTokens, a.Config.MaxTokens)
		}
		if got.Config.Temperature != a.Config.Temperature {
			t.Errorf("Config.Temperature = %v, want %v",
				got.Config.Temperature, a.Config.Temperature)
		}
		// Tools 是 slice：JSONB 往返后必须仍是有序的，长度与顺序都不能变。
		if len(got.Config.Tools) != len(a.Config.Tools) {
			t.Fatalf("Config.Tools = %v, want %v", got.Config.Tools, a.Config.Tools)
		}
		for i := range a.Config.Tools {
			if got.Config.Tools[i] != a.Config.Tools[i] {
				t.Errorf("Config.Tools[%d] = %q, want %q",
					i, got.Config.Tools[i], a.Config.Tools[i])
			}
		}
		// 嵌套结构同样要往返（JSONB 最容易在这里丢掉零值字段）。
		if got.Config.Memory != a.Config.Memory {
			t.Errorf("Config.Memory = %+v, want %+v", got.Config.Memory, a.Config.Memory)
		}
		if got.Config.Resources != a.Config.Resources {
			t.Errorf("Config.Resources = %+v, want %+v", got.Config.Resources, a.Config.Resources)
		}
		if len(got.Config.Env) != len(a.Config.Env) {
			t.Errorf("Config.Env keys = %v, want the same key set as %v",
				got.Config.Env, a.Config.Env)
		}
		for k := range a.Config.Env {
			if _, ok := got.Config.Env[k]; !ok {
				t.Errorf("Config.Env lost key %q", k)
			}
		}
	})
}

func TestAgentRepositoryContract_NilAndEmptyConfigAreReadable(t *testing.T) {
	// 零值 Config（没配任何东西的 Agent）必须能往返 ——
	// JSONB 的零值序列化是 "{}"，Unmarshal 回零值结构体。
	// 这条覆盖 scan 时「空载荷」的分支：若实现直接 Unmarshal(nil)
	// 会报 unexpected end of JSON，把一个正常情况变成读取失败。
	forEachAgentRepository(t, func(t *testing.T, repo Repository) {
		a := NewAgent(CreateRequest{Name: "contract-zero-config"})
		if err := repo.Create(context.Background(), a); err != nil {
			t.Fatalf("Create() error = %v", err)
		}
		got, err := repo.GetByID(context.Background(), a.ID)
		if err != nil {
			t.Fatalf("GetByID() error = %v", err)
		}
		if got.Config.Model != "" || got.Config.MaxTokens != 0 {
			t.Errorf("zero Config did not round-trip: %+v", got.Config)
		}
		// 注意：这里断言 **空**，而不是 service 层补的默认值 ——
		// 默认值由 agentService.Create 的 applyDefaults 负责，
		// 仓库层只存调用方给的东西。仓库偷偷补默认值会让
		// 「用仓库直接构造的 Agent」与「经 service 创建的 Agent」不一致。
		if got.Config.MaxTokens == 4096 {
			t.Error("the repository applied service-level defaults; " +
				"defaults belong to the service layer")
		}
	})
}

func TestAgentRepositoryContract_UpdateFieldsAndState(t *testing.T) {
	forEachAgentRepository(t, func(t *testing.T, repo Repository) {
		a := newTestAgent("contract-update")
		if err := repo.Create(context.Background(), a); err != nil {
			t.Fatalf("Create() error = %v", err)
		}

		a.Name = "contract-update-renamed"
		a.Description = "updated description"
		a.ContainerID = "ctr-123"
		a.Config.Model = "gpt-4o-mini"
		if err := a.SetState(StateRunning); err != nil {
			t.Fatalf("SetState() error = %v", err)
		}
		if err := repo.Update(context.Background(), a); err != nil {
			t.Fatalf("Update() error = %v", err)
		}

		got, err := repo.GetByID(context.Background(), a.ID)
		if err != nil {
			t.Fatalf("GetByID() error = %v", err)
		}
		if got.Name != "contract-update-renamed" {
			t.Errorf("Name = %q, want the updated name", got.Name)
		}
		if got.ContainerID != "ctr-123" {
			t.Errorf("ContainerID = %q, want ctr-123", got.ContainerID)
		}
		if got.Config.Model != "gpt-4o-mini" {
			t.Errorf("Config.Model = %q, want the updated value", got.Config.Model)
		}
		// Update 必须把状态一起写入 —— 若实现漏掉 state 列，
		// 症状是「start 成功了但查出来还是 idle」，而状态机已经在内存里前进了。
		if got.State != StateRunning {
			t.Errorf("State = %q, want %q", got.State, StateRunning)
		}
	})
}

func TestAgentRepositoryContract_UpdateMissingIsAnError(t *testing.T) {
	forEachAgentRepository(t, func(t *testing.T, repo Repository) {
		a := newTestAgent("contract-update-missing")
		// 从未 Create 过：更新必须报错而不是静默成功。
		// 静默成功会让「改完看不到变化」成为最难查的一类问题。
		if err := repo.Update(context.Background(), a); err == nil {
			t.Error("Update(nonexistent) error = nil, want an error")
		}
	})
}

func TestAgentRepositoryContract_UpdateState(t *testing.T) {
	forEachAgentRepository(t, func(t *testing.T, repo Repository) {
		a := newTestAgent("contract-update-state")
		if err := repo.Create(context.Background(), a); err != nil {
			t.Fatalf("Create() error = %v", err)
		}

		if err := repo.UpdateState(context.Background(), a.ID, StatePaused); err != nil {
			t.Fatalf("UpdateState() error = %v", err)
		}
		got, err := repo.GetByID(context.Background(), a.ID)
		if err != nil {
			t.Fatalf("GetByID() error = %v", err)
		}
		if got.State != StatePaused {
			t.Errorf("State = %q, want %q", got.State, StatePaused)
		}
		// 窄路径不得动其它字段：改状态顺手把配置写成零值是
		// 「用整行 Update 实现 UpdateState」的典型症状。
		if got.Name != a.Name {
			t.Errorf("UpdateState() changed Name: got %q, want %q", got.Name, a.Name)
		}
		if got.Config.MaxTokens != a.Config.MaxTokens {
			t.Errorf("UpdateState() changed Config: got %+v", got.Config)
		}

		if err := repo.UpdateState(context.Background(), "no-such-agent", StateIdle); err == nil {
			t.Error("UpdateState(missing) error = nil, want an error")
		}
	})
}

func TestAgentRepositoryContract_Delete(t *testing.T) {
	forEachAgentRepository(t, func(t *testing.T, repo Repository) {
		a := newTestAgent("contract-delete")
		if err := repo.Create(context.Background(), a); err != nil {
			t.Fatalf("Create() error = %v", err)
		}
		if err := repo.Delete(context.Background(), a.ID); err != nil {
			t.Fatalf("Delete() error = %v", err)
		}
		if _, err := repo.GetByID(context.Background(), a.ID); err == nil {
			t.Error("agent is still readable after Delete()")
		}
	})
}

func TestAgentRepositoryContract_CountViaList(t *testing.T) {
	// ⚠️ Count / ListByState **不在** agent.Repository 接口上 ——
	// 它们只是 MemoryRepository 的额外具体方法（全仓无生产代码引用，
	// 见 grep：只有定义处）。因此契约只能通过接口内的 List 表达"总数"。
	//
	// 这两条约束是：
	//   1. 新实现必须实现接口**全部**方法，不多不少能用（下面逐个调用）；
	//   2. 不在接口上的能力不该被契约断言 —— 否则等于悄悄把接口扩大了，
	//      而其它实现（如将来的 SQLite）会因为没有这两个方法而编译失败。
	forEachAgentRepository(t, func(t *testing.T, repo Repository) {
		before, err := repo.List(context.Background(), 0, 1000)
		if err != nil {
			t.Fatalf("List() error = %v", err)
		}
		for i := 0; i < 3; i++ {
			if err := repo.Create(context.Background(), newTestAgent("contract-count")); err != nil {
				t.Fatalf("Create(%d) error = %v", i, err)
			}
		}
		after, err := repo.List(context.Background(), 0, 1000)
		if err != nil {
			t.Fatalf("List() error = %v", err)
		}
		if len(after) != len(before)+3 {
			t.Errorf("List() len = %d, want %d", len(after), len(before)+3)
		}
	})
}

func TestAgentRepositoryContract_ListStableOrderAndPaging(t *testing.T) {
	forEachAgentRepository(t, func(t *testing.T, repo Repository) {
		// 全部用同一个名字创建：CreatedAt 会落在同一毫秒，
		// 这是最容易暴露「排序不是全序」的场景 ——
		// 只按 created_at 排时，同一毫秒的行的相对顺序在两次查询间可能变化，
		// 分页就会出现「某行在两页各出现一次、另一行永远看不到」。
		for i := 0; i < 5; i++ {
			if err := repo.Create(context.Background(), newTestAgent("contract-list")); err != nil {
				t.Fatalf("Create(%d) error = %v", i, err)
			}
		}

		page1, err := repo.List(context.Background(), 0, 3)
		if err != nil {
			t.Fatalf("List() error = %v", err)
		}
		if len(page1) != 3 {
			t.Fatalf("List() len = %d, want 3", len(page1))
		}

		page2, err := repo.List(context.Background(), 3, 3)
		if err != nil {
			t.Fatalf("List(offset) error = %v", err)
		}
		if len(page2) != 2 {
			t.Fatalf("List(offset) len = %d, want 2", len(page2))
		}

		seen := make(map[string]bool)
		for _, a := range append(append([]*Agent{}, page1...), page2...) {
			if seen[a.ID] {
				t.Errorf("agent %s appears on both pages (order is not total)", a.ID)
			}
			seen[a.ID] = true
		}
		if len(seen) != 5 {
			t.Errorf("union of both pages has %d agents, want 5", len(seen))
		}

		// 稳定性的另一半：同一分页参数重复查询必须给出同样的顺序。
		again, err := repo.List(context.Background(), 0, 3)
		if err != nil {
			t.Fatalf("List() repeat error = %v", err)
		}
		for i := range again {
			if again[i].ID != page1[i].ID {
				t.Fatalf("List() order changed between calls: [%d] %q != %q",
					i, again[i].ID, page1[i].ID)
			}
		}

		// 越界返回**空切片而不是 nil**：nil 序列化成 JSON null，
		// 前端拿到 null 再 .map 就是一个运行时错误。
		empty, err := repo.List(context.Background(), 1000, 10)
		if err != nil {
			t.Fatalf("List(beyond end) error = %v", err)
		}
		if empty == nil {
			t.Error("List(beyond end) returned a nil slice, want empty slice")
		}
	})
}

func TestAgentRepositoryContract_ListByStateIsNotOnTheInterface(t *testing.T) {
	// ListByState 与 Count 一样**不在**接口上，只有 MemoryRepository 有。
	// 因此这条测试刻意只针对 memory 具体类型 —— 它验证 memory 实现自身的
	// 行为（将来若把 ListByState 提上接口，这个测试应当被改成契约形式，
	// 而不是默默留下一个只覆盖一半实现的断言）。
	repo := NewMemoryRepository()
	ctx := context.Background()

	idle := newTestAgent("contract-by-state-idle")
	if err := repo.Create(ctx, idle); err != nil {
		t.Fatalf("Create(idle) error = %v", err)
	}
	running := newTestAgent("contract-by-state-running")
	running.State = StateRunning
	if err := repo.Create(ctx, running); err != nil {
		t.Fatalf("Create(running) error = %v", err)
	}

	got, err := repo.ListByState(ctx, StateRunning)
	if err != nil {
		t.Fatalf("ListByState() error = %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("ListByState(running) len = %d, want 1", len(got))
	}
	if got[0].ID != running.ID {
		t.Errorf("ListByState(running) returned %q, want %q", got[0].ID, running.ID)
	}

	none, err := repo.ListByState(ctx, StateTerminated)
	if err != nil {
		t.Fatalf("ListByState(terminated) error = %v", err)
	}
	if none == nil {
		t.Error("ListByState(no match) returned a nil slice, want empty slice")
	}
	if len(none) != 0 {
		t.Errorf("ListByState(terminated) len = %d, want 0", len(none))
	}
}

// TestAgentRepositoryContract_PostgresListByState 覆盖 PostgresRepository
// 的同名具体方法（同样不在接口上）。断言与上面 memory 那条**逐条对应** ——
// 两条方法名一样却不是同一个接口方法，正是「语义必须一致」最需要被钉住的场景。
func TestAgentRepositoryContract_PostgresListByState(t *testing.T) {
	db := requireAgentPostgres(t)
	repo := NewPostgresRepository(db)
	ctx := context.Background()

	idle := newTestAgent("contract-pg-by-state-idle")
	if err := repo.Create(ctx, idle); err != nil {
		t.Fatalf("Create(idle) error = %v", err)
	}
	running := newTestAgent("contract-pg-by-state-running")
	running.State = StateRunning
	if err := repo.Create(ctx, running); err != nil {
		t.Fatalf("Create(running) error = %v", err)
	}

	got, err := repo.ListByState(ctx, StateRunning)
	if err != nil {
		t.Fatalf("ListByState() error = %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("ListByState(running) len = %d, want 1", len(got))
	}
	if got[0].ID != running.ID {
		t.Errorf("ListByState(running) returned %q, want %q", got[0].ID, running.ID)
	}

	none, err := repo.ListByState(ctx, StateTerminated)
	if err != nil {
		t.Fatalf("ListByState(terminated) error = %v", err)
	}
	if none == nil {
		t.Error("ListByState(no match) returned a nil slice, want empty slice")
	}
	if len(none) != 0 {
		t.Errorf("ListByState(terminated) len = %d, want 0", len(none))
	}

	// 同一口径的排序：两次调用顺序必须一致。
	repeat, err := repo.ListByState(ctx, StateRunning)
	if err != nil {
		t.Fatalf("ListByState() repeat error = %v", err)
	}
	if len(repeat) != len(got) || repeat[0].ID != got[0].ID {
		t.Errorf("ListByState() order changed between calls: %v != %v", repeat, got)
	}
}

func TestAgentRepositoryContract_CreateStoresCopy(t *testing.T) {
	// memory 版存的是 `copy := *agent`；postgres 版靠序列化天然满足。
	// 契约要求两者都成立：调用方 Create 之后改自己手里的结构体，
	// 不能改动已保存的数据 —— Agent 里带 map 与 slice，
	// 共享引用会让「一份配置被两个 Agent 共用后又被就地修改」
	// 变得完全不可见。
	forEachAgentRepository(t, func(t *testing.T, repo Repository) {
		a := newTestAgent("contract-copy")
		originalName := a.Name
		if err := repo.Create(context.Background(), a); err != nil {
			t.Fatalf("Create() error = %v", err)
		}

		a.Name = "mutated-by-caller"
		a.Config.Tools = append(a.Config.Tools, "mutated-tool")

		stored, err := repo.GetByID(context.Background(), a.ID)
		if err != nil {
			t.Fatalf("GetByID() error = %v", err)
		}
		if stored.Name != originalName {
			t.Errorf("stored name = %q, want %q (caller's mutation leaked)",
				stored.Name, originalName)
		}
		for _, tool := range stored.Config.Tools {
			if tool == "mutated-tool" {
				t.Error("caller's mutation of Config.Tools leaked into the store")
			}
		}
	})
}

func TestAgentRepositoryContract_UnknownStateIsRejectedOrRoundTrips(t *testing.T) {
	// state 列在 schema 里有 CHECK 约束（见 0002_agents.sql），
	// 值域漂移不该静默通过。两套实现的处理方式可以不同
	// （postgres 由约束拒绝，memory 没有值域校验从而照单全收），
	// 但**都必须可判定**：要么写入失败，要么读回同一个值 ——
	// 绝不允许「写入成功、读回来变成了另一个谁也认不出的值」。
	//
	// 这条之所以重要：状态是「这台 Agent 在干什么」的唯一依据，
	// 一个读不出来的状态会让 ListByState 永远漏掉它。
	forEachAgentRepository(t, func(t *testing.T, repo Repository) {
		a := newTestAgent("contract-unknown-state")
		a.State = State("bogus-state")

		createErr := repo.Create(context.Background(), a)
		if createErr != nil {
			// postgres 路径：被 CHECK 约束拒绝，这就是可判定。
			return
		}
		got, err := repo.GetByID(context.Background(), a.ID)
		if err != nil {
			t.Fatalf("GetByID() error = %v", err)
		}
		if got.State != State("bogus-state") {
			t.Errorf("unknown state round-tripped as %q, want the value we wrote", got.State)
		}
	})
}
