package user

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	// 显式注册 pgx 驱动：不依赖别的包的 init() 副作用，
	// 否则 -run 过滤会改变包初始化集合，表现为「有时跑、有时 skip」。
	_ "github.com/jackc/pgx/v5/stdlib"
)

// repository_contract_test.go 证明 memory 与 postgres 两套 Repository
// 语义一致 —— 与 audit 包的契约测试同一个思路：换后端不改变行为。
//
// 用户仓库的契约里有一条特殊且最容易迁移出错的约定：
// **GetByX 未命中返回 (nil, nil) 而不是错误**。service.Create 靠
// `existing != nil` 判断「邮箱已被占用」，若实现返回 error，
// 首次注册会被报成 "failed to check email" —— 正常路径全挂，
// 而错误信息看起来像数据库故障。

// postgresUserDSN 返回测试用连接串，允许 CI 用 AGENTBOT_TEST_DSN 覆盖。
func postgresUserDSN() string {
	if dsn := strings.TrimSpace(os.Getenv("AGENTBOT_TEST_DSN")); dsn != "" {
		return dsn
	}
	return "postgres://agentbot:" + "agentbot" + "@127.0.0.1:5432/agentbot_test?sslmode=disable"
}

// requireUserPostgres 打开真实 PG 连接并清空 users 表。
// 连不上则 skip（CI 常态）；连上了却清理失败则 fatal（真问题）。
func requireUserPostgres(t *testing.T) *sql.DB {
	t.Helper()

	db, err := sql.Open("pgx", postgresUserDSN())
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

	if _, err := db.ExecContext(ctx, `TRUNCATE users`); err != nil {
		_ = db.Close()
		t.Fatalf("failed to prepare users for the contract test: %v", err)
	}

	t.Cleanup(func() { _ = db.Close() })
	return db
}

// userRepositoryFactories 登记全部实现。新增实现时在这里加一行即自动纳入契约。
func userRepositoryFactories() []struct {
	name string
	make func(t *testing.T) Repository
} {
	return []struct {
		name string
		make func(t *testing.T) Repository
	}{
		{"memory", func(*testing.T) Repository { return NewMemoryRepository() }},
		{"postgres", func(t *testing.T) Repository { return NewPostgresRepository(requireUserPostgres(t)) }},
	}
}

func forEachUserRepository(t *testing.T, run func(t *testing.T, repo Repository)) {
	t.Helper()
	for _, f := range userRepositoryFactories() {
		t.Run(f.name, func(t *testing.T) { run(t, f.make(t)) })
	}
}

// newTestUser 造一个字段齐全的用户。
// 用 UUID 派生的 id/email/username，避免测试之间互相干扰。
func newTestUser() *User {
	id := uuid.NewString()
	return &User{
		ID:           id,
		Email:        "u-" + id + "@example.com",
		Username:     "user-" + id,
		PasswordHash: "$2a$12$contracttesthashplaceholder",
		Avatar:       "https://example.com/a.png",
	}
}

func TestUserRepositoryContract_CreateAndReadBack(t *testing.T) {
	forEachUserRepository(t, func(t *testing.T, repo Repository) {
		u := newTestUser()
		if err := repo.Create(u); err != nil {
			t.Fatalf("Create() error = %v", err)
		}

		// 默认值必须被补上：role/status 为空时 Create 要填默认，
		// 否则登录鉴权会因为 role 为空而拒绝所有人。
		if u.Role != RoleUser {
			t.Errorf("Role = %q, want %q", u.Role, RoleUser)
		}
		if u.Status != StatusActive {
			t.Errorf("Status = %q, want %q", u.Status, StatusActive)
		}
		if u.CreatedAt.IsZero() || u.UpdatedAt.IsZero() {
			t.Error("Create() did not assign timestamps")
		}

		for _, tc := range []struct {
			name string
			got  func() (*User, error)
		}{
			{"GetByID", func() (*User, error) { return repo.GetByID(u.ID) }},
			{"GetByEmail", func() (*User, error) { return repo.GetByEmail(u.Email) }},
			{"GetByUsername", func() (*User, error) { return repo.GetByUsername(u.Username) }},
		} {
			got, err := tc.got()
			if err != nil {
				t.Fatalf("%s() error = %v", tc.name, err)
			}
			if got == nil {
				t.Fatalf("%s() = nil, want the user we just created", tc.name)
			}
			if got.ID != u.ID {
				t.Errorf("%s() ID = %q, want %q", tc.name, got.ID, u.ID)
			}
			if got.PasswordHash != u.PasswordHash {
				t.Errorf("%s() did not round-trip PasswordHash", tc.name)
			}
		}
	})
}

