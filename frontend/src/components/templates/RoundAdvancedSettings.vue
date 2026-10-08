<script setup lang="ts">
import {
  NCollapse,
  NCollapseItem,
  NFormItem,
  NGrid,
  NGi,
  NInputNumber,
  NSelect,
  NSwitch,
} from 'naive-ui'
import { useI18n } from 'vue-i18n'

import type { ApiSchemas } from '@/api/client'
import {
  createInlineTermExtractionConfig,
  validateInlineTermExtractionConfig,
} from '@/utils/execution-plan-config'
import type { ExecutionPlanFormRound } from '@/utils/execution-plan-config'

type RetryConfig = ApiSchemas['RetryConfig']
type InlineTermExtractionConfig = ApiSchemas['InlineTermExtractionConfig']
type InlineNumericField = 'max_terms_per_1000_words' | 'min_source_len'

const props = withDefaults(
  defineProps<{
    round: ExecutionPlanFormRound
    disabled?: boolean
  }>(),
  { disabled: false },
)

const emit = defineEmits<{
  'update:inlineTermExtraction': [value: InlineTermExtractionConfig]
}>()

const { t } = useI18n()

const inlineTermExtraction = computed(() => {
  const value = props.round.translate?.inline_term_extraction
  return value && typeof value === 'object' && !Array.isArray(value) ? value : undefined
})

const inlineErrors = computed(() =>
  props.round.mode === 'translate'
    ? validateInlineTermExtractionConfig(props.round.translate?.inline_term_extraction)
    : [],
)

const conflictOptions = computed(() => [
  { label: t('executionPlanEditor.round.inlineTermExtraction.conflictOff'), value: 'off' },
  {
    label: t('executionPlanEditor.round.inlineTermExtraction.conflictRewrite'),
    value: 'rewrite-local',
  },
])

function setInlineEnabled(enabled: boolean): void {
  if (props.disabled || props.round.mode !== 'translate' || !props.round.translate) return
  if (inlineTermExtraction.value) {
    emit('update:inlineTermExtraction', { ...inlineTermExtraction.value, enabled })
  } else if (enabled) {
    emit('update:inlineTermExtraction', {
      ...createInlineTermExtractionConfig(),
      enabled: true,
    })
  }
}

function inlineNumberValue(value: unknown): number | null {
  return typeof value === 'number' && Number.isFinite(value) ? value : null
}

function setInlineNumber(field: InlineNumericField, value: number | null): void {
  if (props.disabled || !inlineTermExtraction.value) return
  // Keep cleared/invalid input in the draft so validation blocks submission instead of
  // silently replacing it with a default, including while extraction is disabled.
  const config = { ...inlineTermExtraction.value } as Record<string, unknown>
  config[field] = value
  emit('update:inlineTermExtraction', config as InlineTermExtractionConfig)
}

function setInlineConflict(value: InlineTermExtractionConfig['conflict_strategy']): void {
  if (props.disabled || !inlineTermExtraction.value) return
  emit('update:inlineTermExtraction', {
    ...inlineTermExtraction.value,
    conflict_strategy: value,
  })
}

// 五种 LLM 轮次模式的 retry 结构完全一致，仅嵌套路径不同，按 mode 取到对应的 retry 对象引用
const retry = computed<RetryConfig | undefined>(() => {
  const round = props.round
  if (round.mode === 'translate') return round.translate?.retry
  if (round.mode === 'extract') return round.extract?.retry
  if (round.mode === 'adjudicate') return round.adjudicate?.retry
  if (round.mode === 'semantic_qa') return round.semantic_qa?.retry
  if (round.mode === 'revise') return round.revise?.retry
  return undefined
})
</script>

