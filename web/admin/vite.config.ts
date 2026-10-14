import { defineConfig } from 'vite'
import react from '@vitejs/plugin-react'

// base 必须是 /admin/：服务端把产物挂在 /admin/* 前缀下
// （见 internal/admin/handler.go 的 staticPrefix）。
// 用默认的 '/' 会让 index.html 引用 /assets/xxx.js，浏览器去根路径取 → 404，
// 表现为「页面白屏 + 控制台一堆 404」，但 `npm run build` 是完全成功的。
export default defineConfig({
  plugins: [react()],
  base: '/admin/',
  build: {
    outDir: 'dist',
    // 关掉 sourcemap：产物要入库分发，map 会让仓库白胖一倍，
    // 而调试信息对只在生产环境看控制台的人没有价值。
    sourcemap: false,
  },
  server: {
    port: 5173,
    // 开发期把 API 请求转给本机 Go 服务，避免前端还要处理跨域。
    proxy: {
      '/api': {
        target: 'http://127.0.0.1:8080',
        changeOrigin: true,
      },
    },
  },
})
