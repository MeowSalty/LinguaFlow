<script setup lang="ts">
import { useI18n } from 'vue-i18n'

import ActivityFeed from '@/components/dashboard/ActivityFeed.vue'
import JobStatusOverview from '@/components/dashboard/JobStatusOverview.vue'
import StatsCard from '@/components/dashboard/StatsCard.vue'
import { useStatsStore } from '@/stores/stats'
import { useOperationsStore } from '@/stores/operations'
import OperationCounts from '@/components/operations/OperationCounts.vue'
import { parseOrganizationId } from '@/utils/organization-scope'
import { sessionGeneration } from '@/api/session-context'

const stats = useStatsStore()
const { t } = useI18n()
const route = useRoute()
const router = useRouter()
const operations = useOperationsStore()
const orgId = computed(() => parseOrganizationId(route.query.org_id))
const refresh = async (): Promise<void> => {
  await Promise.all([stats.loadAll(), operations.ensureSummary(true)])
}

watch(
  [orgId, sessionGeneration],
  () => {
    stats.setActivityOrganization(orgId.value)
    void stats.loadAll()
    void operations.ensureSummary()
  },
  { immediate: true },
)
</script>

<template>
  <div class="lf-page lf-content-narrow">
    <PageHeader :title="t('stats.title')" :subtitle="t('stats.subtitle')">
      <template #actions>
        <NButton
          secondary
          :loading="stats.statsLoading || stats.activitiesLoading"
          @click="refresh()"
        >
          {{ t('common.actions.refresh') }}
        </NButton>
      </template>
    </PageHeader>

    <section class="space-y-3">
      <div class="flex items-center justify-between gap-3">
        <h2 class="text-sm font-semibold text-lf-text-strong">
          {{ t('workbench.home.currentTasks') }}
        </h2>
        <RouterLink to="/operations" class="text-xs text-brand-600 no-underline hover:underline">{{
          t('workbench.home.operations')
        }}</RouterLink>
      </div>
      <OperationCounts
        :summary="operations.summary"
        :loading="operations.summaryLoading"
        :error="operations.summaryError"
      />
    </section>
    <h2 class="text-sm font-semibold text-lf-text-strong">{{ t('workbench.stats.usage') }}</h2>
    <NAlert v-if="stats.statsError && stats.stats" type="warning"
      >{{ t('workbench.stats.stale') }} · {{ stats.statsUpdatedAt }}</NAlert
    >

    <div class="grid grid-cols-1 gap-4 sm:grid-cols-2 xl:grid-cols-4">
      <StatsCard
        :title="t('dashboard.stats.apiCalls')"
        :value="stats.stats?.api_calls ?? 0"
        icon="carbon:api"
        tone="brand"
        :loading="stats.statsLoading"
        :error="stats.stats ? null : stats.statsError"
      />
      <StatsCard
        :title="t('dashboard.stats.inputTokens')"
        :value="stats.stats?.input_tokens ?? 0"
        icon="carbon:cloud-upload"
        tone="info"
        :loading="stats.statsLoading"
        :error="stats.stats ? null : stats.statsError"
      />
      <StatsCard
        :title="t('dashboard.stats.outputTokens')"
        :value="stats.stats?.output_tokens ?? 0"
        icon="carbon:cloud-download"
        tone="accent"
        :loading="stats.statsLoading"
        :error="stats.stats ? null : stats.statsError"
      />
      <StatsCard
        :title="t('dashboard.stats.segmentCount')"
        :value="stats.stats?.segment_count ?? 0"
        icon="carbon:chart-column"
        tone="neutral"
        :loading="stats.statsLoading"
        :error="stats.stats ? null : stats.statsError"
      />
    </div>

    <div class="grid grid-cols-1 gap-4 xl:grid-cols-5">
      <div class="xl:col-span-2">
        <JobStatusOverview />
      </div>
      <div class="xl:col-span-3">
        <div v-if="orgId" class="mb-3 flex items-center justify-between gap-3 text-sm">
          <span>{{ t('workbench.stats.organizationActivity') }} #{{ orgId }}</span
          ><NButton text @click="router.replace('/stats')">{{
            t('workbench.stats.clearScope')
          }}</NButton>
        </div>
        <ActivityFeed />
      </div>
    </div>
  </div>
</template>
