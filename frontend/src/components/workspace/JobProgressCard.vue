<script setup lang="ts">
import { computed } from 'vue'
import { NTooltip } from 'naive-ui'
import { useI18n } from 'vue-i18n'
import type { ApiSchemas } from '@/api/client'
import {
  aggregateRound,
  calculateJobETA,
  calculateJobSpeed,
  formatETA,
  formatEtaCompletionTime,
  formatJobSpeed,
  getJobProgress,
  getJobProgressNumbers,
  getJobProgressText,
  getRoundColumns,
  getStageLabel,
} from '@/composables/useWorkspaceUtils'
import { getJobRoundSkipReason, getRoundDisplayState } from '@/utils/jobPresentation'
import StackedProgressBar from '@/components/common/StackedProgressBar.vue'
import JobRoundStateIcon from './JobRoundStateIcon.vue'
import JobStageProgress from './JobStageProgress.vue'

const props = defineProps<{ job: ApiSchemas['Job'] }>()
const emit = defineEmits<{ focusEvents: [] }>()
const { t, n } = useI18n()
const completedPct = computed(() => getJobProgress(props.job))
const workload = computed(() => getJobProgressNumbers(props.job))
const resources = computed(() => props.job.job_resources ?? [])
const skipped = computed(() => resources.value.reduce((sum, r) => sum + r.skipped_segments, 0))
const warned = computed(() => resources.value.filter((r) => r.warning_message?.trim()).length)
const barTone = computed<'brand' | 'success' | 'warning' | 'neutral'>(() => {
  if (props.job.status === 'completed') return warned.value ? 'warning' : 'success'
  if (props.job.status === 'pausing') return 'warning'
  return props.job.status === 'running' ? 'brand' : 'neutral'
})
const percentClass = computed(() => {
  if (props.job.status === 'failed') return 'text-lf-danger'
  return {
    brand: 'text-brand-500',
    success: 'text-lf-success',
    warning: 'text-lf-warning',
    neutral: 'text-lf-text-muted',
  }[barTone.value]
})
const rounds = computed(() =>
  getRoundColumns(props.job).flatMap((col) => {
    const aggregate = aggregateRound(props.job, col.roundIndex)
    if (!aggregate) return []
    const matches = resources.value.filter((resource) =>
      resource.rounds?.some(
        (round) => round.round_index === col.roundIndex && round.status === aggregate.status,
      ),
    )
    const representative = matches.find((resource) => resource.status === 'running') ?? matches[0]
    const state = getRoundDisplayState(
      props.job.status,
      representative?.status ?? 'pending',
      aggregate.status,
    )
    return [
      {
        index: col.roundIndex,
        label: getStageLabel(col.mode),
        state,
        stateLabel: getJobRoundSkipReason(props.job, { mode: col.mode, status: aggregate.status })
          ? t('termExtraction.skippedGlossaryDisabled')
          : t('workspace.job.detail.' + state),
        count: aggregate.total > 0 ? n(aggregate.completed) + '/' + n(aggregate.total) : '',
      },
    ]
  }),
)
const hasRunningRound = computed(
  () => props.job.status === 'running' && rounds.value.some((round) => round.state === 'running'),
)
const eta = computed(() => (hasRunningRound.value ? calculateJobETA(props.job) : null))
const etaText = computed(() => formatETA(eta.value))
const completionText = computed(() => formatEtaCompletionTime(eta.value))
const speedText = computed(() =>
  hasRunningRound.value ? formatJobSpeed(calculateJobSpeed(props.job)) : '',
)
</script>

