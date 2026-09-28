<script setup lang="ts">
import { computed, ref } from 'vue'
import { NAlert, NCollapse, NCollapseItem, NTabPane, NTabs } from 'naive-ui'
import { useI18n } from 'vue-i18n'

import type { ApiSchemas } from '@/api/client'
import type { SSEEvent } from '@/composables/sseShared'
import {
  formatDate,
  getJobDurationSeconds,
  getJobTriggerLabel,
} from '@/composables/useWorkspaceUtils'
import { formatDetailDuration } from '@/composables/useJobDetailPresentation'
import { isJobTerminal, type JobEventFilter } from '@/utils/jobPresentation'
import JobEventTimeline from './JobEventTimeline.vue'
import JobProgressCard from './JobProgressCard.vue'
import JobResourceTable from './JobResourceTable.vue'

const props = defineProps<{
  job: ApiSchemas['Job']
  externalError?: string | null
  events?: SSEEvent[]
  sseConnected?: boolean
  hasOlder?: boolean
  loadingOlder?: boolean
}>()
const emit = defineEmits<{ clearEvents: []; loadOlder: [] }>()
const { t } = useI18n()
const activeTab = ref<'overview' | 'events'>('overview')
const eventFilter = ref<JobEventFilter>('all')
const durationText = computed(() => {
  const job = props.job
  const seconds =
    job.status === 'paused' && job.started_at
      ? Math.max(
          0,
          (new Date(job.updated_at).getTime() - new Date(job.started_at).getTime()) / 1000,
        )
      : getJobDurationSeconds(job)
  return formatDetailDuration(seconds)
})
const focusEvents = (): void => {
  eventFilter.value = 'anomalies'
  activeTab.value = 'events'
}
</script>

<template>
  <NTabs
    v-model:value="activeTab"
    type="line"
    :animated="false"
    class="job-detail-tabs"
    :tabs-padding="24"
  >
    <NTabPane name="overview" :tab="t('workspace.job.detail.overview')" display-directive="show">
      <div
        class="lf-scroll h-full min-h-0 overflow-y-auto px-4 py-5 sm:px-6"
        data-testid="job-overview-scroll"
      >
        <div class="space-y-5">
          <JobProgressCard :job="job" @focus-events="focusEvents" />
          <NAlert v-if="externalError" type="error" :bordered="false">{{ externalError }}</NAlert>
          <NAlert v-if="job.error_message" type="error" :bordered="false">{{
            job.error_message
          }}</NAlert>
          <section class="space-y-3">
            <dl class="grid grid-cols-2 gap-x-4 gap-y-3 sm:grid-cols-3">
              <div>
                <dt class="text-xs text-lf-text-muted">
                  {{ t('workspace.job.columns.startedAt') }}
                </dt>
                <dd class="mt-1 text-sm tabular-nums text-lf-text-strong">
                  {{ job.started_at ? formatDate(job.started_at) : '—' }}
                </dd>
              </div>
              <div>
                <dt class="text-xs text-lf-text-muted">
                  {{ t('workspace.job.columns.duration') }}
                </dt>
                <dd class="mt-1 text-sm tabular-nums text-lf-text-strong">{{ durationText }}</dd>
              </div>
              <div>
                <dt class="text-xs text-lf-text-muted">{{ t('workspace.job.columns.trigger') }}</dt>
                <dd class="mt-1 text-sm text-lf-text-strong">
                  {{ getJobTriggerLabel(job.trigger_type) }}
                </dd>
              </div>
            </dl>
            <NCollapse class="job-more-info">
              <NCollapseItem name="metadata" :title="t('workspace.job.detail.moreInfo')">
                <dl class="grid grid-cols-1 gap-3 text-xs sm:grid-cols-2">
                  <div>
                    <dt class="text-lf-text-muted">{{ t('common.createdAt') }}</dt>
                    <dd class="mt-1 tabular-nums">{{ formatDate(job.created_at) }}</dd>
                  </div>
                  <div>
                    <dt class="text-lf-text-muted">{{ t('common.updatedAt') }}</dt>
                    <dd class="mt-1 tabular-nums">{{ formatDate(job.updated_at) }}</dd>
                  </div>
                </dl>
              </NCollapseItem>
            </NCollapse>
          </section>
          <JobResourceTable :job="job" />
        </div>
      </div>
    </NTabPane>
    <NTabPane name="events" :tab="t('workspace.job.events.title')" display-directive="show:lazy">
      <JobEventTimeline
        v-model:filter="eventFilter"
        class="px-4 pb-4 pt-4 sm:px-6"
        :events="events ?? []"
        :active="activeTab === 'events'"
        :connected="sseConnected"
        :has-older="hasOlder"
        :loading-older="loadingOlder"
        :job-ended="isJobTerminal(job.status)"
        @clear="emit('clearEvents')"
        @load-older="emit('loadOlder')"
      />
    </NTabPane>
  </NTabs>
</template>

<style scoped>
.job-detail-tabs {
  height: 100%;
  min-height: 0;
  display: flex;
  flex-direction: column;
}
.job-detail-tabs :deep(> .n-tabs-nav) {
  flex: none;
}
.job-detail-tabs :deep(> .n-tab-pane) {
  flex: 1;
  min-height: 0;
  padding: 0;
  overflow: hidden;
}
.job-more-info :deep(.n-collapse-item__header-main) {
  font-size: 12px;
  color: var(--lf-text-muted);
}
</style>
