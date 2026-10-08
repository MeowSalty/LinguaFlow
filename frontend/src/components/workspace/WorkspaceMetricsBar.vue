<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'

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
    class="relative flex flex-wrap items-center gap-x-4 gap-y-1.5 overflow-hidden rounded-lf-card border border-lf-border-soft bg-lf-surface px-4 py-2 shadow-sm shadow-lf-shadow"
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

    <span
      class="whitespace-nowrap text-[11px] font-medium tabular-nums text-lf-text-muted sm:ml-auto"
    >
      {{ translatedPercent }}% {{ t('workspace.stats.progress') }}
    </span>

    <!-- 项目总进度：贴卡片底边的通长色条，无轨道底色（0% 时整条隐藏）；
         浅色段自主段起点叠加，表示已批准部分 -->
    <div v-if="translatedPercent > 0" class="absolute inset-x-0 bottom-0 h-1">
      <div
        class="h-full bg-brand-500 transition-all duration-300"
        :style="{ width: `${translatedPercent}%` }"
      />
      <div
        v-if="approvedPercent > 0"
        class="absolute inset-y-0 left-0 bg-brand-500/40 transition-all duration-300"
        :style="{ width: `${approvedPercent}%` }"
      />
    </div>
  </div>
</template>
