package audit

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	// 显式注册 pgx 驱动。不能依赖 storage 包的副作用导入：
	// 依赖别的包的 init() 副作用，会随 -run 过滤、构建顺序变化而时灵时不灵，
	// 症状正是本文件最初踩到的「同一组测试有时跑、有时被 skip」——
	// 而 skip 的测试看起来仍然是绿的。测试里凡用到驱动，就地显式导入。
	_ "github.com/jackc/pgx/v5/stdlib"
)

// repository_contract_test.go 是「两套 Repository 实现语义一致」的证明。
//
// 为什么必须是**同一组断言跑两套实现**（而不是各写各的测试）：
// 本次改造的全部意义是「换后端不改变行为」。若 memory 与 postgres 各有
// 一套自己的测试，两边都绿也可能语义不同（例如一边 List 忽略 Limit、
// 另一边遵守），而装配层按配置在两者之间切换 —— 这种偏差只会在生产上、
// 换配置的那一刻暴露。契约式测试把这个风险提前到本地。
//
// 允许用 AGENTBOT_TEST_DSN 覆盖连接串；CI（无 PG）会走 memory 一套，
// postgres 一套因连不上而跳过 —— 跳过而不是失败，理由见 storage 包同名注释。

// postgresAuditDSN 返回测试用连接串。
func postgresAuditDSN() string {
	if dsn := strings.TrimSpace(os.Getenv("AGENTBOT_TEST_DSN")); dsn != "" {
		return dsn
	}
	return "postgres://agentbot:agentbot@127.0.0.1:5432/agentbot_test?sslmode=disable"
}

// requireAuditPostgres 打开真实 PG 连接并把 audit_events 清空，
// 让每轮契约测试从确定的空状态开始。
//
// 不复用 storage 包的 requirePostgres：那是 storage 的测试辅助（未导出），
// 且 audit 需要在连接后额外做表清理。连不上则 t.Skip（CI 常态），
// 连上了却清理失败则 t.Fatalf —— 那是真问题，不能被「环境缺失」掩盖。
func requireAuditPostgres(t *testing.T) *sql.DB {
	t.Helper()

	db, err := sql.Open("pgx", postgresAuditDSN())
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

	// 契约断言里含 COUNT(*) 与 Distinct 的全表语义，残留数据会让断言
	// 依赖于上一次运行的结果 —— 必须清空。用 TRUNCATE 而不是 DELETE：
	// 顺带重置 BIGSERIAL，否则 seq 跨轮次持续增长，排序断言写不出确定值。
	if _, err := db.ExecContext(ctx, `TRUNCATE audit_events RESTART IDENTITY`); err != nil {
		_ = db.Close()
		t.Fatalf("failed to prepare audit_events for the contract test: %v", err)
	}

	t.Cleanup(func() { _ = db.Close() })
	return db
}

// repositoryFactory 构造一个待测仓库。契约测试对两种实现用同一份流程。
type repositoryFactory struct {
	name string
	make func(t *testing.T) Repository
}

// repositoryFactories 返回全部实现。新增实现时在这里登记即可自动纳入契约。
func repositoryFactories() []repositoryFactory {
	return []repositoryFactory{
		{
			name: "memory",
			make: func(*testing.T) Repository { return NewMemoryRepository() },
		},
		{
			name: "postgres",
			make: func(t *testing.T) Repository {
				return NewPostgresRepository(requireAuditPostgres(t))
			},
		},
	}
}

// forEachRepository 对每个实现跑同一段断言。
func forEachRepository(t *testing.T, run func(t *testing.T, repo Repository)) {
	t.Helper()
	for _, f := range repositoryFactories() {
		t.Run(f.name, func(t *testing.T) {
			run(t, f.make(t))
		})
	}
}

