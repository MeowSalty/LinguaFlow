<script setup lang="ts">
import {
  NButton,
  NCard,
  NCollapse,
  NCollapseItem,
  NGrid,
  NGi,
  NInput,
  NInputNumber,
  NSelect,
  NSwitch,
} from 'naive-ui'
import type { SelectOption } from 'naive-ui'
import { useI18n } from 'vue-i18n'

import type { ApiSchemas } from '@/api/client'

type ExecutionRoundConfig = ApiSchemas['ExecutionRoundConfig']
type RetryConfig = NonNullable<ExecutionRoundConfig['retry']>
type ExecutionPlanBootstrapConfig = ApiSchemas['ExecutionPlanBootstrapConfig']
type ExecutionPlanRubyRetryConfig = ApiSchemas['ExecutionPlanRubyRetryConfig']

/** 内部轮次模型：确保 retry 始终存在（API schema 中 retry 是可选的） */
type RoundModel = Omit<ExecutionRoundConfig, 'retry'> & { retry: RetryConfig }

// ─── 默认值 ──────────────────────────────────────────────────

const DEFAULT_RETRY: RetryConfig = { max_attempts: 3, backoff_ms: 2000, jitter: true }

const DEFAULT_ROUND: RoundModel = {
  backend_id: 0,
  prompt_template_id: 0,
  profile_id: 0,
  batch_size: 10,
  max_words_per_batch: 0,
  concurrency: 3,
  fallback_shrink: undefined,
  retry: { ...DEFAULT_RETRY },
}

const DEFAULT_BOOTSTRAP: ExecutionPlanBootstrapConfig = {
  enabled: false,
  backend_id: 0,
  prompt_template_id: 0,
  batch_size: 20,
  concurrency: 2,
  max_terms_per_batch: 20,
  min_source_len: 2,
}

const DEFAULT_RUBY_RETRY: ExecutionPlanRubyRetryConfig = {
  enabled: false,
  backend_id: 0,
}

// ─── 工具函数 ────────────────────────────────────────────────

function deepClone<T>(obj: T): T {
  return JSON.parse(JSON.stringify(obj))
}

/** 确保轮次所有字段都有默认值（处理 API 返回的可选字段） */
function mergeRound(source?: Partial<ExecutionRoundConfig>): RoundModel {
  if (!source) return deepClone(DEFAULT_ROUND)
  return {
    name: source.name,
    backend_id: source.backend_id ?? DEFAULT_ROUND.backend_id,
    prompt_template_id: source.prompt_template_id ?? DEFAULT_ROUND.prompt_template_id,
    profile_id: source.profile_id ?? DEFAULT_ROUND.profile_id,
    batch_size: source.batch_size ?? DEFAULT_ROUND.batch_size,
    max_words_per_batch: source.max_words_per_batch ?? DEFAULT_ROUND.max_words_per_batch,
    concurrency: source.concurrency ?? DEFAULT_ROUND.concurrency,
    fallback_shrink: source.fallback_shrink ?? DEFAULT_ROUND.fallback_shrink,
    retry: {
      max_attempts: source.retry?.max_attempts ?? DEFAULT_RETRY.max_attempts,
      backoff_ms: source.retry?.backoff_ms ?? DEFAULT_RETRY.backoff_ms,
      jitter: source.retry?.jitter ?? DEFAULT_RETRY.jitter,
    },
  }
}

/** 确保 bootstrap 所有字段都有默认值 */
function mergeBootstrap(
  source?: Partial<ExecutionPlanBootstrapConfig>,
): ExecutionPlanBootstrapConfig {
  if (!source) return deepClone(DEFAULT_BOOTSTRAP)
  return {
    enabled: source.enabled ?? DEFAULT_BOOTSTRAP.enabled,
    backend_id: source.backend_id ?? DEFAULT_BOOTSTRAP.backend_id,
    prompt_template_id: source.prompt_template_id ?? DEFAULT_BOOTSTRAP.prompt_template_id,
    batch_size: source.batch_size ?? DEFAULT_BOOTSTRAP.batch_size,
    concurrency: source.concurrency ?? DEFAULT_BOOTSTRAP.concurrency,
    max_terms_per_batch: source.max_terms_per_batch ?? DEFAULT_BOOTSTRAP.max_terms_per_batch,
    min_source_len: source.min_source_len ?? DEFAULT_BOOTSTRAP.min_source_len,
  }
}

/** 确保 ruby_retry 所有字段都有默认值 */
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
    bootstrap?: ExecutionPlanBootstrapConfig
    rubyRetry?: ExecutionPlanRubyRetryConfig
    backends: SelectOption[]
    promptTemplates: SelectOption[]
    translationProfiles: SelectOption[]
    disabled?: boolean
  }>(),
  { disabled: false },
)

