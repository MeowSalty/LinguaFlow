<script setup lang="ts">
import {
  NCard,
  NCheckbox,
  NCheckboxGroup,
  NDynamicTags,
  NGrid,
  NGi,
  NInputNumber,
  NSelect,
  NSlider,
  NSwitch,
} from 'naive-ui'
import { useI18n } from 'vue-i18n'

// ─── 类型定义 ────────────────────────────────────────────────

interface TranslationConfigModel {
  // 语言
  source_lang: string
  target_lang: string

  // 分段
  pipeline_split_enabled: boolean
  pipeline_split_strategy: 'paragraph'
  pipeline_split_max_chars: number

  // 保护
  pipeline_protect_enabled: boolean
  pipeline_protect_rules: string[]

  // 翻译核心
  pipeline_translate_concurrency: number
  pipeline_translate_batch_size: number
  pipeline_translate_fallback_shrink: number
  pipeline_translate_rate_limit_per_sec: number
  pipeline_translate_backend_mode: '' | 'prepend' | 'restrict'
  pipeline_translate_backend_order: string[]

  // 重试
  pipeline_translate_retry_max_attempts: number
  pipeline_translate_retry_backoff_seconds: number

  // 修复
  pipeline_translate_repair_enabled: boolean
  pipeline_translate_repair_json_structural: boolean
  pipeline_translate_repair_schema_aliases: boolean
  pipeline_translate_repair_partial: boolean
  pipeline_translate_repair_partial_threshold: number
  pipeline_translate_repair_placeholder_normalize: boolean
  pipeline_translate_repair_prompt_upgrade: boolean

  // 后处理
  pipeline_postprocess_enabled: boolean
  pipeline_postprocess_trim_spaces: boolean

  // 术语表
  glossary_enabled: boolean
  glossary_bootstrap_mode: 'off' | 'pre' | 'inline'
}

// ─── 默认值（与后端 config.Default() 对齐） ─────────────────

const DEFAULTS: TranslationConfigModel = {
  source_lang: '',
  target_lang: '',

  pipeline_split_enabled: true,
  pipeline_split_strategy: 'paragraph',
  pipeline_split_max_chars: 1200,

  pipeline_protect_enabled: true,
  pipeline_protect_rules: ['code', 'link', 'placeholder', 'xml'],

  pipeline_translate_concurrency: 4,
  pipeline_translate_batch_size: 1,
  pipeline_translate_fallback_shrink: 0.5,
  pipeline_translate_rate_limit_per_sec: 5,
  pipeline_translate_backend_mode: '',
  pipeline_translate_backend_order: [],

  pipeline_translate_retry_max_attempts: 3,
  pipeline_translate_retry_backoff_seconds: 1,

  pipeline_translate_repair_enabled: true,
  pipeline_translate_repair_json_structural: true,
  pipeline_translate_repair_schema_aliases: true,
  pipeline_translate_repair_partial: true,
  pipeline_translate_repair_partial_threshold: 0.5,
  pipeline_translate_repair_placeholder_normalize: true,
  pipeline_translate_repair_prompt_upgrade: true,

  pipeline_postprocess_enabled: true,
  pipeline_postprocess_trim_spaces: true,

  glossary_enabled: false,
  glossary_bootstrap_mode: 'off',
}

// ─── 转换函数 ────────────────────────────────────────────────

/** 辅助：从嵌套对象中按路径取值 */
function getNestedValue(obj: Record<string, unknown>, path: string): unknown {
  const parts = path.split('.')
  let current: unknown = obj
  for (const part of parts) {
    if (current == null || typeof current !== 'object') return undefined
    current = (current as Record<string, unknown>)[part]
  }
  return current
}