// baseEvent 造一条填充了全部字段的事件，避免测试之间只有 ID 不同。
func baseEvent(action string) Event {
	return Event{
		Actor:      "user-1",
		ActorType:  ActorTypeUser,
		Action:     action,
		Resource:   ResourceUser,
		ResourceID: "user-1",
		Status:     StatusSuccess,
		IPAddress:  "10.0.0.1",
		UserAgent:  "contract-test/1.0",
		Details:    map[string]interface{}{"note": "hello"},
	}
}

func TestRepositoryContract_AppendFillsDefaults(t *testing.T) {
	forEachRepository(t, func(t *testing.T, repo Repository) {
		ctx := context.Background()

		// 只给必填字段（action），其余交给实现补默认值。
		rec, err := repo.Append(ctx, Event{Action: "user.login"})
		if err != nil {
			t.Fatalf("Append() error = %v", err)
		}
		if rec == nil {
			t.Fatal("Append() returned nil record without error")
		}
		if rec.ID == "" {
			t.Error("Append() did not assign an ID")
		}
		if rec.Seq == 0 {
			t.Error("Append() did not assign a Seq")
		}
		if rec.Timestamp.IsZero() {
			t.Error("Append() did not assign a timestamp")
		}
		if rec.Status != StatusSuccess {
			t.Errorf("Status = %q, want %q", rec.Status, StatusSuccess)
		}
		if rec.ActorType != ActorTypeUser {
			t.Errorf("ActorType = %q, want %q", rec.ActorType, ActorTypeUser)
		}
	})
}

func TestRepositoryContract_AppendRequiresAction(t *testing.T) {
	// 两套实现都要拒绝空 action，并且是**同一个**哨兵错误 ——
	// 调用方据此映射 400，不能按后端分支处理。
	forEachRepository(t, func(t *testing.T, repo Repository) {
		rec, err := repo.Append(context.Background(), Event{Action: ""})
		if !errors.Is(err, ErrActionRequired) {
			t.Fatalf("Append() error = %v, want ErrActionRequired", err)
		}
		if rec != nil {
			t.Fatalf("Append() returned non-nil record on error: %+v", rec)
		}
	})
}

func TestRepositoryContract_AppendRedactsSecrets(t *testing.T) {
	// fail-closed 的写入边界：密钥必须在**写入前**被替换成占位符。
	// 这条是契约里最重要的一条 —— 一旦某个实现漏了脱敏，
	// 「换后端」就从行为等价变成了数据泄漏。
	forEachRepository(t, func(t *testing.T, repo Repository) {
		ctx := context.Background()
		event := baseEvent("user.login")
		event.Details = map[string]interface{}{
			"api_key": "sk-live-abcdefghijklmnop",
			"nested": map[string]interface{}{
				"authorization": "Bearer eyJhbGciOiJIUzI1NiJ9.payload.sig",
				"safe":          "keep-me",
			},
		}

		rec, err := repo.Append(ctx, event)
		if err != nil {
			t.Fatalf("Append() error = %v", err)
		}

		if got := rec.Details["api_key"]; got != RedactedPlaceholder {
			t.Errorf("api_key = %v, want %q", got, RedactedPlaceholder)
		}
		nested, ok := rec.Details["nested"].(map[string]interface{})
		if !ok {
			t.Fatalf("nested details lost its map shape: %T", rec.Details["nested"])
		}
		if got := nested["authorization"]; got != RedactedPlaceholder {
			t.Errorf("nested.authorization = %v, want %q", got, RedactedPlaceholder)
		}
		// 反向断言：非敏感字段不能被顺手改掉，否则脱敏就成了「清空明细」。
		if got := nested["safe"]; got != "keep-me" {
			t.Errorf("nested.safe = %v, want %q", got, "keep-me")
		}
		// 被脱敏的键路径要登记，便于事后排查「这里原本有个凭证」。
		if _, ok := rec.Details[RedactedKeysField]; !ok {
			t.Errorf("details missing %q marker after redaction", RedactedKeysField)
		}

		// 关键：从仓库**读回来**的明文值也必须是脱敏后的。
		// 只断言返回值可能只是「返回时改了一下」，库里仍是明文。
		stored, err := repo.Get(ctx, rec.ID)
		if err != nil {
			t.Fatalf("Get() error = %v", err)
		}
		if got := stored.Details["api_key"]; got != RedactedPlaceholder {
			t.Errorf("stored api_key = %v, want %q (secret leaked into storage)",
				got, RedactedPlaceholder)
		}
	})
}

