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

// https://vite.dev/config/
export default defineConfig({
  plugins: [
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
