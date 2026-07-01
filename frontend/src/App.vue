<script setup lang="ts">
import { darkTheme, dateZhCN, zhCN, type GlobalThemeOverrides } from 'naive-ui'

import BootstrapNoticeHost from '@/components/BootstrapNoticeHost.vue'
import AppLayout from '@/layouts/AppLayout.vue'
import { useLocaleStore } from '@/stores/locale'
import { useThemeStore } from '@/stores/theme'

const route = useRoute()
const locale = useLocaleStore()
const theme = useThemeStore()
const isBlank = computed(() => route.meta.layout === 'blank')
const naiveTheme = computed(() => (theme.isDark ? darkTheme : null))

const themeOverrides: GlobalThemeOverrides = {
  common: {
    primaryColor: '#18a058',
    primaryColorHover: '#36ad6a',
    primaryColorPressed: '#0c7a43',
    primaryColorSuppl: '#36ad6a',
    borderRadius: '8px',
  },
}

const naiveLocale = computed(() => {
  switch (locale.currentLocale) {
    case 'zh-Hans':
    default:
      return zhCN
  }
})

const naiveDateLocale = computed(() => {
  switch (locale.currentLocale) {
    case 'zh-Hans':
    default:
      return dateZhCN
  }
})
</script>

<template>
  <NConfigProvider
    :locale="naiveLocale"
    :date-locale="naiveDateLocale"
    :theme="naiveTheme"
    :theme-overrides="themeOverrides"
  >
    <NMessageProvider>
      <BootstrapNoticeHost />
      <NDialogProvider>
        <RouterView v-slot="{ Component }">
          <component :is="Component" v-if="isBlank" />
          <AppLayout v-else>
            <component :is="Component" />
          </AppLayout>
        </RouterView>
      </NDialogProvider>
    </NMessageProvider>
  </NConfigProvider>
</template>
