<script setup lang="ts">
import { computed, ref, useId, watch } from 'vue'
import {
  NCollapse,
  NCollapseItem,
  NInputNumber,
  NSelect,
  NSwitch,
  type SelectOption,
} from 'naive-ui'
import { useI18n } from 'vue-i18n'
import {
  RUBY_BATCH_FIELDS,
  RUBY_BATCH_WAIT_MAX_MS,
  sameRubyRetryConfig,
  validateRubyRetryConfig,
  type ExecutionPlanFormRubyRetry,
  type RubyBatchField,
} from '@/utils/execution-plan-config'
import { rubyRetryValidationKeys } from '@/locales/ruby-alignment-config'
import ConfigSectionPanel from './ConfigSectionPanel.vue'

const props = withDefaults(
  defineProps<{
    modelValue: ExecutionPlanFormRubyRetry
    backends: SelectOption[]
    disabled?: boolean
  }>(),
  { disabled: false },
)
const emit = defineEmits<{ 'update:modelValue': [value: ExecutionPlanFormRubyRetry] }>()
const { t } = useI18n()
const id = useId()
const errors = computed(() => validateRubyRetryConfig(props.modelValue))
const batchErrors = computed(() => errors.value.filter((error) => error !== 'concurrency'))
const hasExplicitBatch = (value: ExecutionPlanFormRubyRetry) =>
  RUBY_BATCH_FIELDS.some((field) => value[field] != null)
const customized = computed(() => hasExplicitBatch(props.modelValue))
const expanded = ref<string[]>(customized.value || batchErrors.value.length ? ['batch'] : [])
let lastEmitted: ExecutionPlanFormRubyRetry | undefined

function update(patch: Partial<ExecutionPlanFormRubyRetry>): void {
  if (props.disabled) return
  lastEmitted = { ...props.modelValue, ...patch }
  emit('update:modelValue', lastEmitted)
}

// A parent echo must not reopen a section the user intentionally collapsed.
watch(
  () => props.modelValue,
  (value) => {
    if (sameRubyRetryConfig(value, lastEmitted)) return
    expanded.value = hasExplicitBatch(value) || batchErrors.value.length ? ['batch'] : []
  },
  { deep: true },
)
watch(
  () => batchErrors.value.join(','),
  (value, previous) => {
    if (value && value !== previous) expanded.value = ['batch']
  },
)

function numberValue(value: unknown): number | null {
  // Keep malformed incoming values in the draft and its validation, even when not renderable.
  return typeof value === 'number' && Number.isFinite(value) ? value : null
}

const fields = computed(() => [
  {
    key: 'batch_size' as const,
    label: t('rubyAlignmentConfig.batchSize'),
    placeholder: t('rubyAlignmentConfig.defaultBatchSize'),
    hint: t('rubyAlignmentConfig.batchSizeHint'),
    max: Number.MAX_SAFE_INTEGER,
  },
  {
    key: 'max_words_per_batch' as const,
    label: t('rubyAlignmentConfig.maxWords'),
    placeholder: t('rubyAlignmentConfig.defaultMaxWords'),
    hint: t('rubyAlignmentConfig.maxWordsHint'),
    max: Number.MAX_SAFE_INTEGER,
  },
  {
    key: 'batch_wait_ms' as const,
    label: t('rubyAlignmentConfig.batchWait'),
    placeholder: t('rubyAlignmentConfig.defaultBatchWait'),
    hint: t('rubyAlignmentConfig.batchWaitHint'),
    max: RUBY_BATCH_WAIT_MAX_MS,
  },
])
const invalidField = (field: RubyBatchField) =>
  errors.value.includes(field) ||
  (field !== 'batch_wait_ms' && errors.value.includes('batch_limits'))
const describedBy = (field: RubyBatchField) =>
  [
    `${id}-${field}-hint`,
    ...(errors.value.includes(field) ? [`${id}-${field}-error`] : []),
    ...(field !== 'batch_wait_ms' && errors.value.includes('batch_limits')
      ? [`${id}-batch-limits-error`]
      : []),
  ].join(' ')
</script>

