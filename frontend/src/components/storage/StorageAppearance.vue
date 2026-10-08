<script setup lang="ts">
import { computed } from 'vue'
import { darkTheme, NConfigProvider, useThemeVars, type GlobalThemeOverrides } from 'naive-ui'
import { useThemeStore } from '@/stores/theme'

const theme = useThemeStore()
const inherited = useThemeVars()
const overrides = computed<GlobalThemeOverrides>(() => {
  const dark = theme.isDark
  const primaryText = dark ? '#0f172a' : '#ffffff'
  return {
    common: {
      // The parent maps these inherited colors to --lf-text-muted and --lf-text.
      textColor3: inherited.value.textColor2,
      placeholderColor: inherited.value.textColor2,
      primaryColor: dark ? '#60a5fa' : '#2563eb',
      primaryColorHover: dark ? '#93c5fd' : '#1d4ed8',
      primaryColorPressed: dark ? '#60a5fa' : '#1e40af',
      primaryColorSuppl: dark ? '#93c5fd' : '#1d4ed8',
      errorColor: dark ? '#fca5a5' : '#b91c1c',
    },
    Empty: {
      textColor: inherited.value.textColorBase,
      extraTextColor: inherited.value.textColor2,
    },
    Button: {
      textColorPrimary: primaryText,
      textColorHoverPrimary: primaryText,
      textColorPressedPrimary: primaryText,
      textColorFocusPrimary: primaryText,
      textColorDisabledPrimary: primaryText,
    },
    Tag: {
      textColorPrimary: dark ? '#93c5fd' : '#1e40af',
      textColorInfo: dark ? '#93c5fd' : '#1e40af',
      textColorSuccess: dark ? '#86efac' : '#166534',
      textColorWarning: dark ? '#fcd34d' : '#92400e',
      textColorError: dark ? '#fca5a5' : '#991b1b',
    },
  }
})
</script>

<template>
  <NConfigProvider abstract :theme="theme.isDark ? darkTheme : null" :theme-overrides="overrides">
    <slot />
  </NConfigProvider>
</template>