/** 从 API Record 反序列化为 model（处理便捷键归一化） */
function recordToModel(record: Record<string, unknown> | undefined): TranslationConfigModel {
  if (!record) return { ...DEFAULTS }

  const m = { ...DEFAULTS }

  // 语言
  if (typeof record.source_lang === 'string') m.source_lang = record.source_lang
  if (typeof record.target_lang === 'string') m.target_lang = record.target_lang

  // 辅助：按嵌套路径读取，兼容顶层便捷键
  const read = (nestedPath: string, ...altKeys: string[]): unknown => {
    const val = getNestedValue(record, nestedPath)
    if (val !== undefined) return val
    for (const key of altKeys) {
      if (record[key] !== undefined) return record[key]
    }
    return undefined
  }

  // 分段
  const splitEnabled = read('pipeline.split.enabled')
  if (typeof splitEnabled === 'boolean') m.pipeline_split_enabled = splitEnabled
  const splitStrategy = read('pipeline.split.strategy')
  if (splitStrategy === 'paragraph') m.pipeline_split_strategy = splitStrategy
  const splitMaxChars = read('pipeline.split.max_chars')
  if (typeof splitMaxChars === 'number') m.pipeline_split_max_chars = splitMaxChars

  // 保护
  const protectEnabled = read('pipeline.protect.enabled')
  if (typeof protectEnabled === 'boolean') m.pipeline_protect_enabled = protectEnabled
  const protectRules = read('pipeline.protect.rules')
  if (Array.isArray(protectRules)) m.pipeline_protect_rules = protectRules as string[]

  // 翻译核心（支持顶层便捷键）
  const concurrency = read('pipeline.translate.concurrency', 'concurrency')
  if (typeof concurrency === 'number') m.pipeline_translate_concurrency = concurrency
  const batchSize = read('pipeline.translate.batch_size', 'batch_size')
  if (typeof batchSize === 'number') m.pipeline_translate_batch_size = batchSize
  const fallbackShrink = read('pipeline.translate.fallback_shrink', 'fallback_shrink')
  if (typeof fallbackShrink === 'number') m.pipeline_translate_fallback_shrink = fallbackShrink
  const rateLimit = read('pipeline.translate.rate_limit_per_sec', 'rate_limit_per_sec')
  if (typeof rateLimit === 'number') m.pipeline_translate_rate_limit_per_sec = rateLimit
  const backendMode = read('pipeline.translate.backend_mode', 'backend_mode')
  if (backendMode === 'prepend' || backendMode === 'restrict' || backendMode === '') {
    m.pipeline_translate_backend_mode = backendMode as '' | 'prepend' | 'restrict'
  }
  const backendOrder = read('pipeline.translate.backend_order', 'backend_order')
  if (Array.isArray(backendOrder)) m.pipeline_translate_backend_order = backendOrder as string[]

  // 重试
  const maxAttempts = read('pipeline.translate.retry.max_attempts', 'retry.max_attempts')
  if (typeof maxAttempts === 'number') m.pipeline_translate_retry_max_attempts = maxAttempts
  const backoff = read('pipeline.translate.retry.backoff', 'retry.backoff')
  if (typeof backoff === 'number') m.pipeline_translate_retry_backoff_seconds = backoff

  // 修复
  const repairEnabled = read('pipeline.translate.repair.enabled', 'repair.enabled')
  if (typeof repairEnabled === 'boolean') m.pipeline_translate_repair_enabled = repairEnabled
  const jsonStructural = read('pipeline.translate.repair.json_structural', 'repair.json_structural')
  if (typeof jsonStructural === 'boolean') m.pipeline_translate_repair_json_structural = jsonStructural
  const schemaAliases = read('pipeline.translate.repair.schema_aliases', 'repair.schema_aliases')
  if (typeof schemaAliases === 'boolean') m.pipeline_translate_repair_schema_aliases = schemaAliases
  const partial = read('pipeline.translate.repair.partial', 'repair.partial')
  if (typeof partial === 'boolean') m.pipeline_translate_repair_partial = partial
  const partialThreshold = read('pipeline.translate.repair.partial_threshold', 'repair.partial_threshold')
  if (typeof partialThreshold === 'number') m.pipeline_translate_repair_partial_threshold = partialThreshold
  const placeholderNormalize = read('pipeline.translate.repair.placeholder_normalize', 'repair.placeholder_normalize')
  if (typeof placeholderNormalize === 'boolean') m.pipeline_translate_repair_placeholder_normalize = placeholderNormalize
  const promptUpgrade = read('pipeline.translate.repair.prompt_upgrade', 'repair.prompt_upgrade')
  if (typeof promptUpgrade === 'boolean') m.pipeline_translate_repair_prompt_upgrade = promptUpgrade

  // 后处理
  const postEnabled = read('pipeline.postprocess.enabled', 'postprocess.enabled')
  if (typeof postEnabled === 'boolean') m.pipeline_postprocess_enabled = postEnabled
  const trimSpaces = read('pipeline.postprocess.trim_spaces', 'postprocess.trim_spaces')
  if (typeof trimSpaces === 'boolean') m.pipeline_postprocess_trim_spaces = trimSpaces

  // 术语表
  const glossaryEnabled = read('glossary.enabled')
  if (typeof glossaryEnabled === 'boolean') m.glossary_enabled = glossaryEnabled
  const bootstrapMode = read('glossary.bootstrap.mode', 'glossary.bootstrap_mode')
  if (bootstrapMode === 'off' || bootstrapMode === 'pre' || bootstrapMode === 'inline') {
    m.glossary_bootstrap_mode = bootstrapMode as 'off' | 'pre' | 'inline'
  }

  return m
}