<template>
  <NCollapse class="mt-3" :default-expanded-names="inlineErrors.length ? ['advanced'] : []">
    <NCollapseItem name="advanced" :title="t('executionPlanEditor.round.advancedConfig')">
      <section
        v-if="round.mode === 'translate'"
        data-testid="inline-term-extraction"
        class="mb-4 rounded-lg border border-lf-border-soft bg-lf-surface p-3"
      >
        <div class="flex items-start justify-between gap-4">
          <div class="min-w-0">
            <div class="text-sm font-medium text-lf-text-strong">
              {{ t('executionPlanEditor.round.inlineTermExtraction.title') }}
            </div>
            <p class="mt-1 text-xs leading-5 text-lf-text-subtle">
              {{ t('executionPlanEditor.round.inlineTermExtraction.description') }}
            </p>
          </div>
          <NSwitch
            :value="inlineTermExtraction?.enabled === true"
            :aria-label="t('executionPlanEditor.round.inlineTermExtraction.title')"
            :aria-disabled="disabled"
            size="small"
            :disabled="disabled"
            @update:value="setInlineEnabled"
          />
        </div>
        <p v-if="inlineErrors.includes('config')" role="alert" class="mt-2 text-xs text-lf-danger">
          {{ t('executionPlanEditor.round.inlineTermExtraction.validation.config') }}
        </p>
        <p v-if="inlineErrors.includes('enabled')" role="alert" class="mt-2 text-xs text-lf-danger">
          {{ t('executionPlanEditor.round.inlineTermExtraction.validation.enabled') }}
        </p>

        <div
          v-if="inlineTermExtraction"
          class="mt-3 grid grid-cols-1 gap-x-3 gap-y-2 sm:grid-cols-2"
        >
          <NFormItem
            :label="t('executionPlanEditor.round.inlineTermExtraction.maxTerms')"
            :validation-status="
              inlineErrors.includes('max_terms_per_1000_words') ? 'error' : undefined
            "
            :show-require-mark="false"
            size="small"
          >
            <NInputNumber
              :value="inlineNumberValue(inlineTermExtraction.max_terms_per_1000_words)"
              :input-props="{
                'aria-label': t('executionPlanEditor.round.inlineTermExtraction.maxTerms'),
              }"
              :step="0.1"
              size="small"
              :disabled="disabled"
              class="w-full"
              @update:value="(value) => setInlineNumber('max_terms_per_1000_words', value)"
            />
            <template #feedback>
              <span
                :class="
                  inlineErrors.includes('max_terms_per_1000_words')
                    ? 'text-lf-danger'
                    : 'text-lf-text-subtle'
                "
              >
                {{
                  inlineErrors.includes('max_terms_per_1000_words')
                    ? t('executionPlanEditor.round.inlineTermExtraction.validation.maxTerms')
                    : t('executionPlanEditor.round.inlineTermExtraction.maxTermsHint')
                }}
              </span>
            </template>
          </NFormItem>
          <NFormItem
            :label="t('executionPlanEditor.round.inlineTermExtraction.minSourceLen')"
            :validation-status="inlineErrors.includes('min_source_len') ? 'error' : undefined"
            :feedback="
              inlineErrors.includes('min_source_len')
                ? t('executionPlanEditor.round.inlineTermExtraction.validation.minSourceLen')
                : undefined
            "
            :show-require-mark="false"
            size="small"
          >
            <NInputNumber
              :value="inlineNumberValue(inlineTermExtraction.min_source_len)"
              :input-props="{
                'aria-label': t('executionPlanEditor.round.inlineTermExtraction.minSourceLen'),
              }"
              size="small"
              :disabled="disabled"
              class="w-full"
              @update:value="(value) => setInlineNumber('min_source_len', value)"
            />
          </NFormItem>
          <NFormItem
            :label="t('executionPlanEditor.round.inlineTermExtraction.conflictStrategy')"
            :validation-status="inlineErrors.includes('conflict_strategy') ? 'error' : undefined"
            :feedback="
              inlineErrors.includes('conflict_strategy')
                ? t('executionPlanEditor.round.inlineTermExtraction.validation.conflictStrategy')
                : undefined
            "
            :show-require-mark="false"
            size="small"
            class="sm:col-span-2"
          >
            <NSelect
              :value="inlineTermExtraction.conflict_strategy"
              :aria-label="t('executionPlanEditor.round.inlineTermExtraction.conflictStrategy')"
              :options="conflictOptions"
              size="small"
              :disabled="disabled"
              class="w-full"
              @update:value="setInlineConflict"
            />
          </NFormItem>
        </div>
      </section>
      <NGrid cols="1 s:2" responsive="screen" :x-gap="12" :y-gap="10">
        <NGi>
          <div class="mb-1 text-xs text-lf-text-subtle">
            {{ t('executionPlanEditor.round.retryMaxAttempts') }}
          </div>
          <NInputNumber
            v-if="retry"
            v-model:value="retry.max_attempts"
            :input-props="{ 'aria-label': t('executionPlanEditor.round.retryMaxAttempts') }"
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
            v-if="retry"
            v-model:value="retry.backoff_ms"
            :input-props="{ 'aria-label': t('executionPlanEditor.round.retryBackoffMs') }"
            :min="0"
            :max="60000"
            :step="100"
            size="small"
            :disabled="disabled"
            class="w-full"
          />
        </NGi>
      </NGrid>
      <div v-if="retry" class="mt-2 flex items-center gap-2">
        <NSwitch
          v-model:value="retry.jitter"
          :aria-label="t('executionPlanEditor.round.retryJitter')"
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
