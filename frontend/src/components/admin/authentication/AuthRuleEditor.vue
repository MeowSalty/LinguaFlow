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
  useDialog,
} from 'naive-ui'
import type { SelectOption } from 'naive-ui'
import type { ApiSchemas } from '@/api/client-core'
import {
  AUTH_RULE_MAX_CONDITIONS,
  AUTH_RULE_MIN_CONDITIONS,
  authDomainRuleTemplate,
  authRuleConditionProblem,
  authRuleDraftsFromSet,
  authRuleOperatorsForField,
  authRuleSetFromDrafts,
  authRuleSetsEqual,
  authRuleValueShapeFor,
  checkAuthRuleValue,
  createAuthRuleConditionDraft,
  defaultAuthRuleValue,
  findAuthAttributeField,
  isDnsDomainAttributeField,
  isOperatorAllowedForField,
  parseAuthRuleText,
  retargetAuthRuleConditionDraft,
  validateAuthRuleDraftState,
  type AuthAttributeField,
  type AuthAttributeType,
  type AuthRuleConditionDraft,
  type AuthRuleConditionProblem,
  type AuthRuleDraftState,
  type AuthRuleOperator,
  type AuthRuleValueError,
} from './auth-rule-model'

const props = defineProps<{
  fields: AuthAttributeField[]
  disabled?: boolean
  fieldsLoading?: boolean
  emailRequirement?: 'optional' | 'required'
}>()

const value = defineModel<ApiSchemas['AuthRuleSet']>('value', { required: true })
/** Emits the live validity of the draft so the parent can gate its save action. */
const valid = defineModel<boolean>('valid', { default: true })

const { t } = useI18n()
const dialog = useDialog()

const state = ref<AuthRuleDraftState>(authRuleDraftsFromSet(value.value))

