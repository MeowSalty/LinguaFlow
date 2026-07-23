<script setup lang="ts">
import {
  NButton,
  NCard,
  NCollapse,
  NCollapseItem,
  NGrid,
  NGi,
  NInputNumber,
  NSelect,
  NSwitch,
  NRadioGroup,
  NRadioButton,
} from 'naive-ui'
import type { SelectOption } from 'naive-ui'
import { useI18n } from 'vue-i18n'

import type { ApiSchemas } from '@/api/client'

type ExecutionRoundConfig = ApiSchemas['ExecutionRoundConfig']
type TranslateRoundConfig = NonNullable<ExecutionRoundConfig['translate']>
type ExtractRoundConfig = NonNullable<ExecutionRoundConfig['extract']>
type AdjudicateRoundConfig = NonNullable<ExecutionRoundConfig['adjudicate']>
type RetryConfig = NonNullable<TranslateRoundConfig['retry']>
type ExecutionPlanRubyRetryConfig = ApiSchemas['ExecutionPlanRubyRetryConfig']
type RoundMode = ExecutionRoundConfig['mode']
type AdjudicateCode = NonNullable<AdjudicateRoundConfig['adjudicate_codes']>[number]

type RoundModel = ExecutionRoundConfig

// ─── 默认值 ──────────────────────────────────────────────────

const DEFAULT_RETRY: RetryConfig = { max_attempts: 3, backoff_ms: 2000, jitter: true }

const DEFAULT_TRANSLATE: TranslateRoundConfig = {
  prompt_template_id: 0,
  profile_id: 0,
  batch_size: 10,
  max_words_per_batch: 0,
  fallback_shrink: undefined,
  segment_filter: { status_filter: 'pending_only' },
  retry: { ...DEFAULT_RETRY },
}

const DEFAULT_EXTRACT: ExtractRoundConfig = {
  template_id: 0,
  batch_size: 20,
  max_words_per_batch: 0,
  max_terms_per_1000_chars: 25.0,
  min_source_len: 2,
  retry: { ...DEFAULT_RETRY },
}

const DEFAULT_ADJUDICATE: AdjudicateRoundConfig = {
  batch_size: 10,
  max_words_per_batch: 0,
  adjudicate_codes: ['source_residual'],
  retry: { ...DEFAULT_RETRY },
}

const DEFAULT_ROUND: RoundModel = {
  mode: 'translate',
  backend_id: 0,
  concurrency: 3,
  translate: { ...DEFAULT_TRANSLATE },
}

const DEFAULT_RUBY_RETRY: ExecutionPlanRubyRetryConfig = {
  enabled: false,
  backend_id: 0,
}

// ─── 工具函数 ────────────────────────────────────────────────

function deepClone<T>(obj: T): T {
  return JSON.parse(JSON.stringify(obj))
}

function mergeTranslate(source?: Partial<TranslateRoundConfig>): TranslateRoundConfig {
  if (!source) return deepClone(DEFAULT_TRANSLATE)
  return {
    prompt_template_id: source.prompt_template_id ?? DEFAULT_TRANSLATE.prompt_template_id,
    profile_id: source.profile_id ?? DEFAULT_TRANSLATE.profile_id,
    batch_size: source.batch_size ?? DEFAULT_TRANSLATE.batch_size,
    max_words_per_batch: source.max_words_per_batch ?? DEFAULT_TRANSLATE.max_words_per_batch,
    fallback_shrink: source.fallback_shrink ?? DEFAULT_TRANSLATE.fallback_shrink,
    segment_filter: {
      status_filter: source.segment_filter?.status_filter ?? 'pending_only',
    },
    retry: {
      max_attempts: source.retry?.max_attempts ?? DEFAULT_RETRY.max_attempts,
      backoff_ms: source.retry?.backoff_ms ?? DEFAULT_RETRY.backoff_ms,
      jitter: source.retry?.jitter ?? DEFAULT_RETRY.jitter,
    },
  }
}

