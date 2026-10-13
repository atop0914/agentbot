-- 0002_agents：第 2 批迁移的第一个仓库（agent）。
--
-- 对应 docs/persistence.md 第 5 节的第 2 批（agent + tenant + role/authz）。
-- 已应用的迁移不许改（0001_baseline 已在真机跑过并记进 schema_migrations，
-- 改它不会重跑 —— 症状是「本地改了表结构、真机上还是旧的」）。
--
-- 为什么 Agent 比用户更需要持久化：用户丢了还能重新注册，
-- 而 Agent 是资源主体，它的配置、状态机位置与容器绑定点丢了就是丢了。
--
-- 类型选择的理由与 0001 一致：
--   * ID 用 TEXT（Go 侧是 uuid 的 String()），避免驱动层来回转换；
--   * 时间列用 TIMESTAMPTZ 而不是 TIMESTAMP：状态机的 elapsed 计算与
--     后续的监控时间窗口都是跨时区比较，缺时区的时间在夏令时切换那天会算错；
--   * 配置用 JSONB：Config 是结构化但会持续长字段的（tools / memory /
--     resources / env 四组，未来还会加），JSONB 可查询又不需要每加一个
--     子字段就改表结构。

CREATE TABLE IF NOT EXISTS agents (
    id           TEXT        PRIMARY KEY,
    name         TEXT        NOT NULL,
    description  TEXT        NOT NULL DEFAULT '',
    state        TEXT        NOT NULL,
    container_id TEXT        NOT NULL DEFAULT '',
    config       JSONB       NOT NULL DEFAULT '{}'::jsonb,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at   TIMESTAMPTZ NOT NULL DEFAULT now(),

    -- state 列的取值必须是 Go 侧 State 常量之一。
    --
    -- 加这条约束的理由：**值域漂移不会报错，只会静默失真**。
    -- 若某处把 State 拼错（"idel"）或写进一个未定义值，读回来就是一个
    -- 谁也不认识的字符串，And ListByState(StateIdle) 永远查不到它 ——
    -- 表现为「Agent 建出来了但列表里看不见」。枚举列加约束能让这种
    -- 偏差在写入那一刻就响亮失败，而不是等到查询侧对不上才发现。
    CONSTRAINT agents_state_check CHECK (
        state IN ('idle', 'running', 'paused', 'error', 'completed', 'terminated')
    )
);

-- 列表主路径是「按创建时间倒序翻页」，并把 id 作为第二排序键
-- （见 repository_postgres.go 里对全序的解释）。
CREATE INDEX IF NOT EXISTS idx_agents_created_at ON agents (created_at DESC, id DESC);
CREATE INDEX IF NOT EXISTS idx_agents_state      ON agents (state);