/** 辅助：仅在值与默认值不同时设置到嵌套对象 */
function setIfChanged(
  target: Record<string, unknown>,
  path: string,
  value: unknown,
  defaultValue: unknown,
): void {
  if (JSON.stringify(value) === JSON.stringify(defaultValue)) return
  const parts = path.split('.')
  let current: Record<string, unknown> = target
  for (let i = 0; i < parts.length - 1; i++) {
    const key = parts[i]!
    if (!(key in current) || typeof current[key] !== 'object' || current[key] === null) {
      current[key] = {}
    }
    current = current[key] as Record<string, unknown>
  }
  current[parts[parts.length - 1]!] = value
}

/** 将 model 序列化为后端期望的嵌套格式，仅输出非默认值 */
function modelToRecord(model: TranslationConfigModel): Record<string, unknown> {
  const record: Record<string, unknown> = {}

  // 语言
  if (model.source_lang) record.source_lang = model.source_lang
  if (model.target_lang) record.target_lang = model.target_lang

  // 分段
  setIfChanged(record, 'pipeline.split.enabled', model.pipeline_split_enabled, DEFAULTS.pipeline_split_enabled)
  setIfChanged(record, 'pipeline.split.strategy', model.pipeline_split_strategy, DEFAULTS.pipeline_split_strategy)
  setIfChanged(record, 'pipeline.split.max_chars', model.pipeline_split_max_chars, DEFAULTS.pipeline_split_max_chars)

  // 保护
  setIfChanged(record, 'pipeline.protect.enabled', model.pipeline_protect_enabled, DEFAULTS.pipeline_protect_enabled)
  setIfChanged(record, 'pipeline.protect.rules', model.pipeline_protect_rules, DEFAULTS.pipeline_protect_rules)

  // 翻译核心
  setIfChanged(record, 'pipeline.translate.concurrency', model.pipeline_translate_concurrency, DEFAULTS.pipeline_translate_concurrency)
  setIfChanged(record, 'pipeline.translate.batch_size', model.pipeline_translate_batch_size, DEFAULTS.pipeline_translate_batch_size)
  setIfChanged(record, 'pipeline.translate.fallback_shrink', model.pipeline_translate_fallback_shrink, DEFAULTS.pipeline_translate_fallback_shrink)
  setIfChanged(record, 'pipeline.translate.rate_limit_per_sec', model.pipeline_translate_rate_limit_per_sec, DEFAULTS.pipeline_translate_rate_limit_per_sec)
  if (model.pipeline_translate_backend_mode) {
    setIfChanged(record, 'pipeline.translate.backend_mode', model.pipeline_translate_backend_mode, DEFAULTS.pipeline_translate_backend_mode)
  }
  if (model.pipeline_translate_backend_order.length > 0) {
    setIfChanged(record, 'pipeline.translate.backend_order', model.pipeline_translate_backend_order, DEFAULTS.pipeline_translate_backend_order)
  }

  // 重试
  setIfChanged(record, 'pipeline.translate.retry.max_attempts', model.pipeline_translate_retry_max_attempts, DEFAULTS.pipeline_translate_retry_max_attempts)
  setIfChanged(record, 'pipeline.translate.retry.backoff', model.pipeline_translate_retry_backoff_seconds, DEFAULTS.pipeline_translate_retry_backoff_seconds)

  // 修复
  setIfChanged(record, 'pipeline.translate.repair.enabled', model.pipeline_translate_repair_enabled, DEFAULTS.pipeline_translate_repair_enabled)
  setIfChanged(record, 'pipeline.translate.repair.json_structural', model.pipeline_translate_repair_json_structural, DEFAULTS.pipeline_translate_repair_json_structural)
  setIfChanged(record, 'pipeline.translate.repair.schema_aliases', model.pipeline_translate_repair_schema_aliases, DEFAULTS.pipeline_translate_repair_schema_aliases)
  setIfChanged(record, 'pipeline.translate.repair.partial', model.pipeline_translate_repair_partial, DEFAULTS.pipeline_translate_repair_partial)
  setIfChanged(record, 'pipeline.translate.repair.partial_threshold', model.pipeline_translate_repair_partial_threshold, DEFAULTS.pipeline_translate_repair_partial_threshold)
  setIfChanged(record, 'pipeline.translate.repair.placeholder_normalize', model.pipeline_translate_repair_placeholder_normalize, DEFAULTS.pipeline_translate_repair_placeholder_normalize)
  setIfChanged(record, 'pipeline.translate.repair.prompt_upgrade', model.pipeline_translate_repair_prompt_upgrade, DEFAULTS.pipeline_translate_repair_prompt_upgrade)

  // 后处理
  setIfChanged(record, 'pipeline.postprocess.enabled', model.pipeline_postprocess_enabled, DEFAULTS.pipeline_postprocess_enabled)
  setIfChanged(record, 'pipeline.postprocess.trim_spaces', model.pipeline_postprocess_trim_spaces, DEFAULTS.pipeline_postprocess_trim_spaces)

  // 术语表
  setIfChanged(record, 'glossary.enabled', model.glossary_enabled, DEFAULTS.glossary_enabled)
  setIfChanged(record, 'glossary.bootstrap_mode', model.glossary_bootstrap_mode, DEFAULTS.glossary_bootstrap_mode)

  return record
}

