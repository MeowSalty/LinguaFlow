<script setup lang="ts">
import {
  NAlert,
  NButton,
  NCollapse,
  NCollapseItem,
  NDataTable,
  NDescriptions,
  NDescriptionsItem,
  NDrawer,
  NDrawerContent,
  NEmpty,
  NFormItem,
  NSelect,
  NStatistic,
  NTag,
  NText,
  useMessage,
  type DataTableColumns,
  type DataTableRowKey,
  type SelectOption,
} from 'naive-ui'
import { useI18n } from 'vue-i18n'

import { applyGlossaryPrune, previewGlossaryPrune, type ApiSchemas } from '@/api/client'
import BatchContentViewer from '@/components/workspace/BatchContentViewer.vue'
import { useBackendsStore } from '@/stores/backends'
import { usePrunePromptTemplatesStore } from '@/stores/prunePromptTemplates'

type Suggestion = ApiSchemas['GlossaryPruneSuggestion']
type Preview = ApiSchemas['GlossaryPrunePreview']
type ApplyResult = ApiSchemas['GlossaryPruneApplyResult']
type Diagnostics = ApiSchemas['GlossaryPruneDiagnostics']

const props = defineProps<{ projectId: number }>()
const emit = defineEmits<{ applied: [] }>()
const show = defineModel<boolean>('show', { required: true })

const { t } = useI18n()
const message = useMessage()
const backends = useBackendsStore()
const templates = usePrunePromptTemplatesStore()

const backendId = ref<number | null>(null)
const templateId = ref<number | null>(null)
const preview = ref<Preview | null>(null)
const result = ref<ApplyResult | null>(null)
const selectedKeys = ref<DataTableRowKey[]>([])
const previewing = ref(false)
const applying = ref(false)

const backendOptions = computed<SelectOption[]>(() =>
  backends.sortedItems.map((item) => ({ label: item.name, value: item.id })),
)
const templateOptions = computed<SelectOption[]>(() =>
  templates.items.map((item) => ({ label: item.name, value: item.id })),
)

/** 默认选中内置模板（id < 0），否则取列表第一项 */
const resolveDefaultTemplateId = (): number | null => {
  const builtin = templates.items.find((item) => item.id < 0)
  if (builtin) return builtin.id
  return templates.items[0]?.id ?? null
}
const selectedSuggestions = computed(() => {
  const ids = new Set(selectedKeys.value.map(Number))
  return preview.value?.suggestions.filter((item) => ids.has(item.entry_id)) ?? []
})

const diagnostics = computed<Diagnostics | null>(() => preview.value?.diagnostics ?? null)
const hasError = computed(
  () => !!(diagnostics.value?.error_type || diagnostics.value?.error_message),
)
const totalTokens = computed(() => {
  const d = diagnostics.value
  if (!d || d.prompt_tokens == null || d.completion_tokens == null) return null
  return d.prompt_tokens + d.completion_tokens
})

const columns = computed<DataTableColumns<Suggestion>>(() => [
  { type: 'selection', width: 44 },
  {
    title: t('workspace.glossary.prune.columns.action'),
    key: 'action',
    width: 90,
    render: (row) =>
      h(
        NTag,
        {
          size: 'small',
          bordered: false,
          type: row.action === 'delete' ? 'error' : 'warning',
        },
        { default: () => t(`workspace.glossary.prune.actions.${row.action}`) },
      ),
  },
  {
    title: t('workspace.glossary.prune.columns.source'),
    key: 'source',
    minWidth: 150,
    ellipsis: { tooltip: true },
  },
  {
    title: t('workspace.glossary.prune.columns.current'),
    key: 'old_target',
    minWidth: 190,
    render: (row) =>
      h('div', { class: 'space-y-1' }, [
        h(NText, null, { default: () => row.old_target || '—' }),
        row.old_notes ? h('div', { class: 'text-xs text-lf-text-subtle' }, row.old_notes) : null,
      ]),
  },
  {
    title: t('workspace.glossary.prune.columns.suggestion'),
    key: 'new_target',
    minWidth: 210,
    render: (row) =>
      row.action === 'delete'
        ? h(NText, { depth: 3 }, { default: () => t('workspace.glossary.prune.deleteHint') })
        : h('div', { class: 'space-y-1' }, [
            h(
              NText,
              { type: row.target_changed ? 'success' : undefined },
              { default: () => row.new_target || '—' },
            ),
            row.new_notes
              ? h('div', { class: 'text-xs text-lf-text-subtle' }, row.new_notes)
              : null,
          ]),
  },
])

