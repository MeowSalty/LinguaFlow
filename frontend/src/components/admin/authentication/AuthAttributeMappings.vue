<script setup lang="ts">
import { computed, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { NAlert, NButton, NCheckbox, NDynamicTags, NFormItem, NInput, NSelect } from 'naive-ui'
import {
  AUTH_ATTRIBUTE_MAX_MAPPINGS,
  AUTH_ATTRIBUTE_TYPES,
  attributeSourcesForKind,
  cloneAuthAttributeMappings,
  createAuthAttributeMappingDraft,
  validateAuthAttributeMappings,
  type AuthAttributeMapping,
  type AuthAttributeSource,
  type AuthConnectionKind,
} from './auth-attribute-catalog'

const props = defineProps<{ kind: AuthConnectionKind; disabled?: boolean }>()
const mappings = defineModel<AuthAttributeMapping[]>('value', { required: true })
const valid = defineModel<boolean>('valid', { default: true })
const { t } = useI18n()

const issues = computed(() => validateAuthAttributeMappings(mappings.value, props.kind))

const typeOptions = computed(() =>
  AUTH_ATTRIBUTE_TYPES.map((type) => ({
    label: t(`authRules.fieldTypes.${type}`),
    value: type,
  })),
)

const sourceOptions = computed(() =>
  attributeSourcesForKind(props.kind).map((source) => ({
    label: t(`authRules.fieldSources.${source}`),
    value: source,
  })),
)

const rowProblem = (index: number, mapping: AuthAttributeMapping): string | undefined => {
  const issue = issues.value.rows.find((row) => row.index === index)
  if (!issue) return undefined
  return t(`authRules.mappings.${issue.code}`, {
    id: mapping.field_id,
    kind: t(`authRules.protocolKinds.${props.kind}`),
    source: t(`authRules.fieldSources.${mapping.source}`),
  })
}

const updateMapping = (index: number, patch: Partial<AuthAttributeMapping>) => {
  const mapping = mappings.value[index]
  if (!mapping || props.disabled) return
  const next = cloneAuthAttributeMappings(mappings.value)
  next[index] = { ...next[index]!, ...patch }
  mappings.value = next
}

const addMapping = () => {
  if (props.disabled || mappings.value.length >= AUTH_ATTRIBUTE_MAX_MAPPINGS) return
  mappings.value = [
    ...cloneAuthAttributeMappings(mappings.value),
    createAuthAttributeMappingDraft(props.kind),
  ]
}

const removeMapping = (index: number) => {
  if (props.disabled) return
  mappings.value = cloneAuthAttributeMappings(
    mappings.value.filter((_, mappingIndex) => mappingIndex !== index),
  )
}

watch(
  issues,
  (next) => {
    valid.value = next.valid
  },
  { immediate: true, flush: 'sync' },
)

defineExpose({ validate: () => issues.value })
</script>

<template>
  <div class="space-y-3">
    <div class="flex flex-wrap items-center justify-between gap-2">
      <span class="text-sm font-medium text-lf-text-strong">
        {{ t('authRules.mappings.title') }} · {{ mappings.length }} /
        {{ AUTH_ATTRIBUTE_MAX_MAPPINGS }}
      </span>
      <NButton
        size="small"
        :disabled="disabled || mappings.length >= AUTH_ATTRIBUTE_MAX_MAPPINGS"
        @click="addMapping"
      >
        {{ t('authRules.mappings.addField') }}
      </NButton>
    </div>

    <p class="text-xs leading-5 text-lf-text-subtle">
      {{ t('authRules.mappings.description') }}
    </p>

    <NAlert v-if="!issues.valid" type="warning" :bordered="false">
      {{
        issues.limitReached
          ? t('authRules.mappings.limitReached', { max: AUTH_ATTRIBUTE_MAX_MAPPINGS })
          : t('authRules.mappings.invalidHint')
      }}
    </NAlert>

    <p v-if="!mappings.length" class="py-3 text-sm text-lf-text-muted">
      {{ t('authRules.mappings.empty') }}
    </p>

    <div v-for="(mapping, index) in mappings" :key="index" class="lf-panel space-y-3 p-3">
      <div class="flex items-center justify-between gap-2">
        <span class="text-sm font-medium text-lf-text-strong">
          {{ mapping.label || t('authRules.mappings.label') }}
        </span>
        <NButton size="tiny" quaternary :disabled="disabled" @click="removeMapping(index)">
          {{ t('authRules.mappings.removeField') }}
        </NButton>
      </div>

      <div class="grid gap-x-3 gap-y-2 sm:grid-cols-2">
        <NFormItem :label="t('authRules.mappings.fieldId')" :show-feedback="false">
          <NInput
            :value="mapping.field_id"
            :disabled="disabled"
            :placeholder="t('authRules.mappings.fieldIdPlaceholder')"
            @update:value="(field_id: string) => updateMapping(index, { field_id })"
          />
        </NFormItem>
        <NFormItem :label="t('authRules.mappings.label')" :show-feedback="false">
          <NInput
            :value="mapping.label"
            :disabled="disabled"
            :placeholder="t('authRules.mappings.labelPlaceholder')"
            @update:value="(label: string) => updateMapping(index, { label })"
          />
        </NFormItem>
        <NFormItem :label="t('authRules.mappings.source')" :show-feedback="false">
          <NSelect
            :value="mapping.source"
            :disabled="disabled"
            :options="sourceOptions"
            @update:value="(source: AuthAttributeSource) => updateMapping(index, { source })"
          />
        </NFormItem>
        <NFormItem :label="t('authRules.mappings.type')" :show-feedback="false">
          <NSelect
            :value="mapping.type"
            :disabled="disabled"
            :options="typeOptions"
            @update:value="(type: AuthAttributeMapping['type']) => updateMapping(index, { type })"
          />
        </NFormItem>
        <NFormItem :label="t('authRules.mappings.pointer')" :show-feedback="false">
          <NInput
            :value="mapping.pointer"
            :disabled="disabled"
            :placeholder="t('authRules.mappings.pointerPlaceholder')"
            @update:value="(pointer: string) => updateMapping(index, { pointer })"
          />
        </NFormItem>
        <NFormItem :label="t('authRules.mappings.requiredScopes')" :show-feedback="false">
          <NDynamicTags
            :value="mapping.required_scopes ?? []"
            :disabled="disabled"
            @update:value="(required_scopes: string[]) => updateMapping(index, { required_scopes })"
          />
        </NFormItem>
      </div>

      <div class="flex flex-wrap gap-x-4 gap-y-2">
        <NCheckbox
          :checked="mapping.optional ?? false"
          :disabled="disabled"
          @update:checked="(optional: boolean) => updateMapping(index, { optional })"
        >
          {{ t('authRules.mappings.optional') }}
        </NCheckbox>
        <NCheckbox
          :checked="mapping.user_editable ?? false"
          :disabled="disabled"
          @update:checked="(user_editable: boolean) => updateMapping(index, { user_editable })"
        >
          {{ t('authRules.mappings.userEditable') }}
        </NCheckbox>
      </div>

      <p v-if="rowProblem(index, mapping)" role="alert" class="text-xs text-lf-danger">
        {{ rowProblem(index, mapping) }}
      </p>
    </div>
  </div>
</template>
