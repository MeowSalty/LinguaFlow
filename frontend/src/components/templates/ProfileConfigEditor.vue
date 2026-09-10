<script setup lang="ts">
import {
  NCheckbox,
  NCheckboxGroup,
  NInputNumber,
  NRadio,
  NRadioGroup,
  NSelect,
  NSwitch,
} from 'naive-ui'
import { useI18n } from 'vue-i18n'

import type { ApiSchemas } from '@/api/client'

import ConfigSectionPanel from './ConfigSectionPanel.vue'

type ExecutionProfileConfig = ApiSchemas['ExecutionProfileConfig']

// ─── 默认值（与后端 config.Default() 对齐） ─────────────────

const CONFIG_DEFAULTS: ExecutionProfileConfig = {
  protect: {
    enabled: true,
    rules: ['code', 'link', 'placeholder', 'xml'],
  },
  ruby: {
    enabled: false,
    preserve_kinds: ['phonetic', 'semantic', 'creative'],
  },
  postprocess: { enabled: true, trim_spaces: true },
  repair: {
    enabled: true,
    json_structural: true,
    schema_aliases: true,
    placeholder_normalize: true,
    prompt_upgrade: true,
  },
  glossary: {
    bootstrap: {
      enabled: false,
      max_terms_per_1000_chars: 20,
      min_source_len: 2,
      inline_conflict_strategy: 'off',
    },
  },
  context: { enabled: true, before: 1, after: 1, max_chars: 0 },
  qa: {
    enabled: false,
    auto_reject: false,
    checks: undefined,
    length_method: 'char_weight',
    length_ratio_min: 0,
    length_ratio_max: 0,
  },
}

// ─── 工具函数 ────────────────────────────────────────────────

function deepClone<T>(obj: T): T {
  return JSON.parse(JSON.stringify(obj))
}

function mergeConfig(source?: Partial<ExecutionProfileConfig>): ExecutionProfileConfig {
  if (!source) return deepClone(CONFIG_DEFAULTS)
  return {
    protect: {
      ...CONFIG_DEFAULTS.protect,
      ...source.protect,
      rules: source.protect?.rules ?? CONFIG_DEFAULTS.protect.rules,
    },
    ruby: {
      enabled: source.ruby?.enabled ?? CONFIG_DEFAULTS.ruby!.enabled,
      preserve_kinds: source.ruby?.preserve_kinds ?? CONFIG_DEFAULTS.ruby!.preserve_kinds,
    },
    postprocess: { ...CONFIG_DEFAULTS.postprocess, ...source.postprocess },
    repair: { ...CONFIG_DEFAULTS.repair, ...source.repair },
    glossary: {
      bootstrap: { ...CONFIG_DEFAULTS.glossary.bootstrap, ...source.glossary?.bootstrap },
    },
    context: { ...CONFIG_DEFAULTS.context, ...source.context },
    qa: {
      enabled: source.qa?.enabled ?? CONFIG_DEFAULTS.qa!.enabled,
      auto_reject: source.qa?.auto_reject ?? CONFIG_DEFAULTS.qa!.auto_reject,
      checks: source.qa?.checks ?? CONFIG_DEFAULTS.qa!.checks,
      length_method: source.qa?.length_method ?? CONFIG_DEFAULTS.qa!.length_method,
      length_ratio_min: source.qa?.length_ratio_min ?? CONFIG_DEFAULTS.qa!.length_ratio_min,
      length_ratio_max: source.qa?.length_ratio_max ?? CONFIG_DEFAULTS.qa!.length_ratio_max,
    },
  }
}

// ─── Props & Emits ──────────────────────────────────────────

const props = withDefaults(
  defineProps<{
    config: ExecutionProfileConfig
    disabled?: boolean
  }>(),
  { disabled: false },
)

const emit = defineEmits<{
  'update:config': [value: ExecutionProfileConfig]
}>()

// ─── 内部 model ─────────────────────────────────────────────

const { t } = useI18n()

const configModel = ref<ExecutionProfileConfig>(mergeConfig(props.config))

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
  { label: t('profileConfigEditor.protect.ruleOptions.code'), value: 'code' },
  { label: t('profileConfigEditor.protect.ruleOptions.link'), value: 'link' },
  { label: t('profileConfigEditor.protect.ruleOptions.placeholder'), value: 'placeholder' },
  { label: t('profileConfigEditor.protect.ruleOptions.xml'), value: 'xml' },
])