// ─── Props & Emits ──────────────────────────────────────────

const props = withDefaults(
  defineProps<{
    modelValue: Record<string, unknown>
    disabled?: boolean
  }>(),
  { disabled: false },
)

const emit = defineEmits<{
  'update:modelValue': [value: Record<string, unknown>]
}>()

// ─── 内部 model ─────────────────────────────────────────────

const { t } = useI18n()

const model = ref<TranslationConfigModel>(recordToModel(props.modelValue))

// 上次 emit 的序列化结果（用于去重，防止循环）
let lastEmittedJson = JSON.stringify(props.modelValue ?? {})

// 监听外部值变化（仅当外部值与上次 emit 的不同时才同步）
watch(
  () => props.modelValue,
  (newVal) => {
    const json = JSON.stringify(newVal ?? {})
    if (json === lastEmittedJson) return
    model.value = recordToModel(newVal)
  },
  { deep: true },
)

// 监听内部 model 变化，序列化后 emit（跳过与当前 props 值等价的变更）
watch(
  model,
  (newVal) => {
    const record = modelToRecord(newVal)
    const json = JSON.stringify(record)
    if (json === lastEmittedJson) return
    lastEmittedJson = json
    emit('update:modelValue', record)
  },
  { deep: true },
)

// ─── 选项常量 ────────────────────────────────────────────────

