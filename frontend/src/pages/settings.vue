<script setup lang="ts">
import { useI18n } from 'vue-i18n'
import PageHeader from '@/components/common/PageHeader.vue'
import { useServiceStore } from '@/stores/service'

const { t } = useI18n()
const route = useRoute()
const service = useServiceStore()
const tabs = computed(() => [
  { path: '/settings/profile', label: t('workbench.settings.profile') },
  { path: '/settings/security', label: t('workbench.settings.security') },
  { path: '/settings/preferences', label: t('workbench.settings.preferences') },
  ...(!service.isLocal ? [{ path: '/settings/team', label: t('workbench.settings.team') }] : []),
])
</script>
<template>
  <div class="lf-page lf-content-narrow">
    <PageHeader
      :title="t('workbench.settings.title')"
      :subtitle="t('workbench.settings.subtitle')"
    />
    <nav
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
