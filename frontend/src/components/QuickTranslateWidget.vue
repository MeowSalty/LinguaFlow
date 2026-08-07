<script setup lang="ts">
import { useI18n } from 'vue-i18n'
import { useMessage } from 'naive-ui'
import { Icon as IconifyIcon } from '@iconify/vue'

import { quickTranslate } from '@/api/client'
import type { ApiSchemas } from '@/api/client'
import { useExecutionPlanTemplatesStore } from '@/stores/executionPlanTemplates'
import { useProjectsStore } from '@/stores/projects'
import { useLanguageOptions } from '@/composables/useLanguageOptions'
import { renderQualityHighlightedText, getQualityCodeLabel } from '@/composables/useQualityIssues'
import { formatTokens, batchStatusTimelineType } from '@/composables/useWorkspaceUtils'
import SegmentTranslationPreviewDiagnostic from '@/components/workspace/SegmentTranslationPreviewDiagnostic.vue'

const props = withDefaults(defineProps<{ variant?: 'hero' | 'full' }>(), { variant: 'hero' })

const { t } = useI18n()
const message = useMessage()

const planTemplates = useExecutionPlanTemplatesStore()
const projects = useProjectsStore()
const { sourceLanguageOptions, targetLanguageOptions } = useLanguageOptions()

const sourceText = ref('')
const sourceLang = ref('auto')
const targetLang = ref('zh-Hans')
const executionPlanId = ref<number | null>(null)
const projectId = ref<number | null>(null)
const glossary = ref<Array<{ id: number; source: string; target: string; notes: string }>>([])
const glossarySeq = ref(0)
const advancedOpen = ref(props.variant === 'full')
const submitting = ref(false)
const result = ref<ApiSchemas['QuickTranslateResponse'] | null>(null)

const executionPlanOptions = computed(() =>
  planTemplates.items.map((item) => ({ label: item.name, value: item.id })),
)

const projectOptions = computed(() =>
  projects.items.map((item) => ({ label: item.name, value: item.id })),
)

const canSubmit = computed(
  () => sourceText.value.trim().length > 0 && executionPlanId.value != null && !submitting.value,
)

const qualityIssues = computed(() => result.value?.quality_issues ?? [])

const hasRoundSummary = computed(() => (result.value?.round_summary?.length ?? 0) > 0)
const hasWarnings = computed(() => (result.value?.warnings?.length ?? 0) > 0)

const statusLabel = (status: string): string => {
  if (status === 'success') return t('quickTranslate.statusSuccess')
  if (status === 'partial') return t('quickTranslate.statusPartial')
  if (status === 'skipped') return t('quickTranslate.statusSkipped')
  return t('quickTranslate.statusFailed')
}

const addGlossaryRow = (): void => {
  glossarySeq.value += 1
  glossary.value.push({ id: glossarySeq.value, source: '', target: '', notes: '' })
}

const removeGlossaryRow = (id: number): void => {
  glossary.value = glossary.value.filter((row) => row.id !== id)
}

const HighlightedTarget = computed(() => {
  if (!result.value) return null
  return renderQualityHighlightedText(result.value.target_text, result.value.quality_issues)
})

const applyExecutionPlanDefault = (): void => {
  const storedId = Number(localStorage.getItem('linguaflow.quick_translate.plan_id'))
  if (Number.isFinite(storedId) && planTemplates.items.some((item) => item.id === storedId)) {
    executionPlanId.value = storedId
    return
  }
  if (planTemplates.items.length > 0 && executionPlanId.value == null) {
    const firstPlan = planTemplates.items[0]
    if (firstPlan) executionPlanId.value = firstPlan.id
  }
}

const onExecutionPlanChange = (id: number | null): void => {
  executionPlanId.value = id
  if (id != null) localStorage.setItem('linguaflow.quick_translate.plan_id', String(id))
}

