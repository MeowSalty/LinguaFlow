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
    color: 'bg-blue-500/10 text-blue-500',
  },
  {
    title: t('admin.dashboard.stats.activeUsers'),
    value: admin.stats?.active_users ?? 0,
    icon: 'carbon:user-online',
    color: 'bg-green-500/10 text-green-500',
  },
  {
    title: t('admin.dashboard.stats.totalProjects'),
    value: admin.stats?.total_projects ?? 0,
    icon: 'carbon:folder',
    color: 'bg-purple-500/10 text-purple-500',
  },
  {
    title: t('admin.dashboard.stats.totalOrganizations'),
    value: admin.stats?.total_organizations ?? 0,
    icon: 'carbon:enterprise',
    color: 'bg-orange-500/10 text-orange-500',
  },
  {
    title: t('admin.dashboard.stats.totalJobs'),
    value: admin.stats?.total_jobs ?? 0,
    icon: 'carbon:document-tasks',
    color: 'bg-cyan-500/10 text-cyan-500',
  },
  {
    title: t('admin.dashboard.stats.totalResources'),
    value: admin.stats?.total_resources ?? 0,
    icon: 'carbon:document',
    color: 'bg-pink-500/10 text-pink-500',
  },
])

const quickActions = [
  {
    title: t('admin.users.title'),
    description: t('admin.users.description'),
    icon: 'carbon:user-multiple',
    path: '/admin/users',
    color: 'bg-blue-500/10 text-blue-500',
  },
  {
    title: t('admin.auditLogs.title'),
    description: t('admin.auditLogs.description'),
    icon: 'carbon:catalog',
    path: '/admin/audit-logs',
    color: 'bg-purple-500/10 text-purple-500',
  },
  {
    title: t('admin.settings.title'),
    description: t('admin.settings.description'),
    icon: 'carbon:settings',
    path: '/admin/settings',
    color: 'bg-orange-500/10 text-orange-500',
  },
]
</script>

<template>
  <div class="space-y-6">
    <NCard :bordered="false" class="overflow-hidden shadow-sm shadow-lf-shadow">
      <div class="flex flex-col gap-6 lg:flex-row lg:items-center lg:justify-between">
        <div class="space-y-3">
          <div
            class="inline-flex items-center rounded-full bg-lf-brand-soft px-3 py-1 text-xs font-medium text-brand-600"
          >
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
    </NCard>

    <div class="grid grid-cols-1 gap-4 sm:grid-cols-2 lg:grid-cols-3">
      <NCard
        v-for="card in statCards"
        :key="card.title"
        :bordered="false"
        class="shadow-sm shadow-lf-shadow"
      >
        <div class="flex items-center gap-4">
          <div class="flex h-12 w-12 items-center justify-center rounded-xl" :class="card.color">
            <IconifyIcon :icon="card.icon" class="text-2xl" />
          </div>
          <div>
            <div class="text-sm text-lf-text-muted">{{ card.title }}</div>
            <div
              v-if="admin.statsLoading"
              class="mt-1 h-8 w-16 animate-pulse rounded bg-lf-border"
            />
            <div v-else class="mt-1 text-2xl font-semibold text-lf-text-strong">
              {{ card.value.toLocaleString() }}
            </div>
          </div>
        </div>
      </NCard>
    </div>

    <div class="grid grid-cols-1 gap-4 sm:grid-cols-3">
      <NCard
        v-for="action in quickActions"
        :key="action.path"
        hoverable
        :bordered="false"
        class="cursor-pointer shadow-sm shadow-lf-shadow transition-shadow hover:shadow-md hover:shadow-lf-shadow-strong"
        @click="router.push(action.path)"
      >
        <div class="flex items-center gap-3">
          <div class="flex h-10 w-10 items-center justify-center rounded-lg" :class="action.color">
            <IconifyIcon :icon="action.icon" class="text-xl" />
          </div>
          <div>
            <div class="font-medium text-lf-text-strong">{{ action.title }}</div>
            <div class="text-xs text-lf-text-muted">{{ action.description }}</div>
          </div>
        </div>
      </NCard>
    </div>
  </div>
</template>
