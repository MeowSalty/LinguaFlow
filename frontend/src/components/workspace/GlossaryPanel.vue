<script setup lang="ts">
import { inject, ref } from 'vue'
import { NAlert, NButton, NDataTable, NEmpty, NIcon, NInput } from 'naive-ui'
import { useI18n } from 'vue-i18n'

import { type ApiSchemas } from '@/api/client'
import GlossaryPruneDrawer from '@/components/workspace/GlossaryPruneDrawer.vue'
import { GlossaryMgmtKey } from '@/composables/useGlossaryManagement'
import { useGlossaryStore } from '@/stores/glossary'

const { t } = useI18n()
const glossary = useGlossaryStore()

const props = defineProps<{
  projectId: number | null
}>()

const pruneDrawerVisible = ref(false)

// 从父组件注入术语表管理实例（消除重复实例）
const glossaryMgmt = inject(GlossaryMgmtKey)!

const handleCreate = (): void => {
  glossaryMgmt.openCreateGlossaryDrawer()
}

const handleExport = (): void => {
  void glossaryMgmt.handleGlossaryExport()
}

const handleImport = (): void => {
  glossaryMgmt.glossaryImportVisible.value = true
}

const handlePruneApplied = (): void => {
  if (props.projectId) void glossary.loadEntries(props.projectId)
}
</script>

<template>
  <div class="space-y-4 pt-3">
    <div class="rounded-xl border border-lf-border-soft bg-lf-surface-muted/60 p-4">
      <div class="mb-4 flex flex-col gap-1">
        <h3 class="text-base font-semibold text-lf-text-strong">
          {{ t('workspace.sections.glossary.title') }}
        </h3>
        <p class="text-sm text-lf-text-muted">
          {{ t('workspace.sections.glossary.description') }}
        </p>
      </div>
      <div class="flex flex-col gap-3 xl:flex-row xl:items-center xl:justify-between">
        <NInput
          v-model:value="glossary.searchQuery"
          clearable
          class="md:max-w-sm"
          :placeholder="t('workspace.segment.searchPlaceholder')"
        />
        <div class="flex flex-wrap gap-2">
          <NButton
            secondary
            :disabled="!projectId || glossary.items.length === 0"
            @click="pruneDrawerVisible = true"
          >
            <template #icon>
              <NIcon><IconCarbonMagicWand /></NIcon>
            </template>
            {{ t('workspace.glossary.actions.prune') }}
          </NButton>
          <NButton secondary :loading="glossary.importing" @click="handleImport">
            <template #icon>
              <NIcon><IconCarbonUpload /></NIcon>
            </template>
            {{ t('workspace.glossary.actions.import') }}
          </NButton>
          <NButton secondary @click="handleExport">
            <template #icon>
              <NIcon><IconCarbonDownload /></NIcon>
            </template>
            {{ t('workspace.glossary.actions.export') }}
          </NButton>
          <NButton type="primary" @click="handleCreate">
            <template #icon>
              <NIcon><IconCarbonAdd /></NIcon>
            </template>
            {{ t('workspace.glossary.actions.create') }}
          </NButton>
        </div>
      </div>
    </div>

    <NAlert v-if="glossary.error" type="error" :bordered="false">
      {{ glossary.error }}
    </NAlert>

    <NAlert v-if="glossary.importError" type="error" :bordered="false">
      {{ glossary.importError }}
    </NAlert>

    <NDataTable
      :columns="glossaryMgmt.glossaryColumns.value"
      :data="glossary.filteredItems"
      :loading="glossary.loading"
      :row-key="(row: ApiSchemas['GlossaryEntry']) => row.id"
      :scroll-x="960"
    >
      <template #empty>
        <NEmpty
          v-if="!glossary.loading && glossary.filteredItems.length === 0"
          class="py-12"
          :description="
            glossary.searchQuery.trim()
              ? t('workspace.glossary.empty.filtered')
              : t('workspace.glossary.empty.default')
          "
        />
      </template>
    </NDataTable>

    <GlossaryPruneDrawer
      v-if="projectId"
      v-model:show="pruneDrawerVisible"
      :project-id="projectId"
      @applied="handlePruneApplied"
    />
  </div>
</template>
