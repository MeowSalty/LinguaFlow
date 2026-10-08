<script setup lang="ts">
import { useI18n } from 'vue-i18n'
import PageHeader from '@/components/common/PageHeader.vue'
import { useServiceStore } from '@/stores/service'

const { t } = useI18n()
const route = useRoute()
const service = useServiceStore()
const navigation = useTemplateRef<HTMLElement>('navigation')
const tabs = computed(() => [
  { path: '/settings/profile', label: t('workbench.settings.profile') },
  { path: '/settings/security', label: t('workbench.settings.security') },
  { path: '/settings/preferences', label: t('workbench.settings.preferences') },
  { path: '/settings/storage', label: t('storage.title') },
  ...(!service.isLocal ? [{ path: '/settings/team', label: t('workbench.settings.team') }] : []),
])

function revealActiveTab() {
  const nav = navigation.value
  const active = nav?.querySelector<HTMLElement>('[aria-current="page"]')
  if (!nav || !active) return
  const bounds = nav.getBoundingClientRect()
  const tabBounds = active.getBoundingClientRect()
  if (tabBounds.left < bounds.left) nav.scrollLeft += tabBounds.left - bounds.left
  else if (tabBounds.right > bounds.right) nav.scrollLeft += tabBounds.right - bounds.right
}

watch([() => route.path, tabs, navigation], revealActiveTab, { flush: 'post' })
watch(navigation, (nav, _, onCleanup) => {
  if (!nav) return
  const observer = new ResizeObserver(revealActiveTab)
  observer.observe(nav)
  onCleanup(() => observer.disconnect())
})
</script>
<template>
  <div class="lf-page lf-content-narrow">
    <PageHeader
      :title="t('workbench.settings.title')"
      :subtitle="t('workbench.settings.subtitle')"
    />
    <nav
      ref="navigation"
      :aria-label="t('workbench.settings.title')"
      class="flex gap-1 overflow-x-auto border-b border-lf-border-soft pb-2"
    >
      <RouterLink
        v-for="tab in tabs"
        :key="tab.path"
        :to="tab.path"
        :aria-current="route.path === tab.path ? 'page' : undefined"
        class="shrink-0 rounded-lg px-4 py-2 text-sm no-underline transition-colors focus-visible:outline-2 focus-visible:outline-brand-500"
        :class="
          route.path === tab.path
            ? 'bg-brand-500/10 font-medium text-brand-600'
            : 'text-lf-text-muted hover:bg-lf-surface hover:text-lf-text-strong'
        "
      >
        {{ tab.label }}
      </RouterLink>
    </nav>
    <RouterView />
  </div>
</template>