const reset = (): void => {
  backendId.value = backends.sortedItems[0]?.id ?? null
  templateId.value = resolveDefaultTemplateId()
  preview.value = null
  result.value = null
  selectedKeys.value = []
}

const loadDependencies = async (): Promise<void> => {
  await Promise.all([
    backends.items.length ? Promise.resolve() : backends.loadBackends(),
    templates.items.length ? Promise.resolve() : templates.loadTemplates(),
  ])
  reset()
}

const createPreview = async (): Promise<void> => {
  if (!backendId.value) {
    message.warning(t('workspace.glossary.prune.backendRequired'))
    return
  }

  if (templateId.value == null) {
    message.warning(t('workspace.glossary.prune.templateRequired'))
    return
  }

  previewing.value = true
  try {
    preview.value = await previewGlossaryPrune(props.projectId, {
      backend_id: backendId.value,
      template_id: templateId.value,
    })
    selectedKeys.value = preview.value.suggestions.map((item) => item.entry_id)
  } catch (error) {
    message.error(
      error instanceof Error ? error.message : t('workspace.glossary.prune.previewFailed'),
      { duration: 0, closable: true },
    )
  } finally {
    previewing.value = false
  }
}

const applySelected = async (): Promise<void> => {
  if (!selectedSuggestions.value.length) return
  applying.value = true
  try {
    result.value = await applyGlossaryPrune(props.projectId, {
      changes: selectedSuggestions.value.map((item) => ({
        entry_id: item.entry_id,
        action: item.action,
        ...(item.action === 'update' && item.new_target !== undefined
          ? { target: item.new_target }
          : {}),
        ...(item.action === 'update' && item.new_notes !== undefined
          ? { notes: item.new_notes }
          : {}),
      })),
    })
    emit('applied')
    message.success(t('workspace.glossary.prune.applySuccess'))
  } catch (error) {
    message.error(
      error instanceof Error ? error.message : t('workspace.glossary.prune.applyFailed'),
      { duration: 0, closable: true },
    )
  } finally {
    applying.value = false
  }
}

watch(show, (visible) => {
  if (visible) void loadDependencies()
})
</script>

