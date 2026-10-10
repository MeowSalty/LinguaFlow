<script setup lang="ts">
import { NButton, NInputNumber, NSelect, NSwitch, NRadioGroup, NRadioButton } from 'naive-ui'
import type { SelectOption } from 'naive-ui'
import { useI18n } from 'vue-i18n'
import { useId } from 'vue'

import type { ApiSchemas } from '@/api/client'
import {
  cloneExecutionPlanValue,
  createExecutionPlanRound,
  createRoundCodeSelection,
  createRoundModeSelection,
  mergeRubyRetryConfig,
  mergeTranslateRoundConfig,
  roundCodes,
  setRoundCodes,
  validateRoundCodes,
  validateRubyRetryConfig,
} from '@/utils/execution-plan-config'
import type {
  ExecutionPlanFormRound,
  ExecutionPlanFormRubyRetry,
} from '@/utils/execution-plan-config'
import {
  getQualityCodeLabel,
  QUALITY_CODES,
  SEMANTIC_REPAIR_ISSUE_CODES,
} from '@/composables/useQualityIssues'

import ConfigSectionPanel from './ConfigSectionPanel.vue'

type ExecutionRoundConfig = ApiSchemas['ExecutionRoundConfig']
type TranslateRoundConfig = NonNullable<ExecutionRoundConfig['translate']>
type AdjudicateRoundConfig = NonNullable<ExecutionRoundConfig['adjudicate']>
type SemanticQARoundConfig = NonNullable<ExecutionRoundConfig['semantic_qa']>
type ReviseRoundConfig = NonNullable<ExecutionRoundConfig['revise']>
type CorrectRoundConfig = NonNullable<ExecutionRoundConfig['correct']>
type CorrectRuleConfig = NonNullable<CorrectRoundConfig['rules']>[number]
type CorrectRuleName = CorrectRuleConfig['name']
type RetryConfig = NonNullable<TranslateRoundConfig['retry']>
type RoundMode = ExecutionRoundConfig['mode']
type AdjudicateCode = NonNullable<AdjudicateRoundConfig['adjudicate_codes']>[number]
type SemanticQASegmentScope = SemanticQARoundConfig['segment_scope']
type SemanticQAIssueCode = NonNullable<SemanticQARoundConfig['issue_codes']>[number]
type ReviseSegmentScope = ReviseRoundConfig['segment_scope']
type ReviseIssueCode = NonNullable<ReviseRoundConfig['issue_codes']>[number]

type RoundModel = ExecutionPlanFormRound
type FormExtractConfig = NonNullable<RoundModel['extract']>

// ─── 默认值 ──────────────────────────────────────────────────

const DEFAULT_RETRY: RetryConfig = { max_attempts: 3, backoff_ms: 2000, jitter: true }

const DEFAULT_EXTRACT: FormExtractConfig = {
  template_id: null,
  batch_size: 20,
  max_words_per_batch: 0,
  max_terms_per_1000_chars: 25.0,
  min_source_len: 2,
  retry: { ...DEFAULT_RETRY },
}

const DEFAULT_ADJUDICATE: AdjudicateRoundConfig = {
  batch_size: 10,
  max_words_per_batch: 0,
  adjudicate_codes: undefined,
  retry: { ...DEFAULT_RETRY },
}

const DEFAULT_SEMANTIC_QA: SemanticQARoundConfig = {
  batch_size: 10,
  max_words_per_batch: 0,
  segment_scope: undefined,
  issue_codes: undefined,
  retry: { ...DEFAULT_RETRY },
}

const DEFAULT_REVISE: ReviseRoundConfig = {
  batch_size: 10,
  max_words_per_batch: 0,
  segment_scope: undefined,
  issue_codes: undefined,
  retry: { ...DEFAULT_RETRY },
}

// 规则白名单：编辑器固定展示全部已知规则，模板未保存过的按关闭处理
const CORRECT_RULE_NAMES: CorrectRuleName[] = [
  'punctuation_missing_wrap',
  'punctuation_wrap_loss_wrap',
  'width_mix_normalize',
]

const DEFAULT_CORRECT: CorrectRoundConfig = {
  rules: CORRECT_RULE_NAMES.map((name) => ({
    name,
    enabled: name === 'punctuation_missing_wrap',
  })),
}

// ─── 工具函数 ────────────────────────────────────────────────

function deepClone<T>(obj: T): T {
  return cloneExecutionPlanValue(obj)
}

function mergeExtract(source?: Partial<FormExtractConfig>): FormExtractConfig {
  if (!source) return deepClone(DEFAULT_EXTRACT)
  return {
    template_id: source.template_id ?? DEFAULT_EXTRACT.template_id,
    batch_size: source.batch_size ?? DEFAULT_EXTRACT.batch_size,
    max_words_per_batch: source.max_words_per_batch ?? DEFAULT_EXTRACT.max_words_per_batch,
    max_terms_per_1000_chars:
      source.max_terms_per_1000_chars ?? DEFAULT_EXTRACT.max_terms_per_1000_chars,
    min_source_len: source.min_source_len ?? DEFAULT_EXTRACT.min_source_len,
    retry: {
      max_attempts: source.retry?.max_attempts ?? DEFAULT_RETRY.max_attempts,
      backoff_ms: source.retry?.backoff_ms ?? DEFAULT_RETRY.backoff_ms,
      jitter: source.retry?.jitter ?? DEFAULT_RETRY.jitter,
    },
  }
}

function mergeAdjudicate(source?: Partial<AdjudicateRoundConfig>): AdjudicateRoundConfig {
  if (!source) return deepClone(DEFAULT_ADJUDICATE)
  return {
    batch_size: source.batch_size ?? DEFAULT_ADJUDICATE.batch_size,
    max_words_per_batch: source.max_words_per_batch ?? DEFAULT_ADJUDICATE.max_words_per_batch,
    adjudicate_codes:
      source.adjudicate_codes == null ? source.adjudicate_codes : [...source.adjudicate_codes],
    retry: {
      max_attempts: source.retry?.max_attempts ?? DEFAULT_RETRY.max_attempts,
      backoff_ms: source.retry?.backoff_ms ?? DEFAULT_RETRY.backoff_ms,
      jitter: source.retry?.jitter ?? DEFAULT_RETRY.jitter,
    },
  }
}