func TestRepositoryContract_ListOrdersByTimeDescending(t *testing.T) {
	forEachRepository(t, func(t *testing.T, repo Repository) {
		ctx := context.Background()
		base := time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC)

		for i := 0; i < 3; i++ {
			ev := baseEvent("user.login")
			ev.Timestamp = base.Add(time.Duration(i) * time.Minute)
			ev.Details = map[string]interface{}{"i": i}
			if _, err := repo.Append(ctx, ev); err != nil {
				t.Fatalf("Append(%d) error = %v", i, err)
			}
		}

		got, err := repo.List(ctx, Filter{})
		if err != nil {
			t.Fatalf("List() error = %v", err)
		}
		if len(got) != 3 {
			t.Fatalf("List() len = %d, want 3", len(got))
		}
		// 默认顺序是时间倒序（最新的在前）。
		for i := 1; i < len(got); i++ {
			if got[i].Timestamp.After(got[i-1].Timestamp) {
				t.Errorf("List() not sorted descending: [%d]=%v > [%d]=%v",
					i, got[i].Timestamp, i-1, got[i-1].Timestamp)
			}
		}
		if v := numeric(t, got[0].Details["i"]); v != 2 {
			t.Errorf("newest event is not first: got i=%d, want 2", v)
		}
	})
}

func TestRepositoryContract_ListAscendingAndPaging(t *testing.T) {
	forEachRepository(t, func(t *testing.T, repo Repository) {
		ctx := context.Background()
		base := time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC)

		for i := 0; i < 5; i++ {
			ev := baseEvent("user.login")
			ev.Timestamp = base.Add(time.Duration(i) * time.Minute)
			ev.Details = map[string]interface{}{"i": i}
			if _, err := repo.Append(ctx, ev); err != nil {
				t.Fatalf("Append(%d) error = %v", i, err)
			}
		}

		got, err := repo.List(ctx, Filter{SortOrder: SortAsc, Limit: 2, Offset: 1})
		if err != nil {
			t.Fatalf("List() error = %v", err)
		}
		if len(got) != 2 {
			t.Fatalf("List() len = %d, want 2", len(got))
		}
		if v := numeric(t, got[0].Details["i"]); v != 1 {
			t.Errorf("first page item i = %d, want 1 (offset applied)", v)
		}
		if v := numeric(t, got[1].Details["i"]); v != 2 {
			t.Errorf("second page item i = %d, want 2", v)
		}
	})
}

