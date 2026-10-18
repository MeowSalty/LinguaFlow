<script setup lang="ts">
import { computed, h, ref, watch } from 'vue'
import type { VNodeChild } from 'vue'
import { useI18n } from 'vue-i18n'
import {
  NAlert,
  NButton,
  NDynamicTags,
  NFormItem,
  NInput,
  NRadioButton,
  NRadioGroup,
  NSelect,
  NTag,
} from 'naive-ui'
import type { SelectOption } from 'naive-ui'
import type { ApiSchemas } from '@/api/client-core'
import {
  AUTH_RULE_MAX_CONDITIONS,
  AUTH_RULE_MIN_CONDITIONS,
  authRuleConditionProblem,
  authRuleDraftsFromSet,
  authRuleOperatorsForType,
  authRuleSetFromDrafts,
  authRuleSetsEqual,
  authRuleValueShape,
  checkAuthRuleValue,
  createAuthRuleConditionDraft,
  defaultAuthRuleValue,
  findAuthAttributeField,
  isOperatorAllowedForType,
  parseAuthRuleText,
  retargetAuthRuleConditionDraft,
  validateAuthRuleDraftState,
  type AuthAttributeType,
  type AuthRuleConditionDraft,
  type AuthRuleConditionProblem,
  type AuthRuleDraftState,
  type AuthRuleOperator,
  type AuthRuleValueError,
} from './auth-rule-model'

const props = defineProps<{
  fields: ApiSchemas['AuthAttributeField'][]
  disabled?: boolean
  fieldsLoading?: boolean
}>()

const value = defineModel<ApiSchemas['AuthRuleSet']>('value', { required: true })
/** Emits the live validity of the draft so the parent can gate its save action. */
const valid = defineModel<boolean>('valid', { default: true })

const { t } = useI18n()

const state = ref<AuthRuleDraftState>(authRuleDraftsFromSet(value.value))

const operatorLabels: Record<AuthRuleOperator, string> = {
  eq: 'authRules.operators.eq',
  ne: 'authRules.operators.ne',
  in: 'authRules.operators.in',
  gt: 'authRules.operators.gt',
  gte: 'authRules.operators.gte',
  lt: 'authRules.operators.lt',
  lte: 'authRules.operators.lte',
  contains: 'authRules.operators.contains',
  not_contains: 'authRules.operators.not_contains',
  contains_any: 'authRules.operators.contains_any',
  contains_all: 'authRules.operators.contains_all',
}

const fieldTypeLabels: Record<AuthAttributeType, string> = {
  string: 'authRules.fieldTypes.string',
  boolean: 'authRules.fieldTypes.boolean',
  integer: 'authRules.fieldTypes.integer',
  number: 'authRules.fieldTypes.number',
  datetime: 'authRules.fieldTypes.datetime',
  string_list: 'authRules.fieldTypes.string_list',
}

const valueErrorLabels: Record<AuthRuleValueError, string> = {
  invalidPrecision: 'authRules.invalidPrecision',
  invalidNumberFormat: 'authRules.invalidNumberFormat',
  invalidNonFinite: 'authRules.invalidNonFinite',
  invalidUnsafeInteger: 'authRules.invalidUnsafeInteger',
  invalidNotInteger: 'authRules.invalidNotInteger',
  invalidDatetime: 'authRules.invalidDatetime',
  invalidListEmpty: 'authRules.invalidListEmpty',
  invalidListEntry: 'authRules.invalidListEntry',
  invalidValueShape: 'authRules.invalidValueShape',
}

const operatorLabel = (operator: AuthRuleOperator): string => t(operatorLabels[operator])

const fieldTypeLabel = (type: AuthAttributeType): string => t(fieldTypeLabels[type])

const problemMessage = (
  problem: AuthRuleConditionProblem,
  field: ApiSchemas['AuthAttributeField'] | undefined,
  operator: AuthRuleOperator,
): string => {
  switch (problem) {
    case 'unknownField':
      return t('authRules.unknownFieldHint')
    case 'operatorNotAllowed':
      return field
        ? t('authRules.operatorNotAllowed', {
            operator: operatorLabel(operator),
            type: fieldTypeLabel(field.type),
          })
        : t('authRules.operatorNotAllowed', {
            operator: '',
            type: fieldTypeLabel('string'),
          })
    default:
      return t(valueErrorLabels[problem])
  }
}

const fieldOptions = computed(() =>
  props.fields.map((field) => ({
    label: field.label,
    value: field.field_id,
  })),
)

