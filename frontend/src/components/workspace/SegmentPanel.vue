<script setup lang="ts">
import type { DataTableColumns, DataTableRowKey } from 'naive-ui'
import {
  NAlert,
  NButton,
  NDataTable,
  NEmpty,
  NInput,
  NPopover,
  NSelect,
  NSpace,
  NTag,
  NText,
} from 'naive-ui'
import { computed, h, ref, toRef } from 'vue'
import { useI18n } from 'vue-i18n'

import { type ApiSchemas } from '@/api/client'
import {
  useSegmentEditing,
  formatDate,
  getSegmentStatusLabel,
  statusTagType,
} from '@/composables/useSegmentEditing'
import { useProjectWorkspaceStore } from '@/stores/projectWorkspace'

type Segment = ApiSchemas['Segment']

const { t } = useI18n()
const workspace = useProjectWorkspaceStore()

const props = defineProps<{
  projectId: number | null
}>()

const emit = defineEmits<{
  translate: [segment?: Segment]
  refresh: []
}>()

const projectIdRef = toRef(props, 'projectId')
const activeResourceIdRef = toRef(workspace, 'activeResourceId')

const {
  segmentStatusOptions,
  inlineEditingSegmentId,
  inlineEditForm,
  inlineCommentVisible,
  inlineCommentText,
  startInlineEdit,
  cancelInlineEdit,
  saveInlineEdit,
  openInlineComment,
  saveInlineComment,
} = useSegmentEditing(projectIdRef, activeResourceIdRef)

// ── 表格列定义 ──
const segmentColumns = computed<DataTableColumns<Segment>>(() => [
  {
    type: 'selection',
  },
  {
    title: '#',
    key: 'segment_index',
    width: 76,
  },
  {
    title: t('workspace.segment.columns.source'),
    key: 'source_text',
    minWidth: 260,
    render: (row) => {
      if (inlineEditingSegmentId.value === row.id) {
        return h(NInput, {
          value: inlineEditForm.source_text,
          type: 'textarea',
          autosize: { minRows: 2, maxRows: 6 },
          'onUpdate:value': (val: string) => {
            inlineEditForm.source_text = val
          },
        })
      }
      return row.source_text
    },
  },
  {
    title: t('workspace.segment.columns.target'),
    key: 'target_text',
    minWidth: 260,
    render: (row) => {
      if (inlineEditingSegmentId.value === row.id) {
        return h(NInput, {
          value: inlineEditForm.target_text,
          type: 'textarea',
          autosize: { minRows: 2, maxRows: 6 },
          placeholder: t('workspace.segment.form.target'),
          'onUpdate:value': (val: string) => {
            inlineEditForm.target_text = val
          },
        })
      }
      return (
        row.target_text ||
        h(NText, { depth: 3 }, { default: () => t('workspace.segment.emptyTarget') })
      )
    },
  },
  {
    title: t('workspace.segment.columns.status'),
    key: 'status',
    width: 120,
    render: (row) =>
      h(
        NTag,
        { size: 'small', type: statusTagType(row.status) },
        { default: () => getSegmentStatusLabel(row.status) },
      ),
  },
  {
    title: t('workspace.common.updatedAt'),
    key: 'updated_at',
    width: 170,
    render: (row) => formatDate(row.updated_at),
  },
  {
    title: t('workspace.common.actions'),
    key: 'actions',
    width: 220,
    fixed: 'right',
    render: (row) => {
      if (inlineEditingSegmentId.value === row.id) {
        return h(NSpace, { size: 4, wrap: false }, () => [
          h(
            NButton,
            {
              size: 'small',
              quaternary: true,
              onClick: () => cancelInlineEdit(),
            },
            { default: () => t('workspace.segment.actions.cancelInline') },
          ),
          h(
            NButton,
            {
              size: 'small',
              type: 'primary',
              loading: workspace.editingSegmentIds.includes(row.id),
              onClick: () => saveInlineEdit(row),
            },
            { default: () => t('workspace.segment.actions.saveInline') },
          ),
        ])
      }
      return h(NSpace, { size: 4, wrap: false }, () => [
        h(
          NButton,
          {
            size: 'small',
            quaternary: true,
            type: 'primary',
            loading: workspace.editingSegmentIds.includes(row.id),
            onClick: () => startInlineEdit(row),
          },
          { default: () => t('workspace.segment.actions.edit') },
        ),
        h(
          NPopover,
          {
            show: inlineCommentVisible.value === row.id,
            trigger: 'click',
            placement: 'bottom',
            'onUpdate:show': (show: boolean) => {
              if (show) {
                openInlineComment(row)
              } else {
                inlineCommentVisible.value = null
              }
            },
          },
          {
            trigger: () =>
              h(
                NButton,
                {
                  size: 'small',
                  quaternary: true,
                },
                { default: () => t('workspace.segment.actions.comment') },
              ),
            default: () =>
              h('div', { class: 'w-64 space-y-3' }, [
                h(NInput, {
                  value: inlineCommentText.value,
                  type: 'textarea',
                  autosize: { minRows: 2, maxRows: 4 },
                  placeholder: t('workspace.segment.form.comment'),
                  'onUpdate:value': (val: string) => {
                    inlineCommentText.value = val
                  },
                }),
                h(
                  NButton,
                  {
                    size: 'small',
                    type: 'primary',
                    block: true,
                    onClick: () => saveInlineComment(row),
                  },
                  { default: () => t('workspace.common.save') },
                ),
              ]),
          },
        ),
        h(
          NButton,
          {
            size: 'small',
            quaternary: true,
            onClick: () => emit('translate', row),
          },
          { default: () => t('workspace.segment.actions.translate') },
        ),
      ])
    },
  },
])