function mergeSemanticQA(source?: Partial<SemanticQARoundConfig>): SemanticQARoundConfig {
  if (!source) return deepClone(DEFAULT_SEMANTIC_QA)
  const segmentScope = source.segment_scope
  return {
    batch_size: source.batch_size ?? DEFAULT_SEMANTIC_QA.batch_size,
    max_words_per_batch: source.max_words_per_batch ?? DEFAULT_SEMANTIC_QA.max_words_per_batch,
    segment_scope: segmentScope,
    issue_codes: source.issue_codes == null ? source.issue_codes : [...source.issue_codes],
    retry: {
      max_attempts: source.retry?.max_attempts ?? DEFAULT_RETRY.max_attempts,
      backoff_ms: source.retry?.backoff_ms ?? DEFAULT_RETRY.backoff_ms,
      jitter: source.retry?.jitter ?? DEFAULT_RETRY.jitter,
    },
  }
}

function mergeRevise(source?: Partial<ReviseRoundConfig>): ReviseRoundConfig {
  if (!source) return deepClone(DEFAULT_REVISE)
  const segmentScope = source.segment_scope
  return {
    batch_size: source.batch_size ?? DEFAULT_REVISE.batch_size,
    max_words_per_batch: source.max_words_per_batch ?? DEFAULT_REVISE.max_words_per_batch,
    segment_scope: segmentScope,
    issue_codes: source.issue_codes == null ? source.issue_codes : [...source.issue_codes],
    retry: {
      max_attempts: source.retry?.max_attempts ?? DEFAULT_RETRY.max_attempts,
      backoff_ms: source.retry?.backoff_ms ?? DEFAULT_RETRY.backoff_ms,
      jitter: source.retry?.jitter ?? DEFAULT_RETRY.jitter,
    },
  }
}

function mergeCorrect(source?: Partial<CorrectRoundConfig>): CorrectRoundConfig {
  if (!source) return deepClone(DEFAULT_CORRECT)
  // 白名单全量合入：未保存过的规则（如新版本规范新增）以关闭状态出现，保证可见可切换
  const saved = new Map(
    (source.rules ?? []).map((r) => [r.name as CorrectRuleName, r.enabled ?? true]),
  )
  return {
    rules: CORRECT_RULE_NAMES.map((name) => ({
      name,
      enabled: saved.get(name) ?? name === 'punctuation_missing_wrap',
    })),
  }
}

function mergeRound(source?: Partial<RoundModel>): RoundModel {
  if (!source) return createExecutionPlanRound()
  const defaults = createExecutionPlanRound()
  const mode = source.mode ?? 'translate'
  return {
    mode,
    backend_id: source.backend_id ?? defaults.backend_id,
    concurrency: source.concurrency ?? defaults.concurrency,
    translate: mode === 'translate' ? mergeTranslateRoundConfig(source.translate) : undefined,
    extract: mode === 'extract' ? mergeExtract(source.extract) : undefined,
    adjudicate: mode === 'adjudicate' ? mergeAdjudicate(source.adjudicate) : undefined,
    semantic_qa: mode === 'semantic_qa' ? mergeSemanticQA(source.semantic_qa) : undefined,
    revise: mode === 'revise' ? mergeRevise(source.revise) : undefined,
    correct: mode === 'correct' ? mergeCorrect(source.correct) : undefined,
  }
}

function isNoBatch(batchSize?: number, maxWords?: number): boolean {
  return (!batchSize || batchSize === 0) && (!maxWords || maxWords === 0)
}

function setNoBatch(
  target: { batch_size?: number; max_words_per_batch?: number },
  noBatch: boolean,
): void {
  if (noBatch) {
    target.batch_size = 0
    if ('max_words_per_batch' in target) target.max_words_per_batch = 0
  } else {
    target.batch_size = target.batch_size === 0 ? 20 : target.batch_size
  }
}

// ─── Props & Emits ──────────────────────────────────────────

const props = withDefaults(
  defineProps<{
    rounds: ExecutionPlanFormRound[]
    rubyRetry?: ExecutionPlanFormRubyRetry
    backends: SelectOption[]
    promptTemplates: SelectOption[]
    bootstrapPromptTemplates: SelectOption[]
    disabled?: boolean
  }>(),
  { disabled: false },
)

const emit = defineEmits<{
  'update:rounds': [value: ExecutionPlanFormRound[]]
  'update:rubyRetry': [value: ExecutionPlanFormRubyRetry]
}>()

// ─── 内部状态 ────────────────────────────────────────────────

const { t } = useI18n()

const roundsModel = ref<RoundModel[]>(props.rounds.map((r) => mergeRound(r)))
const rubyRetryModel = ref<ExecutionPlanFormRubyRetry>(mergeRubyRetryConfig(props.rubyRetry))
const rubyConcurrencyId = useId()
const rubyConcurrencyInvalid = computed(
  () => validateRubyRetryConfig(rubyRetryModel.value).length > 0,
)

const roundKeys = new WeakMap<RoundModel, number>()
let nextRoundKey = 0
const roundKey = (round: RoundModel): number => {
  let key = roundKeys.get(round)
  if (key === undefined) {
    key = nextRoundKey++
    roundKeys.set(round, key)
  }
  return key
}

// Preserve the local identities used by mode/code drafts when the parent echoes or normalizes
// the current form. The page remounts this editor for a new form or organization.
const reconcileRounds = (incoming: RoundModel[]): RoundModel[] => {
  const previous = roundsModel.value
  const available = new Set(previous)
  const matches = incoming.map((source) => {
    const json = JSON.stringify(source)
    const match = previous.find((round) => available.has(round) && JSON.stringify(round) === json)
    if (match) available.delete(match)
    return match
  })
  return incoming.map((source, index) => {
    const match = matches[index] ?? (available.has(previous[index]!) ? previous[index] : undefined)
    if (!match) return mergeRound(source)
    available.delete(match)
    Object.assign(match, mergeRound(source))
    return match
  })
}

