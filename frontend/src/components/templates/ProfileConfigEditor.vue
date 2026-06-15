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

type TranslationProfileConfig = ApiSchemas['TranslationProfileConfig']

// ─── 默认值（与后端 config.Default() 对齐） ─────────────────

const CONFIG_DEFAULTS: TranslationProfileConfig = {
  split: { enabled: true, strategy: 'paragraph', max_chars: 1200 },
  protect: { enabled: true, rules: ['code', 'link', 'placeholder', 'xml'] },
  postprocess: { enabled: true, trim_spaces: true },
  repair: {
    enabled: true,
    json_structural: true,
    schema_aliases: true,
    partial: true,
    partial_threshold: 0.5,
    placeholder_normalize: true,
    prompt_upgrade: true,
  },
  glossary: {
    enabled: false,
    bootstrap: {
      mode: 'off',
      save: false,
      max_terms_per_batch: 20,
      min_source_len: 2,
      inline_conflict_strategy: 'off',
    },
  },
}

// ─── 工具函数 ────────────────────────────────────────────────

function deepClone<T>(obj: T): T {
  return JSON.parse(JSON.stringify(obj))
}

function mergeConfig(source?: Partial<TranslationProfileConfig>): TranslationProfileConfig {
  if (!source) return deepClone(CONFIG_DEFAULTS)
  return {
    split: { ...CONFIG_DEFAULTS.split, ...source.split },
    protect: {
      ...CONFIG_DEFAULTS.protect,
      ...source.protect,
      rules: source.protect?.rules ?? CONFIG_DEFAULTS.protect.rules,
    },
    postprocess: { ...CONFIG_DEFAULTS.postprocess, ...source.postprocess },
    repair: { ...CONFIG_DEFAULTS.repair, ...source.repair },
    glossary: {
      enabled: source.glossary?.enabled ?? CONFIG_DEFAULTS.glossary.enabled,
      bootstrap: { ...CONFIG_DEFAULTS.glossary.bootstrap, ...source.glossary?.bootstrap },
    },
  }
}

// ─── Props & Emits ──────────────────────────────────────────

const props = withDefaults(
  defineProps<{
    config: TranslationProfileConfig
    disabled?: boolean
  }>(),
  { disabled: false },
)

const emit = defineEmits<{
  'update:config': [value: TranslationProfileConfig]
}>()

// ─── 内部 model ─────────────────────────────────────────────

const { t } = useI18n()

const configModel = ref<TranslationProfileConfig>(mergeConfig(props.config))

// 上次 emit 的 JSON（用于去重）
let lastConfigJson = JSON.stringify(props.config ?? {})

// 监听外部 config 变化
watch(
  () => props.config,
  (newVal) => {
    const json = JSON.stringify(newVal ?? {})
    if (json === lastConfigJson) return
    configModel.value = mergeConfig(newVal)
  },
  { deep: true },
)