const onSubmit = async (): Promise<void> => {
  if (!canSubmit.value) return
  submitting.value = true
  result.value = null
  try {
    const payload: ApiSchemas['QuickTranslateRequest'] = {
      source_text: sourceText.value,
      source_lang: sourceLang.value,
      target_lang: targetLang.value,
      execution_plan_id: executionPlanId.value!,
    }
    if (projectId.value != null) payload.project_id = projectId.value
    const glossaryEntries = glossary.value
      .filter((row) => row.source.trim() && row.target.trim())
      .map((row) => {
        const entry: ApiSchemas['QuickGlossaryEntry'] = {
          source: row.source,
          target: row.target,
          case_sensitive: false,
          forbidden: false,
          mandatory: true,
        }
        if (row.notes.trim()) entry.notes = row.notes.trim()
        return entry
      })
    if (glossaryEntries.length) payload.glossary = glossaryEntries
    const res = await quickTranslate(payload)
    result.value = res
    if (res.status === 'success') message.success(t('quickTranslate.messages.success'))
    else if (res.status === 'partial') message.warning(t('quickTranslate.messages.partial'))
    else message.error(t('quickTranslate.messages.failed'))
  } catch (err) {
    message.error(err instanceof Error ? err.message : t('quickTranslate.messages.failed'))
  } finally {
    submitting.value = false
  }
}

const onCopy = async (): Promise<void> => {
  if (!result.value?.target_text) return
  try {
    await navigator.clipboard.writeText(result.value.target_text)
    message.success(t('quickTranslate.copySuccess'))
  } catch {
    message.error(t('quickTranslate.copyFailed'))
  }
}

onMounted(() => {
  if (!planTemplates.items.length) {
    void planTemplates.loadTemplates().then(applyExecutionPlanDefault)
  } else {
    applyExecutionPlanDefault()
  }
  if (!projects.items.length) void projects.loadProjects()
})
</script>