const protectRuleOptions = computed(() => [
  { label: 'code', value: 'code' },
  { label: 'link', value: 'link' },
  { label: 'placeholder', value: 'placeholder' },
  { label: 'xml', value: 'xml' },
])

const backendModeOptions = computed(() => [
  { label: t('templates.configEditor.translate.backendModeNone'), value: '' },
  { label: 'prepend', value: 'prepend' },
  { label: 'restrict', value: 'restrict' },
])

const bootstrapModeOptions = computed(() => [
  { label: 'off', value: 'off' },
  { label: 'pre', value: 'pre' },
  { label: 'inline', value: 'inline' },
])

const splitStrategyOptions = computed(() => [
  { label: 'paragraph', value: 'paragraph' },
])
</script>

<template>
  <div class="flex flex-col gap-4">
    <!-- 翻译核心 -->
    <NCard size="small" :bordered="true">
      <template #header>
        <span class="text-sm font-semibold">⚙ {{ t('templates.configEditor.translate.title') }}</span>
      </template>
      <NGrid :cols="2" :x-gap="12" :y-gap="10">
        <NGi>
          <div class="mb-1 text-xs text-lf-text-subtle">{{ t('templates.configEditor.translate.concurrency') }}</div>
          <div class="flex items-center gap-2">
            <NSlider
              v-model:value="model.pipeline_translate_concurrency"
              :min="1"
              :max="16"
              :step="1"
              :disabled="disabled"
              class="flex-1"
            />
            <NInputNumber
              v-model:value="model.pipeline_translate_concurrency"
              :min="1"
              :max="16"
              :step="1"
              size="small"
              :disabled="disabled"
              class="w-20"
            />
          </div>
        </NGi>
        <NGi>
          <div class="mb-1 text-xs text-lf-text-subtle">{{ t('templates.configEditor.translate.batchSize') }}</div>
          <NInputNumber
            v-model:value="model.pipeline_translate_batch_size"
            :min="1"
            :max="50"
            :step="1"
            size="small"
            :disabled="disabled"
            class="w-full"
          />
        </NGi>
        <NGi>
          <div class="mb-1 text-xs text-lf-text-subtle">{{ t('templates.configEditor.translate.fallbackShrink') }}</div>
          <NInputNumber
            v-model:value="model.pipeline_translate_fallback_shrink"
            :min="0"
            :max="1"
            :step="0.1"
            size="small"
            :disabled="disabled"
            class="w-full"
          />
        </NGi>
        <NGi>
          <div class="mb-1 text-xs text-lf-text-subtle">{{ t('templates.configEditor.translate.rateLimit') }}</div>
          <NInputNumber
            v-model:value="model.pipeline_translate_rate_limit_per_sec"
            :min="0"
            :step="1"
            size="small"
            :disabled="disabled"
            class="w-full"
          />
        </NGi>
        <NGi>
          <div class="mb-1 text-xs text-lf-text-subtle">{{ t('templates.configEditor.translate.backendMode') }}</div>
          <NSelect
            v-model:value="model.pipeline_translate_backend_mode"
            :options="backendModeOptions"
            size="small"
            :disabled="disabled"
          />
        </NGi>
        <NGi>
          <div class="mb-1 text-xs text-lf-text-subtle">{{ t('templates.configEditor.translate.backendOrder') }}</div>
          <NDynamicTags
            v-model:value="model.pipeline_translate_backend_order"
            size="small"
            :disabled="disabled"
          />
        </NGi>
      </NGrid>
    </NCard>

    <!-- 重试与修复 -->
    <NCard size="small" :bordered="true">
      <template #header>
        <span class="text-sm font-semibold">🔄 {{ t('templates.configEditor.retry.title') }}</span>
      </template>
      <NGrid :cols="2" :x-gap="12" :y-gap="10">
        <NGi>
          <div class="mb-1 text-xs text-lf-text-subtle">{{ t('templates.configEditor.retry.maxAttempts') }}</div>
          <NInputNumber
            v-model:value="model.pipeline_translate_retry_max_attempts"
            :min="0"
            :max="10"
            :step="1"
            size="small"
            :disabled="disabled"
            class="w-full"
          />
        </NGi>
        <NGi>
          <div class="mb-1 text-xs text-lf-text-subtle">{{ t('templates.configEditor.retry.backoff') }}</div>
          <NInputNumber
            v-model:value="model.pipeline_translate_retry_backoff_seconds"
            :min="0"
            :max="60"
            :step="1"
            size="small"
            :disabled="disabled"
            class="w-full"
          />
        </NGi>
      </NGrid>

      <div class="mt-4 flex flex-col gap-3">
        <div class="flex items-center justify-between">
          <span class="text-sm">{{ t('templates.configEditor.repair.enabled') }}</span>
          <NSwitch
            v-model:value="model.pipeline_translate_repair_enabled"
            size="small"
            :disabled="disabled"
          />
        </div>
        <div
          class="ml-4 flex flex-col gap-2"
          :class="{ 'opacity-50 pointer-events-none': !model.pipeline_translate_repair_enabled }"
        >
          <div class="flex items-center justify-between">
            <span class="text-xs text-lf-text-subtle">{{ t('templates.configEditor.repair.jsonStructural') }}</span>
            <NSwitch
              v-model:value="model.pipeline_translate_repair_json_structural"
              size="small"
              :disabled="disabled || !model.pipeline_translate_repair_enabled"
            />
          </div>
          <div class="flex items-center justify-between">
            <span class="text-xs text-lf-text-subtle">{{ t('templates.configEditor.repair.schemaAliases') }}</span>
            <NSwitch
              v-model:value="model.pipeline_translate_repair_schema_aliases"
              size="small"
              :disabled="disabled || !model.pipeline_translate_repair_enabled"
            />
          </div>
          <div class="flex items-center justify-between">
            <span class="text-xs text-lf-text-subtle">{{ t('templates.configEditor.repair.partial') }}</span>
            <NSwitch
              v-model:value="model.pipeline_translate_repair_partial"
              size="small"
              :disabled="disabled || !model.pipeline_translate_repair_enabled"
            />
          </div>
          <div
            class="ml-4 flex items-center gap-2"
            :class="{ 'opacity-50 pointer-events-none': !model.pipeline_translate_repair_partial }"
          >
            <span class="text-xs text-lf-text-subtle">{{ t('templates.configEditor.repair.partialThreshold') }}</span>
            <NInputNumber
              v-model:value="model.pipeline_translate_repair_partial_threshold"
              :min="0"
              :max="1"
              :step="0.1"
              size="tiny"
              :disabled="disabled || !model.pipeline_translate_repair_enabled || !model.pipeline_translate_repair_partial"
              class="w-24"
            />
          </div>
          <div class="flex items-center justify-between">
            <span class="text-xs text-lf-text-subtle">{{ t('templates.configEditor.repair.placeholderNormalize') }}</span>
            <NSwitch
              v-model:value="model.pipeline_translate_repair_placeholder_normalize"
              size="small"
              :disabled="disabled || !model.pipeline_translate_repair_enabled"
            />
          </div>
          <div class="flex items-center justify-between">
            <span class="text-xs text-lf-text-subtle">{{ t('templates.configEditor.repair.promptUpgrade') }}</span>
            <NSwitch
              v-model:value="model.pipeline_translate_repair_prompt_upgrade"
              size="small"
              :disabled="disabled || !model.pipeline_translate_repair_enabled"
            />
          </div>
        </div>
      </div>
    </NCard>

    <!-- 分段策略 -->
    <NCard size="small" :bordered="true">
      <template #header>
        <span class="text-sm font-semibold">📐 {{ t('templates.configEditor.split.title') }}</span>
      </template>
      <NGrid :cols="3" :x-gap="12" :y-gap="10">
        <NGi>
          <div class="mb-1 text-xs text-lf-text-subtle">{{ t('templates.configEditor.split.enabled') }}</div>
          <NSwitch
            v-model:value="model.pipeline_split_enabled"
            size="small"
            :disabled="disabled"
          />
        </NGi>
        <NGi>
          <div class="mb-1 text-xs text-lf-text-subtle">{{ t('templates.configEditor.split.strategy') }}</div>
          <NSelect
            v-model:value="model.pipeline_split_strategy"
            :options="splitStrategyOptions"
            size="small"
            :disabled="disabled || !model.pipeline_split_enabled"
          />
        </NGi>
        <NGi>
          <div class="mb-1 text-xs text-lf-text-subtle">{{ t('templates.configEditor.split.maxChars') }}</div>
          <NInputNumber
            v-model:value="model.pipeline_split_max_chars"
            :min="100"
            :max="10000"
            :step="100"
            size="small"
            :disabled="disabled || !model.pipeline_split_enabled"
            class="w-full"
          />
        </NGi>
      </NGrid>
    </NCard>

    <!-- 内容保护 -->
    <NCard size="small" :bordered="true">
      <template #header>
        <span class="text-sm font-semibold">🛡 {{ t('templates.configEditor.protect.title') }}</span>
      </template>
      <div class="flex items-center justify-between mb-3">
        <span class="text-sm">{{ t('templates.configEditor.protect.enabled') }}</span>
        <NSwitch
          v-model:value="model.pipeline_protect_enabled"
          size="small"
          :disabled="disabled"
        />
      </div>
      <div :class="{ 'opacity-50 pointer-events-none': !model.pipeline_protect_enabled }">
        <div class="mb-1 text-xs text-lf-text-subtle">{{ t('templates.configEditor.protect.rules') }}</div>
        <NCheckboxGroup v-model:value="model.pipeline_protect_rules" :disabled="disabled || !model.pipeline_protect_enabled">
          <div class="flex flex-wrap gap-3">
            <NCheckbox
              v-for="opt in protectRuleOptions"
              :key="opt.value"
              :value="opt.value"
              :label="opt.label"
            />
          </div>
        </NCheckboxGroup>
      </div>
    </NCard>

    <!-- 后处理 -->
    <NCard size="small" :bordered="true">
      <template #header>
        <span class="text-sm font-semibold">✨ {{ t('templates.configEditor.postprocess.title') }}</span>
      </template>
      <div class="flex flex-col gap-3">
        <div class="flex items-center justify-between">
          <span class="text-sm">{{ t('templates.configEditor.postprocess.enabled') }}</span>
          <NSwitch
            v-model:value="model.pipeline_postprocess_enabled"
            size="small"
            :disabled="disabled"
          />
        </div>
        <div class="flex items-center justify-between" :class="{ 'opacity-50 pointer-events-none': !model.pipeline_postprocess_enabled }">
          <span class="text-xs text-lf-text-subtle">{{ t('templates.configEditor.postprocess.trimSpaces') }}</span>
          <NSwitch
            v-model:value="model.pipeline_postprocess_trim_spaces"
            size="small"
            :disabled="disabled || !model.pipeline_postprocess_enabled"
          />
        </div>
      </div>
    </NCard>

    <!-- 术语表 -->
    <NCard size="small" :bordered="true">
      <template #header>
        <span class="text-sm font-semibold">📚 {{ t('templates.configEditor.glossary.title') }}</span>
      </template>
      <div class="flex flex-col gap-3">
        <div class="flex items-center justify-between">
          <span class="text-sm">{{ t('templates.configEditor.glossary.enabled') }}</span>
          <NSwitch
            v-model:value="model.glossary_enabled"
            size="small"
            :disabled="disabled"
          />
        </div>
        <div>
          <div class="mb-1 text-xs text-lf-text-subtle">{{ t('templates.configEditor.glossary.bootstrapMode') }}</div>
          <NSelect
            v-model:value="model.glossary_bootstrap_mode"
            :options="bootstrapModeOptions"
            size="small"
            :disabled="disabled"
          />
        </div>
      </div>
    </NCard>

  </div>
</template>
