<script setup lang="ts">
import { useI18n } from 'vue-i18n'
import { useMessage, type DropdownOption } from 'naive-ui'
import { Icon as IconifyIcon } from '@iconify/vue'

import { useAuthStore } from '@/stores/auth'
import { useLocaleStore } from '@/stores/locale'
import { useServiceStore } from '@/stores/service'
import { useThemeStore, type ThemeMode } from '@/stores/theme'

const router = useRouter()
const route = useRoute()
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

const serviceSummary = computed(() => {
  if (service.isLocal) {
    return t('layout.localModeBadge')
  }
  return service.displayName.trim() || service.baseUrl
})

const userOptions = computed<DropdownOption[]>(() => {
  const items: DropdownOption[] = [
    {
      key: 'username-info',
      type: 'render',
      render: () =>
        h('div', { class: 'px-3 py-2 min-w-[180px]' }, [
          h('div', { class: 'text-sm font-medium text-lf-text-strong' }, displayName.value),
          auth.user?.email
            ? h('div', { class: 'text-xs text-lf-text-muted mt-0.5' }, auth.user.email)
            : null,
          h(
            'div',
            {
              class: 'mt-1.5 truncate text-[11px] font-mono text-lf-text-subtle',
              title: service.baseUrl,
            },
            serviceSummary.value,
          ),
        ]),
    },
    { type: 'divider', key: 'divider-1' },
    {
      label: t('nav.changelog'),
      key: 'changelog',
      icon: () => h(IconifyIcon, { icon: 'carbon:catalog', class: 'text-base' }),
    },
    {
      label: t('nav.about'),
      key: 'about',
      icon: () => h(IconifyIcon, { icon: 'carbon:information', class: 'text-base' }),
    },
    { type: 'divider', key: 'divider-2' },
  ]

  if (service.isLocal) {
    items.push({
      label: t('layout.userMenu.connectRemoteService'),
      key: 'switch-service',
    })
  } else {
    items.push(
      { label: t('layout.userMenu.switchService'), key: 'switch-service' },
      { label: t('layout.userMenu.logout'), key: 'logout' },
    )
  }

  return items
})

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
    return 'carbon:contrast'
  }
  return theme.resolvedTheme === 'dark' ? 'carbon:moon' : 'carbon:sun'
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
    const query = service.isLocal ? { force: '1' } : {}
    await router.push({ path: '/service', query })
  } else if (key === 'changelog') {
    await router.push({ path: '/changelog' })
  } else if (key === 'about') {
    await router.push({ path: '/about' })
  }
}

interface NavItem {
  path: string
  icon: string
  labelKey: string
}

const toDropdownOption = (item: NavItem): DropdownOption => ({
  label: t(item.labelKey),
  key: item.path,
  icon: () => h(IconifyIcon, { icon: item.icon, class: 'text-base' }),
})

const templateNavItems = computed<NavItem[]>(() => [
  { path: '/prompt-templates', icon: 'carbon:prompt-template', labelKey: 'nav.promptTemplates' },
  {
    path: '/bootstrap-prompt-templates',
    icon: 'carbon:text-mining',
    labelKey: 'nav.bootstrapPromptTemplates',
  },
  { path: '/prune-prompt-templates', icon: 'carbon:clean', labelKey: 'nav.prunePromptTemplates' },
  { path: '/execution-profiles', icon: 'carbon:flow', labelKey: 'nav.executionProfiles' },
  {
    path: '/execution-plan-templates',
    icon: 'carbon:plan',
    labelKey: 'nav.executionPlanTemplates',
  },
])

const templateNavOptions = computed<DropdownOption[]>(() =>
  templateNavItems.value.map(toDropdownOption),
)

const isAdmin = computed(() => auth.user?.role === 'admin')

const isTemplateRoute = computed(() =>
  templateNavItems.value.some((item) => route.path.startsWith(item.path)),
)

const toolsNavItems = computed<NavItem[]>(() => [
  { path: '/tools/quick-translate', icon: 'carbon:translate', labelKey: 'nav.quickTranslate' },
  {
    path: '/tools/epub-rotate',
    icon: 'carbon:text-vertical-alignment',
    labelKey: 'nav.epubRotate',
  },
])

const toolsNavOptions = computed<DropdownOption[]>(() => toolsNavItems.value.map(toDropdownOption))

const isToolsRoute = computed(() => route.path.startsWith('/tools'))

const isAdminRoute = computed(() => route.path.startsWith('/admin'))

const onSelectTemplateNav = (key: string | number): void => {
  router.push(String(key))
}

const onSelectToolsNav = (key: string | number): void => {
  router.push(String(key))
}

const onSelectTheme = (key: string | number): void => {
  theme.setMode(String(key) as ThemeMode)
}

const onSelectLocale = (key: string | number): void => {
  locale.setLocale(String(key))
}

