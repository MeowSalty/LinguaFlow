import DefaultTheme from 'vitepress/theme'
import type { Theme } from 'vitepress'
import { h } from 'vue'
import VersionSwitcher from './components/VersionSwitcher.vue'
import './custom.css'

export default {
  extends: DefaultTheme,
  Layout() {
    return h(DefaultTheme.Layout, null, {
      'nav-bar-content-after': () => h(VersionSwitcher),
    })
  },
  enhanceApp({ app }) {
    app.component('VersionSwitcher', VersionSwitcher)
  },
} satisfies Theme
