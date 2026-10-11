-- 0001_baseline：第 1 批迁移的基线。
--
-- 本次只建审计事件表与用户表 —— 对应 docs/persistence.md 第 5 节的第 1 批
-- （audit + user）。其余仓库按批推进，各自在自己的迁移文件里建表。
--
-- 关于类型选择的说明（避免后来者以为是随手写的）：
--   * ID 用 TEXT 而不是 uuid 类型：Go 侧是 string（github.com/google/uuid
--     生成后 String()），用 TEXT 可以避免驱动层来回转换。
--   * 时间列用 TIMESTAMPTZ 而不是 TIMESTAMP：audit 的留存窗口与
--     Purge(cutoff) 是跨时区比较，缺时区的时间在夏令时切换那天会算错。
--   * details 用 JSONB：审计详情是结构化但不定形的，JSONB 可索引可查询，
--     又不需要为每个新事件类型改表结构。

CREATE TABLE IF NOT EXISTS audit_events (
    id          TEXT        PRIMARY KEY,
    seq         BIGSERIAL   NOT NULL,
    event_type  TEXT        NOT NULL,
    actor_id    TEXT        NOT NULL DEFAULT '',
    actor_type  TEXT        NOT NULL DEFAULT '',
    action      TEXT        NOT NULL,
    resource    TEXT        NOT NULL DEFAULT '',
    resource_id TEXT        NOT NULL DEFAULT '',
    status      TEXT        NOT NULL DEFAULT '',
    error       TEXT        NOT NULL DEFAULT '',
    ip_address  TEXT        NOT NULL DEFAULT '',
    user_agent  TEXT        NOT NULL DEFAULT '',
    details     JSONB       NOT NULL DEFAULT '{}'::jsonb,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- 审计查询的主路径是「按时间倒序翻页」，其次是按 actor / action / resource 过滤。
CREATE INDEX IF NOT EXISTS idx_audit_events_created_at ON audit_events (created_at DESC, seq DESC);
CREATE INDEX IF NOT EXISTS idx_audit_events_actor      ON audit_events (actor_id);
CREATE INDEX IF NOT EXISTS idx_audit_events_action     ON audit_events (action);
CREATE INDEX IF NOT EXISTS idx_audit_events_resource   ON audit_events (resource, resource_id);
-- Purge 按 created_at 删，单独建一个正序索引（倒序索引对 range delete 帮助有限）。
CREATE INDEX IF NOT EXISTS idx_audit_events_purge      ON audit_events (created_at);

CREATE TABLE IF NOT EXISTS users (
    id             TEXT        PRIMARY KEY,
    email          TEXT        NOT NULL UNIQUE,
    username       TEXT        NOT NULL UNIQUE,
    password_hash  TEXT        NOT NULL DEFAULT '',
    oauth_provider TEXT        NOT NULL DEFAULT '',
    oauth_id       TEXT        NOT NULL DEFAULT '',
    avatar         TEXT        NOT NULL DEFAULT '',
    role           TEXT        NOT NULL DEFAULT 'user',
    status         TEXT        NOT NULL DEFAULT 'active',
    last_login_at  TIMESTAMPTZ,
    created_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at     TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE UNIQUE INDEX IF NOT EXISTS idx_users_email    ON users (email);
CREATE UNIQUE INDEX IF NOT EXISTS idx_users_username ON users (username);
-- OAuth 绑定查询走 (provider, id)，但未绑定 OAuth 的用户该组合为空串，
-- 多条空串会撞唯一约束 —— 所以这里用**部分索引**，只约束真正绑定了的账号。
CREATE UNIQUE INDEX IF NOT EXISTS idx_users_oauth ON users (oauth_provider, oauth_id)
    WHERE oauth_provider <> '' AND oauth_id <> '';