func TestUserRepositoryContract_MissingLookupReturnsNilNotError(t *testing.T) {
	// 本契约中最关键的一条，理由见文件头。
	forEachUserRepository(t, func(t *testing.T, repo Repository) {
		for _, tc := range []struct {
			name string
			got  func() (*User, error)
		}{
			{"GetByID", func() (*User, error) { return repo.GetByID("no-such-id") }},
			{"GetByEmail", func() (*User, error) { return repo.GetByEmail("nobody@example.com") }},
			{"GetByUsername", func() (*User, error) { return repo.GetByUsername("nobody") }},
		} {
			got, err := tc.got()
			if err != nil {
				t.Errorf("%s(missing) error = %v, want nil (service relies on a nil result)", tc.name, err)
			}
			if got != nil {
				t.Errorf("%s(missing) = %+v, want nil", tc.name, got)
			}
		}
	})
}

func TestUserRepositoryContract_DuplicateEmailIsDetectable(t *testing.T) {
	// 并发注册下「先查后插」之间有窗口，唯一索引是最终防线。
	// 冲突必须翻译成可判定的哨兵错误，否则调用方只能回 500。
	forEachUserRepository(t, func(t *testing.T, repo Repository) {
		first := newTestUser()
		if err := repo.Create(first); err != nil {
			t.Fatalf("Create(first) error = %v", err)
		}

		dup := newTestUser()
		dup.Email = first.Email
		err := repo.Create(dup)
		if !errors.Is(err, ErrEmailExists) {
			t.Errorf("Create(duplicate email) error = %v, want ErrEmailExists", err)
		}

		dupName := newTestUser()
		dupName.Username = first.Username
		err = repo.Create(dupName)
		if !errors.Is(err, ErrUsernameExists) {
			t.Errorf("Create(duplicate username) error = %v, want ErrUsernameExists", err)
		}
	})
}

func TestUserRepositoryContract_UnboundOAuthUsersCoexist(t *testing.T) {
	// 基线迁移为 OAuth 建的是**部分索引**（只约束 provider/id 都非空的行），
	// 因此多个未绑定 OAuth 的账号（两列空串）必须能共存。
	// 这条最容易在「迁移到带唯一索引的库」时被破坏 ——
	// 若实现按普通唯一索引理解，第二个未绑定用户就会插入失败，
	// 表现为「注册第二个人报用户名已存在」这种莫名其妙的错误。
	forEachUserRepository(t, func(t *testing.T, repo Repository) {
		for i := 0; i < 3; i++ {
			u := newTestUser()
			u.OAuthProvider = ""
			u.OAuthID = ""
			if err := repo.Create(u); err != nil {
				t.Fatalf("Create(unbound oauth user %d) error = %v", i, err)
			}
		}

		// 空串查询必须返回 nil 而不是随机命中其中一个（那会把用户登成别人）。
		got, err := repo.GetByOAuth("", "")
		if err != nil {
			t.Fatalf("GetByOAuth(\"\", \"\") error = %v", err)
		}
		if got != nil {
			t.Errorf("GetByOAuth(\"\", \"\") = %q, want nil", got.ID)
		}
	})
}

func TestUserRepositoryContract_GetByOAuth(t *testing.T) {
	forEachUserRepository(t, func(t *testing.T, repo Repository) {
		bound := newTestUser()
		bound.OAuthProvider = "github"
		bound.OAuthID = "gh-" + bound.ID
		if err := repo.Create(bound); err != nil {
			t.Fatalf("Create() error = %v", err)
		}

		got, err := repo.GetByOAuth("github", bound.OAuthID)
		if err != nil {
			t.Fatalf("GetByOAuth() error = %v", err)
		}
		if got == nil || got.ID != bound.ID {
			t.Fatalf("GetByOAuth() = %+v, want user %q", got, bound.ID)
		}

		missing, err := repo.GetByOAuth("github", "gh-nobody")
		if err != nil {
			t.Fatalf("GetByOAuth(missing) error = %v", err)
		}
		if missing != nil {
			t.Errorf("GetByOAuth(missing) = %+v, want nil", missing)
		}
	})
}

func TestUserRepositoryContract_UpdateFields(t *testing.T) {
	forEachUserRepository(t, func(t *testing.T, repo Repository) {
		u := newTestUser()
		if err := repo.Create(u); err != nil {
			t.Fatalf("Create() error = %v", err)
		}

		u.Avatar = "https://example.com/new.png"
		u.Status = StatusInactive
		u.Role = RoleViewer
		if err := repo.Update(u); err != nil {
			t.Fatalf("Update() error = %v", err)
		}

		got, err := repo.GetByID(u.ID)
		if err != nil {
			t.Fatalf("GetByID() error = %v", err)
		}
		if got.Avatar != "https://example.com/new.png" {
			t.Errorf("Avatar = %q, want the updated value", got.Avatar)
		}
		if got.Status != StatusInactive {
			t.Errorf("Status = %q, want %q", got.Status, StatusInactive)
		}
		if got.Role != RoleViewer {
			t.Errorf("Role = %q, want %q", got.Role, RoleViewer)
		}
		// 关键：Update **不得**动密码哈希。
		// 曾见过「改头像顺手把 password_hash 写成空」的实现，
		// 症状是所有用户在改过资料后都无法登录。
		if got.PasswordHash != u.PasswordHash {
			t.Errorf("Update() clobbered PasswordHash: got %q, want %q",
				got.PasswordHash, u.PasswordHash)
		}
	})
}