const emit = defineEmits<{
  'update:rounds': [value: ExecutionRoundConfig[]]
  'update:bootstrap': [value: ExecutionPlanBootstrapConfig]
  'update:rubyRetry': [value: ExecutionPlanRubyRetryConfig]
}>()

// ─── 内部状态 ────────────────────────────────────────────────

const { t } = useI18n()

const roundsModel = ref<RoundModel[]>(props.rounds.map((r) => mergeRound(r)))
const bootstrapModel = ref<ExecutionPlanBootstrapConfig>(mergeBootstrap(props.bootstrap))
const rubyRetryModel = ref<ExecutionPlanRubyRetryConfig>(mergeRubyRetry(props.rubyRetry))

// 上次 emit 的 JSON（用于去重）
let lastRoundsJson = JSON.stringify(props.rounds ?? [])
let lastBootstrapJson = JSON.stringify(props.bootstrap ?? {})
let lastRubyRetryJson = JSON.stringify(props.rubyRetry ?? {})

// 监听外部 rounds 变化
watch(
  () => props.rounds,
  (newVal) => {
    const json = JSON.stringify(newVal ?? [])
    if (json === lastRoundsJson) return
    roundsModel.value = (newVal ?? []).map((r) => mergeRound(r))
  },
  { deep: true },
)

// 监听内部 rounds 变化并 emit
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

// 监听外部 bootstrap 变化
watch(
  () => props.bootstrap,
  (newVal) => {
    const json = JSON.stringify(newVal ?? {})
    if (json === lastBootstrapJson) return
    bootstrapModel.value = mergeBootstrap(newVal)
  },
  { deep: true },
)

// 监听内部 bootstrap 变化并 emit
watch(
  bootstrapModel,
  (newVal) => {
    const json = JSON.stringify(newVal)
    if (json === lastBootstrapJson) return
    lastBootstrapJson = json
    emit('update:bootstrap', deepClone(newVal))
  },
  { deep: true },
)

// 监听外部 rubyRetry 变化
watch(
  () => props.rubyRetry,
  (newVal) => {
    const json = JSON.stringify(newVal ?? {})
    if (json === lastRubyRetryJson) return
    rubyRetryModel.value = mergeRubyRetry(newVal)
  },
  { deep: true },
)