let lastRoundsJson = JSON.stringify(props.rounds ?? [])
let lastRubyRetry = deepClone(props.rubyRetry)
const sameRubyRetry = (first?: ExecutionPlanFormRubyRetry, second?: ExecutionPlanFormRubyRetry) =>
  (['enabled', 'backend_id', 'max_attempts', 'concurrency'] as const).every((field) =>
    Object.is(first?.[field], second?.[field]),
  )

watch(
  () => props.rounds,
  (newVal) => {
    const json = JSON.stringify(newVal ?? [])
    if (json === lastRoundsJson) return
    roundsModel.value = reconcileRounds(newVal ?? [])
  },
  { deep: true },
)

watch(
  roundsModel,
  (newVal) => {
    const json = JSON.stringify(newVal)
    if (json === lastRoundsJson) return
    lastRoundsJson = json
    emit('update:rounds', deepClone(newVal))
  },
  { deep: true },
)

watch(
  () => props.rubyRetry,
  (newVal) => {
    if (sameRubyRetry(newVal, lastRubyRetry)) return
    rubyRetryModel.value = mergeRubyRetryConfig(newVal)
  },
  { deep: true },
)

watch(
  rubyRetryModel,
  (newVal) => {
    if (sameRubyRetry(newVal, lastRubyRetry)) return
    lastRubyRetry = deepClone(newVal)
    emit('update:rubyRetry', deepClone(newVal))
  },
  { deep: true },
)

// ─── 操作方法 ────────────────────────────────────────────────

const modeOptions = computed(() => [
  { label: t('executionPlanEditor.round.modeTranslate'), value: 'translate' as RoundMode },
  { label: t('executionPlanEditor.round.modeExtract'), value: 'extract' as RoundMode },
  { label: t('executionPlanEditor.round.modeAdjudicate'), value: 'adjudicate' as RoundMode },
  { label: t('executionPlanEditor.round.modeSemanticQA'), value: 'semantic_qa' as RoundMode },
  { label: t('executionPlanEditor.round.modeRevise'), value: 'revise' as RoundMode },
  { label: t('executionPlanEditor.round.modeCorrect'), value: 'correct' as RoundMode },
])

const segmentFilterOptions = computed(() => [
  { label: t('executionPlanEditor.round.segmentFilterPendingOnly'), value: 'pending_only' },
  { label: t('executionPlanEditor.round.segmentFilterSkipApproved'), value: 'skip_approved' },
  { label: t('executionPlanEditor.round.segmentFilterAll'), value: 'all' },
])

const adjudicateCodeOptions = computed(() => [
  {
    label: t('executionPlanEditor.round.adjudicateCodeSourceResidual'),
    value: 'source_residual' as AdjudicateCode,
  },
  {
    label: t('executionPlanEditor.round.adjudicateCodeLengthRatio'),
    value: 'length_ratio' as AdjudicateCode,
  },
  {
    label: t('executionPlanEditor.round.adjudicateCodePunctuationSurplus'),
    value: 'punctuation_surplus' as AdjudicateCode,
  },
  {
    label: t('executionPlanEditor.round.adjudicateCodeUntranslated'),
    value: 'untranslated' as AdjudicateCode,
  },
])

const semanticQASegmentScopeOptions = computed(() => [
  {
    label: t('executionPlanEditor.round.semanticQASegmentScopeAll'),
    value: 'all' as SemanticQASegmentScope,
  },
  {
    label: t('executionPlanEditor.round.semanticQASegmentScopeWithIssues'),
    value: 'with_issues' as SemanticQASegmentScope,
  },
  {
    label: t('executionPlanEditor.round.semanticQASegmentScopeWithIssueCodes'),
    value: 'with_issue_codes' as SemanticQASegmentScope,
  },
])

const semanticQAIssueCodes: SemanticQAIssueCode[] = QUALITY_CODES

const semanticQAIssueCodeOptions = computed(() =>
  semanticQAIssueCodes.map((value) => ({
    label: getQualityCodeLabel(value),
    value,
  })),
)

const reviseSegmentScopeOptions = computed(() => [
  {
    label: t('executionPlanEditor.round.reviseSegmentScopeWithIssues'),
    value: 'with_issues' as ReviseSegmentScope,
  },
  {
    label: t('executionPlanEditor.round.reviseSegmentScopeWithIssueCodes'),
    value: 'with_issue_codes' as ReviseSegmentScope,
  },
])

const reviseIssueCodeOptions = computed(() =>
  SEMANTIC_REPAIR_ISSUE_CODES.map((value) => ({
    label: getQualityCodeLabel(value),
    value: value as ReviseIssueCode,
  })),
)

const correctRuleLabelMap: Record<CorrectRuleName, string> = {
  punctuation_missing_wrap: t('executionPlanEditor.round.correctRulePunctuationMissingWrap'),
  punctuation_wrap_loss_wrap: t('executionPlanEditor.round.correctRulePunctuationWrapLossWrap'),
  width_mix_normalize: t('executionPlanEditor.round.correctRuleWidthMixNormalize'),
}

const correctRuleHintMap: Record<CorrectRuleName, string> = {
  punctuation_missing_wrap: t('executionPlanEditor.round.correctRulePunctuationMissingWrapHint'),
  punctuation_wrap_loss_wrap: t('executionPlanEditor.round.correctRulePunctuationWrapLossWrapHint'),
  width_mix_normalize: t('executionPlanEditor.round.correctRuleWidthMixNormalizeHint'),
}

