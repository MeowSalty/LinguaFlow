<script setup lang="ts">
import { useI18n } from 'vue-i18n'
import { useMessage, type DropdownOption } from 'naive-ui'

import { useAuthStore } from '@/stores/auth'
import { useLocaleStore } from '@/stores/locale'
import { useServiceStore } from '@/stores/service'
import { useThemeStore, type ThemeMode } from '@/stores/theme'

const router = useRouter()
const auth = useAuthStore()
const locale = useLocaleStore()
const service = useServiceStore()
const theme = useThemeStore()
const message = useMessage()
const { t } = useI18n()

const displayName = computed(() => {
  if (!auth.user) {
    return ''
  }
  return auth.user.display_name?.trim() || auth.user.username
})

const initial = computed(() => {
  const name = displayName.value
  return name ? name.charAt(0).toUpperCase() : '?'
})

const userOptions = computed<DropdownOption[]>(() => [
  {
    key: 'username-info',
    type: 'render',
    render: () =>
      h('div', { class: 'px-3 py-2 min-w-[180px]' }, [
        h('div', { class: 'text-sm font-medium text-lf-text-strong' }, displayName.value),
        auth.user?.email
          ? h('div', { class: 'text-xs text-lf-text-muted mt-0.5' }, auth.user.email)
          : null,
      ]),
  },
  { type: 'divider', key: 'divider-1' },
  { label: t('layout.userMenu.switchService'), key: 'switch-service' },
  { label: t('layout.userMenu.logout'), key: 'logout' },
])

const localeOptions = computed<DropdownOption[]>(() =>
  locale.availableLocales.map((item) => ({
    label: t(item.labelKey),
    key: item.code,
  })),
)

const themeOptions = computed<DropdownOption[]>(() => [
  { label: `◐ ${t('theme.system')}`, key: 'system' },
  { label: `☀ ${t('theme.light')}`, key: 'light' },
  { label: `☾ ${t('theme.dark')}`, key: 'dark' },
])

const themeIcon = computed(() => {
  if (theme.mode === 'system') {
    return '◐'
  }
  return theme.resolvedTheme === 'dark' ? '☾' : '☀'
})

const onSelectUserAction = async (key: string | number) => {
  if (key === 'logout') {
    try {
      await auth.logout()
      message.success(t('layout.messages.logoutSuccess'))
      await router.push({ path: '/login' })
    } catch (error) {
      console.error(error)
      message.error(t('layout.messages.logoutFailed'))
    }
  } else if (key === 'switch-service') {
    await router.push({ path: '/service' })
  }
}

const onSelectTheme = (key: string | number): void => {
  theme.setMode(String(key) as ThemeMode)
}

const onSelectLocale = (key: string | number): void => {
  locale.setLocale(String(key))
}
</script>

<template>
  <div class="min-h-screen flex flex-col bg-lf-bg text-lf-text">
    <header
      class="sticky top-0 z-10 flex h-16 items-center gap-8 border-b border-lf-border bg-lf-surface px-8 backdrop-blur"
    >
      <RouterLink to="/" class="text-xl font-bold tracking-tight text-brand-500 no-underline">
        {{ t('common.appName') }}
      </RouterLink>

      <nav class="flex items-center gap-6 text-sm" :aria-label="t('nav.main')">
        <RouterLink
          to="/"
          class="text-lf-text-muted no-underline transition-colors hover:text-brand-500"
          active-class="!text-brand-500 font-semibold"
        >
          {{ t('nav.dashboard') }}
        </RouterLink>
        <RouterLink
          to="/projects"
          class="text-lf-text-muted no-underline transition-colors hover:text-brand-500"
          active-class="!text-brand-500 font-semibold"
        >
          {{ t('nav.projects') }}
        </RouterLink>
        <RouterLink
          to="/backends"
          class="text-lf-text-muted no-underline transition-colors hover:text-brand-500"
          active-class="!text-brand-500 font-semibold"
        >
          {{ t('nav.backends') }}
        </RouterLink>
        <RouterLink
          to="/about"
          class="text-lf-text-muted no-underline transition-colors hover:text-brand-500"
          active-class="!text-brand-500 font-semibold"
        >
          {{ t('nav.about') }}
        </RouterLink>
      </nav>

      <div class="ml-auto flex items-center gap-4">
        <NDropdown
          v-if="locale.hasMultipleLocales"
          trigger="click"
          :options="localeOptions"
          placement="bottom-end"
          @select="onSelectLocale"
        >
          <NButton quaternary size="small">
            {{ t('common.language') }}
          </NButton>
        </NDropdown>
        <NDropdown
          trigger="click"
          :options="themeOptions"
          placement="bottom-end"
          @select="onSelectTheme"
        >
          <NButton quaternary circle :title="t('common.theme')" :aria-label="t('common.theme')">
            {{ themeIcon }}
          </NButton>
        </NDropdown>
        <span class="hidden text-xs text-lf-text-subtle sm:inline" :title="service.baseUrl">
          {{ service.displayName }}
        </span>
        <NDropdown
          v-if="auth.user"
          trigger="click"
          :options="userOptions"
          placement="bottom-end"
          @select="onSelectUserAction"
        >
          <button
            type="button"
            class="flex items-center gap-2 rounded-full border border-lf-border bg-lf-surface px-2 py-1 transition-colors hover:border-brand-500"
          >
            <NAvatar round size="small" :style="{ backgroundColor: '#18a058' }">
              {{ initial }}
            </NAvatar>
            <span class="pr-2 text-sm text-lf-text">{{ displayName }}</span>
          </button>
        </NDropdown>
      </div>
    </header>

    <main class="flex-1 px-8 py-10">
      <div class="mx-auto max-w-275">
        <slot />
      </div>
    </main>
  </div>
</template>