// 监听内部 rubyRetry 变化并 emit
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
    <!-- Bootstrap 自举配置 -->
    <NCard size="small" :bordered="true">
      <template #header>
        <span class="text-sm font-semibold">🚀 {{ t('executionPlanEditor.bootstrap.title') }}</span>
      </template>
      <div class="flex items-center justify-between mb-3">
        <span class="text-sm">{{ t('executionPlanEditor.bootstrap.enabled') }}</span>
        <NSwitch v-model:value="bootstrapModel.enabled" size="small" :disabled="disabled" />
      </div>
      <div :class="{ 'opacity-50 pointer-events-none': !bootstrapModel.enabled }">
        <!-- 后端 + 提示词模板选择 -->
        <div class="grid grid-cols-1 gap-3 md:grid-cols-2">
          <div>
            <div class="mb-1 text-xs text-lf-text-subtle">
              {{ t('executionPlanEditor.bootstrap.backend') }}
              <span class="text-red-400">*</span>
            </div>
            <NSelect
              v-model:value="bootstrapModel.backend_id"
              :options="backends"
              size="small"
              :disabled="disabled || !bootstrapModel.enabled"
              :placeholder="t('executionPlanEditor.bootstrap.backendPlaceholder')"
            />
          </div>
          <div>
            <div class="mb-1 text-xs text-lf-text-subtle">
              {{ t('executionPlanEditor.bootstrap.promptTemplate') }}
              <span class="text-red-400">*</span>
            </div>
            <NSelect
              v-model:value="bootstrapModel.prompt_template_id"
              :options="promptTemplates"
              size="small"
              :disabled="disabled || !bootstrapModel.enabled"
              :placeholder="t('executionPlanEditor.bootstrap.promptTemplatePlaceholder')"
            />
          </div>
        </div>
        <!-- 执行参数 -->
        <NGrid :cols="4" :x-gap="12" :y-gap="10" class="mt-3">
          <NGi>
            <div class="mb-1 text-xs text-lf-text-subtle">
              {{ t('executionPlanEditor.bootstrap.batchSize') }}
            </div>
            <NInputNumber
              v-model:value="bootstrapModel.batch_size"
              :min="1"
              :max="10000"
              size="small"
              :disabled="disabled || !bootstrapModel.enabled"
              class="w-full"
            />
          </NGi>
          <NGi>
            <div class="mb-1 text-xs text-lf-text-subtle">
              {{ t('executionPlanEditor.bootstrap.concurrency') }}
            </div>
            <NInputNumber
              v-model:value="bootstrapModel.concurrency"
              :min="1"
              :max="100"
              size="small"
              :disabled="disabled || !bootstrapModel.enabled"
              class="w-full"
            />
          </NGi>
          <NGi>
            <div class="mb-1 text-xs text-lf-text-subtle">
              {{ t('executionPlanEditor.bootstrap.maxTermsPerBatch') }}
            </div>
            <NInputNumber
              v-model:value="bootstrapModel.max_terms_per_batch"
              :min="1"
              :max="1000"
              size="small"
              :disabled="disabled || !bootstrapModel.enabled"
              class="w-full"
            />
          </NGi>
          <NGi>
            <div class="mb-1 text-xs text-lf-text-subtle">
              {{ t('executionPlanEditor.bootstrap.minSourceLen') }}
            </div>
            <NInputNumber
              v-model:value="bootstrapModel.min_source_len"
              :min="1"
              :max="100"
              size="small"
              :disabled="disabled || !bootstrapModel.enabled"
              class="w-full"
            />
          </NGi>
        </NGrid>
      </div>
    </NCard>

    <!-- Ruby Retry 注音对齐重试配置 -->
    <NCard size="small" :bordered="true">
      <template #header>
        <span class="text-sm font-semibold">🔁 {{ t('executionPlanEditor.rubyRetry.title') }}</span>
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
            class="inline-flex h-6 w-6 items-center justify-center rounded-full bg-lf-brand-soft text-xs font-bold text-brand-600"
          >
            {{ index + 1 }}
          </span>
          <span class="text-sm font-semibold">
            {{ round.name || `round-${index + 1}` }}
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

      <!-- 轮次名称 -->
      <div class="mb-1 text-xs text-lf-text-subtle">
        {{ t('executionPlanEditor.round.name') }}
      </div>
      <NInput
        v-model:value="round.name"
        :placeholder="t('executionPlanEditor.round.namePlaceholder', { n: index + 1 })"
        size="small"
        :disabled="disabled"
      />

      <!-- 资源选择 -->
      <div class="mt-3 grid grid-cols-1 gap-3 md:grid-cols-3">
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
            {{ t('executionPlanEditor.round.promptTemplate') }}
            <span class="text-red-400">*</span>
          </div>
          <NSelect
            v-model:value="round.prompt_template_id"
            :options="promptTemplates"
            size="small"
            :disabled="disabled"
            :placeholder="t('executionPlanEditor.round.promptTemplatePlaceholder')"
          />
        </div>
        <div>
          <div class="mb-1 text-xs text-lf-text-subtle">
            {{ t('executionPlanEditor.round.translationProfile') }}
            <span class="text-red-400">*</span>
          </div>
          <NSelect
            v-model:value="round.profile_id"
            :options="translationProfiles"
            size="small"
            :disabled="disabled"
            :placeholder="t('executionPlanEditor.round.profilePlaceholder')"
          />
        </div>
      </div>

      <!-- 执行参数 -->
      <div class="mt-3 grid grid-cols-1 gap-3 md:grid-cols-3">
        <div>
          <div class="mb-1 text-xs text-lf-text-subtle">
            {{ t('executionPlanEditor.round.batchSize') }}
          </div>
          <NInputNumber
            v-model:value="round.batch_size"
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
            v-model:value="round.max_words_per_batch"
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
      <div class="mt-3 grid grid-cols-1 gap-3 md:grid-cols-3">
        <div>
          <div class="mb-1 text-xs text-lf-text-subtle">
            {{ t('executionPlanEditor.round.fallbackShrink') }}
          </div>
          <NInputNumber
            v-model:value="round.fallback_shrink"
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

      <!-- 高级配置（可折叠） -->
      <NCollapse class="mt-3">
        <NCollapseItem :title="t('executionPlanEditor.round.advancedConfig')">
          <NGrid :cols="2" :x-gap="12" :y-gap="10">
            <NGi>
              <div class="mb-1 text-xs text-lf-text-subtle">
                {{ t('executionPlanEditor.round.retryMaxAttempts') }}
              </div>
              <NInputNumber
                v-model:value="round.retry.max_attempts"
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
                v-model:value="round.retry.backoff_ms"
                :min="0"
                :max="60000"
                :step="100"
                size="small"
                :disabled="disabled"
                class="w-full"
              />
            </NGi>
          </NGrid>
          <div class="mt-2 flex items-center gap-2">
            <NSwitch v-model:value="round.retry.jitter" size="small" :disabled="disabled" />
            <span class="text-xs text-lf-text-subtle">
              {{ t('executionPlanEditor.round.retryJitter') }}
            </span>
          </div>
        </NCollapseItem>
      </NCollapse>
    </NCard>

    <!-- 添加轮次按钮 -->
    <NButton dashed block :disabled="disabled" @click="addRound">
      {{ t('executionPlanEditor.actions.addRound') }}
    </NButton>
  </div>
</template>
