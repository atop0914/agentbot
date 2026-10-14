import { useCallback, useEffect, useState } from 'react'
import { ApiError, UnauthorizedError, fetchAdminConfig, fetchSnapshot, getToken, setToken } from './api'
import type { AdminConfig, Snapshot } from './types'
import Section from './components/Section'
import { Distribution, Stat, StatRow, formatSeconds, formatSuccessRate, formatTime } from './components/primitives'

const STATE_LABELS: Record<string, string> = {
  idle: '空闲',
  running: '运行中',
  paused: '已暂停',
  error: '错误',
  stopped: '已停止',
  pending: '等待中',
  completed: '已完成',
  failed: '失败',
  cancelled: '已取消',
}

type LoadState =
  | { kind: 'loading' }
  | { kind: 'ready'; snapshot: Snapshot; config: AdminConfig | null }
  | { kind: 'unauthorized' }
  | { kind: 'error'; message: string }

export default function App() {
  const [state, setState] = useState<LoadState>({ kind: 'loading' })
  const [tokenInput, setTokenInput] = useState('')
  const [busy, setBusy] = useState(false)

  const load = useCallback(async () => {
    setBusy(true)
    try {
      // 两个请求并行：config 是次要信息，失败不应让整个控制台不可用。
      const [snapshot, config] = await Promise.all([
        fetchSnapshot(),
        fetchAdminConfig().catch(() => null),
      ])
      setState({ kind: 'ready', snapshot, config })
    } catch (err) {
      if (err instanceof UnauthorizedError) {
        setState({ kind: 'unauthorized' })
      } else if (err instanceof ApiError) {
        setState({ kind: 'error', message: err.message })
      } else {
        setState({ kind: 'error', message: err instanceof Error ? err.message : String(err) })
      }
    } finally {
      setBusy(false)
    }
  }, [])

  useEffect(() => {
    void load()
  }, [load])

  if (state.kind === 'loading') {
    return <Shell title="AgentBot 管理控制台"><p className="muted">正在加载…</p></Shell>
  }

  if (state.kind === 'unauthorized') {
    return (
      <Shell title="AgentBot 管理控制台">
        <Section title="需要登录">
          <p>
            管理后台的聚合视图需要 <code>role:manage</code> 权限。请粘贴一个具备该权限的
            JWT（登录接口返回的 <code>access_token</code>），或先通过 <code>/api/v1/auth/login</code> 获取。
          </p>
          <div className="token-form">
            <input
              type="password"
              value={tokenInput}
              onChange={(e) => setTokenInput(e.target.value)}
              placeholder="粘贴 access_token"
              aria-label="access token"
            />
            <button
              type="button"
              disabled={busy || tokenInput.trim() === ''}
              onClick={() => {
                setToken(null) // 先清掉旧 token，避免留着过期凭据迷惑下一次请求
                setToken(tokenInput.trim())
                setTokenInput('')
                void load()
              }}
            >
              使用该凭据加载
            </button>
            {getToken() ? (
              <button
                type="button"
                onClick={() => {
                  setToken(null)
                  void load()
                }}
              >
                清除已保存的凭据
              </button>
            ) : null}
          </div>
        </Section>
      </Shell>
    )
  }

  if (state.kind === 'error') {
    return (
      <Shell title="AgentBot 管理控制台">
        <Section title="加载失败">
          <p className="section-error">{state.message}</p>
          <button type="button" disabled={busy} onClick={() => void load()}>
            重试
          </button>
        </Section>
      </Shell>
    )
  }

  const { snapshot, config } = state
  const errors = snapshot.errors ?? {}
  const overview = snapshot.overview

  return (
    <Shell title={config?.title || snapshot.title || 'AgentBot 管理控制台'} version={snapshot.version}>
      <div className="toolbar">
        <span className="muted">生成于 {formatTime(snapshot.generated_at)}</span>
        <button type="button" disabled={busy} onClick={() => void load()}>
          刷新
        </button>
      </div>

      {/* 全页级降级：overview.degraded 为真说明至少一个分区失败，计数是部分数据。
          必须显著提示 —— 否则运维会把「部分计数」当成「真实总量」，
          进而得出「很健康」的错误结论。 */}
      {overview?.degraded ? (
        <div className="banner banner-warn">
          ⚠ 部分分区聚合失败，当前计数为不完整数据。缺失分区：
          {Object.keys(errors).length > 0 ? Object.keys(errors).join('、') : '（未提供明细）'}
        </div>
      ) : null}

      <Section title="概览" error={errors.overview}>
        {overview ? (
          <StatRow>
            <Stat label="Agent 总数" value={overview.total_agents} />
            <Stat label="运行中" value={overview.running_agents} tone="ok" />
            <Stat label="异常" value={overview.failed_agents} tone={overview.failed_agents > 0 ? 'danger' : 'default'} />
            <Stat label="任务总数" value={overview.total_tasks} />
            <Stat label="运行中任务" value={overview.running_tasks} />
            <Stat label="失败任务" value={overview.failed_tasks} tone={overview.failed_tasks > 0 ? 'danger' : 'default'} />
            <Stat label="未处理告警" value={overview.open_alerts} tone={overview.open_alerts > 0 ? 'warn' : 'default'} />
            <Stat label="审计事件" value={overview.audit_events} />
          </StatRow>
        ) : (
          <p className="muted">无数据</p>
        )}
      </Section>

      <Section
        title="Agent"
        error={errors.agents}
        // 分区内二级降级：监控摘要取数失败时，状态分布与最近列表仍然可信。
        warning={
          snapshot.agents?.monitoring?.degraded
            ? '监控摘要部分取数失败，Agent 状态分布不受影响。'
            : undefined
        }
      >
        {snapshot.agents ? (
          <>
            <div className="two-col">
              <div>
                <h3>状态分布</h3>
                <Distribution data={snapshot.agents.by_state} labels={STATE_LABELS} />
              </div>
              <div>
                <h3>最近更新</h3>
                {snapshot.agents.recently && snapshot.agents.recently.length > 0 ? (
                  <table className="list">
                    <thead>
                      <tr>
                        <th>名称</th>
                        <th>状态</th>
                        <th>更新时间</th>
                      </tr>
                    </thead>
                    <tbody>
                      {snapshot.agents.recently.map((a) => (
                        <tr key={a.id}>
                          <td>{a.name}</td>
                          <td>{STATE_LABELS[a.state] ?? a.state}</td>
                          <td className="muted">{formatTime(a.updated_at)}</td>
                        </tr>
                      ))}
                    </tbody>
                  </table>
                ) : (
                  <p className="muted">无数据</p>
                )}
              </div>
            </div>

            {snapshot.agents.monitoring && snapshot.agents.monitoring.series?.length ? (
              <>
                <h3>监控摘要（窗口 {snapshot.agents.monitoring.series_window}）</h3>
                <table className="list">
                  <thead>
                    <tr>
                      <th>Agent</th>
                      <th>采样</th>
                      <th>CPU 均值/峰值</th>
                      <th>内存均值/峰值</th>
                      <th>任务成功率</th>
                    </tr>
                  </thead>
                  <tbody>
                    {snapshot.agents.monitoring.series.map((s) => (
                      <tr key={s.agent_id}>
                        <td className="mono">{s.agent_id}</td>
                        <td>{s.empty ? <span className="muted">空窗口</span> : s.samples}</td>
                        <td>{s.empty ? '—' : `${s.cpu_avg.toFixed(1)} / ${s.cpu_peak.toFixed(1)}`}</td>
                        <td>{s.empty ? '—' : `${s.memory_avg.toFixed(1)} / ${s.memory_peak.toFixed(1)}`}</td>
                        <td>{formatSuccessRate(s.task_success_rate)}</td>
                      </tr>
                    ))}
                  </tbody>
                </table>
              </>
            ) : null}

            {snapshot.agents.monitoring?.dispositions ? (
              <>
                <h3>告警处置进度</h3>
                <StatRow>
                  <Stat label="未处理" value={snapshot.agents.monitoring.dispositions.open} tone="warn" />
                  <Stat label="已确认" value={snapshot.agents.monitoring.dispositions.acknowledged} />
                  <Stat label="已解决" value={snapshot.agents.monitoring.dispositions.resolved} tone="ok" />
                  <Stat
                    label="未确认"
                    value={snapshot.agents.monitoring.dispositions.unacknowledged}
                    tone={snapshot.agents.monitoring.dispositions.unacknowledged > 0 ? 'warn' : 'default'}
                  />
                  <Stat
                    label="平均响应"
                    value={formatSeconds(snapshot.agents.monitoring.dispositions.avg_time_to_ack_seconds)}
                  />
                  <Stat
                    label="平均解决"
                    value={formatSeconds(snapshot.agents.monitoring.dispositions.avg_time_to_resolve_seconds)}
                  />
                </StatRow>
              </>
            ) : null}
          </>
        ) : (
          <p className="muted">无数据</p>
        )}
      </Section>

      <Section title="任务" error={errors.tasks}>
        {snapshot.tasks ? (
          <>
            <p className="muted">共 {snapshot.tasks.total} 个任务</p>
            <Distribution data={snapshot.tasks.by_state} labels={STATE_LABELS} />
          </>
        ) : (
          <p className="muted">无数据</p>
        )}
      </Section>

      <Section title="告警" error={errors.alerts}>
        {snapshot.alerts ? (
          <>
            <div className="two-col">
              <div>
                <h3>按严重级别</h3>
                <Distribution data={snapshot.alerts.by_severity} sort={false} />
                <p className="muted">未处理告警共 {snapshot.alerts.total} 条</p>
              </div>
              <div>
                <h3>告警列表</h3>
                {snapshot.alerts.items && snapshot.alerts.items.length > 0 ? (
                  <table className="list">
                    <thead>
                      <tr>
                        <th>级别</th>
                        <th>类型</th>
                        <th>Agent</th>
                        <th>消息</th>
                        <th>时间</th>
                      </tr>
                    </thead>
                    <tbody>
                      {snapshot.alerts.items.map((a) => (
                        <tr key={a.id}>
                          <td className={`sev sev-${a.severity}`}>{a.severity}</td>
                          <td>{a.type}</td>
                          <td className="mono">{a.agent_id}</td>
                          <td>{a.message}</td>
                          <td className="muted">{formatTime(a.created_at)}</td>
                        </tr>
                      ))}
                    </tbody>
                  </table>
                ) : (
                  <p className="muted">无数据</p>
                )}
              </div>
            </div>
          </>
        ) : (
          <p className="muted">无数据</p>
        )}
      </Section>

      <Section title="审计事件" error={errors.audit}>
        {snapshot.audit ? (
          <>
            <div className="two-col">
              <div>
                <h3>按动作</h3>
                <Distribution data={snapshot.audit.by_action} />
                <p className="muted">共 {snapshot.audit.total} 条</p>
              </div>
              <div>
                <h3>最近事件</h3>
                {snapshot.audit.recent && snapshot.audit.recent.length > 0 ? (
                  <table className="list">
                    <thead>
                      <tr>
                        <th>时间</th>
                        <th>操作者</th>
                        <th>动作</th>
                        <th>资源</th>
                        <th>结果</th>
                      </tr>
                    </thead>
                    <tbody>
                      {snapshot.audit.recent.map((e) => (
                        <tr key={e.id}>
                          <td className="muted">{formatTime(e.timestamp)}</td>
                          <td className="mono">
                            {e.actor}
                            <span className="muted"> ({e.actor_type})</span>
                          </td>
                          <td>{e.action}</td>
                          <td className="mono">
                            {e.resource}
                            {e.resource_id ? `/${e.resource_id}` : ''}
                          </td>
                          <td className={e.status === 'success' ? 'ok' : 'danger'}>{e.status}</td>
                        </tr>
                      ))}
                    </tbody>
                  </table>
                ) : (
                  <p className="muted">无数据</p>
                )}
                {snapshot.audit.truncated ? (
                  <p className="muted">（列表已截断，仅显示最近的部分事件）</p>
                ) : null}
              </div>
            </div>
          </>
        ) : (
          <p className="muted">无数据</p>
        )}
      </Section>

      <Section title="用户与授权" error={errors.users}>
        {snapshot.users ? (
          <>
            <StatRow>
              <Stat label="用户总数" value={snapshot.users.total} />
              {snapshot.users.authz ? (
                <>
                  <Stat label="授权总数" value={snapshot.users.authz.total_assignments} />
                  <Stat label="用户授权" value={snapshot.users.authz.user_assignments} />
                  <Stat label="Agent 授权" value={snapshot.users.authz.agent_assignments} />
                  <Stat
                    label="已过期"
                    value={snapshot.users.authz.expired}
                    tone={snapshot.users.authz.expired > 0 ? 'warn' : 'default'}
                  />
                  <Stat label="判定次数" value={snapshot.users.authz.decisions} />
                  <Stat
                    label="拒绝次数"
                    value={snapshot.users.authz.denials}
                    tone={snapshot.users.authz.denials > 0 ? 'warn' : 'default'}
                  />
                  <Stat
                    label="降级次数"
                    value={snapshot.users.authz.degraded}
                    tone={snapshot.users.authz.degraded > 0 ? 'danger' : 'default'}
                  />
                </>
              ) : null}
            </StatRow>
            <div className="two-col">
              <div>
                <h3>按状态</h3>
                <Distribution data={snapshot.users.by_status} sort={false} />
                <h3>按角色</h3>
                <Distribution data={snapshot.users.by_role} />
              </div>
              <div>
                <h3>最近用户</h3>
                {snapshot.users.recent && snapshot.users.recent.length > 0 ? (
                  <table className="list">
                    <thead>
                      <tr>
                        <th>用户名</th>
                        <th>邮箱</th>
                        <th>状态</th>
                        <th>角色</th>
                        <th>来源</th>
                      </tr>
                    </thead>
                    <tbody>
                      {snapshot.users.recent.map((u) => (
                        <tr key={u.id}>
                          <td>{u.username}</td>
                          <td className="muted">{u.email}</td>
                          <td>{u.status}</td>
                          <td>{u.role}</td>
                          <td className="muted">{u.provider || '本地'}</td>
                        </tr>
                      ))}
                    </tbody>
                  </table>
                ) : (
                  <p className="muted">无数据</p>
                )}
              </div>
            </div>
          </>
        ) : (
          <p className="muted">无数据</p>
        )}
      </Section>

      <Section title="网络出口" error={errors.network} warning={snapshot.network?.degraded ? '出口流量统计部分取数失败。' : undefined}>
        {snapshot.network ? (
          <>
            <StatRow>
              <Stat
                label="出口策略"
                value={snapshot.network.total_rules}
                tone={snapshot.network.total_rules === 0 ? 'warn' : 'default'}
              />
              <Stat label="允许规则" value={snapshot.network.allow_rules} />
              <Stat label="拒绝规则" value={snapshot.network.deny_rules} />
              <Stat label="出站请求" value={snapshot.network.total_requests} />
              <Stat label="被拦截" value={snapshot.network.blocked_requests} tone={snapshot.network.blocked_requests > 0 ? 'warn' : 'default'} />
              <Stat label="请求失败" value={snapshot.network.failed_requests} tone={snapshot.network.failed_requests > 0 ? 'danger' : 'default'} />
              <Stat label="外联域名" value={snapshot.network.distinct_domains} />
              <Stat
                label="可疑请求"
                value={snapshot.network.suspicious_requests}
                tone={snapshot.network.suspicious_requests > 0 ? 'danger' : 'default'}
              />
            </StatRow>
            <p className="muted">
              统计窗口 {snapshot.network.window}，生成于 {formatTime(snapshot.network.generated_at)}，总流量{' '}
              {formatBytes(snapshot.network.total_bytes)}
            </p>
            {snapshot.network.suspicious_domains && snapshot.network.suspicious_domains.length > 0 ? (
              <>
                <h3>可疑外联目标</h3>
                <table className="list">
                  <thead>
                    <tr>
                      <th>域名</th>
                      <th>请求</th>
                      <th>被拦截</th>
                      <th>命中原因</th>
                    </tr>
                  </thead>
                  <tbody>
                    {snapshot.network.suspicious_domains.map((d) => (
                      <tr key={d.domain}>
                        <td className="mono">{d.domain}</td>
                        <td>{d.requests}</td>
                        <td>{d.blocked}</td>
                        <td className="muted">{d.reasons?.join('、') ?? '—'}</td>
                      </tr>
                    ))}
                  </tbody>
                </table>
              </>
            ) : null}
            {snapshot.network.top_domains && snapshot.network.top_domains.length > 0 ? (
              <>
                <h3>请求最多的外联目标</h3>
                <table className="list">
                  <thead>
                    <tr>
                      <th>域名</th>
                      <th>请求</th>
                      <th>被拦截</th>
                      <th>流量</th>
                    </tr>
                  </thead>
                  <tbody>
                    {snapshot.network.top_domains.map((d) => (
                      <tr key={d.domain}>
                        <td className="mono">{d.domain}</td>
                        <td>{d.requests}</td>
                        <td>{d.blocked}</td>
                        <td>{formatBytes(d.bytes)}</td>
                      </tr>
                    ))}
                  </tbody>
                </table>
              </>
            ) : null}
          </>
        ) : (
          <p className="muted">无数据</p>
        )}
      </Section>

      <Section title="功能开关" error={errors.plugins}>
        {snapshot.plugins ? (
          <div className="two-col">
            <div>
              <h3>已启用</h3>
              {snapshot.plugins.enabled && snapshot.plugins.enabled.length > 0 ? (
                <ul className="chips">
                  {snapshot.plugins.enabled.map((f) => (
                    <li key={f} className="chip chip-on">
                      {f}
                    </li>
                  ))}
                </ul>
              ) : (
                <p className="muted">无</p>
              )}
            </div>
            <div>
              <h3>已关闭</h3>
              {snapshot.plugins.disabled && snapshot.plugins.disabled.length > 0 ? (
                <ul className="chips">
                  {snapshot.plugins.disabled.map((f) => (
                    <li key={f} className="chip chip-off">
                      {f}
                    </li>
                  ))}
                </ul>
              ) : (
                <p className="muted">无</p>
              )}
            </div>
          </div>
        ) : (
          <p className="muted">无数据</p>
        )}
      </Section>

      <Section title="静态资源" error={errors.static}>
        {snapshot.static ? (
          <table className="list">
            <tbody>
              <tr>
                <th scope="row">挂载前缀</th>
                <td className="mono">{snapshot.static.prefix}</td>
              </tr>
              <tr>
                <th scope="row">产物目录</th>
                <td className="mono">{snapshot.static.dir}</td>
              </tr>
              <tr>
                <th scope="row">已挂载</th>
                <td className={snapshot.static.mounted ? 'ok' : 'warn'}>{snapshot.static.mounted ? '是' : '否'}</td>
              </tr>
              <tr>
                <th scope="row">index.html</th>
                <td className={snapshot.static.index_exists ? 'ok' : 'warn'}>
                  {snapshot.static.index_exists ? '存在' : '缺失'}
                </td>
              </tr>
              {snapshot.static.message ? (
                <tr>
                  <th scope="row">说明</th>
                  <td className="muted">{snapshot.static.message}</td>
                </tr>
              ) : null}
            </tbody>
          </table>
        ) : (
          <p className="muted">无数据</p>
        )}
      </Section>
    </Shell>
  )
}

function formatBytes(raw: number): string {
  if (!raw || raw <= 0) {
    return '0 B'
  }
  const units = ['B', 'KiB', 'MiB', 'GiB', 'TiB']
  let value = raw
  let idx = 0
  while (value >= 1024 && idx < units.length - 1) {
    value /= 1024
    idx += 1
  }
  return `${value.toFixed(idx === 0 ? 0 : 1)} ${units[idx]}`
}

function Shell({ title, version, children }: { title: string; version?: string; children: React.ReactNode }) {
  return (
    <div className="app">
      <header className="app-header">
        <h1>{title}</h1>
        {version ? <span className="version">v{version}</span> : null}
      </header>
      <main className="app-main">{children}</main>
      <footer className="app-footer muted">
        数据来源：<code>GET /api/v1/admin/snapshot</code>
      </footer>
    </div>
  )
}