<template>
  <section class="lf-panel space-y-6 p-5">
    <!-- 原文输入 -->
    <div class="space-y-2">
      <p class="text-sm font-medium text-lf-text-strong">{{ t('quickTranslate.sourceLabel') }}</p>
      <NInput
        v-model:value="sourceText"
        type="textarea"
        :autosize="{ minRows: 4, maxRows: 12 }"
        :placeholder="t('quickTranslate.sourcePlaceholder')"
      />
    </div>

    <!-- 语言与执行计划 -->
    <div class="grid grid-cols-1 gap-4 sm:grid-cols-3">
      <div class="space-y-2">
        <p class="text-sm font-medium text-lf-text-strong">
          {{ t('quickTranslate.sourceLangLabel') }}
        </p>
        <NSelect v-model:value="sourceLang" :options="sourceLanguageOptions" />
      </div>
      <div class="space-y-2">
        <p class="text-sm font-medium text-lf-text-strong">
          {{ t('quickTranslate.targetLangLabel') }}
        </p>
        <NSelect v-model:value="targetLang" :options="targetLanguageOptions" />
      </div>
      <div class="space-y-2">
        <p class="text-sm font-medium text-lf-text-strong">
          {{ t('quickTranslate.executionPlanLabel') }}
        </p>
        <NSelect
          :value="executionPlanId"
          filterable
          :options="executionPlanOptions"
          :placeholder="t('quickTranslate.executionPlanPlaceholder')"
          :loading="planTemplates.loading"
          @update:value="onExecutionPlanChange"
        />
        <p v-if="executionPlanOptions.length === 0" class="text-xs text-lf-text-muted">
          {{ t('quickTranslate.executionPlanEmpty') }}
        </p>
      </div>
    </div>

    <!-- 高级选项 -->
    <div class="space-y-3 border-t border-lf-border-soft pt-5">
      <button
        type="button"
        class="flex items-center gap-1.5 text-sm font-medium text-lf-text-muted transition-colors hover:text-lf-text-strong"
        @click="advancedOpen = !advancedOpen"
      >
        <IconifyIcon
          :icon="advancedOpen ? 'carbon:chevron-up' : 'carbon:chevron-down'"
          class="text-base"
        />
        {{ t('quickTranslate.advancedToggle') }}
      </button>

      <div
        v-if="advancedOpen"
        class="space-y-5 rounded-xl border border-lf-border-soft bg-lf-surface-muted/40 p-4"
      >
        <div class="space-y-2">
          <p class="text-sm font-medium text-lf-text-strong">
            {{ t('quickTranslate.projectLabel') }}
          </p>
          <NSelect
            v-model:value="projectId"
            clearable
            :options="projectOptions"
            :placeholder="t('quickTranslate.projectPlaceholder')"
            :loading="projects.loading"
          />
        </div>

        <div class="space-y-2.5">
          <div class="space-y-0.5">
            <p class="text-sm font-medium text-lf-text-strong">
              {{ t('quickTranslate.glossaryTitle') }}
            </p>
            <p class="text-xs text-lf-text-muted">{{ t('quickTranslate.glossaryHint') }}</p>
          </div>
          <div
            v-for="row in glossary"
            :key="row.id"
            class="grid grid-cols-1 gap-2 sm:grid-cols-[1fr_1fr_1.4fr_auto] sm:items-center"
          >
            <NInput
              v-model:value="row.source"
              :placeholder="t('quickTranslate.glossarySourcePlaceholder')"
            />
            <NInput
              v-model:value="row.target"
              :placeholder="t('quickTranslate.glossaryTargetPlaceholder')"
            />
            <NInput
              v-model:value="row.notes"
              :placeholder="t('quickTranslate.glossaryNotesPlaceholder')"
            />
            <NButton
              quaternary
              size="small"
              class="justify-self-end"
              :aria-label="t('quickTranslate.glossaryRemove')"
              @click="removeGlossaryRow(row.id)"
            >
              <IconifyIcon icon="carbon:close" />
            </NButton>
          </div>
          <NButton dashed size="small" @click="addGlossaryRow">
            <IconifyIcon icon="carbon:add" />
            {{ t('quickTranslate.glossaryAdd') }}
          </NButton>
        </div>
      </div>
    </div>

    <!-- 提交 -->
    <div class="flex justify-end border-t border-lf-border-soft pt-5">
      <NButton
        type="primary"
        size="large"
        :loading="submitting"
        :disabled="!canSubmit"
        @click="onSubmit"
      >
        <IconifyIcon icon="carbon:translate" />
        {{ submitting ? t('quickTranslate.submitting') : t('quickTranslate.submit') }}
      </NButton>
    </div>

    <!-- 结果 -->
    <div
      v-if="result"
      class="overflow-hidden rounded-xl border border-lf-border-soft bg-lf-surface-muted/30"
    >
      <!-- 结果头部 -->
      <div
        class="flex flex-wrap items-center justify-between gap-3 border-b border-lf-border-soft bg-lf-surface px-4 py-3"
      >
        <div class="flex flex-wrap items-center gap-2">
          <NTag
            :type="batchStatusTimelineType(result.status, 'info')"
            :bordered="false"
            size="small"
          >
            {{ statusLabel(result.status) }}
          </NTag>
          <span class="text-xs text-lf-text-muted">
            {{
              t('quickTranslate.langPair', {
                source: result.source_lang ?? sourceLang,
                target: result.target_lang ?? targetLang,
              })
            }}
          </span>
        </div>
        <NButton v-if="result.target_text" quaternary size="small" @click="onCopy">
          <IconifyIcon icon="carbon:copy" />
          {{ t('quickTranslate.copy') }}
        </NButton>
      </div>

      <!-- 译文正文（核心，左色条强调） -->
      <div class="border-l-2 border-brand-500/50 bg-lf-brand-soft/20 px-4 py-4">
        <div class="text-lg leading-8 whitespace-pre-wrap text-lf-text-strong">
          <component :is="HighlightedTarget" />
        </div>
      </div>

      <!-- 质量问题 -->
      <div v-if="qualityIssues.length" class="space-y-2 border-t border-lf-border-soft px-4 py-3">
        <p class="text-[11px] font-medium tracking-wide text-lf-text-subtle uppercase">
          {{ t('quickTranslate.qualityIssuesTitle') }}
        </p>
        <div class="space-y-1.5">
          <div
            v-for="(issue, idx) in qualityIssues"
            :key="idx"
            class="flex items-start gap-2 text-sm text-lf-text"
          >
            <NTag
              size="tiny"
              :type="issue.severity === 'error' ? 'error' : 'warning'"
              :bordered="false"
            >
              {{ getQualityCodeLabel(issue.code) }}
            </NTag>
            <span>{{ issue.message }}</span>
          </div>
        </div>
      </div>

      <!-- 轮次概览（每轮一行，对齐整齐） -->
      <div v-if="hasRoundSummary" class="space-y-2 border-t border-lf-border-soft px-4 py-3">
        <p class="text-[11px] font-medium tracking-wide text-lf-text-subtle uppercase">
          {{ t('quickTranslate.roundSummaryTitle') }}
        </p>
        <div class="space-y-1.5">
          <div
            v-for="round in result.round_summary"
            :key="round.index"
            class="flex items-center gap-2 text-xs"
          >
            <span class="w-6 shrink-0 font-mono text-lf-text-subtle">#{{ round.index + 1 }}</span>
            <NTag size="tiny" :bordered="false">{{ round.mode }}</NTag>
            <NTag
              size="tiny"
              :type="batchStatusTimelineType(round.status, 'info')"
              :bordered="false"
            >
              {{ statusLabel(round.status) }}
            </NTag>
            <NTag v-if="round.backend" size="tiny" :bordered="false">{{ round.backend }}</NTag>
            <span class="ml-auto font-mono text-xs tabular-nums text-lf-text-muted">
              {{ round.duration_ms }}ms
            </span>
          </div>
        </div>
      </div>

      <!-- 用量（轻量内联指标条） -->
      <div v-if="result.usage" class="border-t border-lf-border-soft px-4 py-3">
        <div class="flex flex-wrap items-baseline gap-x-6 gap-y-1">
          <div class="flex items-baseline gap-1.5">
            <span class="text-xs text-lf-text-muted">{{ t('quickTranslate.usageApiCalls') }}</span>
            <span class="font-mono text-sm font-semibold tabular-nums text-lf-text-strong">
              {{ result.usage.api_calls }}
            </span>
          </div>
          <div class="flex items-baseline gap-1.5">
            <span class="text-xs text-lf-text-muted">{{
              t('quickTranslate.usageInputTokens')
            }}</span>
            <span class="font-mono text-sm font-semibold tabular-nums text-lf-text-strong">
              {{ formatTokens(result.usage.input_tokens) }}
            </span>
          </div>
          <div class="flex items-baseline gap-1.5">
            <span class="text-xs text-lf-text-muted">{{
              t('quickTranslate.usageOutputTokens')
            }}</span>
            <span class="font-mono text-sm font-semibold tabular-nums text-lf-text-strong">
              {{ formatTokens(result.usage.output_tokens) }}
            </span>
          </div>
        </div>
      </div>

      <!-- 警告 -->
      <div v-if="hasWarnings" class="space-y-2 border-t border-lf-border-soft px-4 py-3">
        <NAlert
          v-for="(warning, idx) in result.warnings"
          :key="idx"
          type="warning"
          :title="t('quickTranslate.warningsTitle')"
        >
          {{ warning }}
        </NAlert>
      </div>

      <!-- 诊断批次（默认收起） -->
      <div v-if="result.batches?.length" class="border-t border-lf-border-soft px-4 py-3">
        <NCollapse :default-expanded-names="props.variant === 'full' ? ['batches'] : []">
          <NCollapseItem name="batches" :title="t('quickTranslate.batchesTitle')">
            <div class="space-y-3">
              <SegmentTranslationPreviewDiagnostic
                v-for="(batch, i) in result.batches"
                :key="i"
                :batch="batch"
                :index="i"
              />
            </div>
          </NCollapseItem>
        </NCollapse>
      </div>
    </div>
  </section>
</template>