/** Menu rows show label + machine identity on separate lines; the trigger stays label-only. */
const renderFieldOption = (option: SelectOption, selected: boolean): VNodeChild => {
  const label = String(option.label ?? '')
  if (selected) return label
  const field = findAuthAttributeField(props.fields, String(option.value ?? ''))
  const meta = field
    ? `${field.field_id} · ${fieldTypeLabel(field.type)}`
    : String(option.value ?? '')
  return h('div', { class: 'py-0.5' }, [
    h('div', { class: 'text-sm' }, label),
    h('div', { class: 'font-mono text-xs text-lf-text-muted' }, meta),
  ])
}
/** Keep field_id searchable even though the option label now shows only the human name. */
const filterFieldOption = (pattern: string, option: SelectOption): boolean =>
  `${String(option.label ?? '')} ${String(option.value ?? '')}`
    .toLowerCase()
    .includes(pattern.toLowerCase())

const unknownFieldOptions = computed(() => {
  const known = new Set(props.fields.map((field) => field.field_id))
  const unknown = new Set(
    state.value.conditions
      .map((condition) => condition.field_id)
      .filter((fieldId) => fieldId.length > 0 && !known.has(fieldId)),
  )
  return [...unknown].map((fieldId) => ({
    label: t('authRules.unknownFieldOption', { id: fieldId }),
    value: fieldId,
  }))
})

const selectOptions = computed(() => [...fieldOptions.value, ...unknownFieldOptions.value])

const conditionProblems = computed(() =>
  state.value.conditions.map((draft) => authRuleConditionProblem(draft, props.fields)),
)

const allProblems = computed(() => validateAuthRuleDraftState(state.value, props.fields))
const canAddCondition = computed(
  () =>
    !props.disabled &&
    props.fields.length > 0 &&
    state.value.conditions.length < AUTH_RULE_MAX_CONDITIONS,
)
const canRemoveCondition = computed(
  () =>
    !props.disabled &&
    state.value.conditions.length > AUTH_RULE_MIN_CONDITIONS &&
    state.value.mode === 'conditions',
)

/** List-shaped values (dynamic tags) get the whole row so entries tile horizontally. */
const conditionGridClass = (draft: AuthRuleConditionDraft): string =>
  shapeOf(draft) === 'list'
    ? 'grid gap-2 md:grid-cols-[minmax(0,2.2fr)_minmax(0,1fr)]'
    : 'grid gap-2 md:grid-cols-[minmax(0,2fr)_minmax(0,1fr)_minmax(0,1.3fr)]'

const fieldOf = (draft: AuthRuleConditionDraft): ApiSchemas['AuthAttributeField'] | undefined =>
  findAuthAttributeField(props.fields, draft.field_id)

const shapeOf = (draft: AuthRuleConditionDraft) => {
  const field = fieldOf(draft)
  if (!field || !isOperatorAllowedForType(field.type, draft.operator)) return null
  return authRuleValueShape(field.type, draft.operator)
}

const operatorOptions = (draft: AuthRuleConditionDraft) => {
  const field = fieldOf(draft)
  const operators = field ? authRuleOperatorsForType(field.type) : []
  const options = operators.map((operator) => ({
    label: operatorLabel(operator),
    value: operator,
  }))
  if (!field) options.push({ label: operatorLabel(draft.operator), value: draft.operator })
  return options
}

const isRowDisabled = (draft: AuthRuleConditionDraft): boolean =>
  props.disabled === true || fieldOf(draft) === undefined

const textPlaceholder = (draft: AuthRuleConditionDraft): string => {
  switch (shapeOf(draft)) {
    case 'integer':
      return t('authRules.integerPlaceholder')
    case 'number':
      return t('authRules.numberPlaceholder')
    case 'datetime':
      return t('authRules.datetimePlaceholder')
    default:
      return t('authRules.textPlaceholder')
  }
}

const booleanValue = (draft: AuthRuleConditionDraft): string | null =>
  typeof draft.value === 'boolean' ? String(draft.value) : null
const setBooleanValue = (draft: AuthRuleConditionDraft, next: string): void => {
  draft.value = next === 'true'
  draft.typedValue = true
}
const listValue = (draft: AuthRuleConditionDraft): string[] =>
  Array.isArray(draft.value) ? [...draft.value] : []
const setListValue = (draft: AuthRuleConditionDraft, next: string[]): void => {
  draft.value = [...next]
  draft.typedValue = true
}