func TestRepositoryContract_ListFilters(t *testing.T) {
	forEachRepository(t, func(t *testing.T, repo Repository) {
		ctx := context.Background()
		base := time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC)

		first := baseEvent("agent.created")
		first.Actor = "alice"
		first.Timestamp = base

		second := baseEvent("agent.created")
		second.Actor = "bob"
		second.Status = StatusFailure
		second.Timestamp = base.Add(time.Minute)

		third := baseEvent("agent.deleted")
		third.Actor = "alice"
		third.ResourceID = "agent-9"
		third.Timestamp = base.Add(2 * time.Minute)

		for _, ev := range []Event{first, second, third} {
			if _, err := repo.Append(ctx, ev); err != nil {
				t.Fatalf("Append() error = %v", err)
			}
		}

		cases := []struct {
			name   string
			filter Filter
			want   int
		}{
			{"by actor", Filter{Actor: "alice"}, 2},
			{"by action", Filter{EventType: "agent.created"}, 2},
			{"by status", Filter{Status: StatusFailure}, 1},
			{"by resource id", Filter{ResourceID: "agent-9"}, 1},
			{"by ip", Filter{IPAddress: "10.0.0.1"}, 3},
			{"by unknown actor", Filter{Actor: "nobody"}, 0},
			{"by time window", Filter{StartTime: ptrTime(base.Add(90 * time.Second))}, 1},
		}
		for _, tc := range cases {
			got, err := repo.List(ctx, tc.filter)
			if err != nil {
				t.Fatalf("%s: List() error = %v", tc.name, err)
			}
			if len(got) != tc.want {
				t.Errorf("%s: List() len = %d, want %d", tc.name, len(got), tc.want)
			}
			// 全局不变量：合法请求下永远返回非 nil 切片
			// （nil 会 JSON 序列化成 null，破坏接口契约）。
			if got == nil {
				t.Errorf("%s: List() returned nil slice", tc.name)
			}
		}
	})
}

func TestRepositoryContract_CountIgnoresPaging(t *testing.T) {
	forEachRepository(t, func(t *testing.T, repo Repository) {
		ctx := context.Background()
		for i := 0; i < 4; i++ {
			if _, err := repo.Append(ctx, baseEvent("user.login")); err != nil {
				t.Fatalf("Append() error = %v", err)
			}
		}

		// Count 必须统计全量，不因 Limit/Offset 而变 —— 否则分页组件的
		// 总页数会等于每页条数，翻页在前端直接错乱。
		n, err := repo.Count(ctx, Filter{Limit: 1, Offset: 3})
		if err != nil {
			t.Fatalf("Count() error = %v", err)
		}
		if n != 4 {
			t.Errorf("Count() = %d, want 4 (paging must not affect count)", n)
		}
	})
}

func TestRepositoryContract_GetMissingEvent(t *testing.T) {
	forEachRepository(t, func(t *testing.T, repo Repository) {
		ctx := context.Background()
		if _, err := repo.Get(ctx, "does-not-exist"); !errors.Is(err, ErrEventNotFound) {
			t.Errorf("Get(unknown) error = %v, want ErrEventNotFound", err)
		}
		if _, err := repo.Get(ctx, ""); !errors.Is(err, ErrIDRequired) {
			t.Errorf("Get(\"\") error = %v, want ErrIDRequired", err)
		}
	})
}

func TestRepositoryContract_Distinct(t *testing.T) {
	forEachRepository(t, func(t *testing.T, repo Repository) {
		ctx := context.Background()

		a := baseEvent("agent.created")
		a.Actor = "bob"
		a.IPAddress = "10.0.0.2"
		b := baseEvent("user.login")
		b.Actor = "alice"
		b.IPAddress = "10.0.0.1"
		// 第三条起空值覆盖：空值不应出现在结果里。
		c := baseEvent("agent.created")
		c.Actor = ""
		c.IPAddress = ""

		for _, ev := range []Event{a, b, c} {
			if _, err := repo.Append(ctx, ev); err != nil {
				t.Fatalf("Append() error = %v", err)
			}
		}

		actors, err := repo.Distinct(ctx, DistinctActor)
		if err != nil {
			t.Fatalf("Distinct(actor) error = %v", err)
		}
		// 排序保证接口输出稳定：alice 在前。
		if len(actors) != 2 || actors[0] != "alice" || actors[1] != "bob" {
			t.Errorf("Distinct(actor) = %v, want [alice bob]", actors)
		}

		actions, err := repo.Distinct(ctx, DistinctAction)
		if err != nil {
			t.Fatalf("Distinct(action) error = %v", err)
		}
		if len(actions) != 2 || actions[0] != "agent.created" || actions[1] != "user.login" {
			t.Errorf("Distinct(action) = %v, want [agent.created user.login]", actions)
		}

		if _, err := repo.Distinct(ctx, "not_a_field"); !errors.Is(err, ErrUnknownDistinctField) {
			t.Errorf("Distinct(unknown) error = %v, want ErrUnknownDistinctField", err)
		}
	})
}