// ── 批量选择 ──
const selectedSegmentIds = ref<DataTableRowKey[]>([])

const clearSelectedSegments = (): void => {
  selectedSegmentIds.value = []
}

// ── 暴露给父组件，供浮动操作岛使用 ──
defineExpose({
  selectedSegmentIds,
  clearSelectedSegments,
})

// ── 原有处理函数 ──
const handleRefresh = (): void => {
  emit('refresh')
}
</script>

<template>
  <div class="space-y-4 pt-3">
    <div class="rounded-xl border border-lf-border-soft bg-lf-surface-muted/60 p-4">
      <div class="mb-4 flex flex-col gap-1">
        <h3 class="text-base font-semibold text-lf-text-strong">
          {{ t('workspace.sections.segments.title') }}
        </h3>
        <p class="text-sm text-lf-text-muted">
          {{ t('workspace.sections.segments.description') }}
        </p>
      </div>
      <div class="flex flex-col gap-3 xl:flex-row xl:items-center xl:justify-between">
        <div class="flex flex-1 flex-col gap-3 md:flex-row">
          <NSelect
            v-model:value="workspace.activeResourceId"
            clearable
            class="md:max-w-sm"
            :options="
              workspace.resources.map((resource) => ({
                label: resource.path,
                value: resource.id,
              }))
            "
            :placeholder="t('workspace.segment.resourcePlaceholder')"
            @update:value="(value: number | null) => workspace.setActiveResource(value)"
          />
          <NInput
            v-model:value="workspace.segmentSearch"
            clearable
            class="md:max-w-sm"
            :disabled="!workspace.activeResourceId"
            :placeholder="t('workspace.segment.searchPlaceholder')"
          />
          <NSelect
            v-model:value="workspace.segmentStatusFilter"
            class="md:w-44"
            :disabled="!workspace.activeResourceId"
            :options="segmentStatusOptions"
          />
        </div>
        <div class="flex flex-wrap gap-3">
          <NButton
            secondary
            :disabled="!workspace.activeResourceId"
            :loading="workspace.loadingSegments"
            @click="handleRefresh"
          >
            {{ t('workspace.actions.refresh') }}
          </NButton>
        </div>
      </div>
    </div>

    <NAlert v-if="workspace.segmentsError" type="error" :bordered="false">
      {{ workspace.segmentsError }}
    </NAlert>

    <NEmpty
      v-if="!workspace.activeResourceId"
      class="py-12"
      :description="t('workspace.segment.noResource')"
    />
    <template v-else>
      <NDataTable
        remote
        :columns="segmentColumns"
        :data="workspace.segments"
        :loading="workspace.loadingSegments"
        :row-key="(row: Segment) => row.id"
        :scroll-x="1040"
        v-model:checked-row-keys="selectedSegmentIds"
      />
      <div v-if="workspace.segmentsCursor" class="flex justify-center pt-3">
        <NButton
          :loading="workspace.loadingSegments"
          @click="workspace.loadSegments(projectId!, workspace.activeResourceId!, true)"
        >
          {{ t('common.loadMore') }}
        </NButton>
      </div>
      <NEmpty
        v-if="!workspace.loadingSegments && workspace.segments.length === 0"
        class="py-12"
        :description="t('workspace.segment.empty')"
      />
    </template>
  </div>
</template>
