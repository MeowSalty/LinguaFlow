<script setup lang="ts">
import { inject, ref } from 'vue'
import { NAlert, NButton, NDataTable, NEmpty, NIcon, NInput, NTooltip } from 'naive-ui'
import { useI18n } from 'vue-i18n'

import { type ApiSchemas } from '@/api/client'
import GlossaryPruneDrawer from '@/components/workspace/GlossaryPruneDrawer.vue'
import { GlossaryMgmtKey } from '@/composables/useGlossaryManagement'
import { useGlossaryStore, type GlossarySyncQueueItem } from '@/stores/glossary'

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

const handlePruneApplied = async (payload: {
  targetChanges: GlossarySyncQueueItem[]
}): Promise<void> => {
  if (!props.projectId) return
  await glossary.loadEntries(props.projectId)

  if (payload.targetChanges.length === 0) return

  // 仅同步术语表中已确认写入新译文的条目，避免 partial apply 误同步
  const confirmedById = new Map(glossary.items.map((entry) => [entry.id, entry]))
  const confirmedChanges = payload.targetChanges.filter((item) => {
    const entry = confirmedById.get(item.entryId)
    return entry != null && entry.target === item.newTarget
  })
  if (confirmedChanges.length === 0) return

  pruneDrawerVisible.value = false
  await glossary.openSyncQueue(props.projectId, confirmedChanges)
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
      <div
        class="flex flex-col gap-2.5 rounded-xl border border-lf-border-soft bg-lf-surface px-3 py-2.5 shadow-sm shadow-lf-shadow sm:flex-row sm:items-center sm:justify-between sm:gap-3"
      >
        <div class="flex min-w-0 flex-1 items-center gap-3">
          <NInput
            v-model:value="glossary.searchQuery"
            clearable
            size="small"
            class="w-full sm:max-w-xs"
            :placeholder="t('workspace.segment.searchPlaceholder')"
          />
          <span
            v-if="glossary.items.length > 0"
            class="hidden shrink-0 text-xs tabular-nums text-lf-text-muted sm:inline"
          >
            {{ glossary.filteredItems.length }}
            <span class="text-lf-text-subtle">/ {{ glossary.items.length }}</span>
          </span>
        </div>
        <div class="flex shrink-0 items-center justify-end gap-0.5">
          <NTooltip trigger="hover" placement="top">
            <template #trigger>
              <span class="inline-flex">
                <NButton
                  quaternary
                  circle
                  size="small"
                  class="text-lf-text-muted hover:text-lf-text-strong"
                  :disabled="!projectId || glossary.items.length === 0"
                  :aria-label="t('workspace.glossary.actions.prune')"
                  @click="pruneDrawerVisible = true"
                >
                  <template #icon>
                    <NIcon size="16"><IconCarbonMagicWand /></NIcon>
                  </template>
                </NButton>
              </span>
            </template>
            {{ t('workspace.glossary.actions.prune') }}
          </NTooltip>
          <NTooltip trigger="hover" placement="top">
            <template #trigger>
              <span class="inline-flex">
                <NButton
                  quaternary
                  circle
                  size="small"
                  class="text-lf-text-muted hover:text-lf-text-strong"
                  :loading="glossary.importing"
                  :aria-label="t('workspace.glossary.actions.import')"
                  @click="handleImport"
                >
                  <template #icon>
                    <NIcon size="16"><IconCarbonUpload /></NIcon>
                  </template>
                </NButton>
              </span>
            </template>
            {{ t('workspace.glossary.actions.import') }}
          </NTooltip>
          <NTooltip trigger="hover" placement="top">
            <template #trigger>
              <span class="inline-flex">
                <NButton
                  quaternary
                  circle
                  size="small"
                  class="text-lf-text-muted hover:text-lf-text-strong"
                  :aria-label="t('workspace.glossary.actions.export')"
                  @click="handleExport"
                >
                  <template #icon>
                    <NIcon size="16"><IconCarbonDownload /></NIcon>
                  </template>
                </NButton>
              </span>
            </template>
            {{ t('workspace.glossary.actions.export') }}
          </NTooltip>
          <span class="mx-1.5 h-4 w-px bg-lf-border-soft" />
          <NButton type="primary" size="small" strong @click="handleCreate">
            <template #icon>
              <NIcon size="16"><IconCarbonAdd /></NIcon>
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
