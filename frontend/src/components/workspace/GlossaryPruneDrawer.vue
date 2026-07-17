<script setup lang="ts">
import {
  NAlert,
  NButton,
  NCollapse,
  NCollapseItem,
  NDataTable,
  NDrawer,
  NDrawerContent,
  NEmpty,
  NFormItem,
  NSelect,
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
import type { GlossarySyncQueueItem } from '@/stores/glossary'
import { usePrunePromptTemplatesStore } from '@/stores/prunePromptTemplates'

type Suggestion = ApiSchemas['GlossaryPruneSuggestion']
type Preview = ApiSchemas['GlossaryPrunePreview']
type ApplyResult = ApiSchemas['GlossaryPruneApplyResult']
type Diagnostics = ApiSchemas['GlossaryPruneDiagnostics']

const props = defineProps<{ projectId: number }>()
const emit = defineEmits<{
  applied: [payload: { targetChanges: GlossarySyncQueueItem[] }]
}>()
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

const formatMetric = (value: string | number | null | undefined): string => {
  if (value == null || value === '') return '—'
  return typeof value === 'number' ? value.toLocaleString() : String(value)
}

const diagnosticMetrics = computed(() => {
  const d = diagnostics.value
  if (!d) return []

  const parsed =
    d.parsed_count != null && d.entry_count != null
      ? `${d.parsed_count} / ${d.entry_count}`
      : formatMetric(d.parsed_count ?? d.entry_count)

  const metrics: { key: string; label: string; value: string; mono?: boolean }[] = [
    {
      key: 'backend',
      label: t('workspace.glossary.prune.diagnostics.metrics.backend'),
      value: formatMetric(d.backend_name),
    },
    {
      key: 'template',
      label: t('workspace.glossary.prune.diagnostics.metrics.template'),
      value: formatMetric(d.template_name),
    },
    {
      key: 'duration',
      label: t('workspace.glossary.prune.diagnostics.metrics.duration'),
      value:
        d.duration_ms != null
          ? t('workspace.glossary.prune.diagnostics.metrics.durationValue', { ms: d.duration_ms })
          : '—',
    },
    {
      key: 'http',
      label: t('workspace.glossary.prune.diagnostics.metrics.httpStatus'),
      value: formatMetric(d.http_status),
      mono: true,
    },
    {
      key: 'prompt',
      label: t('workspace.glossary.prune.diagnostics.metrics.promptTokens'),
      value: formatMetric(d.prompt_tokens),
    },
    {
      key: 'completion',
      label: t('workspace.glossary.prune.diagnostics.metrics.completionTokens'),
      value: formatMetric(d.completion_tokens),
    },
    {
      key: 'total',
      label: t('workspace.glossary.prune.diagnostics.metrics.totalTokens'),
      value:
        totalTokens.value != null
          ? t('workspace.glossary.prune.diagnostics.metrics.totalTokensValue', {
              total: totalTokens.value.toLocaleString(),
            })
          : '—',
    },
    {
      key: 'parsed',
      label: t('workspace.glossary.prune.diagnostics.metrics.parsedCount'),
      value: parsed,
    },
    {
      key: 'repaired',
      label: t('workspace.glossary.prune.diagnostics.metrics.repairedOps'),
      value: d.repaired_ops?.length
        ? `${t('workspace.glossary.prune.diagnostics.metrics.repairedOpsValue', {
            count: d.repaired_ops.length,
          })} (${d.repaired_ops.join(', ')})`
        : '—',
    },
  ]

  if (hasError.value) {
    metrics.push({
      key: 'errorType',
      label: t('workspace.glossary.prune.diagnostics.errorType'),
      value: formatMetric(d.error_type),
      mono: true,
    })
  }

  return metrics
})

const previewStats = computed(() => {
  if (!preview.value) return []
  return [
    {
      key: 'total',
      label: t('workspace.glossary.prune.total'),
      value: preview.value.total,
      valueClass: 'text-lf-text-strong',
      accentClass: 'bg-lf-text-subtle/40',
    },
    {
      key: 'delete',
      label: t('workspace.glossary.prune.toDelete'),
      value: preview.value.to_delete,
      valueClass: 'text-red-500',
      accentClass: 'bg-red-500',
    },
    {
      key: 'update',
      label: t('workspace.glossary.prune.toUpdate'),
      value: preview.value.to_update,
      valueClass: 'text-amber-500',
      accentClass: 'bg-amber-500',
    },
    {
      key: 'keep',
      label: t('workspace.glossary.prune.toKeep'),
      value: preview.value.to_keep,
      valueClass: 'text-brand-500',
      accentClass: 'bg-brand-500',
    },
  ]
})

const resultStats = computed(() => {
  if (!result.value) return []
  return [
    {
      key: 'deleted',
      label: t('workspace.glossary.prune.deleted'),
      value: result.value.deleted,
      valueClass: 'text-red-500',
    },
    {
      key: 'updated',
      label: t('workspace.glossary.prune.updated'),
      value: result.value.updated,
      valueClass: 'text-amber-500',
    },
    {
      key: 'failed',
      label: t('workspace.glossary.prune.failed'),
      value: result.value.failed,
      valueClass: result.value.failed ? 'text-red-500' : 'text-lf-text-strong',
    },
  ]
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
    const selected = selectedSuggestions.value
    result.value = await applyGlossaryPrune(props.projectId, {
      changes: selected.map((item) => ({
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
    // apply 结果仅有计数、无 per-entry 成功列表；存在失败时无法确认哪些 target 已写入，
    // 避免把未真正更新的术语送入译文同步队列。
    const targetChanges: GlossarySyncQueueItem[] =
      result.value.failed > 0
        ? []
        : selected
            .filter(
              (item) =>
                item.action === 'update' &&
                item.target_changed &&
                item.old_target !== item.new_target,
            )
            .map((item) => ({
              entryId: item.entry_id,
              source: item.source,
              oldTarget: item.old_target,
              newTarget: item.new_target,
            }))
    emit('applied', { targetChanges })
    if (result.value.failed > 0) {
      message.warning(t('workspace.glossary.prune.applyPartialSuccess'))
    } else {
      message.success(t('workspace.glossary.prune.applySuccess'))
    }
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
          <div
            v-for="stat in resultStats"
            :key="stat.key"
            class="rounded-xl border border-lf-border-soft bg-lf-surface-muted/60 px-4 py-3"
          >
            <div class="text-xs font-medium text-lf-text-muted">{{ stat.label }}</div>
            <div class="mt-1.5 text-2xl font-semibold tracking-tight" :class="stat.valueClass">
              {{ stat.value.toLocaleString() }}
            </div>
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
              <div class="text-xs text-lf-text-muted">
                {{ t('workspace.glossary.prune.diagnostics.analysisFailedHint') }}
              </div>
            </div>
          </NAlert>

          <div class="grid grid-cols-2 gap-3 sm:grid-cols-4">
            <div
              v-for="stat in previewStats"
              :key="stat.key"
              class="relative overflow-hidden rounded-xl border border-lf-border-soft bg-lf-surface px-3.5 py-3 shadow-sm shadow-lf-shadow"
            >
              <div
                class="absolute inset-y-0 left-0 w-0.5"
                :class="stat.accentClass"
                aria-hidden="true"
              />
              <div class="text-xs font-medium text-lf-text-muted">{{ stat.label }}</div>
              <div class="mt-1.5 text-2xl font-semibold tracking-tight" :class="stat.valueClass">
                {{ stat.value.toLocaleString() }}
              </div>
            </div>
          </div>

          <NCollapse v-if="diagnostics" :default-expanded-names="hasError ? ['diagnostics'] : []">
            <NCollapseItem name="diagnostics">
              <template #header>
                <div class="flex min-w-0 items-center gap-2">
                  <span class="text-sm font-medium text-lf-text-strong">
                    {{ t('workspace.glossary.prune.diagnostics.title') }}
                  </span>
                  <NTag v-if="hasError" size="tiny" type="error" :bordered="false">
                    {{ diagnostics?.error_type || t('workspace.glossary.prune.diagnostics.error') }}
                  </NTag>
                </div>
              </template>

              <div class="space-y-4">
                <div>
                  <div class="mb-2.5 text-xs font-medium text-lf-text-subtle">
                    {{ t('workspace.glossary.prune.diagnostics.summary') }}
                  </div>
                  <div class="grid grid-cols-1 gap-x-6 gap-y-2 sm:grid-cols-2">
                    <div
                      v-for="metric in diagnosticMetrics"
                      :key="metric.key"
                      class="flex items-baseline justify-between gap-3 border-b border-lf-border-soft pb-2"
                    >
                      <span class="shrink-0 text-xs text-lf-text-muted">{{ metric.label }}</span>
                      <span
                        class="min-w-0 text-right text-xs font-medium text-lf-text-strong"
                        :class="metric.mono ? 'font-mono' : ''"
                      >
                        {{ metric.value }}
                      </span>
                    </div>
                  </div>
                </div>

                <div
                  v-if="
                    diagnostics?.system_prompt ||
                    diagnostics?.user_message ||
                    diagnostics?.received_content
                  "
                  class="space-y-3 border-t border-lf-border-soft pt-3"
                >
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
              </div>
            </NCollapseItem>
          </NCollapse>
          <div v-else class="text-center text-xs text-lf-text-muted">
            {{ t('workspace.glossary.prune.diagnostics.noData') }}
          </div>

          <div class="overflow-hidden rounded-xl border border-lf-border-soft">
            <NDataTable
              v-model:checked-row-keys="selectedKeys"
              :columns="columns"
              :data="preview.suggestions"
              :row-key="(row: Suggestion) => row.entry_id"
              :scroll-x="720"
              max-height="460"
              size="small"
            >
              <template #empty>
                <NEmpty :description="t('workspace.glossary.prune.noSuggestions')" class="py-10" />
              </template>
            </NDataTable>
          </div>
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