func TestRepositoryContract_PurgeReturnsDeletedRows(t *testing.T) {
	forEachRepository(t, func(t *testing.T, repo Repository) {
		ctx := context.Background()
		base := time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC)

		for i := 0; i < 3; i++ {
			old := baseEvent("user.login")
			old.Timestamp = base.Add(time.Duration(i) * time.Minute)
			if _, err := repo.Append(ctx, old); err != nil {
				t.Fatalf("Append(old) error = %v", err)
			}
		}
		fresh := baseEvent("user.login")
		fresh.Timestamp = base.Add(24 * time.Hour)
		if _, err := repo.Append(ctx, fresh); err != nil {
			t.Fatalf("Append(fresh) error = %v", err)
		}

		cutoff := base.Add(time.Hour)
		n, err := repo.Purge(ctx, cutoff)
		if err != nil {
			t.Fatalf("Purge() error = %v", err)
		}
		if n != 3 {
			t.Errorf("Purge() = %d, want 3 (real deleted row count)", n)
		}

		remaining, err := repo.List(ctx, Filter{})
		if err != nil {
			t.Fatalf("List() after purge error = %v", err)
		}
		if len(remaining) != 1 {
			t.Fatalf("after purge len = %d, want 1", len(remaining))
		}

		if _, err := repo.Purge(ctx, time.Time{}); !errors.Is(err, ErrCutoffRequired) {
			t.Errorf("Purge(zero) error = %v, want ErrCutoffRequired", err)
		}
	})
}

func TestRepositoryContract_AppendStoresCopy(t *testing.T) {
	// 存进仓库的必须是副本：调用方 Append 之后修改自己手里的 map，
	// 不能改动已保存的事件（这在 memory 版是显式深拷贝，
	// postgres 版靠序列化天然满足 —— 但契约要求两者都成立）。
	forEachRepository(t, func(t *testing.T, repo Repository) {
		ctx := context.Background()
		details := map[string]interface{}{"count": 1}
		ev := baseEvent("user.login")
		ev.Details = details

		rec, err := repo.Append(ctx, ev)
		if err != nil {
			t.Fatalf("Append() error = %v", err)
		}

		details["count"] = 999
		details["injected"] = "after-append"

		stored, err := repo.Get(ctx, rec.ID)
		if err != nil {
			t.Fatalf("Get() error = %v", err)
		}
		if _, ok := stored.Details["injected"]; ok {
			t.Error("caller's later mutation leaked into stored event")
		}
		if v := numeric(t, stored.Details["count"]); v != 1 {
			t.Errorf("stored count = %d, want 1 (mutated after Append)", v)
		}
	})
}

// ptrTime 取时间指针，用于构造 Filter 的可选时间窗。
func ptrTime(t time.Time) *time.Time { return &t }

// numeric 把明细里的数值归一成 int64，消除两套实现的表示差异。
//
// Postgres 版把明细写进 JSONB，读回来时数字必然是 float64（JSON 只有
// 一种数字类型）；memory 版存的是调用方给的原始 int。契约测试若直接
// 断言 int，就会变成「测试在验证后端的编码方式」而不是「验证语义」。
// 正确的契约是：**数值语义相同**，表示形式的差异不属于行为差异。
func numeric(t *testing.T, v interface{}) int64 {
	t.Helper()
	switch n := v.(type) {
	case int:
		return int64(n)
	case int64:
		return n
	case float64:
		return int64(n)
	case json.Number:
		i, err := n.Int64()
		if err != nil {
			t.Fatalf("json.Number %v is not an integer: %v", n, err)
		}
		return i
	default:
		t.Fatalf("detail value %v has unexpected type %T", v, v)
		return 0
	}
}
