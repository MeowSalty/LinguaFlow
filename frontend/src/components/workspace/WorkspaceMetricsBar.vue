<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'

import StackedProgressBar from '@/components/common/StackedProgressBar.vue'

const props = defineProps<{
  totalResources: number
  totalSegments: number
  translatedSegments: number
  approvedSegments: number
  runningJobs: number
}>()

const { t } = useI18n()

const translatedPercent = computed(() => {
  if (props.totalSegments === 0) return 0
  return Math.round((props.translatedSegments / props.totalSegments) * 100)
})

const approvedPercent = computed(() => {
  if (props.totalSegments === 0) return 0
  return Math.round((props.approvedSegments / props.totalSegments) * 100)
})
</script>

<template>
  <div
    class="flex flex-wrap items-center gap-x-4 gap-y-1.5 rounded-lf-card border border-lf-border-soft bg-lf-surface px-4 py-2 shadow-sm shadow-lf-shadow"
  >
    <div class="flex items-baseline gap-1.5">
      <span class="text-base font-semibold tabular-nums tracking-tight text-lf-text-strong">{{
        totalResources
      }}</span>
      <span class="text-[11px] text-lf-text-muted">{{ t('workspace.stats.resources') }}</span>
    </div>

    <span class="hidden h-3.5 w-px bg-lf-border-soft sm:inline-block" />

    <div class="flex items-baseline gap-1.5">
      <span class="text-base font-semibold tabular-nums tracking-tight text-lf-text-strong">{{
        totalSegments.toLocaleString()
      }}</span>
      <span class="text-[11px] text-lf-text-muted">{{ t('workspace.stats.segments') }}</span>
    </div>

    <span class="hidden h-3.5 w-px bg-lf-border-soft sm:inline-block" />

    <div class="flex items-baseline gap-1.5">
      <span class="text-base font-semibold tabular-nums tracking-tight text-lf-text-strong">{{
        runningJobs
      }}</span>
      <span class="text-[11px] text-lf-text-muted">{{ t('workspace.stats.runningJobs') }}</span>
    </div>

    <div class="ml-auto flex shrink-0 items-center gap-2 whitespace-nowrap">
      <StackedProgressBar
        class="w-20"
        :value="translatedPercent"
        :sub-value="approvedPercent"
        height="4px"
      />
      <span class="whitespace-nowrap text-[11px] font-medium tabular-nums text-lf-text-muted">
        {{ translatedPercent }}% {{ t('workspace.stats.progress') }}
      </span>
    </div>
  </div>
</template>
