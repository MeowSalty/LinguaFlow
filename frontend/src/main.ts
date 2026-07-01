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

await bootstrapApp()

app.use(router)
installRouterGuards(router)

app.mount('#app')