<template>
  <ConfigSectionPanel
    :title="t('executionPlanEditor.rubyRetry.title')"
    :description="t('executionPlanEditor.rubyRetry.description')"
    :enabled="modelValue.enabled"
  >
    <template #actions>
      <NSwitch
        :value="modelValue.enabled"
        :disabled="disabled"
        size="small"
        :aria-label="t('executionPlanEditor.rubyRetry.enabled')"
        @update:value="update({ enabled: $event })"
      />
    </template>
    <div class="grid grid-cols-1 gap-3 sm:grid-cols-2">
      <div>
        <div class="mb-1 text-xs text-lf-text-subtle">
          {{ t('executionPlanEditor.rubyRetry.backend') }}
        </div>
        <NSelect
          :value="modelValue.backend_id"
          :options="backends"
          :disabled="disabled"
          :placeholder="t('executionPlanEditor.rubyRetry.backendPlaceholder')"
          clearable
          size="small"
          class="w-full"
          @update:value="update({ backend_id: $event })"
        />
      </div>
      <div>
        <label :for="`${id}-attempts`" class="mb-1 block text-xs text-lf-text-subtle">
          {{ t('executionPlanEditor.rubyRetry.maxAttempts') }}
        </label>
        <NInputNumber
          :value="modelValue.max_attempts"
          :input-props="{ id: `${id}-attempts` }"
          :min="1"
          :max="10"
          :precision="0"
          :disabled="disabled"
          size="small"
          class="w-full"
          @update:value="update({ max_attempts: $event ?? 1 })"
        />
        <p class="mt-1 text-xs text-lf-text-subtle">
          {{ t('executionPlanEditor.rubyRetry.maxAttemptsHint') }}
        </p>
      </div>
    </div>
    <div>
      <label :for="`${id}-concurrency`" class="mb-1 block text-xs text-lf-text-subtle">
        {{ t('rubyAlignmentConfig.concurrency') }}
      </label>
      <NInputNumber
        :value="numberValue(modelValue.concurrency)"
        :input-props="{
          id: `${id}-concurrency`,
          'aria-label': t('rubyAlignmentConfig.concurrency'),
          'aria-invalid': errors.includes('concurrency'),
          'aria-describedby': `${id}-concurrency-hint${errors.includes('concurrency') ? ` ${id}-concurrency-error` : ''}`,
        }"
        :placeholder="t('rubyAlignmentConfig.defaultConcurrency')"
        :min="1"
        :max="Number.MAX_SAFE_INTEGER"
        :precision="0"
        :status="errors.includes('concurrency') ? 'error' : undefined"
        :disabled="disabled"
        clearable
        size="small"
        class="w-full"
        @update:value="update({ concurrency: $event })"
      />
      <p :id="`${id}-concurrency-hint`" class="mt-1 text-xs text-lf-text-subtle">
        {{ t('rubyAlignmentConfig.concurrencyHint') }}
      </p>
      <p
        v-if="errors.includes('concurrency')"
        :id="`${id}-concurrency-error`"
        role="alert"
        class="mt-1 text-xs text-lf-danger"
      >
        {{ t('rubyAlignmentConfig.invalidConcurrency') }}
      </p>
    </div>
    <NCollapse v-model:expanded-names="expanded">
      <NCollapseItem name="batch" display-directive="show">
        <template #header>
          <button
            type="button"
            :aria-expanded="expanded.includes('batch')"
            :aria-controls="`${id}-batch-panel`"
            class="flex-1 rounded-lf-ctl py-1 text-left focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-brand-500"
            @click.stop="expanded = expanded.includes('batch') ? [] : ['batch']"
          >
            {{ t('rubyAlignmentConfig.batchSettings') }}
          </button>
        </template>
        <template #header-extra>
          <span class="text-xs text-lf-text-subtle">
            {{
              t(customized ? 'rubyAlignmentConfig.customBatch' : 'rubyAlignmentConfig.defaultBatch')
            }}
          </span>
        </template>
        <div :id="`${id}-batch-panel`">
          <p class="mb-3 text-xs leading-relaxed text-lf-text-muted">
            {{ t('rubyAlignmentConfig.batchHint') }}
          </p>
          <div class="grid grid-cols-1 gap-3 sm:grid-cols-2">
            <div v-for="field in fields" :key="field.key" class="min-w-0">
              <label :for="`${id}-${field.key}`" class="mb-1 block text-xs text-lf-text-subtle">
                {{ field.label }}
              </label>
              <NInputNumber
                :value="numberValue(modelValue[field.key])"
                :input-props="{
                  id: `${id}-${field.key}`,
                  'aria-label': field.label,
                  'aria-invalid': invalidField(field.key),
                  'aria-describedby': describedBy(field.key),
                }"
                :placeholder="field.placeholder"
                :min="0"
                :max="field.max"
                :precision="0"
                :status="invalidField(field.key) ? 'error' : undefined"
                :disabled="disabled"
                clearable
                size="small"
                class="w-full"
                @update:value="update({ [field.key]: $event })"
              />
              <p
                :id="`${id}-${field.key}-hint`"
                class="mt-1 text-xs leading-relaxed text-lf-text-subtle"
              >
                {{ field.hint }}
              </p>
              <p
                v-if="errors.includes(field.key)"
                :id="`${id}-${field.key}-error`"
                role="alert"
                class="mt-1 text-xs text-lf-danger"
              >
                {{ t(rubyRetryValidationKeys[field.key]) }}
              </p>
            </div>
          </div>
          <p
            v-if="errors.includes('batch_limits')"
            :id="`${id}-batch-limits-error`"
            role="alert"
            class="mt-3 text-xs text-lf-danger"
          >
            {{ t('rubyAlignmentConfig.invalidBatchLimits') }}
          </p>
        </div>
      </NCollapseItem>
    </NCollapse>
  </ConfigSectionPanel>
</template>
