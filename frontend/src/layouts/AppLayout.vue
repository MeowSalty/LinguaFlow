<script setup lang="ts">
import { useI18n } from 'vue-i18n'
import { NButton, NIcon, useMessage, type DropdownOption } from 'naive-ui'
import IconCarbonMoon from '~icons/carbon/moon'
import IconCarbonScreen from '~icons/carbon/screen'
import IconCarbonSun from '~icons/carbon/sun'

import AppLogo from '@/components/AppLogo.vue'
import { APP_NAV_SECTIONS, type AppNavItem } from '@/layouts/navigation'
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

const SIDEBAR_STORAGE_KEY = 'linguaflow.sidebar.collapsed'

const sidebarCollapsed = ref(
  typeof window !== 'undefined' && window.localStorage.getItem(SIDEBAR_STORAGE_KEY) === '1',
)

watch(sidebarCollapsed, (collapsed) => {
  window.localStorage.setItem(SIDEBAR_STORAGE_KEY, collapsed ? '1' : '0')
})

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

const isAdmin = computed(() => auth.user?.role === 'admin')

const navSections = computed(() =>
  APP_NAV_SECTIONS.filter((section) => !section.adminOnly || isAdmin.value),
)

const isActive = (item: AppNavItem): boolean => {
  if (item.path === '/') {
    return route.path === '/'
  }
  return route.path === item.path || route.path.startsWith(`${item.path}/`)
}