<template>
  <section class="space-y-3 rounded-lf-card border border-lf-border-soft bg-lf-surface p-4">
    <div class="flex items-start justify-between gap-3">
      <div class="min-w-0 space-y-1">
        <NTooltip>
          <template #trigger>
            <span class="cursor-help text-xs text-lf-text-muted" tabindex="0">{{
              t('workspace.job.detail.workload')
            }}</span>
          </template>
          {{ t('workspace.job.progress.percentTooltip') }}
        </NTooltip>
        <div class="text-sm tabular-nums text-lf-text-strong">
          {{ n(workload.completed) }}
          <span class="text-lf-text-subtle">/ {{ n(workload.total) }}</span>
        </div>
      </div>
      <span class="text-2xl font-semibold leading-8 tabular-nums" :class="percentClass"
        >{{ completedPct }}<span class="ml-0.5 text-sm">%</span></span
      >
    </div>
    <StackedProgressBar :value="completedPct" :tone="barTone" :error="job.status === 'failed'" />
    <div class="flex flex-wrap items-center gap-x-3 gap-y-2 text-xs text-lf-text-muted">
      <span>{{
        t('workspace.job.detail.resourceCount', { count: n(job.progress.total_resources) })
      }}</span>
      <button
        v-if="job.progress.failed_resources > 0"
        type="button"
        class="inline-flex cursor-pointer items-center gap-1 text-lf-danger hover:underline"
        @click="emit('focusEvents')"
      >
        <IconCarbonErrorFilled />{{
          t('workspace.job.detail.failedResources', { count: n(job.progress.failed_resources) })
        }}
      </button>
      <button
        v-if="warned > 0"
        type="button"
        class="inline-flex cursor-pointer items-center gap-1 text-lf-warning hover:underline"
        @click="emit('focusEvents')"
      >
        <IconCarbonWarningFilled />{{
          t('workspace.job.detail.warnedResources', { count: n(warned) })
        }}
      </button>
      <span v-if="skipped > 0">{{
        t('workspace.job.detail.skippedSegments', { count: n(skipped) })
      }}</span>
    </div>
    <p v-if="job.status === 'cancelled'" class="text-xs text-lf-text-muted">
      {{ t('workspace.job.detail.cancelledHint') }}
    </p>
    <p v-if="job.status === 'pending'" class="text-xs text-lf-text-muted">
      {{ getJobProgressText(job) }}
    </p>
    <div
      v-if="job.status !== 'completed' && rounds.length"
      class="flex flex-wrap gap-x-4 gap-y-2 border-t border-lf-border-soft pt-3"
    >
      <div v-for="round in rounds" :key="round.index" class="flex items-center gap-1.5 text-xs">
        <JobRoundStateIcon :state="round.state" />
        <span :class="round.state === 'running' ? 'text-lf-text-strong' : 'text-lf-text-muted'">{{
          round.label
        }}</span>
        <span class="text-lf-text-subtle">{{ round.stateLabel }}</span>
        <span
          v-if="
            round.count &&
            (round.state === 'running' ||
              round.state === 'pausing' ||
              round.state === 'paused' ||
              round.state === 'stopped')
          "
          class="tabular-nums"
          :class="round.state === 'running' ? 'text-brand-500' : 'text-lf-text-muted'"
          >{{ round.count }}</span
        >
      </div>
    </div>
    <dl
      v-if="hasRunningRound && (etaText || speedText)"
      class="grid grid-cols-2 gap-3 border-t border-lf-border-soft pt-3 sm:grid-cols-3"
    >
      <div v-if="etaText">
        <dt class="text-xs text-lf-text-muted">{{ t('workspace.job.eta.label') }}</dt>
        <dd class="mt-1 text-sm tabular-nums">{{ etaText }}</dd>
      </div>
      <div v-if="completionText">
        <dt class="text-xs text-lf-text-muted">{{ t('workspace.job.eta.expectedLabel') }}</dt>
        <dd class="mt-1 text-sm tabular-nums">{{ completionText }}</dd>
      </div>
      <div v-if="speedText">
        <dt class="text-xs text-lf-text-muted">{{ t('workspace.job.speed.label') }}</dt>
        <dd class="mt-1 text-sm tabular-nums">{{ speedText }}</dd>
      </div>
    </dl>
    <JobStageProgress :stages="job.progress.stages" :pausing="job.status === 'pausing'" />
  </section>
</template>
