# AgentBot 管理控制台（前端工程）

服务端聚合视图 `GET /api/v1/admin/snapshot` 的浏览器端。产物由 Go 服务托管在
`/admin/*`（见 `internal/admin/handler.go`）。

## 运行时依赖（逐个说明用途）

刻意只用 2 个运行时依赖，没引 antd / MUI 之类的 UI 框架 —— 控制台只展示只读的
聚合数据，框架会把 `node_modules` 从 68 个包涨到上千个，而产物是要入库分发的。

| 依赖 | 版本 | 用途 |
|------|------|------|
| `react` | ^18.3.1 | 视图层。五个分区都有「按状态过滤/展开」这类局部状态，纯 DOM 手写会很快失控 |
| `react-dom` | ^18.3.1 | React 的 DOM 渲染器，与 `react` 同版本 |

开发依赖（不入产物）：`vite` + `@vitejs/plugin-react`（构建与 HMR）、
`typescript`（类型检查）、`@types/react` / `@types/react-dom`（类型定义）。

## 常用命令

```bash
npm install       # 安装依赖（首次或 package.json 变更后）
npm run dev       # 开发服务器 http://localhost:5173，/api 自动代理到 :8080
npm run build     # 类型检查 + 构建到 dist/
npm run preview   # 本地预览已构建产物
npm run typecheck # 只做类型检查
```

## dist 是否入库：**入库**（已决定，不要改成忽略）

服务端托管的就是 `web/admin/dist`。让它入库的理由：

1. **二进制分发者不需要 Node 工具链**。`docs/deployment.md` 的分发形态是「一个
   二进制 + 一份 web 目录」，如果 dist 不入库，每个部署者都得先装 Node 再构建 ——
   而控制台并不是可选功能。
2. **CI 不依赖 npm**。`internal/app/admin_console_e2e_test.go` 里有 6 条测试断言
   `/admin/*` 返回真实页面而非占位页。若 dist 不入库，这些断言在 CI 上只能
   全部 skip —— 而 skip 看起来仍是绿的，等于白测。
3. 代价是仓库多约 165KB。相比上面两条，这个代价可以接受。

**反面情形（必须避免）**：两头都不做 —— dist 不入库、部署文档也不提构建步骤。
那时表现为「本地开发一切正常，部署后全是占位页」，而且没有任何测试会发现。

## 两个容易踩的坑

### 1. `base` 必须是 `/admin/`

`vite.config.ts` 里的 `base: '/admin/'` 不能改回默认的 `'/'`。
服务端把产物挂在 `/admin/` 前缀下，若 `index.html` 引用 `/assets/index-xxx.js`，
浏览器会去根路径取 → 404 → **白屏**。

这个错误的隐蔽之处：`npm run build` **完全成功**，产物文件也都存在，
索引页也返回 200。唯一暴露问题的地方是 index.html 里引用资源的**路径**，
所以 `TestAdminConsoleAssetPathsUseAdminBase` 断言的是路径而不是状态码。

### 2. TS 类型必须对照 Go struct tag 抄写

`src/types.ts` 里的每个字段都逐条对照 `internal/admin/types.go` 的 `json` tag。
Go 的 snake_case tag 与 TS 字段名之间没有任何编译期约束 —— 写错不报错，
只是静默 `undefined`，页面表现为「渲染成功但全是 0 / 空」。

Go 侧字段都是 `omitempty`，所以 TS 里全部是可选的：分区聚合失败时键**直接不出现**，
而不是 `null`。渲染代码对 `undefined` 与 `null` 都要处理。

## 降级语义（和「无数据」不是一回事）

快照的九个分区各自独立降级，UI 上必须区分三种状态：

| 状态 | 表现 |
|------|------|
| 分区取数失败 | 该块显示红色原因（`Snapshot.errors[分区名]`），不显示数字 |
| 分区是空数据 | 该块显示「无数据」 |
| 全局降级 | `overview.degraded` 为真 → 页顶黄色横幅，提示计数不完整 |

把这三种混起来（例如失败也显示 0）会让运维把「部分计数」当成真实总量，
进而得出「集群很健康」的错误结论。

## 鉴权

`/admin/*` 静态资源是 public（见 `internal/authz/middleware.go` 的权限表），
但 `/api/v1/admin/snapshot` 需要 `role:manage`。控制台在收到 401 时
显示一个粘贴 access_token 的输入框，token 存在 `localStorage` 的
`agentbot.token` 键下。