// 监听内部 config 变化并 emit
watch(
  configModel,
  (newVal) => {
    const json = JSON.stringify(newVal)
    if (json === lastConfigJson) return
    lastConfigJson = json
    emit('update:config', deepClone(newVal))
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

const splitStrategyOptions = computed(() => [{ label: 'paragraph', value: 'paragraph' }])
</script>

<template>
  <div class="flex flex-col gap-4">
    <!-- 分段策略 -->
    <NCard size="small" :bordered="true">
      <template #header>
        <span class="text-sm font-semibold">📐 {{ t('profileConfigEditor.split.title') }}</span>
      </template>
      <NGrid :cols="3" :x-gap="12" :y-gap="10">
        <NGi>
          <div class="mb-1 text-xs text-lf-text-subtle">
            {{ t('profileConfigEditor.split.enabled') }}
          </div>
          <NSwitch v-model:value="configModel.split.enabled" size="small" :disabled="disabled" />
        </NGi>
        <NGi>
          <div class="mb-1 text-xs text-lf-text-subtle">
            {{ t('profileConfigEditor.split.strategy') }}
          </div>
          <NSelect
            v-model:value="configModel.split.strategy"
            :options="splitStrategyOptions"
            size="small"
            :disabled="disabled || !configModel.split.enabled"
          />
        </NGi>
        <NGi>
          <div class="mb-1 text-xs text-lf-text-subtle">
            {{ t('profileConfigEditor.split.maxChars') }}
          </div>
          <NInputNumber
            v-model:value="configModel.split.max_chars"
            :min="100"
            :max="10000"
            :step="100"
            size="small"
            :disabled="disabled || !configModel.split.enabled"
            class="w-full"
          />
        </NGi>
      </NGrid>
    </NCard>

    <!-- 内容保护 -->
    <NCard size="small" :bordered="true">
      <template #header>
        <span class="text-sm font-semibold">🛡 {{ t('profileConfigEditor.protect.title') }}</span>
      </template>
      <div class="flex items-center justify-between mb-3">
        <span class="text-sm">{{ t('profileConfigEditor.protect.enabled') }}</span>
        <NSwitch v-model:value="configModel.protect.enabled" size="small" :disabled="disabled" />
      </div>
      <div :class="{ 'opacity-50 pointer-events-none': !configModel.protect.enabled }">
        <div class="mb-1 text-xs text-lf-text-subtle">
          {{ t('profileConfigEditor.protect.rules') }}
        </div>
        <NCheckboxGroup
          v-model:value="configModel.protect.rules"
          :disabled="disabled || !configModel.protect.enabled"
        >
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
        <span class="text-sm font-semibold"
          >✨ {{ t('profileConfigEditor.postprocess.title') }}</span
        >
      </template>
      <div class="flex flex-col gap-3">
        <div class="flex items-center justify-between">
          <span class="text-sm">{{ t('profileConfigEditor.postprocess.enabled') }}</span>
          <NSwitch
            v-model:value="configModel.postprocess.enabled"
            size="small"
            :disabled="disabled"
          />
        </div>
        <div
          class="flex items-center justify-between"
          :class="{ 'opacity-50 pointer-events-none': !configModel.postprocess.enabled }"
        >
          <span class="text-xs text-lf-text-subtle">{{
            t('profileConfigEditor.postprocess.trimSpaces')
          }}</span>
          <NSwitch
            v-model:value="configModel.postprocess.trim_spaces"
            size="small"
            :disabled="disabled || !configModel.postprocess.enabled"
          />
        </div>
      </div>
    </NCard>

    <!-- 响应修复 -->
    <NCard size="small" :bordered="true">
      <template #header>
        <span class="text-sm font-semibold">🔧 {{ t('profileConfigEditor.repair.title') }}</span>
      </template>
      <div class="flex flex-col gap-3">
        <div class="flex items-center justify-between">
          <span class="text-sm">{{ t('profileConfigEditor.repair.enabled') }}</span>
          <NSwitch v-model:value="configModel.repair.enabled" size="small" :disabled="disabled" />
        </div>
        <div
          class="ml-4 flex flex-col gap-2"
          :class="{ 'opacity-50 pointer-events-none': !configModel.repair.enabled }"
        >
          <div class="flex items-center justify-between">
            <span class="text-xs text-lf-text-subtle">{{
              t('profileConfigEditor.repair.jsonStructural')
            }}</span>
            <NSwitch
              v-model:value="configModel.repair.json_structural"
              size="small"
              :disabled="disabled || !configModel.repair.enabled"
            />
          </div>
          <div class="flex items-center justify-between">
            <span class="text-xs text-lf-text-subtle">{{
              t('profileConfigEditor.repair.schemaAliases')
            }}</span>
            <NSwitch
              v-model:value="configModel.repair.schema_aliases"
              size="small"
              :disabled="disabled || !configModel.repair.enabled"
            />
          </div>
          <div class="flex items-center justify-between">
            <span class="text-xs text-lf-text-subtle">{{
              t('profileConfigEditor.repair.partial')
            }}</span>
            <NSwitch
              v-model:value="configModel.repair.partial"
              size="small"
              :disabled="disabled || !configModel.repair.enabled"
            />
          </div>
          <div
            class="ml-4 flex items-center gap-2"
            :class="{ 'opacity-50 pointer-events-none': !configModel.repair.partial }"
          >
            <span class="text-xs text-lf-text-subtle">{{
              t('profileConfigEditor.repair.partialThreshold')
            }}</span>
            <NInputNumber
              v-model:value="configModel.repair.partial_threshold"
              :min="0"
              :max="1"
              :step="0.1"
              size="tiny"
              :disabled="disabled || !configModel.repair.enabled || !configModel.repair.partial"
              class="w-24"
            />
          </div>
          <div class="flex items-center justify-between">
            <span class="text-xs text-lf-text-subtle">{{
              t('profileConfigEditor.repair.placeholderNormalize')
            }}</span>
            <NSwitch
              v-model:value="configModel.repair.placeholder_normalize"
              size="small"
              :disabled="disabled || !configModel.repair.enabled"
            />
          </div>
          <div class="flex items-center justify-between">
            <span class="text-xs text-lf-text-subtle">{{
              t('profileConfigEditor.repair.promptUpgrade')
            }}</span>
            <NSwitch
              v-model:value="configModel.repair.prompt_upgrade"
              size="small"
              :disabled="disabled || !configModel.repair.enabled"
            />
          </div>
        </div>
      </div>
    </NCard>

    <!-- 术语表 -->
    <NCard size="small" :bordered="true">
      <template #header>
        <span class="text-sm font-semibold">📚 {{ t('profileConfigEditor.glossary.title') }}</span>
      </template>
      <div class="flex flex-col gap-3">
        <div class="flex items-center justify-between">
          <span class="text-sm">{{ t('profileConfigEditor.glossary.enabled') }}</span>
          <NSwitch v-model:value="configModel.glossary.enabled" size="small" :disabled="disabled" />
        </div>
        <div :class="{ 'opacity-50 pointer-events-none': !configModel.glossary.enabled }">
          <div class="mb-1 text-xs text-lf-text-subtle">
            {{ t('profileConfigEditor.glossary.bootstrapMode') }}
          </div>
          <NSelect
            v-model:value="configModel.glossary.bootstrap.mode"
            :options="bootstrapModeOptions"
            size="small"
            :disabled="disabled || !configModel.glossary.enabled"
          />
        </div>
        <div v-if="configModel.glossary.bootstrap.mode !== 'off'" class="flex flex-col gap-2 ml-4">
          <div class="flex items-center justify-between">
            <span class="text-xs text-lf-text-subtle">{{
              t('profileConfigEditor.glossary.bootstrapSave')
            }}</span>
            <NSwitch
              v-model:value="configModel.glossary.bootstrap.save"
              size="small"
              :disabled="disabled || !configModel.glossary.enabled"
            />
          </div>
          <div class="flex items-center gap-2">
            <span class="text-xs text-lf-text-subtle">{{
              t('profileConfigEditor.glossary.bootstrapMaxTerms')
            }}</span>
            <NInputNumber
              v-model:value="configModel.glossary.bootstrap.max_terms_per_batch"
              :min="1"
              :max="100"
              :step="1"
              size="tiny"
              :disabled="disabled || !configModel.glossary.enabled"
              class="w-24"
            />
          </div>
          <div class="flex items-center gap-2">
            <span class="text-xs text-lf-text-subtle">{{
              t('profileConfigEditor.glossary.bootstrapMinSourceLen')
            }}</span>
            <NInputNumber
              v-model:value="configModel.glossary.bootstrap.min_source_len"
              :min="1"
              :max="100"
              :step="1"
              size="tiny"
              :disabled="disabled || !configModel.glossary.enabled"
              class="w-24"
            />
          </div>
          <div>
            <div class="mb-1 text-xs text-lf-text-subtle">
              {{ t('profileConfigEditor.glossary.bootstrapConflictStrategy') }}
            </div>
            <NSelect
              v-model:value="configModel.glossary.bootstrap.inline_conflict_strategy"
              :options="inlineConflictStrategyOptions"
              size="small"
              :disabled="disabled || !configModel.glossary.enabled"
            />
          </div>
        </div>
      </div>
    </NCard>
  </div>
</template>