const operatorLabels: Record<AuthRuleOperator, string> = {
  eq: 'authRules.operators.eq',
  ne: 'authRules.operators.ne',
  in: 'authRules.operators.in',
  not_in: 'authRules.operators.not_in',
  gt: 'authRules.operators.gt',
  gte: 'authRules.operators.gte',
  lt: 'authRules.operators.lt',
  lte: 'authRules.operators.lte',
  contains: 'authRules.operators.contains',
  not_contains: 'authRules.operators.not_contains',
  starts_with: 'authRules.operators.starts_with',
  not_starts_with: 'authRules.operators.not_starts_with',
  ends_with: 'authRules.operators.ends_with',
  not_ends_with: 'authRules.operators.not_ends_with',
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

const fieldSourceLabels: Record<AuthAttributeField['source'], string> = {
  oidc_id_token: 'authRules.fieldSources.oidc_id_token',
  oidc_userinfo: 'authRules.fieldSources.oidc_userinfo',
  oauth2_profile: 'authRules.fieldSources.oauth2_profile',
  derived: 'authRules.fieldSources.derived',
  registration: 'authRules.fieldSources.registration',
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
  invalidEmptyText: 'authRules.invalidEmptyText',
}

const operatorLabel = (operator: AuthRuleOperator): string =>
  operatorLabels[operator] ? t(operatorLabels[operator]) : String(operator)
const fieldTypeLabel = (type: AuthAttributeType): string => t(fieldTypeLabels[type])
const fieldSourceLabel = (field: AuthAttributeField): string =>
  field.source in fieldSourceLabels ? t(fieldSourceLabels[field.source]) : field.source

const problemMessage = (
  problem: AuthRuleConditionProblem,
  field: AuthAttributeField | undefined,
  operator: AuthRuleOperator,
): string => {
  switch (problem) {
    case 'unknownField':
      return t('authRules.unknownFieldHint')
    case 'unknownOperator':
      return t('authRules.unknownOperatorHint', { operator })
    case 'domainOperator':
      return t('authRules.domainOperatorHint')
    case 'invalidDomainValue':
      return t('authRules.invalidDomainValueHint')
    case 'domainLimitReached':
      return t('authRules.domainLimitReachedHint')
    case 'operatorNotAllowed':
      return field
        ? t('authRules.operatorNotAllowed', {
            operator: operatorLabel(operator),
            type: fieldTypeLabel(field.type),
          })
        : t('authRules.operatorNotAllowed', {
            operator: operatorLabel(operator),
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

/** Menu rows show label, machine identity, type and source; the trigger stays label-only. */
const renderFieldOption = (option: SelectOption, selected: boolean): VNodeChild => {
  const label = String(option.label ?? '')
  if (selected) return label
  const field = findAuthAttributeField(props.fields, String(option.value ?? ''))
  const meta = field
    ? `${field.field_id} · ${fieldTypeLabel(field.type)} · ${fieldSourceLabel(field)}`
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

const allProblems = computed(() =>
  validateAuthRuleDraftState(state.value, props.fields, {
    emailRequirement: props.emailRequirement,
  }),
)

const emailDomainRequired = computed(() =>
  allProblems.value.some((problem) => problem.code === 'emailDomainRequired'),
)

const canAddCondition = computed(
  () =>
    !props.disabled &&
    props.fields.some((field) => authRuleOperatorsForField(field).length > 0) &&
    state.value.conditions.length < AUTH_RULE_MAX_CONDITIONS,
)
const domainFields = computed(() => props.fields.filter(isDnsDomainAttributeField))
const templateFieldId = ref<string | null>(null)
const templateField = computed(
  () =>
    domainFields.value.find((field) => field.field_id === templateFieldId.value) ??
    domainFields.value[0],
)
const domainFieldOptions = computed(() =>
  domainFields.value.map((field) => ({ label: field.label, value: field.field_id })),
)
const canInsertDomainTemplate = (kind: 'allow' | 'block') =>
  !props.disabled &&
  !props.fieldsLoading &&
  templateField.value &&
  isOperatorAllowedForField(templateField.value, kind === 'allow' ? 'in' : 'not_in') &&
  (state.value.mode === 'unrestricted' || state.value.conditions.length < AUTH_RULE_MAX_CONDITIONS)

function insertDomainTemplate(kind: 'allow' | 'block') {
  if (!canInsertDomainTemplate(kind) || !templateField.value) return
  const condition = authRuleDraftsFromSet(authDomainRuleTemplate(kind, [], templateField.value))
    .conditions[0]
  if (!condition) return
  const insert = (match: 'all' | 'any') => {
    state.value = {
      mode: 'conditions',
      match,
      conditions:
        state.value.mode === 'conditions' ? [...state.value.conditions, condition] : [condition],
    }
  }
  if (state.value.mode === 'conditions' && state.value.match === 'any') {
    dialog.warning({
      title: t('authRules.domainTemplateAnyTitle'),
      content: t('authRules.domainTemplateAnyHint'),
      positiveText: t('authRules.domainTemplateConvertAll'),
      negativeText: t('authRules.domainTemplateKeepAny'),
      onPositiveClick: () => insert('all'),
      onNegativeClick: () => insert('any'),
    })
  } else insert('all')
}

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

const fieldOf = (draft: AuthRuleConditionDraft): AuthAttributeField | undefined =>
  findAuthAttributeField(props.fields, draft.field_id)

const shapeOf = (draft: AuthRuleConditionDraft) => {
  const field = fieldOf(draft)
  return field ? authRuleValueShapeFor(field, draft.operator) : null
}

const operatorOptions = (draft: AuthRuleConditionDraft) => {
  const field = fieldOf(draft)
  const options = (field ? authRuleOperatorsForField(field) : []).map((operator) => ({
    label: operatorLabel(operator),
    value: operator,
  }))
  if (!field || !isOperatorAllowedForField(field, draft.operator))
    options.push({ label: operatorLabel(draft.operator), value: draft.operator })
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
  const shape = authRuleValueShapeFor(field, operator)
  if (!shape) return
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
  const field = props.fields.find((field) => authRuleOperatorsForField(field).length > 0)
  if (!field) return
  const draft = createAuthRuleConditionDraft(field)
  if (draft) state.value.conditions.push(draft)
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
    const field = props.fields.find((field) => authRuleOperatorsForField(field).length > 0)
    if (field) {
      const draft = createAuthRuleConditionDraft(field)
      if (draft) state.value.conditions.push(draft)
    }
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

    <div v-if="domainFields.length" class="rounded-lg border border-lf-border p-3 space-y-2">
      <p class="text-sm font-medium">{{ t('authRules.domainTemplates') }}</p>
      <NSelect
        :value="templateField?.field_id"
        :options="domainFieldOptions"
        :disabled="disabled || fieldsLoading"
        :aria-label="t('authRules.domainTemplateField')"
        @update:value="(id: string) => (templateFieldId = id)"
      />
      <div class="flex flex-wrap gap-2">
        <NButton
          size="small"
          :disabled="!canInsertDomainTemplate('allow')"
          @click="insertDomainTemplate('allow')"
        >
          {{ t('authRules.domainTemplateAllow') }}
        </NButton>
        <NButton
          size="small"
          :disabled="!canInsertDomainTemplate('block')"
          @click="insertDomainTemplate('block')"
        >
          {{ t('authRules.domainTemplateBlock') }}
        </NButton>
      </div>
      <p class="text-xs text-lf-text-muted">{{ t('authRules.domainTemplateHint') }}</p>
    </div>

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

        <NAlert v-if="emailDomainRequired" type="warning" :bordered="false" size="small">
          {{ t('authRules.emailDomainRequiredHint') }}
        </NAlert>

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

          <p
            v-if="fieldOf(draft) && isDnsDomainAttributeField(fieldOf(draft)!)"
            class="text-xs leading-5 text-lf-text-subtle"
          >
            {{ t('authRules.domainHint') }}
          </p>
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
