<script setup lang="ts">
import { computed } from 'vue'

import { useProjectWorkspaceStore } from '@/stores/projectWorkspace'
import { t } from '@/i18n'

const props = defineProps<{
  projectId: number | null
}>()

const workspace = useProjectWorkspaceStore()

const chapters = computed(() => workspace.segmentGroups)

/** 当前激活项：null 表示"全部章节"视图 */
const activeKey = computed(() => workspace.epubActiveGroupKey)

const isAllActive = computed(() => activeKey.value === null)

const selectAll = (): void => {
  if (!props.projectId || !workspace.activeResourceId) return
  workspace.exitChapter()
  void workspace.loadSegments(props.projectId, workspace.activeResourceId)
}

const selectChapter = (groupKey: string, title: string): void => {
  if (!props.projectId || !workspace.activeResourceId) return
  workspace.enterChapter(groupKey, title)
  void workspace.loadSegments(props.projectId, workspace.activeResourceId, false, groupKey)
}
</script>

<template>
  <div
    class="flex h-full min-h-0 flex-col overflow-hidden rounded-lf-card border border-lf-border-soft bg-lf-surface shadow-sm shadow-lf-shadow"
  >
    <!-- 头部：标题 + 章节计数 -->
    <div class="flex shrink-0 items-center gap-2 border-b border-lf-border-soft px-3 py-2">
      <span class="text-[13px] font-semibold text-lf-text-strong">
        {{ t('workspace.segment.chapterSidebarTitle') }}
      </span>
      <span class="ml-auto text-[11px] tabular-nums text-lf-text-subtle">
        {{ chapters.length }}
      </span>
    </div>

    <!-- 章节列表 -->
    <div class="lf-scroll min-h-0 flex-1 overflow-y-auto p-1.5">
      <button
        type="button"
        class="relative mb-0.5 block w-full cursor-pointer rounded-lf-ctl px-2.5 py-2 text-left transition-colors"
        :class="
          isAllActive
            ? 'bg-lf-brand-soft text-brand-700'
            : 'text-lf-text-muted hover:bg-lf-surface-muted/60 hover:text-lf-text-strong'
        "
        @click="selectAll"
      >
        <span
          v-if="isAllActive"
          class="absolute top-1/2 left-0 h-4 w-0.5 -translate-y-1/2 rounded-full bg-brand-500"
        />
        <span class="block truncate text-xs font-medium">
          {{ t('workspace.segment.chapterAll') }}
        </span>
      </button>

      <button
        v-for="group in chapters"
        :key="group.group_key"
        type="button"
        class="relative mb-0.5 block w-full cursor-pointer rounded-lf-ctl px-2.5 py-2 text-left transition-colors"
        :class="
          activeKey === group.group_key
            ? 'bg-lf-brand-soft text-brand-700'
            : 'text-lf-text-muted hover:bg-lf-surface-muted/60 hover:text-lf-text-strong'
        "
        @click="selectChapter(group.group_key, group.group_title)"
      >
        <span
          v-if="activeKey === group.group_key"
          class="absolute top-1/2 left-0 h-4 w-0.5 -translate-y-1/2 rounded-full bg-brand-500"
        />
        <span class="block truncate text-xs font-medium" :title="group.group_title">
          {{ group.group_title }}
        </span>
        <span class="mt-0.5 block text-[10.5px] tabular-nums text-lf-text-subtle">
          {{
            t('workspace.segment.chapterProgress', {
              translated: group.translated_count,
              count: group.segment_count,
            })
          }}
        </span>
      </button>
    </div>
  </div>
</template>
