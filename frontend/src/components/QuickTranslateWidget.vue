<script setup lang="ts">
import { useI18n } from 'vue-i18n'
import { useMessage } from 'naive-ui'

import { quickTranslate } from '@/api/client'
import type { ApiSchemas } from '@/api/client'
import { useExecutionPlanTemplatesStore } from '@/stores/executionPlanTemplates'
import { useProjectsStore } from '@/stores/projects'
import { useLanguageOptions } from '@/composables/useLanguageOptions'
import { renderQualityHighlightedText, getQualityCodeLabel } from '@/composables/useQualityIssues'
import { formatTokens, batchStatusTimelineType } from '@/composables/useWorkspaceUtils'
import SegmentTranslationPreviewDiagnostic from '@/components/workspace/SegmentTranslationPreviewDiagnostic.vue'

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
const advancedOpen = ref(false)
const submitting = ref(false)
const result = ref<ApiSchemas['QuickTranslateResponse'] | null>(null)

const hasTranslateRound = (plan: ApiSchemas['ExecutionPlanTemplate']): boolean =>
  plan.rounds.some((round) => round.mode === 'translate')

const translatablePlans = computed(() => planTemplates.items.filter(hasTranslateRound))

const executionPlanOptions = computed(() =>
  planTemplates.items.map((item) => ({
    label: hasTranslateRound(item)
      ? item.name
      : `${item.name}${t('quickTranslate.planNoTranslateRound')}`,
    value: item.id,
    disabled: !hasTranslateRound(item),
  })),
)

const projectOptions = computed(() =>
  projects.items.map((item) => ({ label: item.name, value: item.id })),
)

const selectedPlanTranslatable = computed(
  () =>
    executionPlanId.value != null &&
    planTemplates.items.some(
      (item) => item.id === executionPlanId.value && hasTranslateRound(item),
    ),
)

const canSubmit = computed(
  () => sourceText.value.trim().length > 0 && selectedPlanTranslatable.value && !submitting.value,
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
  const storedPlan = translatablePlans.value.find((item) => item.id === storedId)
  if (Number.isFinite(storedId) && storedPlan) {
    executionPlanId.value = storedPlan.id
    return
  }
  if (executionPlanId.value == null && translatablePlans.value.length > 0) {
    executionPlanId.value = translatablePlans.value[0]!.id
  }
}

const onExecutionPlanChange = (id: number | null): void => {
  executionPlanId.value = id
  if (id != null) localStorage.setItem('linguaflow.quick_translate.plan_id', String(id))
}