const chooseCodeMode = createRoundCodeSelection()
const onSemanticQASegmentScopeChange = (round: RoundModel, scope: SemanticQASegmentScope): void => {
  if (round.semantic_qa) round.semantic_qa.segment_scope = scope
}
const onReviseSegmentScopeChange = (round: RoundModel, scope: ReviseSegmentScope): void => {
  if (round.revise) round.revise.segment_scope = scope
}

const modeBadgeClass = (mode: RoundMode): string => {
  if (mode === 'translate') return 'bg-lf-brand-soft text-brand-600'
  if (mode === 'extract') return 'bg-lf-accent-amber-soft text-lf-accent-amber'
  if (mode === 'adjudicate') return 'bg-lf-accent-violet-soft text-lf-accent-violet'
  if (mode === 'semantic_qa') return 'bg-lf-accent-emerald-soft text-lf-accent-emerald'
  if (mode === 'revise') return 'bg-lf-accent-rose-soft text-lf-accent-rose'
  return 'bg-lf-accent-sky-soft text-lf-accent-sky'
}

const modeLabel = (mode: RoundMode): string => {
  if (mode === 'translate') return t('executionPlanEditor.round.modeTranslate')
  if (mode === 'extract') return t('executionPlanEditor.round.modeExtract')
  if (mode === 'adjudicate') return t('executionPlanEditor.round.modeAdjudicate')
  if (mode === 'semantic_qa') return t('executionPlanEditor.round.modeSemanticQA')
  if (mode === 'revise') return t('executionPlanEditor.round.modeRevise')
  return t('executionPlanEditor.round.modeCorrect')
}

const chooseRoundMode = createRoundModeSelection(mergeRound)
const switchRoundMode = (round: RoundModel, mode: RoundMode): void => {
  if (!props.disabled) chooseRoundMode(round, mode)
}

const updateInlineTermExtraction = (
  round: RoundModel,
  config: ApiSchemas['InlineTermExtractionConfig'],
): void => {
  if (props.disabled || round.mode !== 'translate' || !round.translate) return
  round.translate.inline_term_extraction = config
}

const addRound = (): void => {
  roundsModel.value.push(createExecutionPlanRound())
  emitUpdate()
}

const removeRound = (index: number): void => {
  if (roundsModel.value.length <= 1) return
  roundsModel.value.splice(index, 1)
  emitUpdate()
}

const moveRound = (index: number, direction: -1 | 1): void => {
  const newIndex = index + direction
  if (newIndex < 0 || newIndex >= roundsModel.value.length) return
  const temp = roundsModel.value[index]
  roundsModel.value[index] = roundsModel.value[newIndex]!
  roundsModel.value[newIndex] = temp!
  emitUpdate()
}

const emitUpdate = (): void => {
  lastRoundsJson = JSON.stringify(roundsModel.value)
  emit('update:rounds', deepClone(roundsModel.value))
}
</script>

