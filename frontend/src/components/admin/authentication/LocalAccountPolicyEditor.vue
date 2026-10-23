<script setup lang="ts">
import { computed, onMounted, onScopeDispose, ref, shallowRef, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import {
  NAlert,
  NButton,
  NFormItem,
  NInput,
  NModal,
  NSelect,
  NSpin,
  NSwitch,
  NTag,
  NTooltip,
  useDialog,
  useMessage,
} from 'naive-ui'
import { AuthConnectionError } from '@/api/auth-connections'
import {
  fetchLocalAccount,
  fetchLocalAccountAttributes,
  fetchLocalAccountImpact,
  previewLocalAccount,
  updateLocalAccount,
  type LocalAccount,
  type LocalAccountImpact,
  type LocalAccountPolicy,
  type LocalAccountPreview,
} from '@/api/local-account'
import { captureSession, isSessionCurrent, sessionGeneration } from '@/api/session-context'
import { normalizeEmail, validateEmail } from '@/utils/email'
import { extractErrorMessage } from '@/utils/errors'
import AuthImpact from './AuthImpact.vue'
import AuthRuleEditor from './AuthRuleEditor.vue'
import { AUTH_RULE_MAX_CONDITIONS, type AuthRuleSetProblem } from './auth-rule-model'
import {
  cloneLocalPolicy,
  localPolicyDependencyIssues,
  localPolicyDraft,
  localPolicyEqual,
  localSignupRuleProblems,
  type LocalPolicyDependencyIssue,
} from './local-account-model'

const props = defineProps<{ record: LocalAccount }>()
const emit = defineEmits<{
  changed: [record: LocalAccount]
  close: []
  denied: [detail: string]
}>()

const { t } = useI18n()
const message = useMessage()
const dialog = useDialog()
const record = shallowRef<LocalAccount>(props.record)
const policy = ref<LocalAccountPolicy>(localPolicyDraft(props.record))
const issues = shallowRef(props.record.issues)
const catalog = shallowRef<ApiSchemas['AuthAttributeField'][]>([])
const catalogRevision = ref<number | null>(null)
const catalogLoading = ref(false)
const catalogError = ref('')
const catalogGeneration = ref(0)
const impact = shallowRef<LocalAccountImpact | null>(null)
const impactDialog = ref(false)
const impactError = ref('')
const impactLoading = ref(false)
const impactGeneration = ref(0)
const preview = shallowRef<LocalAccountPreview | null>(null)
const previewDialog = ref(false)
const previewError = ref('')
const previewLoading = ref(false)
const previewGeneration = ref(0)
const previewEmail = ref('')
const conflict = ref(false)
const latest = shallowRef<LocalAccount | null>(null)
const minePolicy = shallowRef<LocalAccountPolicy | null>(null)
const unknownWrite = ref<'matched' | 'mismatched' | 'no-readback' | null>(null)
const saveStarted = ref(false)
const saveGeneration = ref(0)
const busy = ref(false)
const ruleValid = ref(true)

let alive = true
let applyingPolicy = false
let catalogToken = 0
let impactToken = 0
let previewToken = 0
let saveToken = 0

type ApiSchemas = import('@/api/client-core').ApiSchemas

onScopeDispose(() => {
  alive = false
  previewEmail.value = ''
  clearDiagnostics()
  catalogToken++
  impactToken++
  previewToken++
  saveToken++
})

onMounted(() => {
  issues.value = props.record.issues
  void loadCatalog()
})

watch(
  sessionGeneration,
  () => {
    catalogToken++
    impactToken++
    previewToken++
    saveToken++
    previewEmail.value = ''
    clearDiagnostics()
    catalogLoading.value = false
    impactLoading.value = false
    previewLoading.value = false
    busy.value = false
    catalog.value = []
    catalogRevision.value = null
    catalogError.value = ''
  },
  { flush: 'sync' },
)

watch(
  previewEmail,
  () => {
    preview.value = null
    previewError.value = ''
    previewGeneration.value++
  },
  { flush: 'sync' },
)

/** Any draft edit invalidates the transient result it was evaluated against. */
watch(
  policy,
  () => {
    if (applyingPolicy) return
    impact.value = null
    preview.value = null
    unknownWrite.value = null
    impactError.value = ''
    previewError.value = ''
    impactDialog.value = false
    previewDialog.value = false
    impactGeneration.value++
    previewGeneration.value++
    saveGeneration.value++
  },
  // Synchronous so the `applyingPolicy` window still holds while the queued record swap lands:
  // a programmatic apply must not read as an operator edit and clear the diagnostics it installs.
  { deep: true, flush: 'sync' },
)

const dirty = computed(() => !localPolicyEqual(policy.value, record.value.policy))
const dependencyIssues = computed(() => localPolicyDependencyIssues(policy.value))
const ruleIssues = computed(() => localSignupRuleProblems(policy.value, catalog.value))
const emailDomainNeedsRequired = computed(() =>
  ruleIssues.value.some(
    (problem) => problem.scope === 'rule' && problem.code === 'emailDomainRequired',
  ),
)
const formValid = computed(
  () =>
    dependencyIssues.value.length === 0 &&
    ruleIssues.value.length === 0 &&
    !emailDomainNeedsRequired.value &&
    ruleValid.value,
)
const catalogFresh = computed(
  () => catalogRevision.value === record.value.revision && !catalogLoading.value,
)
const unknownWriteVisible = computed(() => unknownWrite.value !== null && !conflict.value)
const unknownWriteBlocked = computed(
  () => unknownWrite.value !== null && unknownWrite.value !== 'matched',
)
const canSave = computed(
  () =>
    dirty.value &&
    formValid.value &&
    catalogFresh.value &&
    !catalogError.value &&
    !busy.value &&
    !conflict.value &&
    !unknownWriteBlocked.value &&
    !saveStarted.value,
)
const canAssess = computed(
  () =>
    formValid.value && catalogFresh.value && !catalogError.value && !busy.value && !conflict.value,
)
const previewEmailIssue = computed(() =>
  validateEmail(previewEmail.value, false) === 'invalid' ? 'invalid' : null,
)
const canRunPreview = computed(() => canAssess.value && !previewEmailIssue.value)
const canPreview = computed(() => canRunPreview.value && !previewDialog.value)
const emailRequirementOptions = [
  { label: t('localAccount.optional'), value: 'optional' },
  { label: t('localAccount.required'), value: 'required' },
]

const dependencyLabels: Record<LocalPolicyDependencyIssue, string> = {
  'username-required': 'localAccount.dependencyUsername',
  'password-login-required': 'localAccount.dependencyPasswordLogin',
}
const dependencyMessages = computed(() =>
  dependencyIssues.value.map((issue) => t(dependencyLabels[issue])),
)

function ruleIssueMessage(problem: AuthRuleSetProblem): string {
  switch (problem.code) {
    case 'unknownField':
      return t('authRules.unknownFieldHint')
    case 'emptyConditions':
      return t('authRules.emptyConditions')
    case 'conditionLimitReached':
      return t('authRules.conditionLimitReached', { max: AUTH_RULE_MAX_CONDITIONS })
    default: {
      const detail = problem.index === undefined ? '' : ` · #${problem.index + 1}`
      return `${t('localAccount.ruleIssue')} · ${problem.code}${detail}`
    }
  }
}

const otherRuleIssues = computed(() =>
  ruleIssues.value.filter(
    (problem) => !(problem.scope === 'rule' && problem.code === 'emailDomainRequired'),
  ),
)
const ruleProblemMessages = computed(() =>
  otherRuleIssues.value.map(ruleIssueMessage).filter(Boolean),
)

const problemMessages = computed(() => [
  ...dependencyMessages.value,
  ...(emailDomainNeedsRequired.value ? [t('localAccount.dependencyEmailDomain')] : []),
  ...ruleProblemMessages.value,
])

const passwordLogin = computed({
  get: () => policy.value.password_login_enabled,
  set: (value: boolean) => {
    policy.value = { ...policy.value, password_login_enabled: value }
  },
})
const usernameLogin = computed({
  get: () => policy.value.username_login_enabled,
  set: (value: boolean) => {
    policy.value = { ...policy.value, username_login_enabled: value }
  },
})
const emailLogin = computed({
  get: () => policy.value.email_login_enabled,
  set: () => {
    policy.value = { ...policy.value, email_login_enabled: false }
  },
})
const passwordEnrollment = computed({
  get: () => policy.value.password_enrollment_enabled,
  set: (value: boolean) => {
    policy.value = { ...policy.value, password_enrollment_enabled: value }
  },
})
const signup = computed({
  get: () => policy.value.signup,
  set: (value: LocalAccountPolicy['signup']) => {
    policy.value = { ...policy.value, signup: value }
  },
})
const signupEnabled = computed({
  get: () => signup.value.enabled,
  set: (value: boolean) => {
    signup.value = { ...signup.value, enabled: value }
  },
})

function issueMessage(issue: ApiSchemas['AuthConfigurationIssue']): string {
  if (issue.code === 'required') return t('localAccount.issueRequired')
  return issue.message
}

function clearDiagnostics() {
  issues.value = []
  impact.value = null
  preview.value = null
  unknownWrite.value = null
  impactError.value = ''
  previewError.value = ''
  impactDialog.value = false
  previewDialog.value = false
  impactGeneration.value = 0
  previewGeneration.value = 0
  saveGeneration.value = 0
  saveStarted.value = false
}

function applyRecord(next: LocalAccount) {
  applyingPolicy = true
  record.value = next
  issues.value = next.issues
  policy.value = localPolicyDraft(next)
  conflict.value = false
  latest.value = null
  minePolicy.value = null
  clearDiagnostics()
  applyingPolicy = false
  emit('changed', next)
  void loadCatalog()
}

async function loadCatalog() {
  const snapshot = captureSession()
  const current = ++catalogToken
  catalogGeneration.value = current
  catalogLoading.value = true
  catalogError.value = ''
  try {
    const data = await fetchLocalAccountAttributes(record.value.revision)
    if (!alive || !isSessionCurrent(snapshot) || current !== catalogToken) return
    catalog.value = data.fields
    catalogRevision.value = data.revision
  } catch (cause) {
    if (!alive || !isSessionCurrent(snapshot) || current !== catalogToken) return
    catalog.value = []
    catalogRevision.value = null
    catalogError.value = extractErrorMessage(cause, t('localAccount.requestFailed'))
  } finally {
    if (alive && isSessionCurrent(snapshot) && current === catalogToken)
      catalogLoading.value = false
  }
}

async function assessImpact() {
  if (!canAssess.value) return
  const snapshot = captureSession()
  const current = ++impactToken
  impactGeneration.value = current
  busy.value = true
  impactLoading.value = true
  impactError.value = ''
  impact.value = null
  try {
    const value = await fetchLocalAccountImpact({
      expected_revision: record.value.revision,
      policy: cloneLocalPolicy(policy.value),
    })
    // A draft edit bumps the epoch, so a result that arrives later no longer fits the draft.
    if (!alive || !isSessionCurrent(snapshot) || current !== impactToken) return
    if (impactGeneration.value !== current) return
    impact.value = value
    impactDialog.value = true
  } catch (cause) {
    if (!alive || !isSessionCurrent(snapshot) || current !== impactToken) return
    if (impactGeneration.value !== current) return
    impactError.value = extractErrorMessage(cause, t('localAccount.requestFailed'))
  } finally {
    if (alive && isSessionCurrent(snapshot) && current === impactToken) {
      busy.value = false
      impactLoading.value = false
    }
  }
}

/** The dialog opens on its own; the request only runs from the dialog's run button. */
function openPreview() {
  if (!canPreview.value) return
  previewDialog.value = true
}

async function simulate() {
  if (!canRunPreview.value) return
  const email = normalizeEmail(previewEmail.value)
  const snapshot = captureSession()
  const current = ++previewToken
  previewGeneration.value = current
  busy.value = true
  previewLoading.value = true
  previewError.value = ''
  preview.value = null
  previewDialog.value = true
  try {
    const value = await previewLocalAccount({
      expected_revision: record.value.revision,
      policy: cloneLocalPolicy(policy.value),
      ...(email ? { registration: { email } } : {}),
    })
    if (!alive || !isSessionCurrent(snapshot) || current !== previewToken) return
    if (previewGeneration.value !== current) return
    preview.value = value
    previewDialog.value = true
  } catch (cause) {
    if (!alive || !isSessionCurrent(snapshot) || current !== previewToken) return
    if (previewGeneration.value !== current) return
    previewError.value = extractErrorMessage(cause, t('localAccount.requestFailed'))
  } finally {
    if (alive && isSessionCurrent(snapshot) && current === previewToken) {
      busy.value = false
      previewLoading.value = false
    }
  }
}

function conflictState(mine: LocalAccountPolicy | null, latestRecord: LocalAccount | null) {
  minePolicy.value = mine
  latest.value = latestRecord
  conflict.value = true
}

async function readbackAfterUnknown(
  snapshot: ReturnType<typeof captureSession>,
  desired: LocalAccountPolicy,
): Promise<boolean> {
  try {
    const target = await fetchLocalAccount()
    if (!alive || !isSessionCurrent(snapshot) || saveToken !== saveGeneration.value) return true
    if (localPolicyEqual(target.policy, desired)) {
      applyRecord(target)
      unknownWrite.value = 'matched'
      return true
    }
    latest.value = target
    unknownWrite.value = 'mismatched'
    return true
  } catch {
    if (alive && isSessionCurrent(snapshot)) unknownWrite.value = 'no-readback'
    return true
  }
}

async function save() {
  if (!canSave.value) return
  const snapshot = captureSession()
  const current = ++saveToken
  saveGeneration.value = current
  const desired = cloneLocalPolicy(policy.value)
  const body = { expected_revision: record.value.revision, policy: desired }
  let patchStarted = false
  busy.value = true
  issues.value = []
  saveStarted.value = true
  try {
    const checkedImpact = await fetchLocalAccountImpact({
      expected_revision: body.expected_revision,
      policy: desired,
    })
    if (!alive || !isSessionCurrent(snapshot) || current !== saveToken) return
    impact.value = checkedImpact
    if (!checkedImpact.can_save) {
      impactDialog.value = true
      saveStarted.value = false
      return
    }

    patchStarted = true
    const saved = await updateLocalAccount(body)
    if (!alive || !isSessionCurrent(snapshot) || current !== saveToken) return
    applyRecord(saved)
    message.success(t('localAccount.saved'))
  } catch (cause) {
    if (!alive || !isSessionCurrent(snapshot) || current !== saveToken) return
    if (cause instanceof AuthConnectionError) {
      issues.value = cause.problem?.issues ?? []
      if (cause.problem?.impact) impact.value = cause.problem.impact
      if ([403, 404].includes(cause.status ?? 0)) {
        saveStarted.value = false
        emit('denied', cause.message)
        return
      }
      if (cause.status === 409 || cause.code === 'revision-conflict') {
        let fetched: LocalAccount | null = null
        try {
          fetched = await fetchLocalAccount()
        } catch {
          fetched = null
        }
        if (!alive || !isSessionCurrent(snapshot)) return
        saveStarted.value = false
        conflictState(desired, fetched)
        message.error(t('localAccount.conflictHint'))
        return
      }
      // Only a PATCH that was already sent can leave the write unconfirmed; a failure before
      // it (impact or validation) is an ordinary error and is never an unknown write.
      if (patchStarted && (cause.status === undefined || cause.status >= 500)) {
        await readbackAfterUnknown(snapshot, desired)
        return
      }
      saveStarted.value = false
      if (!patchStarted) impactError.value = cause.message
      message.error(cause.message)
      return
    }
    message.error(extractErrorMessage(cause, t('localAccount.requestFailed')))
    if (patchStarted) {
      await readbackAfterUnknown(snapshot, desired)
      return
    }
    saveStarted.value = false
  } finally {
    if (alive && isSessionCurrent(snapshot) && current === saveToken) busy.value = false
  }
}

function useLatest() {
  if (!latest.value || busy.value) return
  applyRecord(latest.value)
}

async function reapplyMine() {
  if (!latest.value || !minePolicy.value || busy.value) return
  const snapshot = captureSession()
  const target = latest.value
  applyingPolicy = true
  clearDiagnostics()
  record.value = target
  issues.value = target.issues
  policy.value = cloneLocalPolicy(minePolicy.value)
  conflict.value = false
  latest.value = null
  minePolicy.value = null
  applyingPolicy = false
  emit('changed', target)
  await loadCatalog()
  // Only once the catalog matches the new revision: re-read the impact for the full draft at
  // the new revision and stop there. Writing the policy stays a deliberate manual save.
  if (!alive || !isSessionCurrent(snapshot) || conflict.value) return
  await assessImpact()
}

function discardChanges() {
  applyingPolicy = true
  policy.value = localPolicyDraft(record.value)
  conflict.value = false
  latest.value = null
  minePolicy.value = null
  clearDiagnostics()
  applyingPolicy = false
}

function requestClose() {
  if (busy.value) {
    message.warning(t('localAccount.inflightBlocked'))
    return
  }
  if (!dirty.value && !conflict.value) {
    emit('close')
    return
  }
  dialog.warning({
    title: t('localAccount.discardTitle'),
    content: t('localAccount.discardConfirm'),
    positiveText: t('localAccount.discardAndClose'),
    negativeText: t('localAccount.keepEditing'),
    onPositiveClick: () => {
      discardChanges()
      emit('close')
    },
  })
}

defineExpose({ requestClose })
</script>

<template>
  <div class="space-y-4 p-4" data-testid="auth-local-account-editor">
    <p class="text-sm text-lf-text-muted">{{ t('localAccount.editorSubtitle') }}</p>

    <n-alert v-if="conflict" type="warning" data-testid="auth-local-account-conflict">
      <div class="space-y-3">
        <p>{{ t('localAccount.conflictHint') }}</p>
        <div class="flex flex-wrap gap-2">
          <n-button
            :disabled="busy || !latest"
            data-testid="auth-local-account-conflict-latest"
            @click="useLatest"
          >
            {{ t('localAccount.useLatest') }}
          </n-button>
          <n-button
            :disabled="busy || !latest || !minePolicy"
            data-testid="auth-local-account-conflict-reapply"
            @click="reapplyMine"
          >
            {{ t('localAccount.reapplyMine') }}
          </n-button>
        </div>
      </div>
    </n-alert>

    <n-alert
      v-if="unknownWriteVisible"
      type="warning"
      data-testid="auth-local-account-unknown-write"
    >
      <div class="space-y-2">
        <p>
          {{
            unknownWrite === 'matched'
              ? t('localAccount.unknownWriteMatched')
              : unknownWrite === 'mismatched'
                ? t('localAccount.unknownWriteMismatched')
                : t('localAccount.unknownWriteNoReadback')
          }}
        </p>
        <n-button
          v-if="unknownWrite === 'mismatched' && latest"
          :disabled="busy"
          @click="useLatest"
        >
          {{ t('localAccount.latestReadback') }}
        </n-button>
      </div>
    </n-alert>

    <n-alert v-if="issues.length" type="warning" data-testid="auth-local-account-issues">
      <p class="font-medium">{{ t('localAccount.issuesTitle') }}</p>
      <ul class="mt-2 space-y-1 text-sm">
        <li v-for="(issue, index) in issues" :key="`${issue.field}:${issue.code}:${index}`">
          <span class="font-mono text-xs">{{ issue.field }}</span> · {{ issueMessage(issue) }}
        </li>
      </ul>
    </n-alert>

    <n-alert v-if="problemMessages.length" type="warning">
      <ul class="space-y-1">
        <li v-for="(item, index) in problemMessages" :key="index">{{ item }}</li>
      </ul>
    </n-alert>

    <n-alert v-if="impactError" type="error" data-testid="auth-local-account-impact-error">
      {{ impactError }}
    </n-alert>

    <section class="space-y-4 rounded-xl border border-lf-border p-4">
      <div>
        <h2 class="text-base font-semibold">{{ t('localAccount.loginGroup') }}</h2>
        <p class="mt-1 text-sm text-lf-text-muted">{{ t('localAccount.loginHint') }}</p>
      </div>
      <n-form-item :label="t('localAccount.passwordLoginLabel')">
        <n-switch
          v-model:value="passwordLogin"
          aria-label="允许密码登录"
          data-testid="auth-local-account-password-login"
        />
      </n-form-item>
      <n-form-item :label="t('localAccount.usernameLoginLabel')">
        <n-switch
          v-model:value="usernameLogin"
          aria-label="允许用户名定位"
          data-testid="auth-local-account-username-login"
        />
      </n-form-item>
      <n-form-item :label="t('localAccount.emailLoginLabel')">
        <n-switch
          v-model:value="emailLogin"
          disabled
          aria-disabled="true"
          aria-label="允许邮箱定位"
          data-testid="auth-local-account-email-login"
        />
      </n-form-item>
      <p class="-mt-2 text-xs text-lf-text-muted">{{ t('localAccount.emailLoginHint') }}</p>
    </section>

    <section class="space-y-4 rounded-xl border border-lf-border p-4">
      <div>
        <h2 class="text-base font-semibold">{{ t('localAccount.enrollmentGroup') }}</h2>
        <p class="mt-1 text-sm text-lf-text-muted">{{ t('localAccount.enrollmentHint') }}</p>
      </div>
      <n-form-item :label="t('localAccount.enrollmentLabel')">
        <n-switch
          v-model:value="passwordEnrollment"
          aria-label="允许首次设密"
          data-testid="auth-local-account-enrollment"
        />
      </n-form-item>
    </section>

    <section class="space-y-4 rounded-xl border border-lf-border p-4">
      <div>
        <h2 class="text-base font-semibold">{{ t('localAccount.signupGroup') }}</h2>
        <p class="mt-1 text-sm text-lf-text-muted">{{ t('localAccount.signupHint') }}</p>
      </div>
      <n-form-item :label="t('localAccount.signupEnabledLabel')">
        <n-switch
          v-model:value="signupEnabled"
          aria-label="允许本地注册"
          data-testid="auth-local-account-signup"
        />
      </n-form-item>
      <n-form-item :label="t('localAccount.emailRequirementLabel')">
        <n-select
          v-model:value="signup.email_requirement"
          :options="emailRequirementOptions"
          data-testid="auth-local-account-email-requirement"
        />
      </n-form-item>
      <p class="-mt-2 text-xs text-lf-text-muted">{{ t('localAccount.emailRequiredHint') }}</p>
      <div>
        <div class="mb-2 text-sm font-medium">{{ t('localAccount.rulesLabel') }}</div>
        <n-alert v-if="catalogError" type="warning" :bordered="false" size="small">
          {{ t('localAccount.catalogError') }}
          <n-button size="tiny" class="ml-2" @click="loadCatalog">{{
            t('localAccount.retry')
          }}</n-button>
        </n-alert>
        <n-spin :show="catalogLoading || !catalogFresh">
          <AuthRuleEditor
            v-model:value="policy.signup.rules"
            v-model:valid="ruleValid"
            :fields="catalog"
            :disabled="busy || Boolean(catalogError) || !catalogFresh"
            :fields-loading="catalogLoading"
          />
        </n-spin>
      </div>
    </section>

    <n-alert v-if="!policy.password_login_enabled" type="warning">
      {{ t('localAccount.maintenanceHint') }}
    </n-alert>

    <div class="flex flex-wrap items-center gap-3 border-t border-lf-border pt-4">
      <span
        class="inline-flex items-center gap-2 text-sm"
        :class="dirty ? 'text-lf-warning' : 'text-lf-text-muted'"
      >
        <i
          class="inline-block h-2 w-2 rounded-full"
          :class="dirty ? 'bg-lf-warning' : 'bg-lf-border-strong'"
        />
        {{ t(dirty ? 'localAccount.dirty' : 'localAccount.clean') }}
      </span>
      <n-button
        type="primary"
        :disabled="!canSave"
        :loading="busy"
        data-testid="auth-local-account-save"
        @click="save"
      >
        {{ t('localAccount.save') }}
      </n-button>
      <n-tooltip trigger="hover">
        <template #trigger>
          <n-button
            :disabled="!canAssess"
            :loading="impactLoading"
            data-testid="auth-local-account-impact"
            @click="assessImpact"
          >
            {{ t('localAccount.impact') }}
          </n-button>
        </template>
        {{ t('localAccount.impactStale') }}
      </n-tooltip>
      <n-button
        :disabled="!canPreview"
        :loading="previewLoading"
        data-testid="auth-local-account-preview"
        @click="openPreview"
      >
        {{ t('localAccount.preview') }}
      </n-button>
      <n-button :disabled="busy" data-testid="auth-local-account-close" @click="requestClose">
        {{ t('localAccount.close') }}
      </n-button>
    </div>

    <n-modal
      v-model:show="impactDialog"
      preset="card"
      style="width: min(560px, calc(100vw - 32px)); max-height: 90vh; overflow: auto"
      :bordered="false"
      size="small"
    >
      <template #header>
        <span data-testid="auth-local-account-impact-result">{{ t('localAccount.impact') }}</span>
      </template>
      <n-alert v-if="impactError" type="error">{{ impactError }}</n-alert>
      <n-alert v-if="!policy.password_login_enabled" type="warning" class="mb-3">
        {{ t('localAccount.maintenanceHint') }}
      </n-alert>
      <AuthImpact v-if="impact" :impact="impact" />
    </n-modal>

    <n-modal
      v-model:show="previewDialog"
      preset="card"
      style="width: min(560px, calc(100vw - 32px)); max-height: 90vh; overflow: auto"
      :bordered="false"
      size="small"
    >
      <template #header>
        <span data-testid="auth-local-account-preview-result">{{
          t('localAccount.previewResult')
        }}</span>
      </template>
      <div class="space-y-3">
        <n-form-item :label="t('localAccount.previewEmailLabel')">
          <n-input
            v-model:value="previewEmail"
            :disabled="busy"
            :status="previewEmailIssue ? 'error' : undefined"
            data-testid="auth-local-account-preview-email"
            type="text"
            autocomplete="email"
            :placeholder="t('localAccount.previewEmailOptional')"
          />
        </n-form-item>
        <p class="-mt-2 text-xs text-lf-text-muted">{{ t('localAccount.previewHint') }}</p>
        <n-alert v-if="previewError" type="error">{{ previewError }}</n-alert>
        <div class="flex justify-end">
          <n-button
            type="primary"
            :disabled="!canRunPreview"
            :loading="previewLoading"
            data-testid="auth-local-account-preview-run"
            @click="simulate"
          >
            {{ t('localAccount.previewRun') }}
          </n-button>
        </div>
        <template v-if="preview">
          <dl class="grid gap-3 sm:grid-cols-2">
            <div class="rounded-lg border border-lf-border p-3">
              <dt class="text-sm text-lf-text-muted">{{ t('localAccount.signupResult') }}</dt>
              <dd class="mt-1">
                <n-tag :type="preview.signup.status === 'pass' ? 'success' : 'warning'">
                  {{
                    t(
                      preview.signup.status === 'pass'
                        ? 'localAccount.pass'
                        : preview.signup.status === 'fail'
                          ? 'localAccount.fail'
                          : 'localAccount.unknown',
                    )
                  }}
                </n-tag>
              </dd>
            </div>
            <div class="rounded-lg border border-lf-border p-3">
              <dt class="text-sm text-lf-text-muted">{{ t('localAccount.attributes') }}</dt>
              <dd class="mt-1 break-all text-sm">{{ JSON.stringify(preview.attributes) }}</dd>
            </div>
          </dl>
        </template>
      </div>
    </n-modal>
  </div>
</template>
