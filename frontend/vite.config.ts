import { fileURLToPath, URL } from 'node:url'

import { defineConfig } from 'vite'
import VueRouter from 'vue-router/vite'
import { VueRouterAutoImports } from 'vue-router/unplugin'
import vue from '@vitejs/plugin-vue'
import vueJsx from '@vitejs/plugin-vue-jsx'
import AutoImport from 'unplugin-auto-import/vite'
import Components from 'unplugin-vue-components/vite'
import Icons from 'unplugin-icons/vite'
import IconsResolver from 'unplugin-icons/resolver'
import { NaiveUiResolver } from 'unplugin-vue-components/resolvers'
import vueDevTools from 'vite-plugin-vue-devtools'
import tailwindcss from '@tailwindcss/vite'
import { authCallbackMessages } from './src/locales/auth-callback.ts'

// https://vite.dev/config/
export default defineConfig({
  build: {
    manifest: true,
    rolldownOptions: {
      output: {
        codeSplitting: {
          groups: [
            {
              name: 'auth-callback',
              test: /\/src\/(?:auth\/(?:callback-entry|callback-shell|connection-test-relay)|locales\/auth-callback)\.ts$/,
              priority: 40,
            },
            {
              name: 'auth-exchange',
              test: /\/src\/auth\/callback-exchange\.ts$/,
              priority: 30,
            },
          ],
        },
      },
    },
  },
  plugins: [
    {
      name: 'auth-completion-headers',
      transformIndexHtml: {
        order: 'pre',
        handler(html) {
          return html.replace(/%AUTH_CALLBACK_(\w+)%/g, (_match, key: string) => {
            if (!(key in authCallbackMessages)) throw new Error(`Unknown callback message: ${key}`)
            return authCallbackMessages[key as keyof typeof authCallbackMessages]
          })
        },
      },
      generateBundle(_options, bundle) {
        const chunks = Object.values(bundle).filter((item) => item.type === 'chunk')
        const seen = new Set<string>()
        const visit = (chunk: (typeof chunks)[number]) => {
          if (seen.has(chunk.fileName)) return
          seen.add(chunk.fileName)
          for (const id of chunk.moduleIds) {
            const path = id.replaceAll('\\', '/')
            if (
              /\/src\/(?:main\.ts|i18n\.ts|App\.vue|api\/session-context\.ts|locales\/(?:index|zh-Hans)\.ts)(?:\?|$)|\/node_modules\/(?:vue|@vue|naive-ui|pinia|vue-i18n)\//.test(
                path,
              )
            )
              this.error(`Heavy dependency in auth callback: ${path}`)
          }
          for (const name of chunk.imports) {
            const dependency = bundle[name]
            if (dependency?.type === 'chunk') visit(dependency)
          }
        }
        for (const chunk of chunks) {
          if (
            chunk.moduleIds.some((id) =>
              /\/src\/(?:entry|auth\/callback-entry|auth\/callback-exchange)\.ts$/.test(
                id.replaceAll('\\', '/'),
              ),
            )
          )
            visit(chunk)
        }
      },
      configureServer(server) {
        server.middlewares.use((req, res, next) => {
          if (req.url?.split('?')[0] === '/auth/complete') {
            res.setHeader('Cache-Control', 'no-store')
            res.setHeader('Referrer-Policy', 'no-referrer')
          }
          next()
        })
      },
      configurePreviewServer(server) {
        server.middlewares.use((req, res, next) => {
          if (req.url?.split('?')[0] === '/auth/complete') {
            res.setHeader('Cache-Control', 'no-store')
            res.setHeader('Referrer-Policy', 'no-referrer')
          }
          next()
        })
      },
    },
    VueRouter({
      routesFolder: 'src/pages',
      dts: 'src/typed-router.d.ts',
    }),
    vue(),
    vueJsx(),
    AutoImport({
      imports: ['vue', VueRouterAutoImports, 'pinia'],
      dts: 'src/auto-imports.d.ts',
    }),
    Components({
      resolvers: [
        NaiveUiResolver(),
        IconsResolver({
          prefix: 'Icon',
          enabledCollections: ['carbon'],
        }),
      ],
      dts: 'src/components.d.ts',
      // Keep declarations available while parallel transforms discover their component imports.
      // Repeated prune/rewrite cycles can overlap file reads and writes on Windows.
      syncMode: 'append',
    }),
    Icons({
      autoInstall: true,
    }),
    tailwindcss(),
    vueDevTools(),
  ],
  resolve: {
    alias: {
      '@': fileURLToPath(new URL('./src', import.meta.url)),
    },
  },
})
