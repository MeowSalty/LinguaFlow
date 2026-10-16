<script setup lang="ts">
import { computed, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import {
  NAlert,
  NButton,
  NCheckbox,
  NDynamicTags,
  NFormItem,
  NInput,
  NSelect,
  NTag,
} from 'naive-ui'
import {
  AUTH_ATTRIBUTE_MAX_FIELDS,
  AUTH_ATTRIBUTE_TYPES,
  attributeSourcesForKind,
  cloneAuthAttributeFields,
  createAuthAttributeFieldDraft,
  isDerivedAttributeField,
  validateAuthAttributeMappings,
  type AuthAttributeField,
  type AuthAttributeSource,
  type AuthAttributeType,
  type AuthConnectionKind,
} from './auth-attribute-catalog'

const props = defineProps<{ kind: AuthConnectionKind; disabled?: boolean }>()
const value = defineModel<AuthAttributeField[]>('value', { required: true })
const valid = defineModel<boolean>('valid', { default: true })
const { t } = useI18n()
const issues = computed(() => validateAuthAttributeMappings(value.value, props.kind))
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
const rowSourceOptions = (field: AuthAttributeField) =>
  sourceOptions.value.some((option) => option.value === field.source)
    ? sourceOptions.value
    : [
        ...sourceOptions.value,
        { label: t(`authRules.fieldSources.${field.source}`), value: field.source, disabled: true },
      ]
const rowDisabled = (field: AuthAttributeField) => props.disabled || isDerivedAttributeField(field)
const rowProblem = (index: number, field: AuthAttributeField): string | undefined => {
  const issue = issues.value.rows.find((row) => row.index === index)
  return issue
    ? t(`authRules.mappings.${issue.code}`, {
        id: field.field_id,
        kind: t(`authRules.protocolKinds.${props.kind}`),
        source: t(`authRules.fieldSources.${field.source}`),
      })
    : undefined
}
const updateField = (index: number, patch: Partial<AuthAttributeField>) => {
  const field = value.value[index]
  if (!field || rowDisabled(field)) return
  const next = cloneAuthAttributeFields(value.value)
  next[index] = { ...next[index]!, ...patch }
  value.value = next
}
const addField = () => {
  if (props.disabled || value.value.length >= AUTH_ATTRIBUTE_MAX_FIELDS) return
  value.value = [
    ...cloneAuthAttributeFields(value.value),
    createAuthAttributeFieldDraft(props.kind),
  ]
}
const removeField = (index: number) => {
  const field = value.value[index]
  if (!field || rowDisabled(field)) return
  value.value = cloneAuthAttributeFields(value.value.filter((_, row) => row !== index))
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
      <span class="text-sm font-medium text-lf-text-strong"
        >{{ t('authRules.mappings.title') }} · {{ value.length }} /
        {{ AUTH_ATTRIBUTE_MAX_FIELDS }}</span
      >
      <NButton
        size="small"
        :disabled="disabled || value.length >= AUTH_ATTRIBUTE_MAX_FIELDS"
        @click="addField"
      >
        {{ t('authRules.mappings.addField') }}
      </NButton>
    </div>
    <p class="text-xs leading-5 text-lf-text-subtle">{{ t('authRules.mappings.description') }}</p>
    <NAlert v-if="!issues.valid" type="warning" :bordered="false">
      {{
        issues.limitReached
          ? t('authRules.mappings.limitReached', { max: AUTH_ATTRIBUTE_MAX_FIELDS })
          : t('authRules.mappings.invalidHint')
      }}
    </NAlert>
    <p v-if="!value.length" class="py-3 text-sm text-lf-text-muted">
      {{ t('authRules.mappings.empty') }}
    </p>
    <div v-for="(field, index) in value" :key="index" class="lf-panel space-y-3 p-3">
      <div class="flex items-center justify-between gap-2">
        <span class="text-sm font-medium text-lf-text-strong">{{
          field.label || t('authRules.mappings.label')
        }}</span>
        <NTag v-if="isDerivedAttributeField(field)" size="small" :bordered="false">{{
          t('authRules.mappings.derivedTag')
        }}</NTag>
        <NButton v-else size="tiny" quaternary :disabled="disabled" @click="removeField(index)">{{
          t('authRules.mappings.removeField')
        }}</NButton>
      </div>
      <p v-if="isDerivedAttributeField(field)" class="text-xs leading-5 text-lf-text-subtle">
        {{ t('authRules.mappings.derivedHint') }}
        <span v-if="field.derived_from?.length">{{
          t('authRules.mappings.derivedFrom', { fields: field.derived_from.join(', ') })
        }}</span>
      </p>
      <div class="grid gap-x-3 gap-y-2 sm:grid-cols-2">
        <NFormItem :label="t('authRules.mappings.fieldId')" :show-feedback="false">
          <NInput
            :value="field.field_id"
            :disabled="rowDisabled(field)"
            :placeholder="t('authRules.mappings.fieldIdPlaceholder')"
            @update:value="(field_id: string) => updateField(index, { field_id })"
          />
        </NFormItem>
        <NFormItem :label="t('authRules.mappings.label')" :show-feedback="false">
          <NInput
            :value="field.label"
            :disabled="rowDisabled(field)"
            :placeholder="t('authRules.mappings.labelPlaceholder')"
            @update:value="(label: string) => updateField(index, { label })"
          />
        </NFormItem>
        <NFormItem :label="t('authRules.mappings.source')" :show-feedback="false">
          <NSelect
            :value="field.source"
            :disabled="rowDisabled(field)"
            :options="rowSourceOptions(field)"
            @update:value="(source: AuthAttributeSource) => updateField(index, { source })"
          />
        </NFormItem>
        <NFormItem :label="t('authRules.mappings.type')" :show-feedback="false">
          <NSelect
            :value="field.type"
            :disabled="rowDisabled(field)"
            :options="typeOptions"
            @update:value="(type: AuthAttributeType) => updateField(index, { type })"
          />
        </NFormItem>
        <NFormItem :label="t('authRules.mappings.pointer')" :show-feedback="false">
          <NInput
            :value="field.pointer"
            :disabled="rowDisabled(field)"
            :placeholder="t('authRules.mappings.pointerPlaceholder')"
            @update:value="(pointer: string) => updateField(index, { pointer })"
          />
        </NFormItem>
        <NFormItem :label="t('authRules.mappings.requiredScopes')" :show-feedback="false">
          <NDynamicTags
            :value="field.required_scopes ?? []"
            :disabled="rowDisabled(field)"
            @update:value="(required_scopes: string[]) => updateField(index, { required_scopes })"
          />
        </NFormItem>
      </div>
      <div class="flex flex-wrap gap-x-4 gap-y-2">
        <NCheckbox
          :checked="field.optional ?? false"
          :disabled="rowDisabled(field)"
          @update:checked="(optional: boolean) => updateField(index, { optional })"
          >{{ t('authRules.mappings.optional') }}</NCheckbox
        >
        <NCheckbox
          :checked="field.user_editable ?? false"
          :disabled="rowDisabled(field)"
          @update:checked="(user_editable: boolean) => updateField(index, { user_editable })"
          >{{ t('authRules.mappings.userEditable') }}</NCheckbox
        >
      </div>
      <p v-if="rowProblem(index, field)" role="alert" class="text-xs text-lf-danger">
        {{ rowProblem(index, field) }}
      </p>
    </div>
  </div>
</template>