const onTextInput = (draft: AuthRuleConditionDraft, text: string): void => {
  draft.text = text
  const shape = shapeOf(draft)
  if (!shape || shape === 'boolean' || shape === 'list') return
  draft.typedValue = false
  const check = parseAuthRuleText(text, shape)
  if (check.ok) draft.value = check.value
}

const onFieldChange = (index: number, fieldId: string): void => {
  const draft = state.value.conditions[index]
  if (!draft) return
  const field = findAuthAttributeField(props.fields, fieldId)
  if (!field) {
    // Unknown reference: keep it visible instead of silently rewriting the condition.
    draft.field_id = fieldId
    return
  }
  state.value.conditions[index] = retargetAuthRuleConditionDraft(draft, field)
}

const onOperatorChange = (index: number, operator: AuthRuleOperator): void => {
  const draft = state.value.conditions[index]
  if (!draft) return
  draft.operator = operator
  const field = fieldOf(draft)
  if (!field) return
  const shape = authRuleValueShape(field.type, operator)
  const typed = shape === 'boolean' || shape === 'list'
  if (typed !== draft.typedValue) {
    const reset = defaultAuthRuleValue(shape)
    draft.value = reset
    draft.text = String(reset)
    draft.typedValue = typed
    return
  }
  if (typed) {
    const check = checkAuthRuleValue(draft.value, shape)
    if (!check.ok) draft.value = defaultAuthRuleValue(shape)
    return
  }
  const check = parseAuthRuleText(draft.text, shape)
  if (check.ok) draft.value = check.value
}

const addCondition = (): void => {
  if (!canAddCondition.value) return
  const field = props.fields[0]
  if (!field) return
  state.value.conditions.push(createAuthRuleConditionDraft(field))
}

const removeCondition = (index: number): void => {
  if (!canRemoveCondition.value) return
  state.value.conditions.splice(index, 1)
}

// Keep the draft seed valid: a rule set switching to `conditions` always has ≥1 condition.
watch(
  () => state.value.mode,
  (mode) => {
    if (mode !== 'conditions' || state.value.conditions.length > 0) return
    const field = props.fields[0]
    if (field) state.value.conditions.push(createAuthRuleConditionDraft(field))
  },
  { flush: 'sync' },
)

watch(
  () => value.value,
  (next) => {
    if (authRuleSetsEqual(authRuleSetFromDrafts(state.value), next)) return
    state.value = authRuleDraftsFromSet(next)
  },
  { deep: true, flush: 'sync' },
)

watch(
  () => authRuleSetFromDrafts(state.value),
  (next) => {
    if (authRuleSetsEqual(next, value.value)) return
    value.value = next
  },
  { flush: 'sync' },
)

watch(
  allProblems,
  (problems) => {
    valid.value = problems.length === 0
  },
  { immediate: true, flush: 'sync' },
)

/**
 * Pre-submit guard for the parent. Invalid drafts stay editable (and `valid` stays false),
 * while the model retains the last valid typed value for invalid text input.
 */
const validate = (): {
  valid: boolean
  problems: ReturnType<typeof validateAuthRuleDraftState>
} => ({
  valid: allProblems.value.length === 0,
  problems: allProblems.value,
})

defineExpose({ validate })
</script>