const userOptions = computed<DropdownOption[]>(() => {
  const items: DropdownOption[] = [
    {
      key: 'user-info',
      type: 'render',
      render: () =>
        h('div', { class: 'px-3 py-2 min-w-[180px]' }, [
          h('div', { class: 'text-sm font-medium text-lf-text-strong' }, displayName.value),
          auth.user?.email
            ? h('div', { class: 'mt-0.5 text-xs text-lf-text-muted' }, auth.user.email)
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
    { label: t('nav.changelog'), key: 'changelog' },
    { label: t('nav.about'), key: 'about' },
    { type: 'divider', key: 'divider-2' },
  ]

  if (service.isLocal) {
    items.push({ label: t('layout.userMenu.connectRemoteService'), key: 'switch-service' })
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
  {
    label: t('theme.system'),
    key: 'system',
    icon: () => h(NIcon, null, { default: () => h(IconCarbonScreen) }),
  },
  {
    label: t('theme.light'),
    key: 'light',
    icon: () => h(NIcon, null, { default: () => h(IconCarbonSun) }),
  },
  {
    label: t('theme.dark'),
    key: 'dark',
    icon: () => h(NIcon, null, { default: () => h(IconCarbonMoon) }),
  },
])

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

const onSelectTheme = (key: string | number): void => {
  theme.setMode(String(key) as ThemeMode)
}

const onSelectLocale = (key: string | number): void => {
  locale.setLocale(String(key))
}

const mobileNavOpen = ref(false)

// 视口跨过 lg 断点（桌面侧边栏接管导航）时自动收起移动端抽屉，避免两套导航并存
const desktopNavQuery = window.matchMedia('(min-width: 64rem)')
const onDesktopNavChange = (event: MediaQueryListEvent): void => {
  if (event.matches) {
    mobileNavOpen.value = false
  }
}
onMounted(() => desktopNavQuery.addEventListener('change', onDesktopNavChange))
onUnmounted(() => desktopNavQuery.removeEventListener('change', onDesktopNavChange))

const navigateTo = (path: string): void => {
  mobileNavOpen.value = false
  void router.push(path)
}
</script>

<template>
  <div class="flex min-h-screen bg-lf-bg text-lf-text">
    <!-- 桌面侧边栏（可折叠为图标窄栏） -->
    <aside
      class="sticky top-0 hidden h-screen shrink-0 flex-col border-r border-lf-border-soft bg-lf-surface py-3.5 transition-[width] duration-200 lg:flex"
      :class="sidebarCollapsed ? 'w-[60px] px-2' : 'w-[232px] px-3'"
    >
      <RouterLink
        to="/"
        class="mb-4 flex items-center rounded-lf-ctl no-underline"
        :class="sidebarCollapsed ? 'justify-center' : 'px-2'"
        :aria-label="t('common.appName')"
      >
        <AppLogo size="md" :wordmark="!sidebarCollapsed" />
      </RouterLink>

      <nav class="flex min-h-0 flex-1 flex-col gap-0.5 overflow-y-auto" :aria-label="t('nav.main')">
        <template v-for="(section, si) in navSections" :key="`section-${si}`">
          <div
            v-if="si > 0 && section.labelKey === null"
            class="my-3 border-t border-lf-border-soft"
          />
          <div
            v-else-if="section.labelKey !== null && !sidebarCollapsed"
            class="mt-5 mb-1 px-2.5 text-[11px] font-semibold tracking-wide text-lf-text-subtle"
          >
            {{ t(section.labelKey) }}
          </div>

          <RouterLink
            v-for="item in section.items"
            :key="item.path"
            :to="item.path"
            class="flex h-[34px] items-center gap-2.5 rounded-lf-ctl text-[13.5px] font-medium no-underline transition-colors"
            :class="[
              sidebarCollapsed ? 'justify-center px-0' : 'px-2.5',
              isActive(item)
                ? 'bg-lf-brand-soft text-brand-600'
                : 'text-lf-text-muted hover:bg-lf-surface-muted hover:text-lf-text',
            ]"
            :title="sidebarCollapsed ? t(item.labelKey) : undefined"
          >
            <component
              :is="item.icon"
              class="h-[18px] w-[18px] shrink-0"
              :class="isActive(item) ? 'text-brand-500' : 'text-lf-text-subtle'"
            />
            <span v-if="!sidebarCollapsed" class="whitespace-nowrap">
              {{ t(item.labelKey) }}
            </span>
          </RouterLink>
        </template>
      </nav>

      <div class="mt-2 border-t border-lf-border-soft pt-2">
        <button
          type="button"
          class="flex h-[34px] w-full items-center gap-2.5 rounded-lf-ctl text-[13px] font-medium text-lf-text-subtle transition-colors hover:bg-lf-surface-muted hover:text-lf-text"
          :class="sidebarCollapsed ? 'justify-center px-0' : 'px-2.5'"
          :title="sidebarCollapsed ? t('layout.sidebar.expand') : t('layout.sidebar.collapse')"
          @click="sidebarCollapsed = !sidebarCollapsed"
        >
          <IconCarbonChevronLeft
            class="h-[18px] w-[18px] shrink-0 transition-transform duration-200"
            :class="sidebarCollapsed ? 'rotate-180' : ''"
          />
          <span v-if="!sidebarCollapsed" class="whitespace-nowrap">
            {{ t('layout.sidebar.collapse') }}
          </span>
        </button>
      </div>
    </aside>

    <!-- 主区 -->
    <div class="flex min-w-0 flex-1 flex-col">
      <header
        class="sticky top-0 z-20 flex h-14 shrink-0 items-center gap-2 border-b border-lf-border-soft bg-lf-surface px-4 sm:gap-3 sm:px-6"
      >
        <!-- lg:hidden! 用 important 覆盖 naive-ui 注入的 .n-button display（分层样式斗不过未分层样式） -->
        <NButton
          quaternary
          circle
          class="lg:hidden!"
          :aria-label="t('nav.menu')"
          :title="t('nav.menu')"
          @click="mobileNavOpen = true"
        >
          <template #icon>
            <IconCarbonMenu class="text-lg" />
          </template>
        </NButton>

        <div class="flex-1" />

        <div class="flex items-center gap-2 sm:gap-3">
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
                <IconCarbonScreen v-if="theme.mode === 'system'" class="text-lg" />
                <IconCarbonMoon v-else-if="theme.isDark" class="text-lg" />
                <IconCarbonSun v-else class="text-lg" />
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
              class="lf-grad-bg flex h-8 w-8 shrink-0 cursor-pointer items-center justify-center rounded-full text-xs font-semibold text-white"
              :aria-label="displayName"
            >
              {{ initial }}
            </button>
          </NDropdown>
        </div>
      </header>

      <main class="flex-1 px-5 py-7 sm:px-8">
        <div class="mx-auto max-w-275">
          <slot />
        </div>
      </main>
    </div>

    <!-- 移动端导航抽屉（与桌面侧边栏共用导航数据） -->
    <NDrawer v-model:show="mobileNavOpen" placement="left" :width="280">
      <NDrawerContent :title="t('common.appName')" closable>
        <nav class="flex flex-col gap-0.5 py-2" :aria-label="t('nav.main')">
          <template v-for="(section, si) in navSections" :key="`drawer-section-${si}`">
            <div
              v-if="si > 0 && section.labelKey === null"
              class="my-2 border-t border-lf-border-soft"
            />
            <div
              v-else-if="section.labelKey !== null"
              class="mt-4 mb-1 px-2.5 text-[11px] font-semibold tracking-wide text-lf-text-subtle"
            >
              {{ t(section.labelKey) }}
            </div>

            <button
              v-for="item in section.items"
              :key="item.path"
              type="button"
              class="flex items-center gap-2.5 rounded-lf-ctl px-2.5 py-2 text-left text-sm transition-colors"
              :class="
                isActive(item)
                  ? 'bg-lf-brand-soft font-semibold text-brand-600'
                  : 'text-lf-text-muted hover:bg-lf-surface-muted'
              "
              @click="navigateTo(item.path)"
            >
              <component
                :is="item.icon"
                class="h-[18px] w-[18px] shrink-0"
                :class="isActive(item) ? 'text-brand-500' : 'text-lf-text-subtle'"
              />
              <span class="whitespace-nowrap">{{ t(item.labelKey) }}</span>
            </button>
          </template>
        </nav>
      </NDrawerContent>
    </NDrawer>

    <GlobalJobTrackerWidget />
    <GlobalJobDetailDrawer />
  </div>
</template>
