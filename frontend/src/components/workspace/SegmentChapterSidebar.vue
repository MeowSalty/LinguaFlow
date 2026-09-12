<script setup lang="ts">
import { NButton } from 'naive-ui'
import { computed } from 'vue'

import { useProjectWorkspaceStore } from '@/stores/projectWorkspace'
import { t } from '@/i18n'

const props = defineProps<{
  projectId: number | null
}>()

const emit = defineEmits<{
  close: []
  batchTranslate: []
  batchQaRecheck: []
}>()

const workspace = useProjectWorkspaceStore()

const chapters = computed(() => workspace.segmentGroups)

/** 当前激活项：null 表示"全部章节"视图 */
const activeKey = computed(() => workspace.epubActiveGroupKey)

const isAllActive = computed(() => activeKey.value === null)

/** 多选（批量处理）模式：行点击只切换选中，不导航、不加载正文 */
const multiSelect = computed(() => workspace.chapterMultiSelect)

const selectedKeys = computed(() => workspace.epubSelectedGroupKeys)

const selectedCount = computed(() => workspace.epubSelectedGroupKeys.size)

const selectAll = (): void => {
  if (!props.projectId || !workspace.activeResourceId) return
  workspace.exitChapter()
}

const selectChapter = (groupKey: string, title: string): void => {
  if (!props.projectId || !workspace.activeResourceId) return
  workspace.enterChapter(groupKey, title)
}

const toggleSelection = (groupKey: string): void => {
  workspace.toggleEpubGroupSelection(groupKey)
}
</script>

<template>
  <div
    class="flex h-full min-h-0 flex-col overflow-hidden rounded-lf-card border border-lf-border-soft bg-lf-surface shadow-sm shadow-lf-shadow"
  >
    <!-- 头部：常态 = 标题 + 章节计数 + 批量处理入口；多选态 = 已选数量 + 全选/清空 + 退出 -->
    <div class="flex shrink-0 items-center gap-2 border-b border-lf-border-soft px-3 py-2">
      <template v-if="!multiSelect">
        <span class="text-[13px] font-semibold text-lf-text-strong">
          {{ t('workspace.segment.chapterSidebarTitle') }}
        </span>
        <span class="text-[11px] tabular-nums text-lf-text-subtle">
          {{ chapters.length }}
        </span>
        <button
          type="button"
          class="ml-auto shrink-0 text-xs font-medium text-lf-text-muted transition-colors hover:text-brand-600"
          @click="workspace.enterChapterMultiSelect()"
        >
          {{ t('workspace.segment.batchSelect') }}
        </button>
      </template>
      <template v-else>
        <span class="text-xs font-medium tabular-nums text-lf-text-strong">
          {{ t('workspace.segment.batchSelectedCount', { count: selectedCount }) }}
        </span>
        <button
          type="button"
          class="shrink-0 text-xs text-lf-text-muted transition-colors hover:text-lf-text-strong"
          @click="workspace.selectAllEpubGroups()"
        >
          {{ t('workspace.segment.batchSelectAll') }}
        </button>
        <button
          type="button"
          class="shrink-0 text-xs text-lf-text-muted transition-colors hover:text-lf-text-strong"
          @click="workspace.clearEpubGroupSelection()"
        >
          {{ t('workspace.segment.batchClear') }}
        </button>
        <button
          type="button"
          class="ml-auto shrink-0 text-xs font-medium text-lf-text-muted transition-colors hover:text-lf-text-strong"
          @click="workspace.exitChapterMultiSelect()"
        >
          {{ t('workspace.segment.batchExit') }}
        </button>
      </template>
      <button
        type="button"
        class="flex h-5 w-5 shrink-0 items-center justify-center rounded text-[13px] text-lf-text-subtle transition-colors hover:bg-lf-surface-muted hover:text-lf-text-strong"
        :title="t('workspace.editor.collapsePanel')"
        @click="emit('close')"
      >
        ✕
      </button>
    </div>

    <!-- 章节列表：多选态隐藏"全部章节"导航项，行内复选框仅承载选中标识 -->
    <div class="lf-scroll min-h-0 flex-1 overflow-y-auto p-1.5">
      <button
        v-if="!multiSelect"
        type="button"
        class="relative mb-0.5 block w-full cursor-pointer rounded-lf-ctl px-2.5 py-2 text-left transition-colors"
        :class="
          isAllActive
            ? 'bg-lf-brand-soft text-brand-700'
            : 'text-lf-text-muted hover:bg-lf-surface-muted/60 hover:text-lf-text-strong'
        "
        :aria-current="isAllActive ? 'page' : undefined"
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
          !multiSelect && activeKey === group.group_key
            ? 'bg-lf-brand-soft text-brand-700'
            : multiSelect && selectedKeys.has(group.group_key)
              ? 'bg-lf-brand-soft/70 text-lf-text-strong'
              : 'text-lf-text-muted hover:bg-lf-surface-muted/60 hover:text-lf-text-strong'
        "
        :aria-pressed="multiSelect ? selectedKeys.has(group.group_key) : undefined"
        :aria-current="!multiSelect && activeKey === group.group_key ? 'page' : undefined"
        @click="
          multiSelect
            ? toggleSelection(group.group_key)
            : selectChapter(group.group_key, group.group_title)
        "
      >
        <span
          v-if="!multiSelect && activeKey === group.group_key"
          class="absolute top-1/2 left-0 h-4 w-0.5 -translate-y-1/2 rounded-full bg-brand-500"
        />
        <div v-if="multiSelect" class="flex items-start gap-2">
          <span
            class="mt-0.5 flex h-4 w-4 shrink-0 items-center justify-center rounded border transition-colors"
            :class="
              selectedKeys.has(group.group_key)
                ? 'border-brand-500 bg-brand-500 text-white'
                : 'border-lf-border bg-lf-surface'
            "
            aria-hidden="true"
          >
            <IconCarbonCheckmark v-if="selectedKeys.has(group.group_key)" class="h-3 w-3" />
          </span>
          <span class="min-w-0 flex-1">
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
          </span>
        </div>
        <template v-else>
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
        </template>
      </button>
    </div>

    <!-- 多选态底部操作条：批量翻译 / QA 重检，未选择时禁用 -->
    <div
      v-if="multiSelect"
      class="flex shrink-0 items-center gap-2 border-t border-lf-border-soft p-2"
    >
      <NButton
        size="small"
        type="primary"
        class="flex-1"
        :disabled="selectedCount === 0"
        @click="emit('batchTranslate')"
      >
        {{ t('workspace.segment.batchTranslate') }}
      </NButton>
      <NButton
        size="small"
        class="flex-1"
        :disabled="selectedCount === 0"
        @click="emit('batchQaRecheck')"
      >
        {{ t('workspace.selection.qaRecheck') }}
      </NButton>
    </div>
  </div>
</template>