const inlineConflictStrategyOptions = computed(() => [
  { label: t('profileConfigEditor.glossary.conflictStrategyOptions.off'), value: 'off' },
  {
    label: t('profileConfigEditor.glossary.conflictStrategyOptions.rewriteLocal'),
    value: 'rewrite-local',
  },
])

const rubyPreserveKindsOptions = computed(() => [
  { label: t('profileConfigEditor.ruby.preserveKindsPhonetic'), value: 'phonetic' },
  { label: t('profileConfigEditor.ruby.preserveKindsSemantic'), value: 'semantic' },
  { label: t('profileConfigEditor.ruby.preserveKindsCreative'), value: 'creative' },
])

const lengthMethodOptions = computed(() => [
  { label: t('profileConfigEditor.qa.lengthMethodCharWeight'), value: 'char_weight' },
  { label: t('profileConfigEditor.qa.lengthMethodWordCount'), value: 'word_count' },
])

// 可用的确定性 checker 列表（与后端一致）
const QA_CHECKS = [
  'untranslated',
  'length_ratio',
  'duplicate',
  'source_residual',
  'punctuation_pairing',
  'punctuation_missing',
  'punctuation_surplus',
  'punctuation_wrap_loss',
  'whitespace_irregular',
  'repeated_space',
  'width_mix',
  'script_mismatch',
  'number_mismatch',
  'url_email_mismatch',
  'subtitle_line_count',
  'forbidden_term',
  'term_inconsistency',
  'leftover_placeholder',
  'xml_tag_mismatch',
  'duplicate_source_divergence',
] as const

type QACheckName = (typeof QA_CHECKS)[number]

const checkOptions = computed(() =>
  QA_CHECKS.map((value) => ({ value, label: t(`profileConfigEditor.qa.checks.${value}`) })),
)

// checks 模式：true = 全部（省略），false = 自定义
const checksModeAll = computed<boolean>({
  get: () => !configModel.value.qa?.checks,
  set: (val: boolean) => {
    if (!configModel.value.qa) return
    configModel.value.qa.checks = val ? undefined : ([...QA_CHECKS] as QACheckName[])
  },
})

// 自定义模式下的选中值（与 configModel.qa.checks 双向同步）
const selectedChecks = computed<QACheckName[]>({
  get: () => (configModel.value.qa?.checks ?? []) as QACheckName[],
  set: (val: QACheckName[]) => {
    if (!configModel.value.qa) return
    configModel.value.qa.checks = val.length ? (val as QACheckName[]) : undefined
    // 若用户清空了所有选项，切回「全部」模式以避免提交空数组被误解
    if (val.length === 0) checksModeAll.value = true
  },
})

const lengthRatioError = computed(() => {
  const qa = configModel.value.qa
  if (!qa?.enabled) return ''
  const min = qa.length_ratio_min
  const max = qa.length_ratio_max
  if (min > 0 && max > 0 && min > max) {
    return t('profileConfigEditor.qa.lengthRatioMinMaxError')
  }
  return ''
})

function onRubyUpdate(field: 'enabled', value: boolean): void
function onRubyUpdate(
  field: 'preserve_kinds',
  value: ('phonetic' | 'semantic' | 'creative')[],
): void
function onRubyUpdate(field: string, value: unknown): void {
  if (!configModel.value.ruby) {
    configModel.value.ruby = {
      enabled: false,
      preserve_kinds: ['phonetic', 'semantic', 'creative'],
    }
  }
  const ruby = configModel.value.ruby
  if (field === 'enabled') {
    ruby.enabled = value as boolean
  } else if (field === 'preserve_kinds') {
    ruby.preserve_kinds = value as ('phonetic' | 'semantic' | 'creative')[]
  }
}

defineExpose({ lengthRatioError })
</script>

