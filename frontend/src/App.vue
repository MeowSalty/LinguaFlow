<script setup lang="ts">
import { dateZhCN, zhCN } from 'naive-ui'

import AppLayout from '@/layouts/AppLayout.vue'
import { useLocaleStore } from '@/stores/locale'

const route = useRoute()
const locale = useLocaleStore()
const isBlank = computed(() => route.meta.layout === 'blank')

const naiveLocale = computed(() => {
  switch (locale.currentLocale) {
    case 'zh-CN':
    default:
      return zhCN
  }
})

const naiveDateLocale = computed(() => {
  switch (locale.currentLocale) {
    case 'zh-CN':
    default:
      return dateZhCN
  }
})
</script>

<template>
  <NConfigProvider
    :locale="naiveLocale"
    :date-locale="naiveDateLocale"
    :theme-overrides="{
      common: {
        primaryColor: '#18a058',
        primaryColorHover: '#36ad6a',
        primaryColorPressed: '#0c7a43',
      },
    }"
  >
    <NMessageProvider>
      <RouterView v-slot="{ Component }">
        <component :is="Component" v-if="isBlank" />
        <AppLayout v-else>
          <component :is="Component" />
        </AppLayout>
      </RouterView>
    </NMessageProvider>
  </NConfigProvider>
</template>
