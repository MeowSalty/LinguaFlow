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

const themeOverrides = computed<GlobalThemeOverrides>(() => {
  const isDark = theme.isDark

  return {
    common: {
      primaryColor: '#10b981',
      primaryColorHover: '#34d399',
      primaryColorPressed: '#059669',
      primaryColorSuppl: '#34d399',
      infoColor: isDark ? '#60a5fa' : '#3b82f6',
      successColor: '#10b981',
      warningColor: '#f59e0b',
      errorColor: '#ef4444',
      borderRadius: '10px',
      borderRadiusSmall: '8px',
      fontFamily:
        "Inter, ui-sans-serif, system-ui, -apple-system, BlinkMacSystemFont, 'Segoe UI', Roboto, 'Helvetica Neue', Arial, 'Noto Sans SC', sans-serif",
      fontFamilyMono:
        "ui-monospace, SFMono-Regular, Menlo, Monaco, Consolas, 'Liberation Mono', 'Courier New', monospace",
      bodyColor: isDark ? '#0b1118' : '#f4f7fb',
      cardColor: isDark ? '#121a24' : '#ffffff',
      modalColor: isDark ? '#121a24' : '#ffffff',
      popoverColor: isDark ? '#172131' : '#ffffff',
      tableColor: isDark ? '#121a24' : '#ffffff',
      inputColor: isDark ? '#0e151e' : '#ffffff',
      borderColor: isDark ? '#243041' : '#e2e8f0',
      dividerColor: isDark ? '#1a2433' : '#edf2f7',
      textColorBase: isDark ? '#e2e8f0' : '#0f172a',
      textColor1: isDark ? '#f8fafc' : '#020617',
      textColor2: isDark ? '#94a3b8' : '#64748b',
      textColor3: isDark ? '#64748b' : '#94a3b8',
      hoverColor: isDark ? 'rgba(16, 185, 129, 0.08)' : 'rgba(16, 185, 129, 0.06)',
      boxShadow1: isDark ? '0 1px 2px rgba(0,0,0,0.32)' : '0 1px 2px rgba(15,23,42,0.06)',
      boxShadow2: isDark ? '0 8px 24px rgba(0,0,0,0.4)' : '0 8px 24px rgba(15,23,42,0.08)',
      boxShadow3: isDark ? '0 16px 40px rgba(0,0,0,0.48)' : '0 16px 40px rgba(15,23,42,0.12)',
    },
    Button: {
      fontWeight: '500',
      heightMedium: '36px',
      paddingMedium: '0 16px',
      borderRadiusMedium: '10px',
    },
    Card: {
      borderRadius: '16px',
      paddingMedium: '20px',
      color: isDark ? '#121a24' : '#ffffff',
      colorModal: isDark ? '#121a24' : '#ffffff',
    },
    Input: {
      borderRadius: '10px',
      heightMedium: '36px',
    },
    Select: {
      peers: {
        InternalSelection: {
          borderRadius: '10px',
          heightMedium: '36px',
        },
      },
    },
    Tag: {
      borderRadius: '999px',
      heightSmall: '22px',
      fontSizeSmall: '12px',
    },
    Drawer: {
      borderRadius: '16px',
    },
    DataTable: {
      borderRadius: '12px',
      thColor: isDark ? '#0e151e' : '#f7f9fc',
      thColorModal: isDark ? '#0e151e' : '#f7f9fc',
      thTextColor: isDark ? '#64748b' : '#94a3b8',
      thFontWeight: '500',
      tdColor: isDark ? '#121a24' : '#ffffff',
      tdColorHover: isDark ? 'rgba(16, 185, 129, 0.06)' : 'rgba(16, 185, 129, 0.04)',
      tdTextColor: isDark ? '#e2e8f0' : '#0f172a',
      borderColor: isDark ? '#1a2433' : '#edf2f7',
      thPaddingMedium: '10px 14px',
      tdPaddingMedium: '14px',
      thPaddingSmall: '10px 12px',
      tdPaddingSmall: '12px',
    },
    Tabs: {
      tabBorderRadius: '10px',
      tabFontWeightActive: '600',
    },
  }
})

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