<template>
  <div class="space-y-4">
    <NFormItem :label="t('authRules.modeLabel')" :show-feedback="false">
      <div class="space-y-2">
        <NRadioGroup
          :value="state.mode"
          :disabled="disabled"
          @update:value="(mode: 'unrestricted' | 'conditions') => (state.mode = mode)"
        >
          <NRadioButton value="unrestricted">{{ t('authRules.modeUnrestricted') }}</NRadioButton>
          <NRadioButton value="conditions">{{ t('authRules.modeConditions') }}</NRadioButton>
        </NRadioGroup>
        <p class="text-xs leading-5 text-lf-text-subtle">
          {{
            state.mode === 'unrestricted'
              ? t('authRules.modeUnrestrictedHint')
              : t('authRules.matchLabel')
          }}
        </p>
      </div>
    </NFormItem>

    <template v-if="state.mode === 'conditions'">
      <NFormItem :label="t('authRules.matchLabel')" :show-feedback="false">
        <NRadioGroup
          :value="state.match"
          :disabled="disabled"
          size="small"
          @update:value="(match: 'all' | 'any') => (state.match = match)"
        >
          <NRadioButton value="all">{{ t('authRules.matchAll') }}</NRadioButton>
          <NRadioButton value="any">{{ t('authRules.matchAny') }}</NRadioButton>
        </NRadioGroup>
      </NFormItem>

      <div class="lf-panel space-y-3 p-3">
        <div class="flex flex-wrap items-center justify-between gap-2">
          <div class="flex items-center gap-2">
            <span class="text-sm font-medium text-lf-text-strong">
              {{ t('authRules.conditionsLabel') }}
            </span>
            <NTag size="small" :bordered="false">
              {{
                t('authRules.conditionCount', {
                  count: state.conditions.length,
                  max: AUTH_RULE_MAX_CONDITIONS,
                })
              }}
            </NTag>
          </div>
          <NButton
            size="small"
            :loading="fieldsLoading"
            :disabled="!canAddCondition"
            @click="addCondition"
          >
            {{ t('authRules.addCondition') }}
          </NButton>
        </div>

        <NAlert v-if="allProblems.length > 0" type="warning" :bordered="false" size="small">
          {{ t('authRules.invalidDurationHint') }}
        </NAlert>

        <NAlert
          v-if="fields.length === 0 && !fieldsLoading"
          type="info"
          :bordered="false"
          size="small"
        >
          {{ t('authRules.emptyFieldsHint') }}
        </NAlert>
        <NAlert
          v-else-if="fields.length > 0 && state.conditions.length === 0"
          type="info"
          :bordered="false"
          size="small"
        >
          {{ t('authRules.emptyConditions') }}
        </NAlert>

        <div
          v-for="(draft, index) in state.conditions"
          :key="index"
          class="space-y-2 rounded-lf-ctl border border-lf-border-soft bg-lf-surface-muted/50 p-3"
        >
          <div class="flex items-center justify-between gap-2">
            <span class="text-xs font-medium text-lf-text-muted">
              {{ t('authRules.conditionIndex', { index: index + 1 }) }}
            </span>
            <NButton
              size="tiny"
              quaternary
              :disabled="!canRemoveCondition"
              @click="removeCondition(index)"
            >
              {{ t('authRules.removeCondition') }}
            </NButton>
          </div>

          <div :class="conditionGridClass(draft)">
            <div class="min-w-0" :title="fieldOf(draft)?.label ?? ''">
              <NSelect
                :value="draft.field_id"
                :options="selectOptions"
                :disabled="disabled"
                :placeholder="t('authRules.fieldPlaceholder')"
                filterable
                :consistent-menu-width="false"
                :render-label="renderFieldOption"
                :filter="filterFieldOption"
                @update:value="(fieldId: string) => onFieldChange(index, fieldId)"
              />
            </div>
            <NSelect
              :value="draft.operator"
              :options="operatorOptions(draft)"
              :disabled="isRowDisabled(draft)"
              :placeholder="t('authRules.operatorLabel')"
              @update:value="(operator: AuthRuleOperator) => onOperatorChange(index, operator)"
            />
            <NSelect
              v-if="shapeOf(draft) === 'boolean'"
              :value="booleanValue(draft)"
              :disabled="isRowDisabled(draft)"
              :options="[
                { label: t('authRules.booleanTrue'), value: 'true' },
                { label: t('authRules.booleanFalse'), value: 'false' },
              ]"
              @update:value="(next: string) => setBooleanValue(draft, next)"
            />
            <div v-else-if="shapeOf(draft) === 'list'" class="col-span-full min-w-0">
              <NDynamicTags
                :value="listValue(draft)"
                :disabled="isRowDisabled(draft)"
                :placeholder="t('authRules.listPlaceholder')"
                @update:value="(next: string[]) => setListValue(draft, next)"
              />
            </div>
            <NInput
              v-else
              :value="shapeOf(draft) ? draft.text : JSON.stringify(draft.value)"
              :disabled="isRowDisabled(draft)"
              :placeholder="textPlaceholder(draft)"
              @update:value="(text: string) => onTextInput(draft, text)"
            />
          </div>

          <p v-if="shapeOf(draft) === 'datetime'" class="text-xs leading-5 text-lf-text-subtle">
            {{ t('authRules.datetimeHint') }}
          </p>

          <p v-if="conditionProblems[index]" class="text-xs leading-5 text-lf-danger" role="alert">
            {{ problemMessage(conditionProblems[index]!, fieldOf(draft), draft.operator) }}
          </p>
        </div>
      </div>
    </template>
  </div>
</template>
