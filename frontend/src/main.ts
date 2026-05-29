import './styles/tailwind.css'

import { createApp } from 'vue'
import { createRouter, createWebHistory } from 'vue-router'
import { routes } from 'vue-router/auto-routes'
import { createPinia } from 'pinia'

import App from './App.vue'
import { i18n } from './i18n'
import { installRouterGuards } from './router/guards'
import { useAuthStore } from './stores/auth'
import { useLocaleStore } from './stores/locale'
import { useServiceStore } from './stores/service'
import { useThemeStore } from './stores/theme'

const router = createRouter({
  history: createWebHistory(),
  routes,
})

const app = createApp(App)
const pinia = createPinia()

app.use(pinia)
app.use(i18n)

useLocaleStore()
useThemeStore().initTheme()

// 立即从 localStorage 恢复服务地址 & 登录态 (bootstrap 的同步部分在第一个 await 前完成，
// 因此守卫能立即得到正确的 isAuthenticated)
useServiceStore()
void useAuthStore().bootstrap()

app.use(router)
installRouterGuards(router)

app.mount('#app')
