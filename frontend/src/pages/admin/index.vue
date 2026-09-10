<script setup lang="ts">
import type { Component } from 'vue'
import { useI18n } from 'vue-i18n'
import IconCarbonCatalog from '~icons/carbon/catalog'
import IconCarbonDocument from '~icons/carbon/document'
import IconCarbonDocumentTasks from '~icons/carbon/document-tasks'
import IconCarbonEnterprise from '~icons/carbon/enterprise'
import IconCarbonFolder from '~icons/carbon/folder'
import IconCarbonSettings from '~icons/carbon/settings'
import IconCarbonUserMultiple from '~icons/carbon/user-multiple'
import IconCarbonUserOnline from '~icons/carbon/user-online'

import { useAdminStore } from '@/stores/admin'

const router = useRouter()
const admin = useAdminStore()
const { t } = useI18n()

onMounted(() => {
  admin.loadStats()
})

const statCards = computed<Array<{ title: string; value: number; icon: Component; tone: string }>>(
  () => [
    {
      title: t('admin.dashboard.stats.totalUsers'),
      value: admin.stats?.total_users ?? 0,
      icon: IconCarbonUserMultiple,
      tone: 'bg-lf-info-soft text-lf-info',
    },
    {
      title: t('admin.dashboard.stats.activeUsers'),
      value: admin.stats?.active_users ?? 0,
      icon: IconCarbonUserOnline,
      tone: 'bg-lf-brand-soft text-brand-600',
    },
    {
      title: t('admin.dashboard.stats.totalProjects'),
      value: admin.stats?.total_projects ?? 0,
      icon: IconCarbonFolder,
      tone: 'bg-lf-brand-soft text-brand-600',
    },
    {
      title: t('admin.dashboard.stats.totalOrganizations'),
      value: admin.stats?.total_organizations ?? 0,
      icon: IconCarbonEnterprise,
      tone: 'bg-lf-surface-muted text-lf-text-muted',
    },
    {
      title: t('admin.dashboard.stats.totalJobs'),
      value: admin.stats?.total_jobs ?? 0,
      icon: IconCarbonDocumentTasks,
      tone: 'bg-lf-info-soft text-lf-info',
    },
    {
      title: t('admin.dashboard.stats.totalResources'),
      value: admin.stats?.total_resources ?? 0,
      icon: IconCarbonDocument,
      tone: 'bg-lf-brand-soft text-brand-600',
    },
  ],
)

const quickActions = computed<
  Array<{
    title: string
    description: string
    icon: Component
    path: string
    tone: string
  }>
>(() => [
  {
    title: t('admin.users.title'),
    description: t('admin.users.description'),
    icon: IconCarbonUserMultiple,
    path: '/admin/users',
    tone: 'bg-lf-info-soft text-lf-info',
  },
  {
    title: t('admin.auditLogs.title'),
    description: t('admin.auditLogs.description'),
    icon: IconCarbonCatalog,
    path: '/admin/audit-logs',
    tone: 'bg-lf-brand-soft text-brand-600',
  },
  {
    title: t('admin.settings.title'),
    description: t('admin.settings.description'),
    icon: IconCarbonSettings,
    path: '/admin/settings',
    tone: 'bg-lf-surface-muted text-lf-text-muted',
  },
])
</script>

<template>
  <div class="lf-page">
    <PageHeader :title="t('admin.dashboard.title')" :subtitle="t('admin.dashboard.description')">
      <NButton secondary :loading="admin.statsLoading" @click="admin.loadStats">
        {{ t('common.actions.refresh') }}
      </NButton>
    </PageHeader>

    <div class="grid grid-cols-1 gap-4 sm:grid-cols-2 lg:grid-cols-3">
      <div v-for="card in statCards" :key="card.title" class="lf-metric">
        <div class="flex items-center gap-4">
          <div class="flex h-12 w-12 items-center justify-center rounded-lf-ctl" :class="card.tone">
            <component :is="card.icon" class="text-2xl" />
          </div>
          <div>
            <div class="lf-metric-label">{{ card.title }}</div>
            <NSkeleton v-if="admin.statsLoading" class="mt-1" width="64px" height="32px" />
            <div v-else class="lf-metric-value">
              {{ card.value.toLocaleString() }}
            </div>
          </div>
        </div>
      </div>
    </div>

    <div class="grid grid-cols-1 gap-4 sm:grid-cols-3">
      <button
        v-for="action in quickActions"
        :key="action.path"
        type="button"
        class="lf-interactive-card flex items-center gap-3 p-4 text-left"
        @click="router.push(action.path)"
      >
        <div class="flex h-10 w-10 items-center justify-center rounded-lf-ctl" :class="action.tone">
          <component :is="action.icon" class="text-xl" />
        </div>
        <div>
          <div class="font-medium text-lf-text-strong">{{ action.title }}</div>
          <div class="mt-0.5 text-xs text-lf-text-muted">{{ action.description }}</div>
        </div>
      </button>
    </div>
  </div>
</template>
