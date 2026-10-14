import type { AdminConfig, Snapshot, SnapshotQuery } from './types'

/**
 * 一个薄 fetch 封装。
 *
 * 设计取舍：
 * - 只做四件事：拼 URL、带 Authorization、解析 JSON、把非 2xx 归一成 ApiError。
 *   不做重试、不做缓存 —— 控制台是人工读的视图，自动重试只会让错误更难看清。
 * - token 从 localStorage 读（键名与既有约定一致），不引入状态管理库。
 */

const TOKEN_KEY = 'agentbot.token'

/** 401 时抛出，调用方据此提示「需要登录」而不是「服务端坏了」。 */
export class UnauthorizedError extends Error {
  constructor() {
    super('未登录或登录已过期')
    this.name = 'UnauthorizedError'
  }
}

/** 其余非 2xx 都归一成这个类型，携带状态码与后端返回的 error 文本。 */
export class ApiError extends Error {
  readonly status: number

  constructor(status: number, message: string) {
    super(message)
    this.name = 'ApiError'
    this.status = status
  }
}

export function getToken(): string | null {
  try {
    return window.localStorage.getItem(TOKEN_KEY)
  } catch {
    // 隐私模式下 localStorage 会抛异常 —— 不能因为读不到 token 就崩掉整个页面。
    return null
  }
}

export function setToken(token: string | null): void {
  try {
    if (token === null) {
      window.localStorage.removeItem(TOKEN_KEY)
    } else {
      window.localStorage.setItem(TOKEN_KEY, token)
    }
  } catch {
    // 同上：写入失败只影响「记住登录」，不应中断请求。
  }
}

interface RequestOptions {
  method?: string
  query?: Record<string, string | undefined>
}

async function request<T>(path: string, options: RequestOptions = {}): Promise<T> {
  const url = new URL(path, window.location.origin)
  if (options.query) {
    for (const [key, value] of Object.entries(options.query)) {
      if (value !== undefined && value !== '') {
        url.searchParams.set(key, value)
      }
    }
  }

  const headers: Record<string, string> = { Accept: 'application/json' }
  const token = getToken()
  if (token) {
    headers.Authorization = `Bearer ${token}`
  }

  let resp: Response
  try {
    resp = await fetch(url.toString(), { method: options.method ?? 'GET', headers })
  } catch (err) {
    // fetch 只在网络层失败时 reject（DNS / 连接被拒 / 被 CORS 拦）。
    // 这些情况保持原始 message，比统一成「未知错误」有用得多。
    throw new ApiError(0, `无法连接服务端：${err instanceof Error ? err.message : String(err)}`)
  }

  if (resp.status === 401) {
    throw new UnauthorizedError()
  }
  if (!resp.ok) {
    let detail = resp.statusText
    try {
      const body = (await resp.json()) as { error?: string }
      if (body?.error) {
        detail = body.error
      }
    } catch {
      // 响应体不是 JSON（例如反向代理返回的 HTML 错误页）——保留 statusText。
    }
    throw new ApiError(resp.status, `${resp.status} ${detail}`)
  }

  if (resp.status === 204) {
    return undefined as T
  }
  return (await resp.json()) as T
}

/** 把 SnapshotQuery 转成后端认的查询串。 */
function snapshotQueryParams(query?: SnapshotQuery): Record<string, string | undefined> {
  if (!query) {
    return {}
  }
  return {
    // 后端 ParseSnapshotQuery 认的是逗号分隔的分区名。
    sections: query.sections && query.sections.length > 0 ? query.sections.join(',') : undefined,
    recent_limit: query.recent_limit !== undefined ? String(query.recent_limit) : undefined,
    audit_window: query.audit_window,
  }
}

/** GET /api/v1/admin/snapshot —— 控制台首屏的单一聚合视图。 */
export function fetchSnapshot(query?: SnapshotQuery): Promise<Snapshot> {
  return request<Snapshot>('/api/v1/admin/snapshot', {
    query: snapshotQueryParams(query),
  })
}

/** GET /api/v1/admin/config —— 控制台展示配置。 */
export function fetchAdminConfig(): Promise<AdminConfig> {
  return request<AdminConfig>('/api/v1/admin/config')
}

/** GET /api/v1/admin/static —— 静态资源挂载状态（用于自检页面）。 */
export function fetchStaticStatus(): Promise<{
  prefix: string
  dir: string
  mounted: boolean
  index_exists: boolean
  message?: string
}> {
  return request('/api/v1/admin/static')
}