const navLinkClass =
  'relative flex items-center gap-1.5 rounded-lg px-2.5 py-1.5 text-lf-text-muted no-underline transition-colors hover:bg-lf-surface-muted hover:text-lf-text-strong'
const navActiveClass = '!bg-lf-brand-soft !text-brand-600 font-semibold'

const mobileNavOpen = ref(false)

const navigateTo = (path: string): void => {
  mobileNavOpen.value = false
  void router.push(path)
}
</script>

<template>
  <div class="flex min-h-screen flex-col bg-lf-bg text-lf-text">
    <header
      class="sticky top-0 z-20 border-b border-lf-border-soft backdrop-blur-xl"
      style="background: var(--lf-header-bg)"
    >
      <div class="mx-auto flex h-14 max-w-275 items-center gap-6 px-6 lg:px-8">
        <RouterLink
          to="/"
          class="group inline-flex shrink-0 items-center gap-2 text-lg font-semibold tracking-tight text-lf-text-strong no-underline"
        >
          <span
            class="flex h-7 w-7 items-center justify-center rounded-lg bg-brand-500 text-xs font-bold text-white shadow-sm shadow-brand-500/30"
          >
            L
          </span>
          <span class="hidden sm:inline">{{ t('common.appName') }}</span>
        </RouterLink>

        <NButton
          quaternary
          circle
          class="flex! md:hidden!"
          :aria-label="t('nav.menu')"
          :title="t('nav.menu')"
          @click="mobileNavOpen = true"
        >
          <template #icon>
            <IconifyIcon icon="carbon:menu" class="text-lg" />
          </template>
        </NButton>

        <nav
          class="hidden min-w-0 flex-1 items-center gap-1 overflow-hidden text-sm md:flex"
          :aria-label="t('nav.main')"
        >
          <RouterLink to="/" :class="[navLinkClass]" :active-class="navActiveClass">
            <IconifyIcon icon="carbon:dashboard" class="text-base" />
            <span class="whitespace-nowrap">{{ t('nav.dashboard') }}</span>
          </RouterLink>
          <RouterLink to="/projects" :class="[navLinkClass]" :active-class="navActiveClass">
            <IconifyIcon icon="carbon:folder" class="text-base" />
            <span class="whitespace-nowrap">{{ t('nav.projects') }}</span>
          </RouterLink>
          <RouterLink to="/backends" :class="[navLinkClass]" :active-class="navActiveClass">
            <IconifyIcon icon="carbon:server-proxy" class="text-base" />
            <span class="whitespace-nowrap">{{ t('nav.backends') }}</span>
          </RouterLink>
          <RouterLink to="/stats" :class="[navLinkClass]" :active-class="navActiveClass">
            <IconifyIcon icon="carbon:chart-bar" class="text-base" />
            <span class="whitespace-nowrap">{{ t('nav.stats') }}</span>
          </RouterLink>
          <NDropdown
            trigger="hover"
            :options="templateNavOptions"
            placement="bottom-start"
            @select="onSelectTemplateNav"
          >
            <RouterLink
              to="/prompt-templates"
              :class="[navLinkClass, { [navActiveClass]: isTemplateRoute }]"
            >
              <IconifyIcon icon="carbon:settings" class="text-base" />
              <span class="whitespace-nowrap">{{ t('nav.executionConfig') }}</span>
            </RouterLink>
          </NDropdown>
          <NDropdown
            trigger="hover"
            :options="toolsNavOptions"
            placement="bottom-start"
            @select="onSelectToolsNav"
          >
            <RouterLink
              to="/tools/epub-rotate"
              :class="[navLinkClass, { [navActiveClass]: isToolsRoute }]"
            >
              <IconifyIcon icon="carbon:tool-kit" class="text-base" />
              <span class="whitespace-nowrap">{{ t('nav.tools') }}</span>
            </RouterLink>
          </NDropdown>
          <RouterLink
            v-if="isAdmin"
            to="/admin"
            :class="[navLinkClass, { [navActiveClass]: isAdminRoute }]"
          >
            <IconifyIcon icon="carbon:security" class="text-base" />
            <span class="whitespace-nowrap">{{ t('nav.admin') }}</span>
          </RouterLink>
        </nav>

        <div class="ml-auto flex shrink-0 items-center gap-2 sm:gap-3">
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
              <template #icon>
                <IconifyIcon :icon="themeIcon" class="text-lg" />
              </template>
            </NButton>
          </NDropdown>
          <NTag v-if="service.isLocal" size="small" type="success" :bordered="false">
            {{ t('layout.localModeBadge') }}
          </NTag>
          <NDropdown
            v-if="auth.user"
            trigger="click"
            :options="userOptions"
            placement="bottom-end"
            @select="onSelectUserAction"
          >
            <button
              type="button"
              class="flex items-center gap-2 rounded-full border border-lf-border-soft bg-lf-surface px-1.5 py-1 transition-colors hover:border-brand-500/40 hover:bg-lf-surface-elevated"
            >
              <NAvatar round size="small" class="bg-brand-500 text-xs font-semibold text-white">
                {{ initial }}
              </NAvatar>
              <span class="hidden pr-2 text-sm text-lf-text sm:inline">{{ displayName }}</span>
            </button>
          </NDropdown>
        </div>
      </div>
    </header>

    <main class="flex-1 px-6 py-8 lg:px-8">
      <div class="mx-auto max-w-275">
        <slot />
      </div>
    </main>

    <NDrawer v-model:show="mobileNavOpen" placement="left" :width="280">
      <NDrawerContent :title="t('common.appName')" closable>
        <nav class="flex flex-col gap-1 py-2">
          <button
            type="button"
            class="flex items-center gap-2.5 rounded-lg px-3 py-2.5 text-left text-sm transition-colors hover:bg-lf-surface-muted"
            :class="{ '!bg-lf-brand-soft !text-brand-600 font-semibold': route.path === '/' }"
            @click="navigateTo('/')"
          >
            <IconifyIcon icon="carbon:dashboard" class="text-base" />
            {{ t('nav.dashboard') }}
          </button>
          <button
            type="button"
            class="flex items-center gap-2.5 rounded-lg px-3 py-2.5 text-left text-sm transition-colors hover:bg-lf-surface-muted"
            :class="{
              '!bg-lf-brand-soft !text-brand-600 font-semibold': route.path.startsWith('/projects'),
            }"
            @click="navigateTo('/projects')"
          >
            <IconifyIcon icon="carbon:folder" class="text-base" />
            {{ t('nav.projects') }}
          </button>
          <button
            type="button"
            class="flex items-center gap-2.5 rounded-lg px-3 py-2.5 text-left text-sm transition-colors hover:bg-lf-surface-muted"
            :class="{
              '!bg-lf-brand-soft !text-brand-600 font-semibold': route.path.startsWith('/backends'),
            }"
            @click="navigateTo('/backends')"
          >
            <IconifyIcon icon="carbon:server-proxy" class="text-base" />
            {{ t('nav.backends') }}
          </button>
          <button
            type="button"
            class="flex items-center gap-2.5 rounded-lg px-3 py-2.5 text-left text-sm transition-colors hover:bg-lf-surface-muted"
            :class="{
              '!bg-lf-brand-soft !text-brand-600 font-semibold': route.path.startsWith('/stats'),
            }"
            @click="navigateTo('/stats')"
          >
            <IconifyIcon icon="carbon:chart-bar" class="text-base" />
            {{ t('nav.stats') }}
          </button>

          <div class="mt-2 px-3 text-xs font-medium text-lf-text-subtle">
            {{ t('nav.executionConfig') }}
          </div>
          <button
            v-for="item in templateNavItems"
            :key="item.path"
            type="button"
            class="flex items-center gap-2.5 rounded-lg px-3 py-2.5 text-left text-sm transition-colors hover:bg-lf-surface-muted"
            :class="{
              '!bg-lf-brand-soft !text-brand-600 font-semibold': route.path.startsWith(item.path),
            }"
            @click="navigateTo(item.path)"
          >
            <IconifyIcon :icon="item.icon" class="text-base" />
            {{ t(item.labelKey) }}
          </button>

          <div class="mt-2 px-3 text-xs font-medium text-lf-text-subtle">
            {{ t('nav.tools') }}
          </div>
          <button
            v-for="item in toolsNavItems"
            :key="item.path"
            type="button"
            class="flex items-center gap-2.5 rounded-lg px-3 py-2.5 text-left text-sm transition-colors hover:bg-lf-surface-muted"
            :class="{
              '!bg-lf-brand-soft !text-brand-600 font-semibold': route.path.startsWith(item.path),
            }"
            @click="navigateTo(item.path)"
          >
            <IconifyIcon :icon="item.icon" class="text-base" />
            {{ t(item.labelKey) }}
          </button>

          <button
            v-if="isAdmin"
            type="button"
            class="mt-2 flex items-center gap-2.5 rounded-lg px-3 py-2.5 text-left text-sm transition-colors hover:bg-lf-surface-muted"
            :class="{ '!bg-lf-brand-soft !text-brand-600 font-semibold': isAdminRoute }"
            @click="navigateTo('/admin')"
          >
            <IconifyIcon icon="carbon:security" class="text-base" />
            {{ t('nav.admin') }}
          </button>
        </nav>
      </NDrawerContent>
    </NDrawer>

    <GlobalJobTrackerWidget />
    <GlobalJobDetailDrawer />
  </div>
</template>
