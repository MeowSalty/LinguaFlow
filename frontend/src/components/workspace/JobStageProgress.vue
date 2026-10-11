<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import type { ApiSchemas } from '@/api/client'
import { formatDateTime } from '@/utils/datetime'

const props = defineProps<{
  stages?: ApiSchemas['JobStageCounts']
  pausing: boolean
}>()

const { t, n } = useI18n()
const observedTime = computed(() => {
  if (!props.stages || Number.isNaN(Date.parse(props.stages.as_of))) return t('common.noDate')
  return formatDateTime(props.stages.as_of, { dateStyle: 'short', timeStyle: 'medium' })
})
</script>

<template>
  <div v-if="stages || pausing" class="space-y-3 border-t border-lf-border-soft pt-3">
    <p
      v-if="pausing"
      role="status"
      class="rounded-lf-ctl border border-lf-warning/20 bg-lf-warning/5 px-3 py-2 text-xs leading-relaxed text-lf-warning"
    >
      {{ t('jobStages.pausing') }}
      <span v-if="stages" class="mt-1 block">
        {{
          stages.draining_requests === 0
            ? t('jobStages.drained')
            : t('jobStages.draining', { count: n(stages.draining_requests) })
        }}
      </span>
    </p>

    <section v-if="stages" :aria-label="t('jobStages.title')" class="space-y-3">
      <h3 class="text-xs font-medium text-lf-text-strong">{{ t('jobStages.title') }}</h3>
      <dl class="grid grid-cols-[repeat(auto-fit,minmax(min(100%,9rem),1fr))] gap-3 text-xs">
        <div class="min-w-0 space-y-1">
          <dt class="text-lf-text-muted">{{ t('jobStages.mainRequests') }}</dt>
          <dd class="space-x-1 tabular-nums">
            <span class="text-sm font-medium text-lf-text-strong">{{
              n(stages.main_requests)
            }}</span>
            <span class="text-lf-text-subtle">{{ t('jobStages.requests') }}</span>
          </dd>
        </div>
        <div class="min-w-0 space-y-1">
          <dt class="text-lf-text-muted">{{ t('jobStages.pendingAlignment') }}</dt>
          <dd class="space-x-1 tabular-nums">
            <span class="text-sm font-medium text-lf-text-strong">{{
              n(stages.pending_alignment)
            }}</span>
            <span class="text-lf-text-subtle">{{ t('jobStages.candidates') }}</span>
          </dd>
          <dt class="text-lf-text-muted">{{ t('jobStages.alignmentRequests') }}</dt>
          <dd class="space-x-1 tabular-nums">
            <span class="text-sm font-medium text-lf-text-strong">{{
              n(stages.alignment_requests)
            }}</span>
            <span class="text-lf-text-subtle">{{ t('jobStages.requests') }}</span>
          </dd>
        </div>
        <div class="min-w-0 space-y-1">
          <dt class="text-lf-text-muted">{{ t('jobStages.savingRequests') }}</dt>
          <dd class="space-x-1 tabular-nums">
            <span class="text-sm font-medium text-lf-text-strong">{{
              n(stages.saving_requests)
            }}</span>
            <span class="text-lf-text-subtle">{{ t('jobStages.requests') }}</span>
          </dd>
        </div>
        <div class="min-w-0 space-y-1">
          <dt class="text-lf-text-muted">{{ t('jobStages.confirmedWork') }}</dt>
          <dd class="space-x-1 tabular-nums">
            <span class="text-sm font-medium text-lf-text-strong">{{
              n(stages.confirmed_work)
            }}</span>
            <span class="text-lf-text-subtle">{{ t('jobStages.workUnits') }}</span>
          </dd>
        </div>
      </dl>
      <p class="text-xs leading-relaxed text-lf-text-muted">
        {{ t('jobStages.alignmentHint') }} {{ t('jobStages.countsHint') }}
      </p>
      <details class="text-xs">
        <summary
          class="w-fit cursor-pointer rounded-lf-ctl text-lf-text-muted hover:text-lf-text-strong focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-brand-500"
        >
          {{ t('jobStages.more') }}
        </summary>
        <dl class="mt-3 flex flex-wrap gap-x-6 gap-y-3">
          <div class="min-w-0 space-y-1">
            <dt class="text-lf-text-muted">{{ t('jobStages.readyToCommit') }}</dt>
            <dd class="tabular-nums">
              {{ n(stages.ready_to_commit) }} {{ t('jobStages.candidates') }}
            </dd>
          </div>
          <div class="min-w-0 space-y-1">
            <dt class="text-lf-text-muted">{{ t('jobStages.unknownRequests') }}</dt>
            <dd class="tabular-nums">
              {{ n(stages.unknown_requests) }} {{ t('jobStages.requests') }}
            </dd>
          </div>
          <div class="min-w-0 space-y-1">
            <dt class="text-lf-text-muted">{{ t('jobStages.asOf') }}</dt>
            <dd class="tabular-nums [overflow-wrap:anywhere]">
              <time :datetime="stages.as_of">{{ observedTime }}</time>
            </dd>
          </div>
        </dl>
      </details>
    </section>
  </div>
</template>