function mergeExtract(source?: Partial<ExtractRoundConfig>): ExtractRoundConfig {
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
      source.adjudicate_codes && source.adjudicate_codes.length > 0
        ? [...source.adjudicate_codes]
        : [...(DEFAULT_ADJUDICATE.adjudicate_codes ?? [])],
    retry: {
      max_attempts: source.retry?.max_attempts ?? DEFAULT_RETRY.max_attempts,
      backoff_ms: source.retry?.backoff_ms ?? DEFAULT_RETRY.backoff_ms,
      jitter: source.retry?.jitter ?? DEFAULT_RETRY.jitter,
    },
  }
}

function mergeRound(source?: Partial<ExecutionRoundConfig>): RoundModel {
  if (!source) return deepClone(DEFAULT_ROUND)
  const mode = source.mode ?? 'translate'
  return {
    mode,
    backend_id: source.backend_id ?? DEFAULT_ROUND.backend_id,
    concurrency: source.concurrency ?? DEFAULT_ROUND.concurrency,
    translate: mode === 'translate' ? mergeTranslate(source.translate) : undefined,
    extract: mode === 'extract' ? mergeExtract(source.extract) : undefined,
    adjudicate: mode === 'adjudicate' ? mergeAdjudicate(source.adjudicate) : undefined,
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

function mergeRubyRetry(
  source?: Partial<ExecutionPlanRubyRetryConfig>,
): ExecutionPlanRubyRetryConfig {
  if (!source) return deepClone(DEFAULT_RUBY_RETRY)
  return {
    enabled: source.enabled ?? DEFAULT_RUBY_RETRY.enabled,
    backend_id: source.backend_id ?? DEFAULT_RUBY_RETRY.backend_id,
  }
}

// ─── Props & Emits ──────────────────────────────────────────

const props = withDefaults(
  defineProps<{
    rounds: ExecutionRoundConfig[]
    rubyRetry?: ExecutionPlanRubyRetryConfig
    backends: SelectOption[]
    promptTemplates: SelectOption[]
    bootstrapPromptTemplates: SelectOption[]
    executionProfiles: SelectOption[]
    disabled?: boolean
  }>(),
  { disabled: false },
)

const emit = defineEmits<{
  'update:rounds': [value: ExecutionRoundConfig[]]
  'update:rubyRetry': [value: ExecutionPlanRubyRetryConfig]
}>()

// ─── 内部状态 ────────────────────────────────────────────────

const { t } = useI18n()

const roundsModel = ref<RoundModel[]>(props.rounds.map((r) => mergeRound(r)))
const rubyRetryModel = ref<ExecutionPlanRubyRetryConfig>(mergeRubyRetry(props.rubyRetry))

let lastRoundsJson = JSON.stringify(props.rounds ?? [])
let lastRubyRetryJson = JSON.stringify(props.rubyRetry ?? {})

watch(
  () => props.rounds,
  (newVal) => {
    const json = JSON.stringify(newVal ?? [])
    if (json === lastRoundsJson) return
    roundsModel.value = (newVal ?? []).map((r) => mergeRound(r))
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
    const json = JSON.stringify(newVal ?? {})
    if (json === lastRubyRetryJson) return
    rubyRetryModel.value = mergeRubyRetry(newVal)
  },
  { deep: true },
)

watch(
  rubyRetryModel,
  (newVal) => {
    const json = JSON.stringify(newVal)
    if (json === lastRubyRetryJson) return
    lastRubyRetryJson = json
    emit('update:rubyRetry', deepClone(newVal))
  },
  { deep: true },
)

// ─── 操作方法 ────────────────────────────────────────────────

const modeOptions = computed(() => [
  { label: t('executionPlanEditor.round.modeTranslate'), value: 'translate' as RoundMode },
  { label: t('executionPlanEditor.round.modeExtract'), value: 'extract' as RoundMode },
  { label: t('executionPlanEditor.round.modeAdjudicate'), value: 'adjudicate' as RoundMode },
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
])

const modeBadgeClass = (mode: RoundMode): string => {
  if (mode === 'translate') return 'bg-lf-brand-soft text-brand-600'
  if (mode === 'extract') return 'bg-amber-50 text-amber-600'
  return 'bg-violet-50 text-violet-600'
}

const modeLabel = (mode: RoundMode): string => {
  if (mode === 'translate') return t('executionPlanEditor.round.modeTranslate')
  if (mode === 'extract') return t('executionPlanEditor.round.modeExtract')
  return t('executionPlanEditor.round.modeAdjudicate')
}

const switchRoundMode = (round: RoundModel, mode: RoundMode): void => {
  if (round.mode === mode) return
  round.mode = mode
  round.translate = undefined
  round.extract = undefined
  round.adjudicate = undefined
  if (mode === 'translate') {
    round.translate = deepClone(DEFAULT_TRANSLATE)
  } else if (mode === 'extract') {
    round.extract = deepClone(DEFAULT_EXTRACT)
  } else {
    round.adjudicate = deepClone(DEFAULT_ADJUDICATE)
  }
}

const addRound = (): void => {
  roundsModel.value.push(deepClone(DEFAULT_ROUND))
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
  emit('update:rounds', deepClone(roundsModel.value))
}
</script>

<template>
  <div class="flex flex-col gap-4">
    <!-- Ruby Retry 注音对齐重试配置 -->
    <NCard size="small" :bordered="true">
      <template #header>
        <span class="text-sm font-semibold">{{ t('executionPlanEditor.rubyRetry.title') }}</span>
      </template>
      <div class="flex items-center justify-between mb-3">
        <span class="text-sm">{{ t('executionPlanEditor.rubyRetry.enabled') }}</span>
        <NSwitch v-model:value="rubyRetryModel.enabled" size="small" :disabled="disabled" />
      </div>
      <div :class="{ 'opacity-50 pointer-events-none': !rubyRetryModel.enabled }">
        <div>
          <div class="mb-1 text-xs text-lf-text-subtle">
            {{ t('executionPlanEditor.rubyRetry.backend') }}
          </div>
          <NSelect
            v-model:value="rubyRetryModel.backend_id"
            :options="backends"
            size="small"
            :disabled="disabled || !rubyRetryModel.enabled"
            clearable
            :placeholder="t('executionPlanEditor.rubyRetry.backendPlaceholder')"
          />
        </div>
      </div>
    </NCard>

    <!-- 轮次列表 -->
    <NCard
      v-for="(round, index) in roundsModel"
      :key="index"
      size="small"
      :bordered="true"
      class="relative"
    >
      <template #header>
        <div class="flex items-center gap-2">
          <span
            class="inline-flex h-6 w-6 items-center justify-center rounded-full text-xs font-bold"
            :class="modeBadgeClass(round.mode)"
          >
            {{ index + 1 }}
          </span>
          <span class="text-sm font-semibold">
            {{ modeLabel(round.mode) }}
          </span>
        </div>
      </template>

      <template #header-extra>
        <div class="flex items-center gap-1">
          <NButton
            text
            size="small"
            :disabled="disabled || index === 0"
            @click="moveRound(index, -1)"
          >
            ▲
          </NButton>
          <NButton
            text
            size="small"
            :disabled="disabled || index === roundsModel.length - 1"
            @click="moveRound(index, 1)"
          >
            ▼
          </NButton>
          <NButton
            text
            type="error"
            size="small"
            :disabled="disabled || roundsModel.length <= 1"
            @click="removeRound(index)"
          >
            ✕
          </NButton>
        </div>
      </template>

      <!-- 模式选择 -->
      <div>
        <div class="mb-1 text-xs text-lf-text-subtle">
          {{ t('executionPlanEditor.round.mode') }}
          <span class="text-red-400">*</span>
        </div>
        <NRadioGroup
          :value="round.mode"
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
      <div class="mt-3 grid grid-cols-1 gap-3 md:grid-cols-2">
        <div>
          <div class="mb-1 text-xs text-lf-text-subtle">
            {{ t('executionPlanEditor.round.backend') }}
            <span class="text-red-400">*</span>
          </div>
          <NSelect
            v-model:value="round.backend_id"
            :options="backends"
            size="small"
            :disabled="disabled"
            :placeholder="t('executionPlanEditor.round.backendPlaceholder')"
          />
        </div>
        <div>
          <div class="mb-1 text-xs text-lf-text-subtle">
            {{ t('executionPlanEditor.round.concurrency') }}
            <span class="text-red-400">*</span>
          </div>
          <NInputNumber
            v-model:value="round.concurrency"
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
        <div class="mt-3 grid grid-cols-1 gap-3 md:grid-cols-2">
          <div>
            <div class="mb-1 text-xs text-lf-text-subtle">
              {{ t('executionPlanEditor.round.promptTemplate') }}
            </div>
            <NSelect
              v-model:value="round.translate.prompt_template_id"
              :options="promptTemplates"
              size="small"
              :disabled="disabled"
              :placeholder="t('executionPlanEditor.round.promptTemplatePlaceholder')"
              clearable
            />
          </div>
          <div>
            <div class="mb-1 text-xs text-lf-text-subtle">
              {{ t('executionPlanEditor.round.translationProfile') }}
            </div>
            <NSelect
              v-model:value="round.translate.profile_id"
              :options="executionProfiles"
              size="small"
              :disabled="disabled"
              :placeholder="t('executionPlanEditor.round.profilePlaceholder')"
              clearable
            />
          </div>
        </div>

        <div class="mt-3 grid grid-cols-1 gap-3 md:grid-cols-3">
          <div>
            <div class="mb-1 text-xs text-lf-text-subtle">
              {{ t('executionPlanEditor.round.batchSize') }}
            </div>
            <NInputNumber
              v-model:value="round.translate.batch_size"
              :min="0"
              :max="10000"
              size="small"
              :disabled="disabled"
              class="w-full"
            />
            <div class="mt-1 text-[11px] text-lf-text-subtle">
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
            <div class="mt-1 text-[11px] text-lf-text-subtle">
              {{ t('executionPlanEditor.round.maxWordsPerBatchHint') }}
            </div>
          </div>
          <div>
            <div class="mb-1 text-xs text-lf-text-subtle">
              {{ t('executionPlanEditor.round.fallbackShrink') }}
            </div>
            <NInputNumber
              v-model:value="round.translate.fallback_shrink"
              :min="0.01"
              :max="0.99"
              :step="0.1"
              :placeholder="t('executionPlanEditor.round.fallbackShrinkPlaceholder')"
              size="small"
              :disabled="disabled"
              class="w-full"
            />
          </div>
        </div>

        <!-- 段落过滤配置 -->
        <div class="mt-3">
          <div class="mb-1 text-xs text-lf-text-subtle">
            {{ t('executionPlanEditor.round.segmentFilter') }}
          </div>
          <NSelect
            v-if="round.translate.segment_filter"
            v-model:value="round.translate.segment_filter.status_filter"
            :options="segmentFilterOptions"
            size="small"
            :disabled="disabled"
          />
          <div class="mt-1 text-[11px] text-lf-text-subtle">
            {{ t('executionPlanEditor.round.segmentFilterHint') }}
          </div>
        </div>

        <!-- 高级配置（可折叠） -->
        <NCollapse class="mt-3">
          <NCollapseItem :title="t('executionPlanEditor.round.advancedConfig')">
            <NGrid :cols="2" :x-gap="12" :y-gap="10">
              <NGi>
                <div class="mb-1 text-xs text-lf-text-subtle">
                  {{ t('executionPlanEditor.round.retryMaxAttempts') }}
                </div>
                <NInputNumber
                  v-if="round.translate.retry"
                  v-model:value="round.translate.retry.max_attempts"
                  :min="0"
                  :max="10"
                  size="small"
                  :disabled="disabled"
                  class="w-full"
                />
              </NGi>
              <NGi>
                <div class="mb-1 text-xs text-lf-text-subtle">
                  {{ t('executionPlanEditor.round.retryBackoffMs') }}
                </div>
                <NInputNumber
                  v-if="round.translate.retry"
                  v-model:value="round.translate.retry.backoff_ms"
                  :min="0"
                  :max="60000"
                  :step="100"
                  size="small"
                  :disabled="disabled"
                  class="w-full"
                />
              </NGi>
            </NGrid>
            <div v-if="round.translate.retry" class="mt-2 flex items-center gap-2">
              <NSwitch
                v-model:value="round.translate.retry.jitter"
                size="small"
                :disabled="disabled"
              />
              <span class="text-xs text-lf-text-subtle">
                {{ t('executionPlanEditor.round.retryJitter') }}
              </span>
            </div>
          </NCollapseItem>
        </NCollapse>
      </template>

      <!-- 术语抽取模式配置 -->
      <template v-if="round.mode === 'extract' && round.extract">
        <div class="mt-3 grid grid-cols-1 gap-3 md:grid-cols-2">
          <div>
            <div class="mb-1 text-xs text-lf-text-subtle">
              {{ t('executionPlanEditor.round.extractTemplate') }}
            </div>
            <NSelect
              v-model:value="round.extract.template_id"
              :options="bootstrapPromptTemplates"
              size="small"
              :disabled="disabled"
              :placeholder="t('executionPlanEditor.round.extractTemplatePlaceholder')"
              clearable
            />
          </div>
          <div>
            <div class="mb-1 text-xs text-lf-text-subtle">
              {{ t('executionPlanEditor.round.extractMinSourceLen') }}
            </div>
            <NInputNumber
              v-model:value="round.extract.min_source_len"
              :min="1"
              :max="100"
              size="small"
              :disabled="disabled"
              class="w-full"
            />
          </div>
        </div>

        <div class="mt-3">
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
                :min="0"
                :max="10000"
                size="small"
                :disabled="disabled"
                class="w-full"
              />
              <div class="mt-1 text-[11px] text-lf-text-subtle">
                {{ t('executionPlanEditor.round.extractBatchSizeHint') }}
              </div>
            </div>
            <div>
              <div class="mb-1 text-xs text-lf-text-subtle">
                {{ t('executionPlanEditor.round.extractMaxWordsPerBatch') }}
              </div>
              <NInputNumber
                v-model:value="round.extract.max_words_per_batch"
                :min="0"
                :max="100000"
                size="small"
                :disabled="disabled"
                class="w-full"
              />
              <div class="mt-1 text-[11px] text-lf-text-subtle">
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
        <NCollapse class="mt-3">
          <NCollapseItem :title="t('executionPlanEditor.round.advancedConfig')">
            <NGrid :cols="2" :x-gap="12" :y-gap="10">
              <NGi>
                <div class="mb-1 text-xs text-lf-text-subtle">
                  {{ t('executionPlanEditor.round.retryMaxAttempts') }}
                </div>
                <NInputNumber
                  v-if="round.extract.retry"
                  v-model:value="round.extract.retry.max_attempts"
                  :min="0"
                  :max="10"
                  size="small"
                  :disabled="disabled"
                  class="w-full"
                />
              </NGi>
              <NGi>
                <div class="mb-1 text-xs text-lf-text-subtle">
                  {{ t('executionPlanEditor.round.retryBackoffMs') }}
                </div>
                <NInputNumber
                  v-if="round.extract.retry"
                  v-model:value="round.extract.retry.backoff_ms"
                  :min="0"
                  :max="60000"
                  :step="100"
                  size="small"
                  :disabled="disabled"
                  class="w-full"
                />
              </NGi>
            </NGrid>
            <div v-if="round.extract.retry" class="mt-2 flex items-center gap-2">
              <NSwitch
                v-model:value="round.extract.retry.jitter"
                size="small"
                :disabled="disabled"
              />
              <span class="text-xs text-lf-text-subtle">
                {{ t('executionPlanEditor.round.retryJitter') }}
              </span>
            </div>
          </NCollapseItem>
        </NCollapse>
      </template>

      <!-- 质量裁决模式配置 -->
      <template v-if="round.mode === 'adjudicate' && round.adjudicate">
        <div class="mt-3 rounded-lg border border-lf-border-soft bg-lf-surface-muted/40 px-3 py-2">
          <p class="text-xs leading-5 text-lf-text-muted">
            {{ t('executionPlanEditor.round.adjudicatePromptHint') }}
          </p>
        </div>

        <div class="mt-3 grid grid-cols-1 gap-3 md:grid-cols-2">
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
            <div class="mt-1 text-[11px] text-lf-text-subtle">
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
            <div class="mt-1 text-[11px] text-lf-text-subtle">
              {{ t('executionPlanEditor.round.adjudicateMaxWordsPerBatchHint') }}
            </div>
          </div>
        </div>

        <div class="mt-3">
          <div class="mb-1 text-xs text-lf-text-subtle">
            {{ t('executionPlanEditor.round.adjudicateCodes') }}
          </div>
          <NSelect
            v-model:value="round.adjudicate.adjudicate_codes"
            :options="adjudicateCodeOptions"
            multiple
            size="small"
            :disabled="disabled"
            :placeholder="t('executionPlanEditor.round.adjudicateCodesPlaceholder')"
          />
          <div class="mt-1 text-[11px] text-lf-text-subtle">
            {{ t('executionPlanEditor.round.adjudicateCodesHint') }}
          </div>
        </div>

        <NCollapse class="mt-3">
          <NCollapseItem :title="t('executionPlanEditor.round.advancedConfig')">
            <NGrid :cols="2" :x-gap="12" :y-gap="10">
              <NGi>
                <div class="mb-1 text-xs text-lf-text-subtle">
                  {{ t('executionPlanEditor.round.retryMaxAttempts') }}
                </div>
                <NInputNumber
                  v-if="round.adjudicate.retry"
                  v-model:value="round.adjudicate.retry.max_attempts"
                  :min="0"
                  :max="10"
                  size="small"
                  :disabled="disabled"
                  class="w-full"
                />
              </NGi>
              <NGi>
                <div class="mb-1 text-xs text-lf-text-subtle">
                  {{ t('executionPlanEditor.round.retryBackoffMs') }}
                </div>
                <NInputNumber
                  v-if="round.adjudicate.retry"
                  v-model:value="round.adjudicate.retry.backoff_ms"
                  :min="0"
                  :max="60000"
                  :step="100"
                  size="small"
                  :disabled="disabled"
                  class="w-full"
                />
              </NGi>
            </NGrid>
            <div v-if="round.adjudicate.retry" class="mt-2 flex items-center gap-2">
              <NSwitch
                v-model:value="round.adjudicate.retry.jitter"
                size="small"
                :disabled="disabled"
              />
              <span class="text-xs text-lf-text-subtle">
                {{ t('executionPlanEditor.round.retryJitter') }}
              </span>
            </div>
          </NCollapseItem>
        </NCollapse>
      </template>
    </NCard>

    <!-- 添加轮次按钮 -->
    <NButton dashed block :disabled="disabled" @click="addRound">
      {{ t('executionPlanEditor.actions.addRound') }}
    </NButton>
  </div>
</template>
