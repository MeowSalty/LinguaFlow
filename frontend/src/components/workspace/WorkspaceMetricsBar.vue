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
    class="flex items-center gap-x-5 rounded-xl border border-lf-border-soft bg-lf-surface px-5 py-3 shadow-sm shadow-lf-shadow"
  >
    <!-- 资源文件 -->
    <div class="flex items-baseline gap-1.5">
      <span class="text-lg font-semibold text-lf-text-strong">{{ totalResources }}</span>
      <span class="text-xs text-lf-text-muted">{{ t('workspace.stats.resources') }}</span>
    </div>

    <span class="h-4 w-px bg-lf-border-soft" />

    <!-- 段落总数 -->
    <div class="flex items-baseline gap-1.5">
      <span class="text-lg font-semibold text-lf-text-strong">{{
        totalSegments.toLocaleString()
      }}</span>
      <span class="text-xs text-lf-text-muted">{{ t('workspace.stats.segments') }}</span>
    </div>

    <span class="h-4 w-px bg-lf-border-soft" />

    <!-- 运行中任务 -->
    <div class="flex items-baseline gap-1.5">
      <span class="text-lg font-semibold text-lf-text-strong">{{ runningJobs }}</span>
      <span class="text-xs text-lf-text-muted">{{ t('workspace.stats.runningJobs') }}</span>
    </div>

    <span class="h-4 w-px bg-lf-border-soft" />

    <!-- 翻译进度 -->
    <div class="ml-auto flex shrink-0 items-center gap-2.5 whitespace-nowrap">
      <div class="relative h-1.5 w-24 overflow-hidden rounded-full bg-slate-200 dark:bg-slate-700">
        <div
          class="absolute inset-y-0 left-0 rounded-full bg-blue-500 transition-all duration-500"
          :style="{ width: `${translatedPercent}%` }"
        />
        <div
          class="absolute inset-y-0 left-0 rounded-full bg-emerald-500 transition-all duration-500"
          :style="{ width: `${approvedPercent}%` }"
        />
      </div>
      <span class="whitespace-nowrap text-xs font-medium text-lf-text-muted">
        {{ translatedPercent }}% {{ t('workspace.stats.progress') }}
      </span>
    </div>
  </div>
</template>
