<script setup lang="ts">
import { ref, computed, onMounted } from 'vue'
import { NButton, NIcon, NBadge, NProgress, NEmpty } from 'naive-ui'
import { useI18n } from 'vue-i18n'

import { type ApiSchemas } from '@/api/client'
import { getJobProgressText } from '@/composables/useWorkspaceUtils'
import { useGlobalJobTrackerStore } from '@/stores/globalJobTracker'

type TranslationJob = ApiSchemas['TranslationJob']

const { t } = useI18n()
const tracker = useGlobalJobTrackerStore()

const isPanelOpen = ref(false)

onMounted(() => {
  void tracker.initialize()
})

const activeCount = computed(() => tracker.activeJobs.length)

const togglePanel = (): void => {
  isPanelOpen.value = !isPanelOpen.value
}

const closePanel = (): void => {
  isPanelOpen.value = false
}

const handleOpenDetail = (jobId: number): void => {
  void tracker.openDetail(jobId)
  isPanelOpen.value = false
}

const handleUntrack = (jobId: number, e: MouseEvent): void => {
  e.stopPropagation()
  tracker.untrackJob(jobId)
}

const handleClearCompleted = (): void => {
  tracker.clearCompleted()
}

const isTerminal = (status: TranslationJob['status']): boolean =>
  ['completed', 'failed', 'cancelled'].includes(status)

const progressPercent = (job: TranslationJob): number => {
  if (job.status === 'completed') return 100
  if (job.status === 'failed' || job.status === 'cancelled') return 0
  if (job.total_segments > 0) return Math.round((job.completed_segments / job.total_segments) * 100)
  return 0
}

const progressStatus = (job: TranslationJob): 'success' | 'error' | 'default' => {
  if (job.status === 'completed') return 'success'
  if (job.status === 'failed') return 'error'
  return 'default'
}
</script>

<template>
  <div
    v-if="tracker.initialized && tracker.trackedJobs.length > 0"
    class="fixed bottom-6 right-6 z-50"
  >
    <!-- 展开面板 -->
    <Transition name="tracker-panel">
      <div
        v-if="isPanelOpen"
        class="absolute bottom-16 right-0 w-80 max-h-[60vh] flex flex-col overflow-hidden rounded-2xl border border-lf-border-soft bg-lf-surface/95 shadow-[0_4px_24px_rgba(0,0,0,0.12)] backdrop-blur-xl"
      >
        <!-- 头部 -->
        <div class="flex items-center justify-between border-b border-lf-border-soft px-4 py-3">
          <div class="flex items-center gap-2">
            <span class="text-sm font-semibold text-lf-text-strong">{{
              t('globalJobTracker.title')
            }}</span>
            <NBadge v-if="activeCount > 0" :value="activeCount" type="info" :max="99" />
          </div>
          <NButton quaternary circle size="tiny" @click="closePanel">
            <template #icon>
              <NIcon size="14"><IconCarbonClose /></NIcon>
            </template>
          </NButton>
        </div>

        <!-- 任务列表 -->
        <div class="flex-1 overflow-y-auto">
          <div v-if="tracker.displayJobs.length === 0" class="py-8">
            <NEmpty size="small" :description="t('globalJobTracker.noTrackedJobs')" />
          </div>

          <div
            v-for="job in tracker.displayJobs"
            :key="job.id"
            class="group cursor-pointer border-b border-lf-border-soft/50 px-4 py-3 transition-colors hover:bg-lf-surface-muted/50"
            @click="handleOpenDetail(job.id)"
          >
            <!-- 第一行：状态图标 + 任务ID + 项目名 + 移除按钮 -->
            <div class="flex items-center gap-2">
              <!-- 状态指示器 -->
              <span
                v-if="job.status === 'running'"
                class="h-2 w-2 shrink-0 rounded-full bg-blue-500 animate-pulse"
              />
              <span
                v-else-if="job.status === 'pending'"
                class="h-2 w-2 shrink-0 rounded-full bg-amber-500"
              />
              <span
                v-else-if="job.status === 'completed'"
                class="h-2 w-2 shrink-0 rounded-full bg-emerald-500"
              />
              <span
                v-else-if="job.status === 'failed'"
                class="h-2 w-2 shrink-0 rounded-full bg-red-500"
              />
              <span v-else class="h-2 w-2 shrink-0 rounded-full bg-gray-400" />

              <span class="text-xs font-mono text-lf-text-muted">#{{ job.id }}</span>
              <span v-if="job.project_name" class="mx-0.5 text-xs text-lf-text-subtle">·</span>
              <span class="min-w-0 flex-1 truncate text-sm font-medium text-lf-text-strong">
                {{ job.project_name || '' }}
              </span>

              <!-- 移除按钮（仅终态任务） -->
              <NButton
                v-if="isTerminal(job.status)"
                quaternary
                circle
                size="tiny"
                class="shrink-0 opacity-0 transition-opacity group-hover:opacity-100"
                @click="(e: MouseEvent) => handleUntrack(job.id, e)"
              >
                <template #icon>
                  <NIcon size="12"><IconCarbonClose /></NIcon>
                </template>
              </NButton>
            </div>

            <!-- 第二行：进度条 + 百分比 -->
            <div class="mt-1.5 flex items-center gap-2">
              <NProgress
                type="line"
                :percentage="progressPercent(job)"
                :show-indicator="false"
                :stroke-width="4"
                :border-radius="2"
                :processing="job.status === 'running'"
                :status="progressStatus(job)"
                class="flex-1"
              />
              <span class="w-8 text-right text-xs font-medium text-lf-text-muted tabular-nums">
                {{ progressPercent(job) }}%
              </span>
            </div>

            <!-- 第三行：进度文案 -->
            <div class="mt-1 text-xs text-lf-text-muted">
              {{ getJobProgressText(job) }}
            </div>
          </div>
        </div>

        <!-- 底部操作栏 -->
        <div v-if="tracker.hasTerminalJobs" class="border-t border-lf-border-soft px-4 py-2.5">
          <NButton quaternary size="small" block @click="handleClearCompleted">
            {{ t('globalJobTracker.clearCompleted') }}
          </NButton>
        </div>
      </div>
    </Transition>

    <!-- FAB 按钮 -->
    <button
      type="button"
      class="relative flex h-12 w-12 cursor-pointer items-center justify-center rounded-full border border-lf-border-soft bg-lf-surface/90 shadow-lg shadow-lf-shadow-strong backdrop-blur-xl transition-all hover:ring-2 hover:ring-brand-500/30"
      :class="{ 'ring-2 ring-brand-500/30': isPanelOpen }"
      @click="togglePanel"
    >
      <IconCarbonActivity class="h-4 w-4 text-brand-500" />

      <!-- 活跃任务徽标（带脉冲动画） -->
      <span v-if="activeCount > 0" class="absolute -right-1 -top-1">
        <span
          class="absolute inline-flex h-5 w-5 animate-ping rounded-full bg-brand-400 opacity-40"
        />
        <span class="relative">
          <NBadge :value="activeCount" type="info" :max="99" />
        </span>
      </span>
    </button>
  </div>
</template>

<style scoped>
.tracker-panel-enter-active {
  transition: all 0.2s ease-out;
}

.tracker-panel-leave-active {
  transition: all 0.15s ease-in;
}

.tracker-panel-enter-from {
  transform: scale(0.9) translateY(8px);
  opacity: 0;
}

.tracker-panel-leave-to {
  transform: scale(0.9) translateY(8px);
  opacity: 0;
}
</style>
