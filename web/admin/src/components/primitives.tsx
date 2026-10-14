/**
 * 通用展示原语：统计卡片、键值分布表、时间格式化。
 *
 * 这三样在五个分区里反复出现。抽出来的主要动机不是为了少写代码，而是
 * **让「缺数据」的表现统一**：Go 侧的 map 字段都是 `omitempty`，
 * 分布表在无数据时可能是 undefined 也可能是 {} —— 若各处分别处理，
 * 就会出现「有的地方显示 0、有的地方显示空白」。
 */

interface StatProps {
  label: string
  value: number | string
  /** 需要强调的异常值（如失败数、未处理告警）给一个语义色。 */
  tone?: 'default' | 'warn' | 'danger' | 'ok'
}

export function Stat({ label, value, tone = 'default' }: StatProps) {
  return (
    <div className={`stat stat-${tone}`}>
      <div className="stat-value">{value}</div>
      <div className="stat-label">{label}</div>
    </div>
  )
}

export function StatRow({ children }: { children: React.ReactNode }) {
  return <div className="stat-row">{children}</div>
}

interface DistributionProps {
  /** 键 → 计数。undefined / null / 空对象统一渲染成「无数据」。 */
  data: Record<string, number> | null | undefined
  /** 键的显示名映射（如 idle → 空闲）。未命中的键原样显示。 */
  labels?: Record<string, string>
  /** 按计数降序排列；关闭则保持后端给的顺序。 */
  sort?: boolean
}

export function Distribution({ data, labels, sort = true }: DistributionProps) {
  const entries = Object.entries(data ?? {})
  if (entries.length === 0) {
    return <p className="muted">无数据</p>
  }
  if (sort) {
    entries.sort((a, b) => b[1] - a[1])
  }
  const max = Math.max(...entries.map(([, v]) => v), 1)
  return (
    <table className="dist">
      <tbody>
        {entries.map(([key, value]) => (
          <tr key={key}>
            <th scope="row">{labels?.[key] ?? key}</th>
            <td className="dist-bar-cell">
              {/* 用条宽而不是只给数字：分布异常（某个状态独占）一眼能看出来 */}
              <span className="dist-bar" style={{ width: `${Math.max(2, (value / max) * 100)}%` }} />
            </td>
            <td className="dist-count">{value}</td>
          </tr>
        ))}
      </tbody>
    </table>
  )
}

/** 把 RFC3339 时间格式化成 HH:MM:SS；无效值原样回显而不是显示 Invalid Date。 */
export function formatTime(raw: string | undefined): string {
  if (!raw) {
    return '—'
  }
  const d = new Date(raw)
  if (Number.isNaN(d.getTime())) {
    return raw
  }
  return d.toLocaleString('zh-CN', { hour12: false })
}

/** 秒 → 可读时长；0 表示「还没有已完成的样本」而不是「零秒」。 */
export function formatSeconds(raw: number | undefined): string {
  if (raw === undefined || raw === null || raw <= 0) {
    return '—'
  }
  if (raw < 60) {
    return `${raw.toFixed(1)}s`
  }
  return `${Math.floor(raw / 60)}m${Math.round(raw % 60)}s`
}

/** 百分比；successRate 为负数是后端约定的「无任务数据」（-1）。 */
export function formatSuccessRate(rate: number): string {
  if (rate < 0) {
    return '无任务数据'
  }
  return `${(rate * 100).toFixed(1)}%`
}
