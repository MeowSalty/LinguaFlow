<script setup lang="ts">
import { useI18n } from 'vue-i18n'
import { Icon as IconifyIcon } from '@iconify/vue'

import { useAdminStore } from '@/stores/admin'

const router = useRouter()
const admin = useAdminStore()
const { t } = useI18n()

onMounted(() => {
  admin.loadStats()
})

const statCards = computed(() => [
  {
    title: t('admin.dashboard.stats.totalUsers'),
    value: admin.stats?.total_users ?? 0,
    icon: 'carbon:user-multiple',
    tone: 'bg-lf-info-soft text-lf-info',
  },
  {
    title: t('admin.dashboard.stats.activeUsers'),
    value: admin.stats?.active_users ?? 0,
    icon: 'carbon:user-online',
    tone: 'bg-lf-brand-soft text-brand-600',
  },
  {
    title: t('admin.dashboard.stats.totalProjects'),
    value: admin.stats?.total_projects ?? 0,
    icon: 'carbon:folder',
    tone: 'bg-lf-accent-soft text-lf-accent',
  },
  {
    title: t('admin.dashboard.stats.totalOrganizations'),
    value: admin.stats?.total_organizations ?? 0,
    icon: 'carbon:enterprise',
    tone: 'bg-lf-surface-muted text-lf-text-muted',
  },
  {
    title: t('admin.dashboard.stats.totalJobs'),
    value: admin.stats?.total_jobs ?? 0,
    icon: 'carbon:document-tasks',
    tone: 'bg-lf-info-soft text-lf-info',
  },
  {
    title: t('admin.dashboard.stats.totalResources'),
    value: admin.stats?.total_resources ?? 0,
    icon: 'carbon:document',
    tone: 'bg-lf-brand-soft text-brand-600',
  },
])

const quickActions = [
  {
    title: t('admin.users.title'),
    description: t('admin.users.description'),
    icon: 'carbon:user-multiple',
    path: '/admin/users',
    tone: 'bg-lf-info-soft text-lf-info',
  },
  {
    title: t('admin.auditLogs.title'),
    description: t('admin.auditLogs.description'),
    icon: 'carbon:catalog',
    path: '/admin/audit-logs',
    tone: 'bg-lf-accent-soft text-lf-accent',
  },
  {
    title: t('admin.settings.title'),
    description: t('admin.settings.description'),
    icon: 'carbon:settings',
    path: '/admin/settings',
    tone: 'bg-lf-surface-muted text-lf-text-muted',
  },
]
</script>

<template>
  <div class="lf-page">
    <section class="lf-page-header">
      <div class="flex flex-col gap-6 lg:flex-row lg:items-center lg:justify-between">
        <div class="space-y-3">
          <div class="lf-eyebrow">
            {{ t('admin.eyebrow') }}
          </div>
          <div>
            <h1 class="text-3xl font-semibold tracking-tight text-lf-text-strong">
              {{ t('admin.dashboard.title') }}
            </h1>
            <p class="mt-2 max-w-2xl text-sm leading-6 text-lf-text-muted">
              {{ t('admin.dashboard.description') }}
            </p>
          </div>
        </div>
        <NButton secondary :loading="admin.statsLoading" @click="admin.loadStats">
          {{ t('admin.users.actions.refresh') }}
        </NButton>
      </div>
    </section>

    <div class="grid grid-cols-1 gap-4 sm:grid-cols-2 lg:grid-cols-3">
      <div v-for="card in statCards" :key="card.title" class="lf-metric">
        <div class="flex items-center gap-4">
          <div class="flex h-12 w-12 items-center justify-center rounded-xl" :class="card.tone">
            <IconifyIcon :icon="card.icon" class="text-2xl" />
          </div>
          <div>
            <div class="lf-metric-label">{{ card.title }}</div>
            <div
              v-if="admin.statsLoading"
              class="mt-1 h-8 w-16 animate-pulse rounded bg-lf-border-soft"
            />
            <div v-else class="lf-metric-value !mt-1">
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
        <div class="flex h-10 w-10 items-center justify-center rounded-xl" :class="action.tone">
          <IconifyIcon :icon="action.icon" class="text-xl" />
        </div>
        <div>
          <div class="font-medium text-lf-text-strong">{{ action.title }}</div>
          <div class="mt-0.5 text-xs text-lf-text-muted">{{ action.description }}</div>
        </div>
      </button>
    </div>
  </div>
</template>
