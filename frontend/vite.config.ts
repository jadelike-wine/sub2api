import { defineConfig, loadEnv, Plugin } from 'vite'
import vue from '@vitejs/plugin-vue'
import checker from 'vite-plugin-checker'
import VueDevTools from 'vite-plugin-vue-devtools'
import { resolve } from 'path'
import { HttpsProxyAgent } from 'https-proxy-agent'

/**
 * Vite 插件：开发模式下注入公开配置到 index.html
 * 与生产模式的后端注入行为保持一致，消除闪烁
 */
function injectPublicSettings(backendUrl: string): Plugin {
  return {
    name: 'inject-public-settings',
    apply: 'serve',
    transformIndexHtml: {
      order: 'pre',
      async handler(html) {
        try {
          const response = await fetch(`${backendUrl}/api/v1/settings/public`, {
            signal: AbortSignal.timeout(2000)
          })
          if (response.ok) {
            const data = await response.json()
            if (data.code === 0 && data.data) {
              const script = `<script>window.__APP_CONFIG__=${JSON.stringify(data.data)};</script>`
              return html.replace('</head>', `${script}\n</head>`)
            }
          }
        } catch (e) {
          console.warn('[vite] 无法获取公开配置，将回退到 API 调用:', (e as Error).message)
        }
        return html
      }
    }
  }
}

export default defineConfig(({ mode }) => {
  // 加载环境变量
  const env = loadEnv(mode, process.cwd(), '')
  const backendUrl = env.VITE_DEV_PROXY_TARGET || 'http://localhost:8080'
  const devPort = Number(env.VITE_DEV_PORT || 3000)
  const upstreamProxy = env.VITE_UPSTREAM_PROXY // e.g. http://127.0.0.1:7890

  return {
    plugins: [
      vue(),
      VueDevTools(),
      checker({
        vueTsc: true
      }),
      injectPublicSettings(backendUrl)
    ],
  resolve: {
    alias: {
      '@': resolve(__dirname, 'src'),
      // 使用 vue-i18n 运行时版本，避免 CSP unsafe-eval 问题
      'vue-i18n': 'vue-i18n/dist/vue-i18n.runtime.esm-bundler.js'
    }
  },
  define: {
    // 启用 vue-i18n JIT 编译，在 CSP 环境下处理消息插值
    // JIT 编译器生成 AST 对象而非 JS 代码，无需 unsafe-eval
    __INTLIFY_JIT_COMPILATION__: true
  },
  build: {
    outDir: '../backend/internal/web/dist',
    emptyOutDir: true,
    rollupOptions: {
      output: {
        /**
         * 手动分包配置
         *
         * 仅对真正独立、体积较大的库单独分包；Vue 生态（vue / vue-router /
         * pinia / @vue/* / vue-i18n / @intlify / @vueuse / element-plus 等）
         * 与 lodash 等工具库之间存在 Rollup 模块去重产生的交叉引用，强制拆分
         * 会导致循环依赖并触发 "Cannot access 'X' before initialization"
         * 的 TDZ 错误（生产环境白屏）。因此这些包交给 Rollup 自动分包。
         */
        manualChunks(id: string) {
          if (id.includes('node_modules')) {
            // 图表库：仅仪表盘使用，独立且体积较大
            if (id.includes('/chart.js/') || id.includes('/vue-chartjs/')) {
              return 'vendor-chart'
            }

            // Stripe：仅在支付流程中按需加载，避免进入首页公共依赖
            if (id.includes('/@stripe/stripe-js/')) {
              return 'vendor-stripe'
            }

            // xlsx：体积较大且仅在导出场景按需使用
            if (id.includes('/xlsx/')) {
              return 'vendor-xlsx'
            }

            // 其余第三方库（含 Vue 生态、lodash、element-plus 等）
            // 交给 Rollup 自动分包，避免人工边界引发循环依赖
          }
        }
      }
    }
  },
    server: {
      host: '0.0.0.0',
      port: devPort,
      proxy: {
        '/api': {
          target: backendUrl,
          changeOrigin: true,
          agent: upstreamProxy ? new HttpsProxyAgent(upstreamProxy) : undefined
        },
        '/v1': {
          target: backendUrl,
          changeOrigin: true,
          agent: upstreamProxy ? new HttpsProxyAgent(upstreamProxy) : undefined
        },
        '/setup': {
          target: backendUrl,
          changeOrigin: true,
          agent: upstreamProxy ? new HttpsProxyAgent(upstreamProxy) : undefined
        }
      }
    }
  }
})