func TestUserRepositoryContract_UpdateLastLogin(t *testing.T) {
	forEachUserRepository(t, func(t *testing.T, repo Repository) {
		u := newTestUser()
		if err := repo.Create(u); err != nil {
			t.Fatalf("Create() error = %v", err)
		}

		before, err := repo.GetByID(u.ID)
		if err != nil {
			t.Fatalf("GetByID() error = %v", err)
		}
		// 新用户从未登录：last_login_at 是 NULL，必须能读成 nil。
		// 这条覆盖了 scan 时 sql.NullTime 的用法 ——
		// 用 time.Time 直接接 NULL 会报
		// "converting NULL to time.Time is unsupported"。
		if before.LastLoginAt != nil {
			t.Errorf("new user LastLoginAt = %v, want nil", before.LastLoginAt)
		}

		if err := repo.UpdateLastLogin(u.ID); err != nil {
			t.Fatalf("UpdateLastLogin() error = %v", err)
		}

		after, err := repo.GetByID(u.ID)
		if err != nil {
			t.Fatalf("GetByID() error = %v", err)
		}
		if after.LastLoginAt == nil {
			t.Fatal("LastLoginAt is still nil after UpdateLastLogin()")
		}
	})
}

func TestUserRepositoryContract_Delete(t *testing.T) {
	forEachUserRepository(t, func(t *testing.T, repo Repository) {
		u := newTestUser()
		if err := repo.Create(u); err != nil {
			t.Fatalf("Create() error = %v", err)
		}
		if err := repo.Delete(u.ID); err != nil {
			t.Fatalf("Delete() error = %v", err)
		}
		got, err := repo.GetByID(u.ID)
		if err != nil {
			t.Fatalf("GetByID() after delete error = %v", err)
		}
		if got != nil {
			t.Errorf("user still readable after Delete(): %+v", got)
		}
	})
}

func TestUserRepositoryContract_ListStableOrderAndTotal(t *testing.T) {
	forEachUserRepository(t, func(t *testing.T, repo Repository) {
		ids := make([]string, 0, 5)
		for i := 0; i < 5; i++ {
			u := newTestUser()
			if err := repo.Create(u); err != nil {
				t.Fatalf("Create(%d) error = %v", i, err)
			}
			ids = append(ids, u.ID)
		}

		// 同一毫秒批量创建是最容易暴露排序不确定性的场景：
		// 只按 created_at 排会让相对顺序在两次查询之间变化。
		page1, total, err := repo.List(0, 3)
		if err != nil {
			t.Fatalf("List() error = %v", err)
		}
		if total != 5 {
			t.Errorf("List() total = %d, want 5", total)
		}
		if len(page1) != 3 {
			t.Fatalf("List() len = %d, want 3", len(page1))
		}

		page2, _, err := repo.List(3, 3)
		if err != nil {
			t.Fatalf("List(offset) error = %v", err)
		}
		if len(page2) != 2 {
			t.Fatalf("List(offset) len = %d, want 2", len(page2))
		}

		// 两页不许出现重复行：这是排序全序性的直接检验。
		seen := make(map[string]bool)
		for _, u := range append(append([]*User{}, page1...), page2...) {
			if seen[u.ID] {
				t.Errorf("user %s appears on both pages (order is not total)", u.ID)
			}
			seen[u.ID] = true
		}
		if len(seen) != 5 {
			t.Errorf("union of both pages has %d users, want 5", len(seen))
		}

		// 稳定性的另一半：同一分页参数重复查询必须给出同样的顺序。
		again, _, err := repo.List(0, 3)
		if err != nil {
			t.Fatalf("List() repeat error = %v", err)
		}
		for i := range again {
			if again[i].ID != page1[i].ID {
				t.Fatalf("List() order changed between calls: [%d] %q != %q",
					i, again[i].ID, page1[i].ID)
			}
		}

		// 合法请求下永远返回非 nil 切片（nil 会序列化成 JSON null）。
		empty, _, err := repo.List(1000, 10)
		if err != nil {
			t.Fatalf("List(beyond end) error = %v", err)
		}
		if empty == nil {
			t.Error("List(beyond end) returned a nil slice, want empty slice")
		}
	})
}

func TestUserRepositoryContract_CreateStoresCopy(t *testing.T) {
	// memory 版必须存副本；postgres 版靠序列化天然满足。
	// 契约要求两者都成立：调用方 Create 之后改自己手里的结构体，
	// 不能改动已保存的用户。
	forEachUserRepository(t, func(t *testing.T, repo Repository) {
		u := newTestUser()
		originalEmail := u.Email
		if err := repo.Create(u); err != nil {
			t.Fatalf("Create() error = %v", err)
		}

		u.Email = "mutated@example.com"
		u.Username = "mutated"

		stored, err := repo.GetByID(u.ID)
		if err != nil {
			t.Fatalf("GetByID() error = %v", err)
		}
		if stored.Email != originalEmail {
			t.Errorf("stored email = %q, want %q (caller's mutation leaked)",
				stored.Email, originalEmail)
		}
	})
}
