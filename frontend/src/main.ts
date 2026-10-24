import './styles/tailwind.css'

import { createApp } from 'vue'
import { createRouter, createWebHistory } from 'vue-router'
import { routes } from 'vue-router/auto-routes'
import { createPinia } from 'pinia'

import App from './App.vue'
import { bootstrapApp } from './bootstrap'
import { i18n } from './i18n'
import { installRouterGuards } from './router/guards'
import { useLocaleStore } from './stores/locale'
import { useThemeStore } from './stores/theme'
import { installSessionIsolation } from './stores/sessionIsolation'
import { installCrossTabSessionSync } from './auth/cross-tab-session'
import { useServiceStore } from './stores/service'
import {
  assertCallbackPrepared,
  finishCallbackStartup,
  hasPreparedCallback,
} from './auth/callback-startup'
import { releaseCallbackShell } from './auth/callback-shell'
import {
  activateExternalCallback,
  installExternalAuthLifecycle,
  prepareExternalCallback,
} from './auth/external-browser'

const callbackPrepared = hasPreparedCallback()
assertCallbackPrepared()
installExternalAuthLifecycle()
if (!callbackPrepared) prepareExternalCallback()

const router = createRouter({
  history: createWebHistory(),
  // 旧即时翻译页已并入首页，保留重定向以兼容历史链接与书签
  routes: [...routes, { path: '/tools/quick-translate', redirect: '/' }],
})

const app = createApp(App)
const pinia = createPinia()
installSessionIsolation(pinia)

app.use(pinia)
app.use(i18n)

useLocaleStore()
useThemeStore().initTheme()

installCrossTabSessionSync(() => useServiceStore().isLocal)
await bootstrapApp()
if (callbackPrepared) assertCallbackPrepared()
else activateExternalCallback()

installRouterGuards(router)
app.use(router)
await router.isReady()
if (callbackPrepared) {
  if (router.currentRoute.value.meta.layout !== 'blank') await import('./layouts/AppLayout.vue')
  assertCallbackPrepared()
}

app.mount('#app')
releaseCallbackShell()
finishCallbackStartup()
