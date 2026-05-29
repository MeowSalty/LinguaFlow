import { defineStore } from 'pinia'
import { computed, ref, watch } from 'vue'

export type ThemeMode = 'light' | 'dark' | 'system'
export type ResolvedTheme = 'light' | 'dark'

const STORAGE_KEY = 'linguaflow.theme'
const themeModes = ['light', 'dark', 'system'] as const

const isThemeMode = (value: string | null): value is ThemeMode => {
  return themeModes.includes(value as ThemeMode)
}

const readStoredMode = (): ThemeMode => {
  if (typeof window === 'undefined') {
    return 'system'
  }

  const stored = window.localStorage.getItem(STORAGE_KEY)
  return isThemeMode(stored) ? stored : 'system'
}

const getSystemTheme = (): ResolvedTheme => {
  if (typeof window === 'undefined') {
    return 'light'
  }

  return window.matchMedia('(prefers-color-scheme: dark)').matches ? 'dark' : 'light'
}

export const useThemeStore = defineStore('theme', () => {
  const mode = ref<ThemeMode>(readStoredMode())
  const systemTheme = ref<ResolvedTheme>(getSystemTheme())
  const initialized = ref(false)

  const resolvedTheme = computed<ResolvedTheme>(() =>
    mode.value === 'system' ? systemTheme.value : mode.value,
  )
  const isDark = computed(() => resolvedTheme.value === 'dark')

  const applyTheme = (): void => {
    if (typeof document === 'undefined') {
      return
    }

    document.documentElement.dataset.theme = resolvedTheme.value
    document.documentElement.style.colorScheme = resolvedTheme.value
  }

  const setMode = (nextMode: ThemeMode): void => {
    mode.value = nextMode

    if (typeof window !== 'undefined') {
      window.localStorage.setItem(STORAGE_KEY, nextMode)
    }
  }

  const initTheme = (): void => {
    if (initialized.value || typeof window === 'undefined') {
      applyTheme()
      return
    }

    const mediaQuery = window.matchMedia('(prefers-color-scheme: dark)')
    const handleSystemThemeChange = (event: MediaQueryListEvent): void => {
      systemTheme.value = event.matches ? 'dark' : 'light'
    }

    systemTheme.value = mediaQuery.matches ? 'dark' : 'light'
    mediaQuery.addEventListener('change', handleSystemThemeChange)
    initialized.value = true
    applyTheme()
  }

  watch(resolvedTheme, applyTheme)

  return {
    mode,
    resolvedTheme,
    isDark,
    setMode,
    initTheme,
  }
})