<template>
  <div class="flex flex-col gap-4">
    <!-- 内容保护 -->
    <ConfigSectionPanel
      :title="t('profileConfigEditor.protect.title')"
      :description="t('profileConfigEditor.protect.description')"
      :enabled="configModel.protect.enabled"
    >
      <template #actions>
        <NSwitch
          v-model:value="configModel.protect.enabled"
          size="small"
          :disabled="disabled"
          :aria-label="t('profileConfigEditor.protect.enabled')"
        />
      </template>

      <div class="mb-1 text-xs text-lf-text-subtle">
        {{ t('profileConfigEditor.protect.rules') }}
      </div>
      <NCheckboxGroup v-model:value="configModel.protect.rules" :disabled="disabled">
        <div class="flex flex-wrap gap-x-4 gap-y-2">
          <NCheckbox
            v-for="opt in protectRuleOptions"
            :key="opt.value"
            :value="opt.value"
            :label="opt.label"
          />
        </div>
      </NCheckboxGroup>
    </ConfigSectionPanel>

    <!-- Ruby 注音 -->
    <ConfigSectionPanel
      :title="t('profileConfigEditor.ruby.title')"
      :description="t('profileConfigEditor.ruby.description')"
      :enabled="configModel.ruby?.enabled ?? false"
    >
      <template #actions>
        <NSwitch
          :value="configModel.ruby?.enabled ?? false"
          size="small"
          :disabled="disabled"
          :aria-label="t('profileConfigEditor.ruby.enabled')"
          @update:value="(val: boolean) => onRubyUpdate('enabled', val)"
        />
      </template>

      <div class="mb-1 text-xs text-lf-text-subtle">
        {{ t('profileConfigEditor.ruby.preserveKinds') }}
      </div>
      <NCheckboxGroup
        :value="configModel.ruby?.preserve_kinds ?? ['phonetic', 'semantic', 'creative']"
        :disabled="disabled"
        @update:value="
          (val: (string | number)[]) =>
            onRubyUpdate('preserve_kinds', val as ('phonetic' | 'semantic' | 'creative')[])
        "
      >
        <div class="flex flex-wrap gap-x-4 gap-y-2">
          <NCheckbox
            v-for="opt in rubyPreserveKindsOptions"
            :key="opt.value"
            :value="opt.value"
            :label="opt.label"
          />
        </div>
      </NCheckboxGroup>
    </ConfigSectionPanel>

    <!-- 后处理 -->
    <ConfigSectionPanel
      :title="t('profileConfigEditor.postprocess.title')"
      :description="t('profileConfigEditor.postprocess.description')"
      :enabled="configModel.postprocess.enabled"
    >
      <template #actions>
        <NSwitch
          v-model:value="configModel.postprocess.enabled"
          size="small"
          :disabled="disabled"
          :aria-label="t('profileConfigEditor.postprocess.enabled')"
        />
      </template>

      <div class="flex items-center justify-between">
        <span class="text-sm text-lf-text">{{
          t('profileConfigEditor.postprocess.trimSpaces')
        }}</span>
        <NSwitch
          v-model:value="configModel.postprocess.trim_spaces"
          size="small"
          :disabled="disabled"
        />
      </div>
    </ConfigSectionPanel>

    <!-- 响应修复 -->
    <ConfigSectionPanel
      :title="t('profileConfigEditor.repair.title')"
      :description="t('profileConfigEditor.repair.description')"
      :enabled="configModel.repair.enabled"
    >
      <template #actions>
        <NSwitch
          v-model:value="configModel.repair.enabled"
          size="small"
          :disabled="disabled"
          :aria-label="t('profileConfigEditor.repair.enabled')"
        />
      </template>

      <div class="flex items-center justify-between">
        <span class="text-sm text-lf-text">{{
          t('profileConfigEditor.repair.jsonStructural')
        }}</span>
        <NSwitch
          v-model:value="configModel.repair.json_structural"
          size="small"
          :disabled="disabled"
        />
      </div>
      <div class="flex items-center justify-between">
        <span class="text-sm text-lf-text">{{
          t('profileConfigEditor.repair.schemaAliases')
        }}</span>
        <NSwitch
          v-model:value="configModel.repair.schema_aliases"
          size="small"
          :disabled="disabled"
        />
      </div>
      <div class="flex items-center justify-between">
        <span class="text-sm text-lf-text">
          {{ t('profileConfigEditor.repair.placeholderNormalize') }}
        </span>
        <NSwitch
          v-model:value="configModel.repair.placeholder_normalize"
          size="small"
          :disabled="disabled"
        />
      </div>
      <div class="flex items-center justify-between">
        <span class="text-sm text-lf-text">{{
          t('profileConfigEditor.repair.promptUpgrade')
        }}</span>
        <NSwitch
          v-model:value="configModel.repair.prompt_upgrade"
          size="small"
          :disabled="disabled"
        />
      </div>
    </ConfigSectionPanel>

    <!-- 术语表 -->
    <ConfigSectionPanel
      :title="t('profileConfigEditor.glossary.title')"
      :description="t('profileConfigEditor.glossary.description')"
      :enabled="configModel.glossary.bootstrap.enabled"
    >
      <template #actions>
        <NSwitch
          v-model:value="configModel.glossary.bootstrap.enabled"
          size="small"
          :disabled="disabled"
          :aria-label="t('profileConfigEditor.glossary.bootstrapEnabled')"
        />
      </template>

      <div class="grid grid-cols-1 gap-3 sm:grid-cols-2">
        <div>
          <div class="mb-1 text-xs text-lf-text-subtle">
            {{ t('profileConfigEditor.glossary.bootstrapMaxTerms') }}
          </div>
          <NInputNumber
            v-model:value="configModel.glossary.bootstrap.max_terms_per_1000_chars"
            :min="0"
            :max="100"
            :step="0.1"
            size="small"
            :disabled="disabled"
            class="w-full"
          />
        </div>
        <div>
          <div class="mb-1 text-xs text-lf-text-subtle">
            {{ t('profileConfigEditor.glossary.bootstrapMinSourceLen') }}
          </div>
          <NInputNumber
            v-model:value="configModel.glossary.bootstrap.min_source_len"
            :min="1"
            :max="100"
            :step="1"
            size="small"
            :disabled="disabled"
            class="w-full"
          />
        </div>
      </div>
      <div>
        <div class="mb-1 text-xs text-lf-text-subtle">
          {{ t('profileConfigEditor.glossary.bootstrapConflictStrategy') }}
        </div>
        <NSelect
          v-model:value="configModel.glossary.bootstrap.inline_conflict_strategy"
          :options="inlineConflictStrategyOptions"
          size="small"
          :disabled="disabled"
          class="w-full"
        />
      </div>
    </ConfigSectionPanel>

    <!-- 上下文窗口 -->
    <ConfigSectionPanel
      :title="t('profileConfigEditor.context.title')"
      :description="t('profileConfigEditor.context.description')"
      :enabled="configModel.context.enabled"
    >
      <template #actions>
        <NSwitch
          v-model:value="configModel.context.enabled"
          size="small"
          :disabled="disabled"
          :aria-label="t('profileConfigEditor.context.enabled')"
        />
      </template>

      <div class="grid grid-cols-1 gap-3 sm:grid-cols-3">
        <div>
          <div class="mb-1 text-xs text-lf-text-subtle">
            {{ t('profileConfigEditor.context.before') }}
          </div>
          <NInputNumber
            v-model:value="configModel.context.before"
            :min="0"
            :max="10"
            :step="1"
            size="small"
            :disabled="disabled"
            class="w-full"
          />
        </div>
        <div>
          <div class="mb-1 text-xs text-lf-text-subtle">
            {{ t('profileConfigEditor.context.after') }}
          </div>
          <NInputNumber
            v-model:value="configModel.context.after"
            :min="0"
            :max="10"
            :step="1"
            size="small"
            :disabled="disabled"
            class="w-full"
          />
        </div>
        <div>
          <div class="mb-1 text-xs text-lf-text-subtle">
            {{ t('profileConfigEditor.context.maxChars') }}
          </div>
          <NInputNumber
            v-model:value="configModel.context.max_chars"
            :min="0"
            :max="10000"
            :step="100"
            size="small"
            :disabled="disabled"
            class="w-full"
          />
          <div class="mt-1 text-xs text-lf-text-subtle">
            {{ t('profileConfigEditor.context.maxCharsHint') }}
          </div>
        </div>
      </div>
    </ConfigSectionPanel>

    <!-- 质量检测 -->
    <ConfigSectionPanel
      :title="t('profileConfigEditor.qa.title')"
      :description="t('profileConfigEditor.qa.description')"
      :enabled="configModel.qa!.enabled"
    >
      <template #actions>
        <NSwitch
          v-model:value="configModel.qa!.enabled"
          size="small"
          :disabled="disabled"
          :aria-label="t('profileConfigEditor.qa.enabled')"
        />
      </template>

      <div class="flex items-center justify-between">
        <span class="text-sm text-lf-text">{{ t('profileConfigEditor.qa.autoReject') }}</span>
        <NSwitch v-model:value="configModel.qa!.auto_reject" size="small" :disabled="disabled" />
      </div>

      <!-- 确定性检查项 -->
      <div class="rounded-lf-ctl border border-lf-border-soft bg-lf-surface-muted/40 p-3">
        <div class="mb-2 flex items-center justify-between">
          <span class="text-xs font-medium text-lf-text-strong">
            {{ t('profileConfigEditor.qa.checksTitle') }}
          </span>
          <NRadioGroup
            :value="checksModeAll ? 'all' : 'custom'"
            size="small"
            :disabled="disabled"
            @update:value="
              (val: string) => {
                checksModeAll = val === 'all'
              }
            "
          >
            <NRadio value="all">{{ t('profileConfigEditor.qa.checksAll') }}</NRadio>
            <NRadio value="custom">{{ t('profileConfigEditor.qa.checksCustom') }}</NRadio>
          </NRadioGroup>
        </div>
        <div v-if="checksModeAll" class="text-xs text-lf-text-subtle">
          {{ t('profileConfigEditor.qa.checksAllHint') }}
        </div>
        <div v-else>
          <div class="mb-1 text-xs text-lf-text-subtle">
            {{ t('profileConfigEditor.qa.checksHint') }}
          </div>
          <NCheckboxGroup
            :value="selectedChecks"
            :disabled="disabled"
            @update:value="
              (val: Array<string | number>) => {
                selectedChecks = val as QACheckName[]
              }
            "
          >
            <div class="grid grid-cols-2 gap-x-4 gap-y-2 sm:grid-cols-3">
              <NCheckbox
                v-for="opt in checkOptions"
                :key="opt.value"
                :value="opt.value"
                :label="opt.label"
              />
            </div>
          </NCheckboxGroup>
        </div>
      </div>

      <!-- 长度计算方式 -->
      <div>
        <div class="mb-1 text-xs text-lf-text-subtle">
          {{ t('profileConfigEditor.qa.lengthMethod') }}
        </div>
        <NSelect
          v-model:value="configModel.qa!.length_method"
          :options="lengthMethodOptions"
          size="small"
          :disabled="disabled"
          class="w-full"
        />
      </div>

      <!-- 长度比 -->
      <div class="grid grid-cols-1 gap-3 sm:grid-cols-2">
        <div>
          <div class="mb-1 flex items-center gap-2">
            <NCheckbox
              :checked="configModel.qa!.length_ratio_min > 0"
              :disabled="disabled"
              @update:checked="
                (val: boolean) => {
                  configModel.qa!.length_ratio_min = val ? 0.2 : 0
                }
              "
            />
            <span class="text-xs text-lf-text-subtle">
              {{ t('profileConfigEditor.qa.lengthRatioMin') }}
            </span>
          </div>
          <NInputNumber
            v-model:value="configModel.qa!.length_ratio_min"
            :min="0.01"
            :step="0.05"
            size="small"
            :disabled="disabled || configModel.qa!.length_ratio_min === 0"
            class="w-full"
          />
        </div>
        <div>
          <div class="mb-1 flex items-center gap-2">
            <NCheckbox
              :checked="configModel.qa!.length_ratio_max > 0"
              :disabled="disabled"
              @update:checked="
                (val: boolean) => {
                  configModel.qa!.length_ratio_max = val ? 3 : 0
                }
              "
            />
            <span class="text-xs text-lf-text-subtle">
              {{ t('profileConfigEditor.qa.lengthRatioMax') }}
            </span>
          </div>
          <NInputNumber
            v-model:value="configModel.qa!.length_ratio_max"
            :min="0.01"
            :max="10"
            :step="0.05"
            size="small"
            :disabled="disabled || configModel.qa!.length_ratio_max === 0"
            class="w-full"
          />
        </div>
      </div>

      <div class="text-xs text-lf-text-subtle">
        {{ t('profileConfigEditor.qa.lengthRatioHint') }}
      </div>
      <div v-if="lengthRatioError" class="text-xs text-lf-danger">{{ lengthRatioError }}</div>
    </ConfigSectionPanel>
  </div>
</template>
