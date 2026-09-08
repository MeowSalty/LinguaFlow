<script setup lang="ts">
import { darkTheme, dateZhCN, zhCN, type GlobalThemeOverrides } from 'naive-ui'

import BootstrapNoticeHost from '@/components/BootstrapNoticeHost.vue'
import AppLayout from '@/layouts/AppLayout.vue'
import { useLocaleStore } from '@/stores/locale'
import { useThemeStore } from '@/stores/theme'
import { readLfTokens, type LfTokenName, type LfTokenState } from '@/utils/themeTokens'

const route = useRoute()
const locale = useLocaleStore()
const theme = useThemeStore()
const isBlank = computed(() => route.meta.layout === 'blank')
const naiveTheme = computed(() => (theme.isDark ? darkTheme : null))

// naive-ui 主题色的唯一来源是 tailwind.css 中的 --lf-* CSS 变量。
// 主题切换时 theme store 先写入 html[data-theme]，post 时序确保此处重读到新主题的值。
const lfTokens = reactive<LfTokenState>({})

watch(
  () => theme.resolvedTheme,
  () => {
    Object.assign(lfTokens, readLfTokens())
  },
  { immediate: true, flush: 'post' },
)

const tok = (name: LfTokenName): string | undefined => lfTokens[name]

const themeOverrides = computed<GlobalThemeOverrides>(() => ({
  common: {
    primaryColor: tok('--lf-brand-500'),
    primaryColorHover: tok('--lf-brand-400'),
    primaryColorPressed: tok('--lf-brand-700'),
    primaryColorSuppl: tok('--lf-brand-400'),
    infoColor: tok('--lf-info'),
    successColor: tok('--lf-success'),
    warningColor: tok('--lf-warning'),
    errorColor: tok('--lf-danger'),
    borderRadius: '8px',
    borderRadiusSmall: '6px',
    fontFamily:
      "Inter, ui-sans-serif, system-ui, -apple-system, BlinkMacSystemFont, 'Segoe UI', Roboto, 'Helvetica Neue', Arial, 'Noto Sans SC', sans-serif",
    fontFamilyMono:
      "ui-monospace, SFMono-Regular, Menlo, Monaco, Consolas, 'Liberation Mono', 'Courier New', monospace",
    bodyColor: tok('--lf-bg'),
    cardColor: tok('--lf-surface'),
    modalColor: tok('--lf-surface'),
    popoverColor: tok('--lf-surface-elevated'),
    tableColor: tok('--lf-surface'),
    inputColor: tok('--lf-surface'),
    borderColor: tok('--lf-border'),
    dividerColor: tok('--lf-border-soft'),
    textColorBase: tok('--lf-text'),
    textColor1: tok('--lf-text-strong'),
    textColor2: tok('--lf-text-muted'),
    textColor3: tok('--lf-text-subtle'),
    hoverColor: tok('--lf-hover'),
    boxShadow1: tok('--lf-shadow-1'),
    boxShadow2: tok('--lf-shadow-2'),
    boxShadow3: tok('--lf-shadow-3'),
  },
  Button: {
    fontWeight: '500',
    heightMedium: '34px',
    paddingMedium: '0 14px',
    borderRadiusMedium: '8px',
  },
  Card: {
    borderRadius: '10px',
    paddingMedium: '20px',
  },
  Input: {
    borderRadius: '8px',
    heightMedium: '34px',
    // naive 暗色主题将输入框静止边框硬编码为透明（border: 1px solid #0000），
    // 与亮色的 borderColor 派生值不对称；此处显式取 token 保证明暗一致
    border: `1px solid ${tok('--lf-border')}`,
    borderDisabled: `1px solid ${tok('--lf-border')}`,
  },
  Select: {
    peers: {
      InternalSelection: {
        borderRadius: '8px',
        heightMedium: '34px',
        border: `1px solid ${tok('--lf-border')}`,
        borderDisabled: `1px solid ${tok('--lf-border')}`,
      },
    },
  },
  Tag: {
    borderRadius: '999px',
    heightSmall: '22px',
    fontSizeSmall: '12px',
  },
  Drawer: {
    borderRadius: '10px',
  },
  DataTable: {
    borderRadius: '10px',
    thColor: tok('--lf-surface-muted'),
    thColorModal: tok('--lf-surface-muted'),
    thTextColor: tok('--lf-text-subtle'),
    thFontWeight: '600',
    tdColor: tok('--lf-surface'),
    tdColorHover: tok('--lf-hover'),
    tdTextColor: tok('--lf-text'),
    borderColor: tok('--lf-border-soft'),
    thPaddingMedium: '10px 14px',
    tdPaddingMedium: '12px 14px',
    thPaddingSmall: '10px 12px',
    tdPaddingSmall: '12px',
  },
  Tabs: {
    tabBorderRadius: '8px',
    tabFontWeightActive: '600',
  },
}))

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
      <NNotificationProvider>
        <BootstrapNoticeHost />
        <NDialogProvider>
          <RouterView v-slot="{ Component }">
            <component :is="Component" v-if="isBlank" />
            <AppLayout v-else>
              <component :is="Component" />
            </AppLayout>
          </RouterView>
        </NDialogProvider>
      </NNotificationProvider>
    </NMessageProvider>
  </NConfigProvider>
</template>
