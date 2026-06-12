<script setup lang="ts">
import {
  NCard,
  NCheckbox,
  NCheckboxGroup,
  NGrid,
  NGi,
  NInputNumber,
  NSelect,
  NSwitch,
} from 'naive-ui'
import { useI18n } from 'vue-i18n'

import type { ApiSchemas } from '@/api/client'

type TemplatePipelineConfig = ApiSchemas['TemplatePipelineConfig']
type TemplateGlossaryConfig = ApiSchemas['TemplateGlossaryConfig']

// ─── 默认值（与后端 config.Default() 对齐） ─────────────────

const PIPELINE_DEFAULTS: TemplatePipelineConfig = {
  split: { enabled: true, strategy: 'paragraph', max_chars: 1200 },
  protect: { enabled: true, rules: ['code', 'link', 'placeholder', 'xml'] },
  retry: { max_attempts: 3, backoff_ms: 1000, jitter: false },
  repair: {
    enabled: true,
    json_structural: true,
    schema_aliases: true,
    partial: true,
    partial_threshold: 0.5,
    placeholder_normalize: true,
    prompt_upgrade: true,
  },
  postprocess: { enabled: true, trim_spaces: true },
}

const GLOSSARY_DEFAULTS: TemplateGlossaryConfig = {
  enabled: false,
  bootstrap: {
    mode: 'off',
    save: false,
    max_terms_per_batch: 20,
    min_source_len: 2,
    inline_conflict_strategy: 'off',
  },
}

// ─── 工具函数 ────────────────────────────────────────────────

function deepClone<T>(obj: T): T {
  return JSON.parse(JSON.stringify(obj))
}

function mergePipeline(source?: Partial<TemplatePipelineConfig>): TemplatePipelineConfig {
  if (!source) return deepClone(PIPELINE_DEFAULTS)
  return {
    split: { ...PIPELINE_DEFAULTS.split, ...source.split },
    protect: {
      ...PIPELINE_DEFAULTS.protect,
      ...source.protect,
      rules: source.protect?.rules ?? PIPELINE_DEFAULTS.protect.rules,
    },
    retry: { ...PIPELINE_DEFAULTS.retry, ...source.retry },
    repair: { ...PIPELINE_DEFAULTS.repair, ...source.repair },
    postprocess: { ...PIPELINE_DEFAULTS.postprocess, ...source.postprocess },
  }
}

function mergeGlossary(source?: Partial<TemplateGlossaryConfig>): TemplateGlossaryConfig {
  if (!source) return deepClone(GLOSSARY_DEFAULTS)
  return {
    enabled: source.enabled ?? GLOSSARY_DEFAULTS.enabled,
    bootstrap: { ...GLOSSARY_DEFAULTS.bootstrap, ...source.bootstrap },
  }
}

// ─── Props & Emits ──────────────────────────────────────────

const props = withDefaults(
  defineProps<{
    pipeline: TemplatePipelineConfig
    glossary: TemplateGlossaryConfig
    disabled?: boolean
  }>(),
  { disabled: false },
)

const emit = defineEmits<{
  'update:pipeline': [value: TemplatePipelineConfig]
  'update:glossary': [value: TemplateGlossaryConfig]
}>()

// ─── 内部 model ─────────────────────────────────────────────

const { t } = useI18n()

const pipelineModel = ref<TemplatePipelineConfig>(mergePipeline(props.pipeline))
const glossaryModel = ref<TemplateGlossaryConfig>(mergeGlossary(props.glossary))

// 上次 emit 的 JSON（用于去重）
let lastPipelineJson = JSON.stringify(props.pipeline ?? {})
let lastGlossaryJson = JSON.stringify(props.glossary ?? {})

// 监听外部 pipeline 变化
watch(
  () => props.pipeline,
  (newVal) => {
    const json = JSON.stringify(newVal ?? {})
    if (json === lastPipelineJson) return
    pipelineModel.value = mergePipeline(newVal)
  },
  { deep: true },
)

// 监听外部 glossary 变化
watch(
  () => props.glossary,
  (newVal) => {
    const json = JSON.stringify(newVal ?? {})
    if (json === lastGlossaryJson) return
    glossaryModel.value = mergeGlossary(newVal)
  },
  { deep: true },
)

// 监听内部 pipeline 变化并 emit
watch(
  pipelineModel,
  (newVal) => {
    const json = JSON.stringify(newVal)
    if (json === lastPipelineJson) return
    lastPipelineJson = json
    emit('update:pipeline', deepClone(newVal))
  },
  { deep: true },
)

