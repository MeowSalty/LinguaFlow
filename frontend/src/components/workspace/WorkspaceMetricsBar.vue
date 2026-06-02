<script setup lang="ts">
import { NIcon, NProgress } from 'naive-ui'
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'

const props = defineProps<{
  totalResources: number
  readyResources: number
  totalSegments: number
  translatedSegments: number
  runningJobs: number
}>()

const { t } = useI18n()

const progressPercent = computed(() => {
  if (props.totalSegments === 0) return 0
  return Math.round((props.translatedSegments / props.totalSegments) * 100)
})
</script>

<template>
  <div
    class="flex flex-wrap items-center gap-x-5 gap-y-2 rounded-xl border border-lf-border-soft bg-lf-surface px-5 py-3 shadow-sm shadow-lf-shadow"
  >
    <!-- 资源文件 -->
    <div class="flex items-center gap-2">
      <div
        class="flex h-7 w-7 items-center justify-center rounded-lg bg-blue-50 text-blue-500 dark:bg-blue-500/10"
      >
        <NIcon size="14"><IconLucideFiles /></NIcon>
      </div>
      <span class="text-sm font-medium text-lf-text-strong">{{ totalResources }}</span>
      <span class="text-xs text-lf-text-muted">{{ t('workspace.stats.resources') }}</span>
    </div>

    <span class="hidden h-4 w-px bg-lf-border-soft sm:block" />

    <!-- 就绪资源 -->
    <div class="flex items-center gap-2">
      <div
        class="flex h-7 w-7 items-center justify-center rounded-lg bg-emerald-50 text-emerald-500 dark:bg-emerald-500/10"
      >
        <NIcon size="14"><IconLucideCheckCircle2 /></NIcon>
      </div>
      <span class="text-sm font-medium text-lf-text-strong">{{ readyResources }}</span>
      <span class="text-xs text-lf-text-muted">{{ t('workspace.stats.readyResources') }}</span>
    </div>

    <span class="hidden h-4 w-px bg-lf-border-soft sm:block" />

    <!-- 段落总数 -->
    <div class="flex items-center gap-2">
      <div
        class="flex h-7 w-7 items-center justify-center rounded-lg bg-indigo-50 text-indigo-500 dark:bg-indigo-500/10"
      >
        <NIcon size="14"><IconLucideRows3 /></NIcon>
      </div>
      <span class="text-sm font-medium text-lf-text-strong">{{
        totalSegments.toLocaleString()
      }}</span>
      <span class="text-xs text-lf-text-muted">{{ t('workspace.stats.segments') }}</span>
    </div>

    <span class="hidden h-4 w-px bg-lf-border-soft sm:block" />

    <!-- 运行中任务 -->
    <div class="flex items-center gap-2">
      <div
        class="flex h-7 w-7 items-center justify-center rounded-lg bg-amber-50 text-amber-500 dark:bg-amber-500/10"
      >
        <NIcon size="14"><IconLucideActivity /></NIcon>
      </div>
      <span class="text-sm font-medium text-lf-text-strong">{{ runningJobs }}</span>
      <span class="text-xs text-lf-text-muted">{{ t('workspace.stats.runningJobs') }}</span>
    </div>

    <span class="hidden h-4 w-px bg-lf-border-soft lg:block" />

    <!-- 翻译进度 -->
    <div class="flex shrink-0 items-center gap-2.5 whitespace-nowrap">
      <NProgress
        type="line"
        :percentage="progressPercent"
        :show-indicator="false"
        :height="6"
        :border-radius="3"
        :color="progressPercent > 0 ? undefined : '#94a3b8'"
        :rail-color="progressPercent > 0 ? undefined : '#e2e8f0'"
        class="w-24"
        status="success"
      />
      <span class="whitespace-nowrap text-xs font-medium text-lf-text-muted">
        {{ progressPercent }}% {{ t('workspace.stats.progress') }}
      </span>
    </div>
  </div>
</template>