<template>
  <NDrawer v-model:show="show" :width="760" placement="right">
    <NDrawerContent :title="t('workspace.glossary.prune.title')" closable :native-scrollbar="false">
      <div v-if="result" class="space-y-5">
        <NAlert :type="result.failed ? 'warning' : 'success'" :bordered="false">
          {{ t('workspace.glossary.prune.resultSummary') }}
        </NAlert>
        <div class="grid grid-cols-3 gap-3">
          <div class="rounded-lg bg-lf-surface-muted p-4">
            <NStatistic :label="t('workspace.glossary.prune.deleted')" :value="result.deleted" />
          </div>
          <div class="rounded-lg bg-lf-surface-muted p-4">
            <NStatistic :label="t('workspace.glossary.prune.updated')" :value="result.updated" />
          </div>
          <div class="rounded-lg bg-lf-surface-muted p-4">
            <NStatistic :label="t('workspace.glossary.prune.failed')" :value="result.failed" />
          </div>
        </div>
      </div>

      <div v-else class="space-y-5">
        <NAlert type="info" :bordered="false">
          {{ t('workspace.glossary.prune.description') }}
        </NAlert>

        <div class="grid grid-cols-1 gap-3 md:grid-cols-2">
          <NFormItem :label="t('workspace.glossary.prune.backend')" required>
            <NSelect
              v-model:value="backendId"
              filterable
              :loading="backends.loading"
              :options="backendOptions"
              :placeholder="t('workspace.glossary.prune.backendPlaceholder')"
            />
          </NFormItem>
          <NFormItem :label="t('workspace.glossary.prune.template')">
            <NSelect
              v-model:value="templateId"
              filterable
              :loading="templates.loading"
              :options="templateOptions"
            />
          </NFormItem>
        </div>

        <template v-if="preview">
          <NAlert
            v-if="hasError"
            type="error"
            :bordered="false"
            :title="t('workspace.glossary.prune.diagnostics.analysisFailed')"
          >
            <div class="space-y-1">
              <template v-if="diagnostics?.error_message">{{ diagnostics.error_message }}</template>
              <div class="text-xs opacity-80">
                {{ t('workspace.glossary.prune.diagnostics.analysisFailedHint') }}
              </div>
            </div>
          </NAlert>

          <div class="grid grid-cols-2 gap-3 sm:grid-cols-4">
            <div class="rounded-lg bg-lf-surface-muted p-3">
              <NStatistic :label="t('workspace.glossary.prune.total')" :value="preview.total" />
            </div>
            <div class="rounded-lg bg-red-50 p-3 dark:bg-red-950/20">
              <NStatistic
                :label="t('workspace.glossary.prune.toDelete')"
                :value="preview.to_delete"
              />
            </div>
            <div class="rounded-lg bg-amber-50 p-3 dark:bg-amber-950/20">
              <NStatistic
                :label="t('workspace.glossary.prune.toUpdate')"
                :value="preview.to_update"
              />
            </div>
            <div class="rounded-lg bg-emerald-50 p-3 dark:bg-emerald-950/20">
              <NStatistic :label="t('workspace.glossary.prune.toKeep')" :value="preview.to_keep" />
            </div>
          </div>

          <NCollapse v-if="diagnostics" :default-expanded-names="hasError ? ['diagnostics'] : []">
            <NCollapseItem name="diagnostics">
              <template #header>
                <div class="flex items-center gap-2">
                  <span class="text-sm font-medium">
                    {{ t('workspace.glossary.prune.diagnostics.title') }}
                  </span>
                  <NTag v-if="hasError" size="tiny" type="error" :bordered="false">
                    {{ diagnostics?.error_type || t('workspace.glossary.prune.diagnostics.error') }}
                  </NTag>
                </div>
              </template>

              <div class="space-y-4">
                <div>
                  <div class="mb-2 text-xs font-medium text-lf-text-subtle">
                    {{ t('workspace.glossary.prune.diagnostics.summary') }}
                  </div>
                  <NDescriptions label-placement="left" :column="2" size="small" bordered>
                    <NDescriptionsItem
                      :label="t('workspace.glossary.prune.diagnostics.metrics.backend')"
                    >
                      {{ diagnostics?.backend_name || '—' }}
                    </NDescriptionsItem>
                    <NDescriptionsItem
                      :label="t('workspace.glossary.prune.diagnostics.metrics.template')"
                    >
                      {{ diagnostics?.template_name || '—' }}
                    </NDescriptionsItem>
                    <NDescriptionsItem
                      :label="t('workspace.glossary.prune.diagnostics.metrics.duration')"
                    >
                      <template v-if="diagnostics?.duration_ms != null">
                        {{
                          t('workspace.glossary.prune.diagnostics.metrics.durationValue', {
                            ms: diagnostics.duration_ms,
                          })
                        }}
                      </template>
                      <template v-else>—</template>
                    </NDescriptionsItem>
                    <NDescriptionsItem
                      :label="t('workspace.glossary.prune.diagnostics.metrics.httpStatus')"
                    >
                      {{ diagnostics?.http_status ?? '—' }}
                    </NDescriptionsItem>
                    <NDescriptionsItem
                      :label="t('workspace.glossary.prune.diagnostics.metrics.promptTokens')"
                    >
                      {{ diagnostics?.prompt_tokens?.toLocaleString() ?? '—' }}
                    </NDescriptionsItem>
                    <NDescriptionsItem
                      :label="t('workspace.glossary.prune.diagnostics.metrics.completionTokens')"
                    >
                      {{ diagnostics?.completion_tokens?.toLocaleString() ?? '—' }}
                    </NDescriptionsItem>
                    <NDescriptionsItem
                      :label="t('workspace.glossary.prune.diagnostics.metrics.totalTokens')"
                    >
                      <template v-if="totalTokens != null">
                        {{
                          t('workspace.glossary.prune.diagnostics.metrics.totalTokensValue', {
                            total: totalTokens.toLocaleString(),
                          })
                        }}
                      </template>
                      <template v-else>—</template>
                    </NDescriptionsItem>
                    <NDescriptionsItem
                      :label="t('workspace.glossary.prune.diagnostics.metrics.parsedCount')"
                    >
                      <span
                        v-if="diagnostics?.parsed_count != null && diagnostics?.entry_count != null"
                      >
                        {{ diagnostics.parsed_count }} / {{ diagnostics.entry_count }}
                      </span>
                      <template v-else>{{
                        diagnostics?.parsed_count ?? diagnostics?.entry_count ?? '—'
                      }}</template>
                    </NDescriptionsItem>
                    <NDescriptionsItem
                      :label="t('workspace.glossary.prune.diagnostics.metrics.repairedOps')"
                    >
                      <template v-if="diagnostics?.repaired_ops?.length">
                        {{
                          t('workspace.glossary.prune.diagnostics.metrics.repairedOpsValue', {
                            count: diagnostics.repaired_ops.length,
                          })
                        }}
                        <span class="ml-1 text-xs text-lf-text-subtle">
                          ({{ diagnostics.repaired_ops.join(', ') }})
                        </span>
                      </template>
                      <template v-else>—</template>
                    </NDescriptionsItem>
                    <NDescriptionsItem
                      v-if="hasError"
                      :label="t('workspace.glossary.prune.diagnostics.errorType')"
                    >
                      <span class="font-mono text-xs">{{ diagnostics?.error_type || '—' }}</span>
                    </NDescriptionsItem>
                  </NDescriptions>
                </div>

                <BatchContentViewer
                  v-if="diagnostics?.system_prompt"
                  :content="diagnostics.system_prompt"
                  :label="t('workspace.glossary.prune.diagnostics.content.systemPrompt')"
                  :truncated="diagnostics?.system_truncated"
                  :original-length="diagnostics?.system_length"
                />
                <BatchContentViewer
                  v-if="diagnostics?.user_message"
                  :content="diagnostics.user_message"
                  :label="t('workspace.glossary.prune.diagnostics.content.userMessage')"
                  :truncated="diagnostics?.user_truncated"
                  :original-length="diagnostics?.user_length"
                />
                <BatchContentViewer
                  v-if="diagnostics?.received_content"
                  :content="diagnostics.received_content"
                  :label="t('workspace.glossary.prune.diagnostics.content.received')"
                  :truncated="diagnostics?.received_truncated"
                  :original-length="diagnostics?.received_length"
                />
              </div>
            </NCollapseItem>
          </NCollapse>
          <div
            v-else
            class="rounded-lg bg-lf-surface-muted p-3 text-center text-xs text-lf-text-muted"
          >
            {{ t('workspace.glossary.prune.diagnostics.noData') }}
          </div>

          <NDataTable
            v-model:checked-row-keys="selectedKeys"
            :columns="columns"
            :data="preview.suggestions"
            :row-key="(row: Suggestion) => row.entry_id"
            :scroll-x="720"
            max-height="460"
          >
            <template #empty>
              <NEmpty :description="t('workspace.glossary.prune.noSuggestions')" class="py-10" />
            </template>
          </NDataTable>
        </template>
      </div>

      <template #footer>
        <div class="flex w-full items-center justify-between gap-3">
          <NText v-if="preview && !result" depth="3" class="text-xs">
            {{ t('workspace.glossary.prune.selectedCount', { count: selectedKeys.length }) }}
          </NText>
          <span v-else />
          <div class="flex gap-2">
            <NButton @click="show = false">{{ t('workspace.glossary.prune.close') }}</NButton>
            <NButton
              v-if="!result"
              secondary
              :loading="previewing"
              :disabled="!backendId"
              @click="createPreview"
            >
              <template #icon><IconCarbonMagicWand /></template>
              {{
                t(
                  preview
                    ? 'workspace.glossary.prune.analyzeAgain'
                    : 'workspace.glossary.prune.analyze',
                )
              }}
            </NButton>
            <NButton
              v-if="preview && !result"
              type="primary"
              :loading="applying"
              :disabled="selectedKeys.length === 0"
              @click="applySelected"
            >
              {{ t('workspace.glossary.prune.apply', { count: selectedKeys.length }) }}
            </NButton>
          </div>
        </div>
      </template>
    </NDrawerContent>
  </NDrawer>
</template>