// 监听内部 glossary 变化并 emit
watch(
  glossaryModel,
  (newVal) => {
    const json = JSON.stringify(newVal)
    if (json === lastGlossaryJson) return
    lastGlossaryJson = json
    emit('update:glossary', deepClone(newVal))
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

const bootstrapModeOptions = computed(() => [
  { label: 'off', value: 'off' },
  { label: 'pre', value: 'pre' },
  { label: 'inline', value: 'inline' },
])

const inlineConflictStrategyOptions = computed(() => [
  { label: 'off', value: 'off' },
  { label: 'rewrite-local', value: 'rewrite-local' },
])

const splitStrategyOptions = computed(() => [
  { label: 'paragraph', value: 'paragraph' },
])
</script>

<template>
  <div class="flex flex-col gap-4">
    <!-- 重试 -->
    <NCard size="small" :bordered="true">
      <template #header>
        <span class="text-sm font-semibold">🔄 {{ t('templates.configEditor.retry.title') }}</span>
      </template>
      <NGrid :cols="3" :x-gap="12" :y-gap="10">
        <NGi>
          <div class="mb-1 text-xs text-lf-text-subtle">{{ t('templates.configEditor.retry.maxAttempts') }}</div>
          <NInputNumber
            v-model:value="pipelineModel.retry.max_attempts"
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
            v-model:value="pipelineModel.retry.backoff_ms"
            :min="0"
            :max="60000"
            :step="100"
            size="small"
            :disabled="disabled"
            class="w-full"
          />
        </NGi>
        <NGi>
          <div class="mb-1 text-xs text-lf-text-subtle">{{ t('templates.configEditor.retry.jitter') }}</div>
          <NSwitch
            v-model:value="pipelineModel.retry.jitter"
            size="small"
            :disabled="disabled"
          />
        </NGi>
      </NGrid>

      <!-- 修复 -->
      <div class="mt-4 flex flex-col gap-3">
        <div class="flex items-center justify-between">
          <span class="text-sm">{{ t('templates.configEditor.repair.enabled') }}</span>
          <NSwitch
            v-model:value="pipelineModel.repair.enabled"
            size="small"
            :disabled="disabled"
          />
        </div>
        <div
          class="ml-4 flex flex-col gap-2"
          :class="{ 'opacity-50 pointer-events-none': !pipelineModel.repair.enabled }"
        >
          <div class="flex items-center justify-between">
            <span class="text-xs text-lf-text-subtle">{{ t('templates.configEditor.repair.jsonStructural') }}</span>
            <NSwitch
              v-model:value="pipelineModel.repair.json_structural"
              size="small"
              :disabled="disabled || !pipelineModel.repair.enabled"
            />
          </div>
          <div class="flex items-center justify-between">
            <span class="text-xs text-lf-text-subtle">{{ t('templates.configEditor.repair.schemaAliases') }}</span>
            <NSwitch
              v-model:value="pipelineModel.repair.schema_aliases"
              size="small"
              :disabled="disabled || !pipelineModel.repair.enabled"
            />
          </div>
          <div class="flex items-center justify-between">
            <span class="text-xs text-lf-text-subtle">{{ t('templates.configEditor.repair.partial') }}</span>
            <NSwitch
              v-model:value="pipelineModel.repair.partial"
              size="small"
              :disabled="disabled || !pipelineModel.repair.enabled"
            />
          </div>
          <div
            class="ml-4 flex items-center gap-2"
            :class="{ 'opacity-50 pointer-events-none': !pipelineModel.repair.partial }"
          >
            <span class="text-xs text-lf-text-subtle">{{ t('templates.configEditor.repair.partialThreshold') }}</span>
            <NInputNumber
              v-model:value="pipelineModel.repair.partial_threshold"
              :min="0"
              :max="1"
              :step="0.1"
              size="tiny"
              :disabled="disabled || !pipelineModel.repair.enabled || !pipelineModel.repair.partial"
              class="w-24"
            />
          </div>
          <div class="flex items-center justify-between">
            <span class="text-xs text-lf-text-subtle">{{ t('templates.configEditor.repair.placeholderNormalize') }}</span>
            <NSwitch
              v-model:value="pipelineModel.repair.placeholder_normalize"
              size="small"
              :disabled="disabled || !pipelineModel.repair.enabled"
            />
          </div>
          <div class="flex items-center justify-between">
            <span class="text-xs text-lf-text-subtle">{{ t('templates.configEditor.repair.promptUpgrade') }}</span>
            <NSwitch
              v-model:value="pipelineModel.repair.prompt_upgrade"
              size="small"
              :disabled="disabled || !pipelineModel.repair.enabled"
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
            v-model:value="pipelineModel.split.enabled"
            size="small"
            :disabled="disabled"
          />
        </NGi>
        <NGi>
          <div class="mb-1 text-xs text-lf-text-subtle">{{ t('templates.configEditor.split.strategy') }}</div>
          <NSelect
            v-model:value="pipelineModel.split.strategy"
            :options="splitStrategyOptions"
            size="small"
            :disabled="disabled || !pipelineModel.split.enabled"
          />
        </NGi>
        <NGi>
          <div class="mb-1 text-xs text-lf-text-subtle">{{ t('templates.configEditor.split.maxChars') }}</div>
          <NInputNumber
            v-model:value="pipelineModel.split.max_chars"
            :min="100"
            :max="10000"
            :step="100"
            size="small"
            :disabled="disabled || !pipelineModel.split.enabled"
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
          v-model:value="pipelineModel.protect.enabled"
          size="small"
          :disabled="disabled"
        />
      </div>
      <div :class="{ 'opacity-50 pointer-events-none': !pipelineModel.protect.enabled }">
        <div class="mb-1 text-xs text-lf-text-subtle">{{ t('templates.configEditor.protect.rules') }}</div>
        <NCheckboxGroup v-model:value="pipelineModel.protect.rules" :disabled="disabled || !pipelineModel.protect.enabled">
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
            v-model:value="pipelineModel.postprocess.enabled"
            size="small"
            :disabled="disabled"
          />
        </div>
        <div class="flex items-center justify-between" :class="{ 'opacity-50 pointer-events-none': !pipelineModel.postprocess.enabled }">
          <span class="text-xs text-lf-text-subtle">{{ t('templates.configEditor.postprocess.trimSpaces') }}</span>
          <NSwitch
            v-model:value="pipelineModel.postprocess.trim_spaces"
            size="small"
            :disabled="disabled || !pipelineModel.postprocess.enabled"
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
            v-model:value="glossaryModel.enabled"
            size="small"
            :disabled="disabled"
          />
        </div>
        <div :class="{ 'opacity-50 pointer-events-none': !glossaryModel.enabled }">
          <div class="mb-1 text-xs text-lf-text-subtle">{{ t('templates.configEditor.glossary.bootstrapMode') }}</div>
          <NSelect
            v-model:value="glossaryModel.bootstrap.mode"
            :options="bootstrapModeOptions"
            size="small"
            :disabled="disabled || !glossaryModel.enabled"
          />
        </div>
        <div
          v-if="glossaryModel.bootstrap.mode !== 'off'"
          class="flex flex-col gap-2 ml-4"
        >
          <div class="flex items-center justify-between">
            <span class="text-xs text-lf-text-subtle">{{ t('templates.configEditor.glossary.bootstrapSave') }}</span>
            <NSwitch
              v-model:value="glossaryModel.bootstrap.save"
              size="small"
              :disabled="disabled || !glossaryModel.enabled"
            />
          </div>
          <div class="flex items-center gap-2">
            <span class="text-xs text-lf-text-subtle">{{ t('templates.configEditor.glossary.bootstrapMaxTerms') }}</span>
            <NInputNumber
              v-model:value="glossaryModel.bootstrap.max_terms_per_batch"
              :min="1"
              :max="100"
              :step="1"
              size="tiny"
              :disabled="disabled || !glossaryModel.enabled"
              class="w-24"
            />
          </div>
          <div class="flex items-center gap-2">
            <span class="text-xs text-lf-text-subtle">{{ t('templates.configEditor.glossary.bootstrapMinSourceLen') }}</span>
            <NInputNumber
              v-model:value="glossaryModel.bootstrap.min_source_len"
              :min="1"
              :max="100"
              :step="1"
              size="tiny"
              :disabled="disabled || !glossaryModel.enabled"
              class="w-24"
            />
          </div>
          <div>
            <div class="mb-1 text-xs text-lf-text-subtle">{{ t('templates.configEditor.glossary.bootstrapConflictStrategy') }}</div>
            <NSelect
              v-model:value="glossaryModel.bootstrap.inline_conflict_strategy"
              :options="inlineConflictStrategyOptions"
              size="small"
              :disabled="disabled || !glossaryModel.enabled"
            />
          </div>
        </div>
      </div>
    </NCard>
  </div>
</template>