const onSwapLanguages = (): void => {
  if (sourceLang.value === 'auto') return
  ;[sourceLang.value, targetLang.value] = [targetLang.value, sourceLang.value]
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
  <div class="space-y-6">
    <!-- 输入面板：翻译控制台 -->
    <section class="lf-panel overflow-hidden">
      <!-- 语言与执行计划工具条 -->
      <div class="flex flex-wrap items-center gap-2 border-b border-lf-border-soft px-5 py-3.5">
        <NSelect
          v-model:value="sourceLang"
          class="w-full! sm:w-44!"
          :options="sourceLanguageOptions"
          :aria-label="t('quickTranslate.sourceLangLabel')"
        />
        <span class="hidden shrink-0 sm:inline-flex">
          <NButton
            quaternary
            circle
            size="small"
            :disabled="sourceLang === 'auto'"
            :aria-label="t('quickTranslate.swapLangs')"
            @click="onSwapLanguages"
          >
            <IconCarbonArrowsHorizontal class="text-base" />
          </NButton>
        </span>
        <NSelect
          v-model:value="targetLang"
          class="w-full! sm:w-44!"
          :options="targetLanguageOptions"
          :aria-label="t('quickTranslate.targetLangLabel')"
        />
        <div class="ml-auto flex w-full min-w-0 items-center gap-2.5 sm:w-auto">
          <span class="shrink-0 text-xs font-medium text-lf-text-subtle">
            {{ t('quickTranslate.executionPlanLabel') }}
          </span>
          <NSelect
            class="w-full! sm:w-52!"
            :value="executionPlanId"
            filterable
            :options="executionPlanOptions"
            :placeholder="t('quickTranslate.executionPlanPlaceholder')"
            :loading="planTemplates.loading"
            @update:value="onExecutionPlanChange"
          />
        </div>
        <p
          v-if="!planTemplates.loading && executionPlanOptions.length === 0"
          class="w-full text-xs text-lf-text-muted"
        >
          {{ t('quickTranslate.executionPlanEmpty') }}
        </p>
        <p
          v-else-if="!planTemplates.loading && translatablePlans.length === 0"
          class="w-full text-xs text-lf-text-muted"
        >
          {{ t('quickTranslate.noTranslatablePlan') }}
        </p>
      </div>

      <!-- 原文输入 -->
      <div class="px-5 py-4">
        <NInput
          v-model:value="sourceText"
          type="textarea"
          :autosize="{ minRows: 6, maxRows: 14 }"
          :placeholder="t('quickTranslate.sourcePlaceholder')"
          :aria-label="t('quickTranslate.sourceLabel')"
        />
      </div>

      <!-- 高级选项展开区 -->
      <div
        v-if="advancedOpen"
        class="space-y-5 border-t border-lf-border-soft bg-lf-surface-muted/40 px-5 py-4"
      >
        <div class="space-y-2">
          <p class="text-xs font-medium text-lf-text-muted">
            {{ t('quickTranslate.projectLabel') }}
          </p>
          <NSelect
            v-model:value="projectId"
            class="max-w-sm!"
            clearable
            :options="projectOptions"
            :placeholder="t('quickTranslate.projectPlaceholder')"
            :loading="projects.loading"
          />
        </div>

        <div class="space-y-2.5">
          <div class="space-y-0.5">
            <p class="text-xs font-medium text-lf-text-muted">
              {{ t('quickTranslate.glossaryTitle') }}
            </p>
            <p class="text-xs text-lf-text-subtle">{{ t('quickTranslate.glossaryHint') }}</p>
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
              circle
              size="small"
              class="justify-self-end"
              :aria-label="t('quickTranslate.glossaryRemove')"
              @click="removeGlossaryRow(row.id)"
            >
              <IconCarbonClose />
            </NButton>
          </div>
          <NButton dashed size="small" @click="addGlossaryRow">
            <IconCarbonAdd />
            {{ t('quickTranslate.glossaryAdd') }}
          </NButton>
        </div>
      </div>

      <!-- 底部操作条 -->
      <div
        class="flex flex-wrap items-center justify-between gap-3 border-t border-lf-border-soft px-5 py-3.5"
      >
        <button
          type="button"
          class="flex items-center gap-1.5 text-sm font-medium text-lf-text-muted transition-colors hover:text-lf-text-strong"
          @click="advancedOpen = !advancedOpen"
        >
          <IconCarbonChevronUp v-if="advancedOpen" class="text-base" />
          <IconCarbonChevronDown v-else class="text-base" />
          {{ t('quickTranslate.advancedToggle') }}
        </button>
        <div class="flex items-center gap-4">
          <span class="text-xs text-lf-text-subtle tabular-nums">
            {{ t('quickTranslate.charCount', { count: sourceText.length }) }}
          </span>
          <NButton
            type="primary"
            size="large"
            :loading="submitting"
            :disabled="!canSubmit"
            @click="onSubmit"
          >
            <IconCarbonTranslate />
            {{ submitting ? t('quickTranslate.submitting') : t('quickTranslate.submit') }}
          </NButton>
        </div>
      </div>
    </section>

    <!-- 结果面板：独立卡片 -->
    <section v-if="result" class="lf-panel overflow-hidden">
      <!-- 头部：状态 + 语言对 + 复制 -->
      <div
        class="flex flex-wrap items-center justify-between gap-3 border-b border-lf-border-soft px-5 py-3.5"
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
          <IconCarbonCopy />
          {{ t('quickTranslate.copy') }}
        </NButton>
      </div>

      <!-- 译文正文（品牌色左条强调） -->
      <div class="border-l-2 border-brand-500/50 bg-lf-brand-soft/20 px-5 py-4">
        <div class="text-lg leading-8 whitespace-pre-wrap text-lf-text-strong">
          <component :is="HighlightedTarget" />
        </div>
      </div>

      <!-- 质量问题 -->
      <div v-if="qualityIssues.length" class="space-y-2 border-t border-lf-border-soft px-5 py-3.5">
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

      <!-- 轮次概览 -->
      <div v-if="hasRoundSummary" class="space-y-2 border-t border-lf-border-soft px-5 py-3.5">
        <p class="text-[11px] font-medium tracking-wide text-lf-text-subtle uppercase">
          {{ t('quickTranslate.roundSummaryTitle') }}
        </p>
        <div class="space-y-1.5">
          <div
            v-for="round in result.round_summary"
            :key="round.index"
            class="flex min-w-0 flex-wrap items-center gap-2 text-xs"
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

      <!-- 用量 -->
      <div v-if="result.usage" class="border-t border-lf-border-soft px-5 py-3.5">
        <div class="flex flex-wrap items-baseline gap-x-6 gap-y-1">
          <div class="flex items-baseline gap-1.5">
            <span class="text-xs text-lf-text-muted">{{ t('quickTranslate.usageApiCalls') }}</span>
            <span class="font-mono text-sm font-semibold tabular-nums text-lf-text-strong">
              {{ result.usage.api_calls }}
            </span>
          </div>
          <div class="flex items-baseline gap-1.5">
            <span class="text-xs text-lf-text-muted">
              {{ t('quickTranslate.usageInputTokens') }}
            </span>
            <span class="font-mono text-sm font-semibold tabular-nums text-lf-text-strong">
              {{ formatTokens(result.usage.input_tokens) }}
            </span>
          </div>
          <div class="flex items-baseline gap-1.5">
            <span class="text-xs text-lf-text-muted">
              {{ t('quickTranslate.usageOutputTokens') }}
            </span>
            <span class="font-mono text-sm font-semibold tabular-nums text-lf-text-strong">
              {{ formatTokens(result.usage.output_tokens) }}
            </span>
          </div>
        </div>
      </div>

      <!-- 警告 -->
      <div v-if="hasWarnings" class="space-y-2 border-t border-lf-border-soft px-5 py-3.5">
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
      <div v-if="result.batches?.length" class="border-t border-lf-border-soft px-5 py-3.5">
        <NCollapse>
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
    </section>
  </div>
</template>