<template>
  <div class="flex flex-col gap-4">
    <!-- Ruby Retry 注音对齐重试配置 -->
    <ConfigSectionPanel
      :title="t('executionPlanEditor.rubyRetry.title')"
      :description="t('executionPlanEditor.rubyRetry.description')"
    >
      <template #actions>
        <NSwitch
          v-model:value="rubyRetryModel.enabled"
          size="small"
          :disabled="disabled"
          :aria-label="t('executionPlanEditor.rubyRetry.enabled')"
        />
      </template>
      <div v-if="rubyRetryModel.enabled" class="grid grid-cols-1 gap-3 sm:grid-cols-2">
        <div>
          <div class="mb-1 text-xs text-lf-text-subtle">
            {{ t('executionPlanEditor.rubyRetry.backend') }}
          </div>
          <NSelect
            v-model:value="rubyRetryModel.backend_id"
            :options="backends"
            size="small"
            :disabled="disabled"
            clearable
            :placeholder="t('executionPlanEditor.rubyRetry.backendPlaceholder')"
            class="w-full"
          />
        </div>
        <div>
          <div class="mb-1 text-xs text-lf-text-subtle">
            {{ t('executionPlanEditor.rubyRetry.maxAttempts') }}
          </div>
          <NInputNumber
            v-model:value="rubyRetryModel.max_attempts"
            :min="1"
            :max="10"
            size="small"
            :disabled="disabled"
            class="w-full"
          />
          <div class="mt-1 text-xs text-lf-text-subtle">
            {{ t('executionPlanEditor.rubyRetry.maxAttemptsHint') }}
          </div>
        </div>
      </div>
      <div class="mt-3">
        <div class="mb-1 text-xs text-lf-text-subtle">
          {{ t('rubyAlignmentConfig.concurrency') }}
        </div>
        <NInputNumber
          v-model:value="rubyRetryModel.concurrency"
          :input-props="{
            'aria-label': t('rubyAlignmentConfig.concurrency'),
            'aria-invalid': rubyConcurrencyInvalid,
            'aria-describedby': rubyConcurrencyId,
          }"
          :placeholder="t('rubyAlignmentConfig.defaultConcurrency')"
          :status="rubyConcurrencyInvalid ? 'error' : undefined"
          clearable
          size="small"
          :disabled="disabled"
          class="w-full"
        />
        <p :id="rubyConcurrencyId" class="mt-1 text-xs text-lf-text-subtle">
          {{ t('rubyAlignmentConfig.concurrencyHint') }}
        </p>
        <p v-if="rubyConcurrencyInvalid" role="alert" class="mt-1 text-xs text-lf-danger">
          {{ t('rubyAlignmentConfig.invalidConcurrency') }}
        </p>
      </div>
    </ConfigSectionPanel>

    <!-- 轮次列表 -->
    <ConfigSectionPanel
      v-for="(round, index) in roundsModel"
      data-testid="execution-round"
      :key="roundKey(round)"
    >
      <template #title>
        <div class="flex flex-wrap items-center gap-2">
          <span
            class="inline-flex h-6 w-6 items-center justify-center rounded-full text-xs font-bold"
            :class="modeBadgeClass(round.mode)"
          >
            {{ index + 1 }}
          </span>
          <span class="text-sm font-semibold text-lf-text-strong">{{ modeLabel(round.mode) }}</span>
          <span
            v-if="
              round.mode === 'translate' &&
              round.translate?.inline_term_extraction?.enabled === true
            "
            data-testid="inline-term-extraction-badge"
            class="rounded-full bg-lf-accent-amber-soft px-2 py-0.5 text-xs font-medium text-lf-accent-amber"
          >
            {{ t('executionPlanEditor.round.inlineTermExtraction.enabledBadge') }}
          </span>
        </div>
      </template>
      <template #actions>
        <div class="flex items-center gap-1">
          <NButton
            quaternary
            circle
            size="small"
            :disabled="disabled || index === 0"
            :aria-label="t('executionPlanEditor.actions.moveUp')"
            @click="moveRound(index, -1)"
          >
            <template #icon>
              <NIcon size="14"><IconCarbonArrowUp /></NIcon>
            </template>
          </NButton>
          <NButton
            quaternary
            circle
            size="small"
            :disabled="disabled || index === roundsModel.length - 1"
            :aria-label="t('executionPlanEditor.actions.moveDown')"
            @click="moveRound(index, 1)"
          >
            <template #icon>
              <NIcon size="14"><IconCarbonArrowDown /></NIcon>
            </template>
          </NButton>
          <NButton
            quaternary
            circle
            type="error"
            size="small"
            :disabled="disabled || roundsModel.length <= 1"
            :aria-label="t('executionPlanEditor.actions.removeRound')"
            @click="removeRound(index)"
          >
            <template #icon>
              <NIcon size="14"><IconCarbonClose /></NIcon>
            </template>
          </NButton>
        </div>
      </template>

      <!-- 模式选择 -->
      <div>
        <div class="mb-1 text-xs text-lf-text-subtle">
          {{ t('executionPlanEditor.round.mode') }}
          <span class="text-lf-danger">*</span>
        </div>
        <NRadioGroup
          :value="round.mode"
          :aria-label="t('executionPlanEditor.round.mode')"
          size="small"
          :disabled="disabled"
          @update:value="(v: RoundMode) => switchRoundMode(round, v)"
        >
          <NRadioButton
            v-for="opt in modeOptions"
            :key="opt.value"
            :value="opt.value"
            :label="opt.label"
          />
        </NRadioGroup>
      </div>

      <!-- 公共字段：后端 + 并发 -->
      <div class="grid grid-cols-1 gap-3 md:grid-cols-2">
        <div v-if="round.mode !== 'correct'">
          <div class="mb-1 text-xs text-lf-text-subtle">
            {{ t('executionPlanEditor.round.backend') }}
            <span class="text-lf-danger">*</span>
          </div>
          <NSelect
            v-model:value="round.backend_id"
            :aria-label="t('executionPlanEditor.round.backend')"
            :options="backends"
            size="small"
            :disabled="disabled"
            :placeholder="t('executionPlanEditor.round.backendPlaceholder')"
            class="w-full"
          />
        </div>
        <div v-if="round.mode !== 'correct'">
          <div class="mb-1 text-xs text-lf-text-subtle">
            {{ t('executionPlanEditor.round.concurrency') }}
            <span class="text-lf-danger">*</span>
          </div>
          <NInputNumber
            v-model:value="round.concurrency"
            :input-props="{ 'aria-label': t('executionPlanEditor.round.concurrency') }"
            :min="1"
            :max="100"
            size="small"
            :disabled="disabled"
            class="w-full"
          />
        </div>
      </div>

      <!-- 翻译模式配置 -->
      <template v-if="round.mode === 'translate' && round.translate">
        <div>
          <div class="mb-1 text-xs text-lf-text-subtle">
            {{ t('executionPlanEditor.round.promptTemplate') }}
            <span class="text-lf-danger">*</span>
          </div>
          <NSelect
            v-model:value="round.translate.prompt_template_id"
            :options="promptTemplates"
            size="small"
            :disabled="disabled"
            :placeholder="t('executionPlanEditor.round.promptTemplatePlaceholder')"
            clearable
            class="w-full"
          />
        </div>

        <div class="grid grid-cols-1 gap-3 md:grid-cols-3">
          <div>
            <div class="mb-1 text-xs text-lf-text-subtle">
              {{ t('executionPlanEditor.round.batchSize') }}
            </div>
            <NInputNumber
              v-model:value="round.translate.batch_size"
              :input-props="{ 'aria-label': t('executionPlanEditor.round.batchSize') }"
              :min="0"
              :max="10000"
              size="small"
              :disabled="disabled"
              class="w-full"
            />
            <div class="mt-1 text-xs text-lf-text-subtle">
              {{ t('executionPlanEditor.round.batchSizeHint') }}
            </div>
          </div>
          <div>
            <div class="mb-1 text-xs text-lf-text-subtle">
              {{ t('executionPlanEditor.round.maxWordsPerBatch') }}
            </div>
            <NInputNumber
              v-model:value="round.translate.max_words_per_batch"
              :min="0"
              :max="100000"
              size="small"
              :disabled="disabled"
              class="w-full"
            />
            <div class="mt-1 text-xs text-lf-text-subtle">
              {{ t('executionPlanEditor.round.maxWordsPerBatchHint') }}
            </div>
          </div>
          <div>
            <div class="mb-1 text-xs text-lf-text-subtle">
              {{ t('executionPlanEditor.round.fallbackShrink') }}
              <span class="text-lf-danger">*</span>
            </div>
            <NInputNumber
              v-model:value="round.translate.fallback_shrink"
              :min="0.01"
              :max="1"
              :step="0.1"
              :placeholder="t('executionPlanEditor.round.fallbackShrinkPlaceholder')"
              size="small"
              :disabled="disabled"
              class="w-full"
            />
            <div class="mt-1 text-xs text-lf-text-subtle">
              {{ t('executionPlanEditor.round.fallbackShrinkHint') }}
            </div>
          </div>
        </div>

        <!-- 段落过滤配置 -->
        <div>
          <div class="mb-1 text-xs text-lf-text-subtle">
            {{ t('executionPlanEditor.round.segmentFilter') }}
          </div>
          <NSelect
            v-if="round.translate.segment_filter"
            v-model:value="round.translate.segment_filter.status_filter"
            :options="segmentFilterOptions"
            size="small"
            :disabled="disabled"
            class="w-full"
          />
          <div class="mt-1 text-xs text-lf-text-subtle">
            {{ t('executionPlanEditor.round.segmentFilterHint') }}
          </div>
        </div>

        <!-- 高级配置（可折叠） -->
        <RoundAdvancedSettings
          :round="round"
          :disabled="disabled"
          @update:inline-term-extraction="(value) => updateInlineTermExtraction(round, value)"
        />
      </template>

      <!-- 术语抽取模式配置 -->
      <template v-if="round.mode === 'extract' && round.extract">
        <div class="grid grid-cols-1 gap-3 md:grid-cols-2">
          <div>
            <div class="mb-1 text-xs text-lf-text-subtle">
              {{ t('executionPlanEditor.round.extractTemplate') }}
              <span class="text-lf-danger">*</span>
            </div>
            <NSelect
              v-model:value="round.extract.template_id"
              :aria-label="t('executionPlanEditor.round.extractTemplate')"
              :options="bootstrapPromptTemplates"
              size="small"
              :disabled="disabled"
              :placeholder="t('executionPlanEditor.round.extractTemplatePlaceholder')"
              clearable
              class="w-full"
            />
          </div>
          <div>
            <div class="mb-1 text-xs text-lf-text-subtle">
              {{ t('executionPlanEditor.round.extractMinSourceLen') }}
            </div>
            <NInputNumber
              v-model:value="round.extract.min_source_len"
              :input-props="{ 'aria-label': t('executionPlanEditor.round.extractMinSourceLen') }"
              :min="1"
              :max="100"
              size="small"
              :disabled="disabled"
              class="w-full"
            />
          </div>
        </div>

        <div>
          <div class="mb-2 flex items-center gap-2">
            <NSwitch
              :value="isNoBatch(round.extract.batch_size, round.extract.max_words_per_batch)"
              size="small"
              :disabled="disabled"
              @update:value="(v: boolean) => setNoBatch(round.extract!, v)"
            />
            <span class="text-xs text-lf-text-subtle">
              {{ t('executionPlanEditor.round.noBatch') }}
            </span>
          </div>
          <div
            class="grid grid-cols-1 gap-3 md:grid-cols-2"
            :class="{
              'opacity-50 pointer-events-none': isNoBatch(
                round.extract.batch_size,
                round.extract.max_words_per_batch,
              ),
            }"
          >
            <div>
              <div class="mb-1 text-xs text-lf-text-subtle">
                {{ t('executionPlanEditor.round.extractBatchSize') }}
              </div>
              <NInputNumber
                v-model:value="round.extract.batch_size"
                :input-props="{ 'aria-label': t('executionPlanEditor.round.extractBatchSize') }"
                :min="0"
                :max="10000"
                size="small"
                :disabled="disabled"
                class="w-full"
              />
              <div class="mt-1 text-xs text-lf-text-subtle">
                {{ t('executionPlanEditor.round.extractBatchSizeHint') }}
              </div>
            </div>
            <div>
              <div class="mb-1 text-xs text-lf-text-subtle">
                {{ t('executionPlanEditor.round.extractMaxWordsPerBatch') }}
              </div>
              <NInputNumber
                v-model:value="round.extract.max_words_per_batch"
                :input-props="{
                  'aria-label': t('executionPlanEditor.round.extractMaxWordsPerBatch'),
                }"
                :min="0"
                :max="100000"
                size="small"
                :disabled="disabled"
                class="w-full"
              />
              <div class="mt-1 text-xs text-lf-text-subtle">
                {{ t('executionPlanEditor.round.extractMaxWordsPerBatchHint') }}
              </div>
            </div>
          </div>
          <div class="mt-3">
            <div class="mb-1 text-xs text-lf-text-subtle">
              {{ t('executionPlanEditor.round.extractMaxTerms') }}
            </div>
            <NInputNumber
              v-model:value="round.extract.max_terms_per_1000_chars"
              :input-props="{ 'aria-label': t('executionPlanEditor.round.extractMaxTerms') }"
              :min="0"
              :max="1000"
              :step="0.1"
              size="small"
              :disabled="disabled"
              class="w-full"
            />
          </div>
        </div>

        <!-- 高级配置（可折叠） -->
        <RoundAdvancedSettings :round="round" :disabled="disabled" />
      </template>

      <!-- 质量裁决模式配置 -->
      <template v-if="round.mode === 'adjudicate' && round.adjudicate">
        <div class="rounded-lf-ctl border border-lf-border-soft bg-lf-surface-muted/40 px-3 py-2">
          <p class="text-xs leading-5 text-lf-text-muted">
            {{ t('executionPlanEditor.round.adjudicatePromptHint') }}
          </p>
        </div>

        <div class="grid grid-cols-1 gap-3 md:grid-cols-2">
          <div>
            <div class="mb-1 text-xs text-lf-text-subtle">
              {{ t('executionPlanEditor.round.adjudicateBatchSize') }}
            </div>
            <NInputNumber
              v-model:value="round.adjudicate.batch_size"
              :min="0"
              :max="10000"
              size="small"
              :disabled="disabled"
              class="w-full"
            />
            <div class="mt-1 text-xs text-lf-text-subtle">
              {{ t('executionPlanEditor.round.adjudicateBatchSizeHint') }}
            </div>
          </div>
          <div>
            <div class="mb-1 text-xs text-lf-text-subtle">
              {{ t('executionPlanEditor.round.adjudicateMaxWordsPerBatch') }}
            </div>
            <NInputNumber
              v-model:value="round.adjudicate.max_words_per_batch"
              :min="0"
              :max="100000"
              size="small"
              :disabled="disabled"
              class="w-full"
            />
            <div class="mt-1 text-xs text-lf-text-subtle">
              {{ t('executionPlanEditor.round.adjudicateMaxWordsPerBatchHint') }}
            </div>
          </div>
        </div>

        <div>
          <div class="mb-1 text-xs text-lf-text-subtle">
            {{ t('executionPlanEditor.round.adjudicateCodes') }}
          </div>
          <NRadioGroup
            :value="roundCodes(round) === undefined ? 'default' : 'specified'"
            :disabled="disabled"
            size="small"
            class="mb-2"
            @update:value="(value: 'default' | 'specified') => chooseCodeMode(round, value)"
          >
            <NRadioButton value="default">{{
              t('configurationProfiles.defaultValue')
            }}</NRadioButton>
            <NRadioButton value="specified">{{
              t('configurationProfiles.specified')
            }}</NRadioButton>
          </NRadioGroup>
          <NSelect
            :value="round.adjudicate.adjudicate_codes ?? []"
            @update:value="(value: string[]) => setRoundCodes(round, value)"
            :options="adjudicateCodeOptions"
            multiple
            size="small"
            :disabled="disabled || roundCodes(round) === undefined"
            :placeholder="t('configurationProfiles.chooseCodes')"
            class="w-full"
          />
          <div class="mt-1 text-xs text-lf-text-subtle">
            {{ t('configurationProfiles.adjudicateHint') }}
          </div>
        </div>

        <RoundAdvancedSettings :round="round" :disabled="disabled" />
      </template>

      <!-- 语义质检模式配置 -->
      <template v-if="round.mode === 'semantic_qa' && round.semantic_qa">
        <div class="rounded-lf-ctl border border-lf-border-soft bg-lf-surface-muted/40 px-3 py-2">
          <p class="text-xs leading-5 text-lf-text-muted">
            {{ t('executionPlanEditor.round.semanticQAPromptHint') }}
          </p>
        </div>

        <div>
          <div class="mb-1 text-xs text-lf-text-subtle">
            {{ t('executionPlanEditor.round.semanticQASegmentScope') }}
          </div>
          <NSelect
            :value="round.semantic_qa.segment_scope ?? 'all'"
            :aria-label="t('executionPlanEditor.round.semanticQASegmentScope')"
            :options="semanticQASegmentScopeOptions"
            size="small"
            :disabled="disabled"
            :placeholder="t('executionPlanEditor.round.semanticQASegmentScopePlaceholder')"
            class="w-full"
            @update:value="
              (val: SemanticQASegmentScope) => onSemanticQASegmentScopeChange(round, val)
            "
          />
          <div class="mt-1 text-xs text-lf-text-subtle">
            {{ t('executionPlanEditor.round.semanticQASegmentScopeHint') }}
          </div>
        </div>

        <div>
          <div class="mb-1 text-xs text-lf-text-subtle">
            {{ t('executionPlanEditor.round.semanticQAIssueCodes') }}
          </div>
          <NRadioGroup
            :value="roundCodes(round) === undefined ? 'default' : 'specified'"
            :disabled="disabled || round.semantic_qa.segment_scope !== 'with_issue_codes'"
            size="small"
            class="mb-2"
            @update:value="(value: 'default' | 'specified') => chooseCodeMode(round, value)"
          >
            <NRadioButton value="default">{{
              t('configurationProfiles.defaultValue')
            }}</NRadioButton>
            <NRadioButton value="specified">{{
              t('configurationProfiles.specified')
            }}</NRadioButton>
          </NRadioGroup>
          <NSelect
            :value="round.semantic_qa.issue_codes ?? []"
            :aria-label="t('executionPlanEditor.round.semanticQAIssueCodes')"
            @update:value="(value: string[]) => setRoundCodes(round, value)"
            :options="semanticQAIssueCodeOptions"
            multiple
            size="small"
            :disabled="disabled || round.semantic_qa.segment_scope !== 'with_issue_codes'"
            :placeholder="t('executionPlanEditor.round.semanticQAIssueCodesPlaceholder')"
            class="w-full"
          />
          <div class="mt-1 text-xs text-lf-text-subtle">
            {{
              round.semantic_qa.segment_scope === 'with_issue_codes'
                ? t('executionPlanEditor.round.semanticQAIssueCodesHint')
                : t('configurationProfiles.ignoredCodes')
            }}
          </div>
        </div>

        <div class="grid grid-cols-1 gap-3 md:grid-cols-2">
          <div>
            <div class="mb-1 text-xs text-lf-text-subtle">
              {{ t('executionPlanEditor.round.semanticQABatchSize') }}
            </div>
            <NInputNumber
              v-model:value="round.semantic_qa.batch_size"
              :min="0"
              :max="10000"
              size="small"
              :disabled="disabled"
              class="w-full"
            />
            <div class="mt-1 text-xs text-lf-text-subtle">
              {{ t('executionPlanEditor.round.semanticQABatchSizeHint') }}
            </div>
          </div>
          <div>
            <div class="mb-1 text-xs text-lf-text-subtle">
              {{ t('executionPlanEditor.round.semanticQAMaxWordsPerBatch') }}
            </div>
            <NInputNumber
              v-model:value="round.semantic_qa.max_words_per_batch"
              :min="0"
              :max="100000"
              size="small"
              :disabled="disabled"
              class="w-full"
            />
            <div class="mt-1 text-xs text-lf-text-subtle">
              {{ t('executionPlanEditor.round.semanticQAMaxWordsPerBatchHint') }}
            </div>
          </div>
        </div>

        <RoundAdvancedSettings :round="round" :disabled="disabled" />
      </template>

      <!-- LLM 修订模式配置 -->
      <template v-if="round.mode === 'revise' && round.revise">
        <div class="rounded-lf-ctl border border-lf-border-soft bg-lf-surface-muted/40 px-3 py-2">
          <p class="text-xs leading-5 text-lf-text-muted">
            {{ t('executionPlanEditor.round.revisePromptHint') }}
          </p>
        </div>

        <div>
          <div class="mb-1 text-xs text-lf-text-subtle">
            {{ t('executionPlanEditor.round.reviseSegmentScope') }}
          </div>
          <NSelect
            :value="round.revise.segment_scope ?? 'with_issues'"
            :aria-label="t('executionPlanEditor.round.reviseSegmentScope')"
            :options="reviseSegmentScopeOptions"
            size="small"
            :disabled="disabled"
            :placeholder="t('executionPlanEditor.round.reviseSegmentScopePlaceholder')"
            class="w-full"
            @update:value="(val: ReviseSegmentScope) => onReviseSegmentScopeChange(round, val)"
          />
          <div class="mt-1 text-xs text-lf-text-subtle">
            {{ t('executionPlanEditor.round.reviseSegmentScopeHint') }}
          </div>
        </div>

        <div>
          <div class="mb-1 text-xs text-lf-text-subtle">
            {{ t('executionPlanEditor.round.reviseIssueCodes') }}
          </div>
          <NRadioGroup
            :value="roundCodes(round) === undefined ? 'default' : 'specified'"
            :disabled="disabled"
            size="small"
            class="mb-2"
            @update:value="(value: 'default' | 'specified') => chooseCodeMode(round, value)"
          >
            <NRadioButton value="default">{{
              t('configurationProfiles.defaultValue')
            }}</NRadioButton>
            <NRadioButton value="specified">{{
              t('configurationProfiles.specified')
            }}</NRadioButton>
          </NRadioGroup>
          <NSelect
            :value="round.revise.issue_codes ?? []"
            :aria-label="t('executionPlanEditor.round.reviseIssueCodes')"
            @update:value="(value: string[]) => setRoundCodes(round, value)"
            :options="reviseIssueCodeOptions"
            multiple
            size="small"
            :disabled="
              disabled ||
              (roundCodes(round) === undefined && round.revise.segment_scope !== 'with_issue_codes')
            "
            :placeholder="t('executionPlanEditor.round.reviseIssueCodesPlaceholder')"
            class="w-full"
          />
          <div class="mt-1 text-xs text-lf-text-subtle">
            {{ t('executionPlanEditor.round.reviseIssueCodesHint') }}
          </div>
        </div>

        <div class="grid grid-cols-1 gap-3 md:grid-cols-2">
          <div>
            <div class="mb-1 text-xs text-lf-text-subtle">
              {{ t('executionPlanEditor.round.reviseBatchSize') }}
            </div>
            <NInputNumber
              v-model:value="round.revise.batch_size"
              :min="0"
              :max="10000"
              size="small"
              :disabled="disabled"
              class="w-full"
            />
            <div class="mt-1 text-xs text-lf-text-subtle">
              {{ t('executionPlanEditor.round.reviseBatchSizeHint') }}
            </div>
          </div>
          <div>
            <div class="mb-1 text-xs text-lf-text-subtle">
              {{ t('executionPlanEditor.round.reviseMaxWordsPerBatch') }}
            </div>
            <NInputNumber
              v-model:value="round.revise.max_words_per_batch"
              :min="0"
              :max="100000"
              size="small"
              :disabled="disabled"
              class="w-full"
            />
            <div class="mt-1 text-xs text-lf-text-subtle">
              {{ t('executionPlanEditor.round.reviseMaxWordsPerBatchHint') }}
            </div>
          </div>
        </div>

        <RoundAdvancedSettings :round="round" :disabled="disabled" />
      </template>

      <p v-if="validateRoundCodes(round)" role="alert" class="text-xs text-lf-danger">
        {{
          validateRoundCodes(round) === 'required'
            ? t('configurationProfiles.codesRequired')
            : t('configurationProfiles.invalidCodes')
        }}
      </p>
      <p
        v-else-if="
          (round.mode === 'adjudicate' || round.mode === 'revise') &&
          roundCodes(round)?.length === 0
        "
        class="text-xs text-lf-text-subtle"
      >
        {{ t('configurationProfiles.emptyCodes') }}
      </p>
      <!-- 本地改写模式配置 -->
      <template v-if="round.mode === 'correct' && round.correct">
        <div class="rounded-lf-ctl border border-lf-border-soft bg-lf-surface-muted/40 px-3 py-2">
          <p class="text-xs leading-5 text-lf-text-muted">
            {{ t('executionPlanEditor.round.correctPromptHint') }}
          </p>
        </div>

        <!-- 改写规则列表 -->
        <div>
          <div class="mb-1 text-xs text-lf-text-subtle">
            {{ t('executionPlanEditor.round.correctRules') }}
            <span class="text-lf-danger">*</span>
          </div>
          <div class="space-y-2">
            <div
              v-for="(rule, rIdx) in round.correct.rules"
              :key="rIdx"
              class="flex items-start gap-3 rounded-lf-ctl border border-lf-border-soft bg-lf-surface px-3 py-2"
            >
              <div class="flex flex-1 flex-col gap-1">
                <span class="text-sm font-medium text-lf-text-strong">
                  {{ correctRuleLabelMap[rule.name] }}
                </span>
                <span class="text-xs text-lf-text-subtle">
                  {{ correctRuleHintMap[rule.name] }}
                </span>
              </div>
              <NSwitch v-model:value="rule.enabled" size="small" :disabled="disabled" />
            </div>
          </div>
          <div class="mt-1 text-xs text-lf-text-subtle">
            {{ t('executionPlanEditor.round.correctRulesHint') }}
          </div>
        </div>
      </template>
    </ConfigSectionPanel>

    <!-- 添加轮次按钮 -->
    <NButton dashed block :disabled="disabled" @click="addRound">
      <template #icon>
        <NIcon size="16"><IconCarbonAdd /></NIcon>
      </template>
      {{ t('executionPlanEditor.actions.addRound') }}
    </NButton>
  </div>
</template>
