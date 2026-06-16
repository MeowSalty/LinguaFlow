<script setup lang="ts">
import { NAlert, NButton, NDataTable, NEmpty, NInput, NSelect } from 'naive-ui'
import { toRef } from 'vue'
import { useI18n } from 'vue-i18n'

import { type ApiSchemas } from '@/api/client'
import { useSegmentEditing } from '@/composables/useSegmentEditing'
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

const { segmentColumns, segmentStatusOptions } = useSegmentEditing(
  projectIdRef,
  activeResourceIdRef,
  (segment) => emit('translate', segment),
)

const handleRefresh = (): void => {
  emit('refresh')
}

const handleTranslateAll = (): void => {
  emit('translate')
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
          <NButton
            type="primary"
            :disabled="workspace.segments.length === 0"
            @click="handleTranslateAll"
          >
            {{ t('workspace.job.actions.createFromSegments') }}
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
