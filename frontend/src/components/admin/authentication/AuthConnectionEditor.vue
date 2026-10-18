<script setup lang="ts">
import {
  computed,
  onMounted,
  onScopeDispose,
  ref,
  shallowRef,
  watch,
  type InputHTMLAttributes,
} from 'vue'
import { useI18n } from 'vue-i18n'
import {
  NAlert,
  NButton,
  NCard,
  NCollapse,
  NCollapseItem,
  NForm,
  NFormItem,
  NInput,
  NInputNumber,
  NModal,
  NPopconfirm,
  NSelect,
  NSpace,
  NSpin,
  NSwitch,
  NTag,
  useDialog,
  useMessage,
} from 'naive-ui'
import type { ApiSchemas } from '@/api/client-core'
import {
  archiveAuthConnection,
  AuthConnectionError,
  checkAuthConnection,
  configureAuthConnection,
  disableAuthConnection,
  enableAuthConnection,
  fetchAuthConnection,
  fetchAuthConnectionAttributes,
  fetchAuthConnectionImpact,
  fetchAuthConnectionLogo,
  previewAuthConnection,
  previewAuthConnectionAttributes,
  restoreAuthConnection,
  updateAuthConnection,
  uploadAuthConnectionLogo,
  type AuthAttributeField,
  type AuthConnection,
  type AuthConnectionCheck,
  type AuthConnectionImpact,
  type AuthConnectionChanges,
} from '@/api/auth-connections'
import { captureSession, isSessionCurrent, type SessionSnapshot } from '@/api/session-context'
import {
  applyConnectionTestEvidence,
  matchConnectionTest,
  startConnectionTest,
  testEvidenceFields,
  type ConnectionTestEvidence,
  type ConnectionTestHandle,
} from '@/auth/connection-test'
import AuthAttributeMappings from './AuthAttributeMappings.vue'
import AuthDiagnostics from './AuthDiagnostics.vue'
import AuthImpact from './AuthImpact.vue'
import AuthProtocolFields from './AuthProtocolFields.vue'
import AuthRuleEditor from './AuthRuleEditor.vue'
import {
  clone,
  connectionChanges,
  connectionForm,
  connectionFormValid,
  connectionProtocolChanged,
  connectionTestCandidate,
  hasUnsafeRuleNumbers,
  mergeDraftAttributes,
  parsePreviewSources,
  stableJson,
  type SecretMode,
} from './auth-connection-model'
import { validateAuthAttributeMappings, type AuthConnectionKind } from './auth-attribute-catalog'

const props = defineProps<{ connection: AuthConnection }>()
const emit = defineEmits<{
  changed: [record: AuthConnection]
  close: []
  denied: [detail: string]
}>()
const { t } = useI18n()
const message = useMessage()
const dialog = useDialog()
const record = shallowRef(props.connection)
const form = ref(connectionForm(props.connection))
const secret = ref('')
const secretMode = ref<SecretMode>('keep')
const busy = ref(false)
const testing = ref(false)
const issues = shallowRef<ApiSchemas['AuthConfigurationIssue'][]>([])
const conflict = ref(false)
const latest = shallowRef<AuthConnection | null>(null)
const conflictChanges = shallowRef<AuthConnectionChanges | null>(null)
const check = shallowRef<AuthConnectionCheck | null>(null)
const checkDialog = ref(false)
const impact = shallowRef<AuthConnectionImpact | null>(null)
const impactDialog = ref(false)
const preview = shallowRef<ApiSchemas['AuthRulesPreview'] | null>(null)
const catalog = shallowRef<AuthAttributeField[]>([])
const catalogLoading = ref(false)
const catalogError = ref('')
const mappingsValid = ref(true)
const ruleValid = ref({ login: true, signup: true, link: true })
const sources = ref(`{
  "oidc_id_token": {},
  "oidc_userinfo": {}
}`)
const logoInput = ref<HTMLInputElement | null>(null)
const logoPreview = ref('')
let logoRequest = 0
let catalogRequest = 0
let testHandle: ConnectionTestHandle | null = null
let testSequence = 0
let sentTestId = ''
let alive = true
let applyingRecord = false

const external = computed(() => record.value.kind !== 'password')
const archived = computed(() => record.value.archived)
const state = computed(() => record.value.state)
const revision = computed(() => record.value.aggregate_revision)
const unsafeNumbers = computed(
  () => hasUnsafeRuleNumbers(form.value) || hasUnsafeRuleNumbers(record.value.configuration),
)
const savedForm = computed(() => connectionForm(record.value))
const presentationDirty = computed(
  () =>
    form.value.display_name.trim() !== savedForm.value.display_name ||
    form.value.description !== savedForm.value.description ||
    form.value.display_order !== savedForm.value.display_order,
)
const configurationDirty = computed(() => {
  try {
    return (
      Object.keys(
        connectionChanges(
          record.value,
          form.value,
          secretMode.value,
          secret.value,
          'configuration',
        ),
      ).length > 0
    )
  } catch {
    return true
  }
})
const secretDirty = computed(() => secretMode.value !== 'keep')
const dirty = computed(
  () => presentationDirty.value || configurationDirty.value || secretDirty.value,
)
/** Presentation stays editable while unsafe rule numbers only block protocol groups. */
const locked = computed(() => busy.value || archived.value || !external.value)
const protocolLocked = computed(() => locked.value || unsafeNumbers.value)
const fields = computed(() =>
  mergeDraftAttributes(
    catalog.value,
    savedForm.value.attribute_mappings ?? [],
    form.value.attribute_mappings ?? [],
  ),
)
const purposes = ['login', 'signup', 'link'] as const
const secretOptions = computed(() => [
  { label: t('authAdmin.secretKeep'), value: 'keep' },
  { label: t('authAdmin.secretReplace'), value: 'replace' },
  { label: t('authAdmin.secretClear'), value: 'clear' },
])
const candidateChanges = computed<AuthConnectionChanges>(() => {
  try {
    return connectionChanges(
      record.value,
      form.value,
      secretMode.value,
      secret.value,
      'configuration',
    )
  } catch {
    return {}
  }
})
const candidateValid = computed(() => {
  try {
    connectionChanges(record.value, form.value, secretMode.value, secret.value, 'configuration')
    return true
  } catch {
    return false
  }
})
const mappingCheck = computed(() =>
  validateAuthAttributeMappings(form.value.attribute_mappings ?? [], externalKind.value),
)
const rulesValid = computed(() => Object.values(ruleValid.value).every(Boolean))
/** Password renders read-only; externalKind narrows the union for protocol-only components. */
const externalKind = computed<AuthConnectionKind>(() =>
  record.value.kind === 'password' ? 'oidc' : record.value.kind,
)
/** Effective purposes render only when at least one is enabled; an empty row is dead weight. */
const hasCapabilities = computed(
  () =>
    record.value.capabilities.can_login ||
    record.value.capabilities.can_signup ||
    record.value.capabilities.can_link,
)
/** Raw validator fields map to localized labels; unknown identifiers fall back untouched. */
function setupIssueLabel(field: string): string {
  switch (field) {
    case 'oidc.issuer':
      return t('authAdmin.issuer')
    case 'oidc.client_id':
    case 'oauth2.client_id':
      return t('authAdmin.clientId')
    case 'oidc.client_secret':
    case 'client_secret':
      return t('authAdmin.secret')
    case 'oauth2.authorization_endpoint':
      return t('authAdmin.authorizationEndpoint')
    case 'oauth2.token_endpoint':
      return t('authAdmin.tokenEndpoint')
    case 'oauth2.profile_endpoint':
      return t('authAdmin.profileEndpoint')
    case 'oauth2.subject_mapping.pointer':
      return t('authAdmin.subjectPointer')
    case 'server.authentication.public_base_url':
      return t('authAdmin.serverPublicBaseUrl')
    case 'server.authentication.completion_urls':
      return t('authAdmin.completionUrls')
    default:
      return field
  }
}
function setupIssueMessage(issue: ApiSchemas['AuthConfigurationIssue']): string {
  if (issue.code === 'required') return t('authAdmin.issueRequired')
  return issue.message
}
/** Editable fields resolve to in-form anchors; server-side fields stay report-only. */
const setupFieldAnchors: Record<string, string> = {
  'oidc.issuer': 'oidc.issuer',
  'oidc.client_id': 'oidc.client_id',
  'oidc.client_secret': 'client_secret',
  'oauth2.authorization_endpoint': 'oauth2.authorization_endpoint',
  'oauth2.token_endpoint': 'oauth2.token_endpoint',
  'oauth2.profile_endpoint': 'oauth2.profile_endpoint',
  'oauth2.client_id': 'oauth2.client_id',
  'oauth2.subject_mapping.pointer': 'oauth2.subject_mapping.pointer',
  client_secret: 'client_secret',
}
const setupChecklist = computed(() =>
  record.value.setup.protocol.required_fields.map((field) => ({
    field,
    label: setupIssueLabel(field),
    missing: record.value.setup.issues.some(
      (issue) => issue.field === field && issue.code === 'required',
    ),
    clickable: field in setupFieldAnchors,
  })),
)
function scrollToSetupField(field: string) {
  const anchor = setupFieldAnchors[field]
  if (!anchor) return
  document.querySelector(`[data-config-field="${anchor}"]`)?.scrollIntoView({
    behavior: 'smooth',
    block: 'center',
  })
}
/** Issue rows whose field resolves to an in-form anchor close the dialog and jump to the field. */
function onIssueClick(issue: ApiSchemas['AuthConfigurationIssue']) {
  if (!(issue.field in setupFieldAnchors)) return
  checkDialog.value = false
  scrollToSetupField(issue.field)
}
const rootEl = ref<HTMLElement | null>(null)
const activeAnchor = ref('setup')
let scrollRoot: HTMLElement | null = null
let spyFrame = 0
const tocItems = computed(() => {
  const items = [
    { id: 'setup', label: t('authAdmin.setup') },
    { id: 'presentation', label: t('authAdmin.presentationTitle') },
    { id: 'protocol', label: t('authAdmin.protocolTitle') },
    { id: 'mappings', label: t('authAdmin.mappings') },
    { id: 'policy', label: t('authAdmin.policy') },
    { id: 'test', label: t('authAdmin.test') },
  ]
  if (record.value.kind === 'oidc') items.push({ id: 'preview', label: t('authAdmin.preview') })
  return items
})
function resolveScrollRoot(): HTMLElement | null {
  let node = rootEl.value?.parentElement ?? null
  while (node && node !== document.body) {
    const style = window.getComputedStyle(node)
    if (style.overflowY === 'auto' || style.overflowY === 'scroll') return node
    node = node.parentElement
  }
  return null
}
function spy() {
  const root = rootEl.value
  const container = scrollRoot
  if (!root || !container) return
  const limit = container.getBoundingClientRect().top + 140
  const atBottom = container.scrollTop + container.clientHeight >= container.scrollHeight - 4
  if (atBottom) {
    activeAnchor.value = tocItems.value[tocItems.value.length - 1]?.id ?? 'setup'
    return
  }
  let found = tocItems.value[0]?.id ?? ''
  for (const item of tocItems.value) {
    const section = root.querySelector<HTMLElement>(`[data-anchor="${item.id}"]`)
    if (section && section.getBoundingClientRect().top <= limit) found = item.id
  }
  activeAnchor.value = found
}
function onScrollSpy() {
  if (spyFrame) return
  spyFrame = requestAnimationFrame(() => {
    spyFrame = 0
    spy()
  })
}
function scrollToAnchor(id: string) {
  rootEl.value
    ?.querySelector<HTMLElement>(`[data-anchor="${id}"]`)
    ?.scrollIntoView({ behavior: 'smooth', block: 'start' })
}
onMounted(() => {
  scrollRoot = resolveScrollRoot()
  scrollRoot?.addEventListener('scroll', onScrollSpy, { passive: true })
  spy()
})
onScopeDispose(() => {
  scrollRoot?.removeEventListener('scroll', onScrollSpy)
  if (spyFrame) cancelAnimationFrame(spyFrame)
})
/** data-testid keeps full input element placement without tripping Vue's excess-property check. */
function inputProps(
  id: string,
  attrs: InputHTMLAttributes = {},
): InputHTMLAttributes & { 'data-testid': string } {
  return { ...attrs, 'data-testid': id }
}
/** Protocol validation keeps the saved presentation values so display edits never block a configuration save. */
function protocolValidationForm(): typeof form.value {
  return {
    ...form.value,
    display_name: savedForm.value.display_name,
    display_order: savedForm.value.display_order,
  }
}
const partialValid = computed(() =>
  connectionFormValid(record.value.kind, protocolValidationForm(), false),
)
const completeValid = computed(
  () =>
    connectionFormValid(record.value.kind, protocolValidationForm(), true) &&
    mappingCheck.value.valid &&
    rulesValid.value,
)
const deploymentReady = computed(
  () =>
    Boolean(record.value.setup.callback_url && record.value.setup.completion_urls.length) &&
    !record.value.setup.issues.some(
      (issue) => issue.field.startsWith('server.') || issue.field.startsWith('credentials.'),
    ),
)
const credentialReady = computed(() =>
  secretMode.value === 'replace'
    ? Boolean(secret.value.length)
    : secretMode.value === 'keep' && form.value.has_client_secret,
)
const canSavePresentation = computed(
  () =>
    !locked.value &&
    !conflict.value &&
    presentationDirty.value &&
    Boolean(form.value.display_name.trim()),
)
const canSaveConfiguration = computed(() => {
  if (
    !external.value ||
    locked.value ||
    unsafeNumbers.value ||
    conflict.value ||
    !configurationDirty.value ||
    !candidateValid.value
  )
    return false
  if (
    catalogLoading.value ||
    Boolean(catalogError.value) ||
    !mappingCheck.value.valid ||
    !rulesValid.value
  )
    return false
  return state.value === 'enabled' ? completeValid.value : partialValid.value
})
const canCheck = computed(
  () =>
    external.value &&
    !archived.value &&
    !busy.value &&
    !conflict.value &&
    !unsafeNumbers.value &&
    candidateValid.value,
)
const canStartTest = computed(
  () =>
    external.value &&
    !archived.value &&
    !testing.value &&
    !busy.value &&
    !conflict.value &&
    !unsafeNumbers.value &&
    candidateValid.value &&
    deploymentReady.value &&
    credentialReady.value &&
    ['configuring', 'enabled'].includes(state.value ?? '') &&
    completeValid.value,
)
const evidenceNow = ref(Date.now())
const evidenceExpiryTimer = setInterval(() => {
  evidenceNow.value = Date.now()
}, 1000)
onScopeDispose(() => clearInterval(evidenceExpiryTimer))
/** Persisted capability summaries are separate from live, candidate-bound test evidence. */
const currentEvidence = computed(() => {
  const ev = evidence.value
  if (!ev || ev.cancelled || !candidateValid.value || Date.parse(ev.expiresAt) <= evidenceNow.value)
    return null
  return matchConnectionTest(ev, record.value, candidateChanges.value, 'enable') ? ev : null
})
const evidence = shallowRef<ConnectionTestEvidence | null>(null)
const testStale = computed(() => Boolean(evidence.value && !currentEvidence.value))
const currentTestResult = computed(() => currentEvidence.value?.result ?? null)

function formatTime(value: string) {
  const date = new Date(value)
  return Number.isFinite(date.getTime()) ? date.toLocaleString() : value
}
const appliedTestSummary = computed(() => {
  const validation = record.value.validation
  if (!validation.applied_test_id) return t('authAdmin.neverTested')
  const fresh = t(
    validation.fresh_supported ? 'authAdmin.freshSupported' : 'authAdmin.freshUnsupported',
  )
  return `${t('authAdmin.lastTest')} · ${fresh}${validation.tested_at ? ` · ${formatTime(validation.tested_at)}` : ''}`
})
function clearDiagnostics() {
  check.value = null
  impact.value = null
  preview.value = null
  issues.value = []
}

function releaseLogo() {
  if (logoPreview.value) URL.revokeObjectURL(logoPreview.value)
  logoPreview.value = ''
}

onScopeDispose(() => {
  alive = false
  secret.value = ''
  sources.value = ''
  catalogRequest++
  logoRequest++
  testSequence++
  releaseLogo()
  if (testHandle) void testHandle.cancel()
})

watch(
  () =>
    candidateValid.value
      ? stableJson(connectionTestCandidate(record.value, candidateChanges.value))
      : 'invalid-candidate',
  () => {
    if (applyingRecord) return
    clearDiagnostics()
    if (testHandle) void testHandle.cancel()
  },
  { flush: 'sync' },
)
watch(secretMode, (mode) => {
  if (mode !== 'replace') secret.value = ''
})

async function loadCatalog() {
  if (!external.value) return
  const context = captureSession()
  const current = ++catalogRequest
  catalogLoading.value = true
  catalogError.value = ''
  try {
    const data = await fetchAuthConnectionAttributes(record.value.id, revision.value)
    if (!alive || !isSessionCurrent(context) || current !== catalogRequest) return
    catalog.value = data.fields
  } catch (cause) {
    if (alive && isSessionCurrent(context) && current === catalogRequest)
      catalogError.value = cause instanceof Error ? cause.message : t('authAdmin.catalogFailed')
  } finally {
    if (alive && isSessionCurrent(context) && current === catalogRequest)
      catalogLoading.value = false
  }
}

async function loadLogo() {
  const current = ++logoRequest
  const context = captureSession()
  releaseLogo()
  if (!external.value || !record.value.configuration.logo_url) return
  try {
    const image = await fetchAuthConnectionLogo(record.value.id)
    if (alive && current === logoRequest && isSessionCurrent(context))
      logoPreview.value = URL.createObjectURL(image)
  } catch {
    // Logo preview is optional and does not invalidate configuration.
  }
}

function applyRecord(
  next: AuthConnection,
  preservePresentation = false,
  preserveConfiguration = false,
) {
  applyingRecord = true
  const current = clone(form.value)
  record.value = next
  const fresh = connectionForm(next)
  if (preservePresentation) {
    fresh.display_name = current.display_name
    fresh.description = current.description
    fresh.display_order = current.display_order
  }
  if (preserveConfiguration) {
    fresh.oidc = clone(current.oidc)
    fresh.oauth2 = clone(current.oauth2)
    fresh.policy = clone(current.policy)
    fresh.attribute_mappings = clone(current.attribute_mappings)
    fresh.evidence_max_age_seconds = current.evidence_max_age_seconds
    fresh.logo_url = connectionForm(next).logo_url
  }
  form.value = fresh
  secret.value = preserveConfiguration ? secret.value : ''
  if (!preserveConfiguration) secretMode.value = 'keep'
  conflict.value = false
  latest.value = null
  conflictChanges.value = null
  sentTestId = ''
  clearDiagnostics()
  if (next.archived || next.state === 'disabled') {
    if (testHandle) void testHandle.cancel()
    evidence.value = null
  }
  const applied = applyConnectionTestEvidence(evidence.value, next)
  if (applied) evidence.value = applied
  else if (
    evidence.value &&
    !matchConnectionTest(evidence.value, next, candidateChanges.value, 'enable')
  ) {
    if (testHandle) void testHandle.cancel()
    evidence.value = null
  }
  applyingRecord = false
  emit('changed', next)
  void loadCatalog()
  void loadLogo()
}

interface RunGuard {
  write?: boolean
  changes?: AuthConnectionChanges | null
  state?: AuthConnection['state']
  archived?: boolean
}

async function run<T>(action: () => Promise<T>, apply: (value: T) => void, guard?: RunGuard) {
  if (busy.value) return
  const context = captureSession()
  busy.value = true
  issues.value = []
  try {
    const value = await action()
    if (!alive) return
    if (!isSessionCurrent(context)) return
    apply(value)
  } catch (cause) {
    await handleError(cause, context, guard)
  } finally {
    if (alive && isSessionCurrent(context)) busy.value = false
  }
}

const sameJson = (a: unknown, b: unknown): boolean => stableJson(a) === stableJson(b)

function writeConfirmedApplied(
  target: AuthConnection,
  changes: AuthConnectionChanges,
  guard?: RunGuard,
): boolean {
  if (target.id !== record.value.id || target.aggregate_revision !== revision.value + 1)
    return false
  const cfg = target.configuration
  let checkable = false
  if (guard?.state !== undefined) {
    checkable = true
    if (target.state !== guard.state) return false
  }
  if (guard?.archived !== undefined) {
    checkable = true
    if (target.archived !== guard.archived) return false
  }
  if (changes.display_name !== undefined) {
    checkable = true
    if (changes.display_name !== cfg.display_name) return false
  }
  if (changes.description !== undefined) {
    checkable = true
    if (changes.description !== cfg.description) return false
  }
  if (changes.display_order !== undefined) {
    checkable = true
    if (changes.display_order !== cfg.display_order) return false
  }
  if (changes.oidc !== undefined && record.value.kind === 'oidc') {
    checkable = true
    if (!sameJson(changes.oidc, cfg.oidc)) return false
  }
  if (changes.oauth2 !== undefined && record.value.kind === 'generic_oauth2') {
    checkable = true
    if (!sameJson(changes.oauth2, cfg.oauth2)) return false
  }
  if (changes.policy !== undefined) {
    checkable = true
    if (!sameJson(changes.policy, cfg.policy)) return false
  }
  if (changes.attribute_mappings !== undefined) {
    checkable = true
    if (!sameJson(changes.attribute_mappings, cfg.attribute_mappings)) return false
  }
  if (changes.evidence_max_age_seconds !== undefined) {
    checkable = true
    if (changes.evidence_max_age_seconds !== cfg.evidence_max_age_seconds) return false
  }
  if ('client_secret' in changes) {
    checkable = true
    if (changes.client_secret === null) {
      if (cfg.has_client_secret) return false
    } else {
      // An applied unique test identifies the atomic secret promotion without revealing the secret.
      if (!sentTestId || target.validation.applied_test_id !== sentTestId) return false
    }
  }
  if (sentTestId) {
    checkable = true
    if (target.validation.applied_test_id !== sentTestId) return false
  }
  return checkable
}

async function resolveWriteAmbiguity(context: SessionSnapshot, guard: RunGuard) {
  const changes = guard.changes ?? null
  let target: AuthConnection | null = null
  try {
    target = await fetchAuthConnection(record.value.id)
  } catch {
    target = null
  }
  if (!alive || !isSessionCurrent(context)) return
  if (target && writeConfirmedApplied(target, changes ?? {}, guard)) {
    const fields = Object.keys(changes ?? {})
    const presentationWrite = fields.some((field) =>
      ['display_name', 'description', 'display_order'].includes(field),
    )
    const configurationWrite = fields.some(
      (field) => !['display_name', 'description', 'display_order'].includes(field),
    )
    const stateWrite = guard.state !== undefined || guard.archived !== undefined
    applyRecord(target, !stateWrite && !presentationWrite, !stateWrite && !configurationWrite)
    message.success(t('authAdmin.saved'))
    return
  }
  if (target) applyRecord(target, true, true)
  message.error(t('authAdmin.transportUncertain'))
}

async function handleError(cause: unknown, context: SessionSnapshot, guard?: RunGuard) {
  if (!alive || !isSessionCurrent(context)) return
  if (cause instanceof AuthConnectionError) {
    issues.value = cause.problem?.issues ?? []
    if (cause.problem?.impact) impact.value = cause.problem.impact
    if ([403, 404].includes(cause.status ?? 0)) {
      secret.value = ''
      emit('denied', cause.message)
      return
    }
    if (cause.code === 'revision-conflict') {
      message.error(cause.message)
      conflictChanges.value = clone(guard?.changes ?? {})
      let fetched: AuthConnection | null = null
      try {
        fetched = await fetchAuthConnection(record.value.id)
      } catch {
        fetched = null
      }
      if (!alive || !isSessionCurrent(context)) return
      latest.value = fetched
      conflict.value = true
      return
    }
    if (
      ['test-expired', 'test-mismatch', 'test-required', 'state-conflict'].includes(
        cause.code ?? '',
      )
    ) {
      if (testHandle) void testHandle.cancel()
      evidence.value = null
    }
    if (guard?.write && (cause.status ?? 0) >= 500) {
      await resolveWriteAmbiguity(context, guard)
      return
    }
    message.error(
      cause.problem?.remediation ? `${cause.message} ${cause.problem.remediation}` : cause.message,
    )
    return
  }
  message.error(cause instanceof Error ? cause.message : t('authAdmin.requestFailed'))
  if (guard?.write) await resolveWriteAmbiguity(context, guard)
}

function savePresentation() {
  if (!canSavePresentation.value) return
  sentTestId = ''
  const changes = connectionChanges(
    record.value,
    form.value,
    secretMode.value,
    secret.value,
    'presentation',
  )
  void run(
    () => updateAuthConnection(record.value.id, { expected_revision: revision.value, ...changes }),
    (next) => {
      applyRecord(next, false, true)
      message.success(t('authAdmin.saved'))
    },
    { write: true, changes: clone(changes) },
  )
}
function proofFor(
  required: boolean,
  purpose: 'save' | 'enable' = 'save',
): { test_id: string; verifier: string } | undefined {
  const matched = currentEvidence.value
  if (!matched || !matchConnectionTest(matched, record.value, candidateChanges.value, purpose)) {
    if (required) message.error(t('authAdmin.testNeedsRetest'))
    return undefined
  }
  try {
    return testEvidenceFields(matched)
  } catch (cause) {
    message.error(cause instanceof Error ? cause.message : t('authAdmin.testExpired'))
    return undefined
  }
}

function saveConfiguration() {
  if (!canSaveConfiguration.value) return
  const changes = candidateChanges.value
  const requiresProof = state.value === 'enabled' && connectionProtocolChanged(changes)
  const proof = proofFor(requiresProof)
  if (requiresProof && !proof) return
  sentTestId = proof?.test_id ?? ''
  void run(
    () =>
      updateAuthConnection(record.value.id, {
        expected_revision: revision.value,
        ...changes,
        ...proof,
      }),
    (next) => {
      applyRecord(next, presentationDirty.value, false)
      message.success(t('authAdmin.saved'))
    },
    { write: true, changes: clone(changes) },
  )
}

function checkConfiguration() {
  if (!canCheck.value) return
  void run(
    () =>
      checkAuthConnection(record.value.id, {
        expected_revision: revision.value,
        changes: candidateChanges.value,
      }),
    (next) => {
      check.value = next
      checkDialog.value = true
    },
  )
}

function assess() {
  if (!canCheck.value) return
  void run(
    () =>
      fetchAuthConnectionImpact(record.value.id, {
        expected_revision: revision.value,
        changes: candidateChanges.value,
      }),
    (next) => {
      impact.value = next
      impactDialog.value = true
    },
  )
}

function loadCandidateAttributes() {
  if (!canCheck.value) return
  void run(
    () =>
      previewAuthConnectionAttributes(record.value.id, {
        expected_revision: revision.value,
        changes: candidateChanges.value,
      }),
    (next) => {
      catalog.value = next.fields
      message.success(t('authAdmin.candidateAttributes'))
    },
  )
}

function simulate() {
  if (!canCheck.value || record.value.kind !== 'oidc') return
  try {
    const parsed = parsePreviewSources(sources.value)
    void run(
      () =>
        previewAuthConnection(record.value.id, {
          expected_revision: revision.value,
          changes: candidateChanges.value,
          sources: parsed,
        }),
      (next) => {
        preview.value = next
      },
    )
  } catch {
    message.error(t('authAdmin.previewInvalid'))
  }
}

/** A new test retires the previous handle; sequence guards keep stale callbacks from overwriting it. */
function startTest() {
  if (!canStartTest.value) return
  if (testHandle) {
    void testHandle.cancel()
    testHandle = null
    evidence.value = null
  }
  const sequence = ++testSequence
  let handle: ConnectionTestHandle
  try {
    handle = startConnectionTest(record.value, candidateChanges.value)
  } catch (cause) {
    message.error(cause instanceof Error ? cause.message : t('authAdmin.testUnavailable'))
    return
  }
  testHandle = handle
  testing.value = true
  void handle.result
    .then((next) => {
      if (!alive || sequence !== testSequence) return
      evidence.value = next
      message.success(t('authAdmin.testResult'))
    })
    .catch((cause: unknown) => {
      if (!alive || sequence !== testSequence) return
      message.error(cause instanceof Error ? cause.message : t('authAdmin.testInvalid'))
      if (evidence.value?.cancelled) evidence.value = null
    })
    .finally(() => {
      if (!alive || sequence !== testSequence) return
      testing.value = false
      if (testHandle === handle && !evidence.value) testHandle = null
    })
}

function cancelTest() {
  if (testHandle) void testHandle.cancel()
}

function stateAction(action: 'configure' | 'enable' | 'disable') {
  if (busy.value || archived.value || dirty.value || !external.value) {
    if (dirty.value) message.error(t('authAdmin.saveBeforeAction'))
    return
  }
  if (action === 'enable' && !record.value.configuration_complete) {
    message.error(t('authAdmin.invalidForm'))
    return
  }
  /** Enabling always needs a fresh matched proof; validation.applied_test_id is only a summary. */
  const proof = action === 'enable' ? proofFor(true, 'enable') : undefined
  if (action === 'enable' && !proof) return
  sentTestId = proof?.test_id ?? ''
  const body = { expected_revision: revision.value, ...proof }
  const request =
    action === 'configure'
      ? configureAuthConnection
      : action === 'enable'
        ? enableAuthConnection
        : disableAuthConnection
  void run(
    () => request(record.value.id, body),
    (next) => {
      applyRecord(next)
      message.success(t(`authAdmin.${action}Done`))
    },
    {
      write: true,
      changes: {},
      state: action === 'configure' ? 'configuring' : action === 'enable' ? 'enabled' : 'disabled',
    },
  )
}

function archiveOrRestore() {
  if (busy.value || dirty.value || !external.value) {
    if (dirty.value) message.error(t('authAdmin.saveBeforeAction'))
    return
  }
  const restoring = record.value.archived
  sentTestId = ''
  void run(
    () =>
      (restoring ? restoreAuthConnection : archiveAuthConnection)(record.value.id, revision.value),
    (next) => {
      applyRecord(next)
      message.success(t(restoring ? 'authAdmin.restoredDone' : 'authAdmin.archivedDone'))
    },
    { write: true, changes: {}, archived: !restoring },
  )
}

function reloadLatest() {
  if (!latest.value || busy.value) return
  applyRecord(latest.value)
}

function reapplyChanges() {
  if (!latest.value || !conflictChanges.value || busy.value) return
  const target = latest.value
  const changes = conflictChanges.value
  const next = connectionForm(target)
  const mine = clone(form.value)
  const previous = savedForm.value
  // Keep edits outside the failed save group when explicitly rebasing onto the latest record.
  if (!sameJson(mine.display_name, previous.display_name)) next.display_name = mine.display_name
  if (!sameJson(mine.description, previous.description)) next.description = mine.description
  if (!sameJson(mine.display_order, previous.display_order)) next.display_order = mine.display_order
  if (!sameJson(mine.policy, previous.policy)) next.policy = clone(mine.policy)
  if (!sameJson(mine.attribute_mappings, previous.attribute_mappings))
    next.attribute_mappings = clone(mine.attribute_mappings)
  if (!sameJson(mine.oidc, previous.oidc)) next.oidc = clone(mine.oidc)
  if (!sameJson(mine.oauth2, previous.oauth2)) next.oauth2 = clone(mine.oauth2)
  if (mine.evidence_max_age_seconds !== previous.evidence_max_age_seconds)
    next.evidence_max_age_seconds = mine.evidence_max_age_seconds
  if (changes.display_name !== undefined) next.display_name = changes.display_name
  if (changes.description !== undefined) next.description = changes.description
  if (changes.display_order !== undefined) next.display_order = changes.display_order
  if (changes.policy) next.policy = clone(changes.policy)
  if (changes.attribute_mappings) next.attribute_mappings = clone(changes.attribute_mappings)
  if (changes.evidence_max_age_seconds !== undefined)
    next.evidence_max_age_seconds = changes.evidence_max_age_seconds
  if (changes.oidc) next.oidc = clone(changes.oidc)
  if (changes.oauth2) next.oauth2 = clone(changes.oauth2)
  record.value = target
  if ('client_secret' in changes) {
    if (changes.client_secret === null) {
      secretMode.value = 'clear'
      secret.value = ''
    } else {
      secretMode.value = 'replace'
      secret.value = changes.client_secret ?? ''
    }
  }
  form.value = next
  conflict.value = false
  latest.value = null
  conflictChanges.value = null
  sentTestId = ''
  clearDiagnostics()
  evidence.value = null
  emit('changed', target)
  void loadCatalog()
  void loadLogo()
}

function uploadLogo(event: Event) {
  const input = event.target as HTMLInputElement
  const file = input.files?.[0]
  input.value = ''
  if (!file || busy.value || archived.value || !external.value) return
  sentTestId = ''
  void run(
    () => uploadAuthConnectionLogo(record.value.id, revision.value, file),
    (next) => {
      applyRecord(next, presentationDirty.value, configurationDirty.value || secretDirty.value)
      message.success(t('authAdmin.logoSaved'))
    },
    { write: true, changes: {} },
  )
}

async function copyCallback() {
  const url = record.value.setup.callback_url
  if (!url) return
  try {
    await navigator.clipboard.writeText(url)
    message.success(t('authAdmin.copied'))
  } catch {
    message.warning(t('authAdmin.copyFailed'))
  }
}

function discardChanges() {
  form.value = connectionForm(record.value)
  secret.value = ''
  secretMode.value = 'keep'
  conflict.value = false
  latest.value = null
  conflictChanges.value = null
  sentTestId = ''
  clearDiagnostics()
  evidence.value = null
}

/** The editor owns dirty-close confirmation; the drawer only asks it to close. */
function requestClose() {
  if (busy.value) return
  if (!dirty.value) {
    emit('close')
    return
  }
  dialog.warning({
    title: t('authAdmin.discardTitle'),
    content: t('authAdmin.discardConfirm'),
    positiveText: t('authAdmin.discardAndClose'),
    negativeText: t('authAdmin.keepEditing'),
    onPositiveClick: () => {
      discardChanges()
      emit('close')
    },
  })
}

defineExpose({ requestClose })

const conflictFields = computed(
  () => Object.keys(conflictChanges.value ?? {}) as Array<keyof AuthConnectionChanges>,
)
function conflictLabel(field: keyof AuthConnectionChanges) {
  const labels: Partial<Record<keyof AuthConnectionChanges, string>> = {
    display_name: 'name',
    description: 'description',
    display_order: 'order',
    oidc: 'protocolTitle',
    oauth2: 'protocolTitle',
    policy: 'policy',
    attribute_mappings: 'mappings',
    evidence_max_age_seconds: 'evidenceAge',
    client_secret: 'secret',
  }
  return t(`authAdmin.${labels[field] ?? 'configurationUnsaved'}`)
}
function conflictValue(field: keyof AuthConnectionChanges, mine: boolean) {
  if (field === 'client_secret') {
    if (mine)
      return t(
        conflictChanges.value?.client_secret === null
          ? 'authAdmin.secretClear'
          : 'authAdmin.secretReplace',
      )
    return t(
      latest.value?.configuration.has_client_secret
        ? 'authAdmin.secretPresent'
        : 'authAdmin.secretAbsent',
    )
  }
  const value = mine ? conflictChanges.value?.[field] : latest.value?.configuration[field]
  return value && typeof value === 'object'
    ? JSON.stringify(value, null, 2)
    : String(value ?? t('authAdmin.none'))
}

onMounted(() => {
  void loadCatalog()
  void loadLogo()
})
</script>
<template>
  <div
    ref="rootEl"
    class="grid grid-cols-[minmax(0,1fr)_auto] gap-x-5 px-6 py-5"
    data-testid="auth-connection-editor"
  >
    <div class="col-start-1 row-start-1 min-w-0 space-y-5">
      <n-alert v-if="conflict" type="warning">
        <div class="space-y-3">
          <p>{{ t('authAdmin.conflict') }}</p>
          <div v-if="latest && conflictFields.length" class="grid gap-3 sm:grid-cols-2">
            <section
              v-for="mine in [false, true]"
              :key="String(mine)"
              class="min-w-0 rounded-lg border border-lf-border p-3"
            >
              <h2 class="font-medium">
                {{ t(mine ? 'authAdmin.myChanges' : 'authAdmin.latestSettings') }}
              </h2>
              <dl class="mt-3 space-y-3">
                <div v-for="field in conflictFields" :key="field">
                  <dt class="text-xs font-medium">{{ conflictLabel(field) }}</dt>
                  <dd class="mt-1 whitespace-pre-wrap break-words text-sm">
                    {{ conflictValue(field, mine) }}
                  </dd>
                </div>
              </dl>
            </section>
          </div>
          <n-space>
            <n-button
              :disabled="busy || !latest"
              data-testid="auth-connection-conflict-latest"
              @click="reloadLatest"
              >{{ t('authAdmin.reload') }}</n-button
            >
            <n-button
              :disabled="busy || !latest"
              data-testid="auth-connection-conflict-reapply"
              @click="reapplyChanges"
              >{{ t('authAdmin.reapply') }}</n-button
            >
          </n-space>
        </div>
      </n-alert>
      <n-alert v-if="issues.length" type="warning">
        <div class="space-y-2">
          <p class="font-medium">{{ t('authAdmin.issues') }}</p>
          <div v-for="issue in issues" :key="`${issue.field}:${issue.code}`" class="text-sm">
            <span class="font-medium">{{ setupIssueLabel(issue.field) }}</span> ·
            {{ setupIssueMessage(issue) }}
          </div>
        </div>
      </n-alert>
      <n-alert v-if="unsafeNumbers" type="error">{{ t('authAdmin.unsafeNumbers') }}</n-alert>

      <section
        class="scroll-mt-5 space-y-3 rounded-xl border border-lf-border p-4"
        data-anchor="setup"
      >
        <div class="flex flex-wrap items-center justify-between gap-3">
          <div>
            <h2 class="text-base font-semibold">{{ t('authAdmin.setup') }}</h2>
            <p class="mt-1 text-sm text-lf-text-muted">{{ t(`authAdmin.${record.kind}`) }}</p>
          </div>
          <n-space>
            <n-tag v-if="record.archived" type="warning">{{ t('authAdmin.archived') }}</n-tag>
            <n-tag
              v-else-if="record.kind !== 'password' && !record.configuration_complete"
              type="warning"
            >
              {{
                record.setup.issues.length
                  ? t('authAdmin.incompleteCount', { count: record.setup.issues.length })
                  : t('authAdmin.configuring')
              }}
            </n-tag>
            <n-tag v-else-if="record.kind !== 'password' && state === 'enabled'" type="success">{{
              t('authAdmin.enabled')
            }}</n-tag>
            <n-tag
              v-else-if="record.kind !== 'password'"
              :type="state === 'disabled' ? 'default' : 'warning'"
            >
              {{ state === 'disabled' ? t('authAdmin.disabled') : t('authAdmin.configuring') }}
            </n-tag>
          </n-space>
        </div>
        <div v-if="hasCapabilities" class="flex flex-wrap items-center gap-2">
          <span class="text-sm text-lf-text-muted">{{ t('authAdmin.capabilities') }}</span>
          <n-tag
            v-if="record.capabilities.can_login"
            size="small"
            :bordered="false"
            type="success"
            >{{ t('authAdmin.login') }}</n-tag
          >
          <n-tag
            v-if="record.capabilities.can_signup"
            size="small"
            :bordered="false"
            type="success"
            >{{ t('authAdmin.signup') }}</n-tag
          >
          <n-tag
            v-if="record.capabilities.can_link"
            size="small"
            :bordered="false"
            type="success"
            >{{ t('authAdmin.link') }}</n-tag
          >
        </div>
        <p
          v-if="!record.setup.callback_url && record.kind !== 'password'"
          class="text-sm text-lf-text-muted"
        >
          {{ t('authAdmin.noCallback') }}
        </p>
        <div
          v-if="record.setup.callback_url || record.setup.completion_urls.length"
          class="grid gap-4 lg:grid-cols-2"
        >
          <div v-if="record.setup.callback_url" class="rounded-lg bg-lf-surface-muted p-3">
            <p class="text-xs font-medium uppercase tracking-wide text-lf-text-muted">
              {{ t('authAdmin.callbackUrl') }}
            </p>
            <div class="mt-2 flex items-start gap-2">
              <code class="min-w-0 flex-1 break-all text-sm">{{ record.setup.callback_url }}</code>
              <n-button
                size="tiny"
                data-testid="auth-connection-copy-callback"
                @click="copyCallback"
                >{{ t('authAdmin.copy') }}</n-button
              >
            </div>
          </div>
          <div
            v-if="record.setup.completion_urls.length"
            class="rounded-lg bg-lf-surface-muted p-3"
          >
            <p class="text-xs font-medium uppercase tracking-wide text-lf-text-muted">
              {{ t('authAdmin.completionUrls') }}
            </p>
            <ul class="mt-2 space-y-1 break-all text-sm">
              <li v-for="url in record.setup.completion_urls" :key="url">
                <code>{{ url }}</code>
              </li>
            </ul>
          </div>
        </div>
        <div v-if="setupChecklist.length" class="space-y-2">
          <p class="text-sm font-medium">{{ t('authAdmin.requiredFields') }}</p>
          <div class="flex flex-wrap gap-2">
            <template v-for="item in setupChecklist" :key="item.field">
              <button
                v-if="item.missing && item.clickable"
                type="button"
                class="inline-flex items-center gap-1 rounded-md border border-lf-warning/40 bg-lf-warning-soft px-2 py-0.5 text-xs text-lf-text transition hover:border-lf-warning hover:bg-lf-warning/15"
                :title="t('authAdmin.jumpToField')"
                :data-testid="`auth-connection-setup-field-${item.field}`"
                @click="scrollToSetupField(item.field)"
              >
                {{ item.label }}
              </button>
              <span
                v-else-if="item.missing"
                class="inline-flex items-center gap-1 rounded-md border border-lf-warning/40 bg-lf-warning-soft px-2 py-0.5 text-xs text-lf-text"
                :data-testid="`auth-connection-setup-field-${item.field}`"
              >
                {{ item.label }}
              </span>
              <span
                v-else
                class="inline-flex items-center gap-1 rounded-md border border-lf-border bg-lf-surface-muted px-2 py-0.5 text-xs text-lf-text-muted"
              >
                <span class="text-lf-success" aria-hidden="true">✓</span>{{ item.label }}
              </span>
            </template>
          </div>
        </div>
        <p v-if="record.setup.issues.length" class="text-xs text-lf-text-muted">
          {{ t('authAdmin.incompleteJumpHint', { count: record.setup.issues.length }) }}
        </p>
        <p v-else class="text-sm text-lf-text-muted">{{ t('authAdmin.noIssues') }}</p>
      </section>

      <n-alert v-if="record.kind === 'password'" type="info">
        {{ t('authAdmin.passwordReadOnly') }}
        <div class="mt-2">
          <RouterLink to="/admin/settings">{{ t('authAdmin.systemSettings') }}</RouterLink>
        </div>
      </n-alert>
      <section
        v-if="record.kind === 'password'"
        class="space-y-3 rounded-xl border border-lf-border p-4"
      >
        <h2 class="text-base font-semibold">{{ t('authAdmin.policy') }}</h2>
        <p class="text-sm text-lf-text-muted">{{ t('authAdmin.passwordRules') }}</p>
        <dl class="grid gap-3 sm:grid-cols-3">
          <div v-for="purpose in purposes" :key="purpose">
            <dt class="text-sm text-lf-text-muted">{{ t(`authAdmin.${purpose}Enabled`) }}</dt>
            <dd class="mt-2">
              <n-tag
                :bordered="false"
                :type="form.policy[`${purpose}_enabled`] ? 'success' : 'default'"
              >
                {{
                  t(form.policy[`${purpose}_enabled`] ? 'authAdmin.enabled' : 'authAdmin.disabled')
                }}
              </n-tag>
            </dd>
          </div>
        </dl>
      </section>

      <n-form v-if="form" :disabled="locked" label-placement="top">
        <n-card size="small" class="mb-4 scroll-mt-5" data-anchor="presentation">
          <template #header>{{ t('authAdmin.name') }}</template>
          <div class="grid gap-3 md:grid-cols-2">
            <n-form-item :label="t('authAdmin.name')">
              <n-input
                v-model:value="form.display_name"
                :input-props="
                  inputProps('auth-connection-name', { 'aria-label': t('authAdmin.name') })
                "
              />
            </n-form-item>
            <n-form-item :label="t('authAdmin.order')">
              <n-input-number
                v-model:value="form.display_order"
                :show-button="false"
                :precision="0"
                class="w-full"
                :input-props="
                  inputProps('auth-connection-order', { 'aria-label': t('authAdmin.order') })
                "
              />
            </n-form-item>
          </div>
          <n-form-item :label="t('authAdmin.description')">
            <n-input
              v-model:value="form.description"
              type="textarea"
              :autosize="{ minRows: 2, maxRows: 4 }"
              :input-props="
                inputProps('auth-connection-description', {
                  'aria-label': t('authAdmin.description'),
                })
              "
            />
          </n-form-item>
          <div class="flex flex-wrap items-center justify-between gap-3">
            <div v-if="logoPreview" class="flex items-center gap-3">
              <img
                :src="logoPreview"
                alt=""
                width="40"
                height="40"
                class="rounded-md object-contain"
              />
              <span class="text-sm text-lf-text-muted">{{ t('authAdmin.logo') }}</span>
            </div>
            <div v-if="external" class="flex flex-wrap items-center gap-3">
              <input
                ref="logoInput"
                type="file"
                accept="image/png,image/webp"
                class="hidden"
                @change="uploadLogo"
              />
              <n-button
                :disabled="busy || archived"
                data-testid="auth-connection-logo-upload"
                @click="logoInput?.click()"
                >{{ t('authAdmin.logoUpload') }}</n-button
              >
              <span class="text-xs text-lf-text-muted">{{ t('authAdmin.logoHint') }}</span>
            </div>
          </div>
          <n-button
            class="mt-3"
            type="primary"
            secondary
            :disabled="!canSavePresentation"
            :loading="busy"
            data-testid="auth-connection-save-presentation"
            @click="savePresentation"
            >{{ t('authAdmin.savePresentation') }}</n-button
          >
        </n-card>
        <template v-if="external">
          <n-card size="small" class="mb-4 scroll-mt-5" data-anchor="protocol">
            <template #header>{{ t('authAdmin.protocolTitle') }}</template>
            <div
              v-if="
                record.setup.protocol.client_auth_methods.length ||
                record.setup.protocol.scope_guidance
              "
              class="mb-4 space-y-1 text-sm text-lf-text-muted"
            >
              <p v-if="record.setup.protocol.client_auth_methods.length">
                <span class="font-medium text-lf-text">
                  {{ t('authAdmin.clientAuthMethods') }}:
                </span>
                {{ record.setup.protocol.client_auth_methods.join(' / ') }}
              </p>
              <p v-if="record.setup.protocol.scope_guidance">
                <span class="font-medium text-lf-text">{{ t('authAdmin.scopeGuidance') }}:</span>
                {{ record.setup.protocol.scope_guidance }}
              </p>
            </div>
            <n-alert v-if="catalogError" type="warning" class="mb-3">
              {{ catalogError }}
              <n-button
                size="small"
                data-testid="auth-connection-retry-catalog"
                @click="loadCatalog"
                >{{ t('authAdmin.retry') }}</n-button
              >
            </n-alert>
            <n-spin :show="catalogLoading">
              <AuthProtocolFields
                v-model:value="form"
                :kind="externalKind"
                :disabled="protocolLocked"
              />
              <n-form-item :label="t('authAdmin.secret')" data-config-field="client_secret">
                <div class="w-full space-y-2">
                  <div class="text-sm text-lf-text-muted">
                    {{
                      t(
                        form.has_client_secret
                          ? 'authAdmin.secretPresent'
                          : 'authAdmin.secretAbsent',
                      )
                    }}
                  </div>
                  <n-select
                    v-model:value="secretMode"
                    :options="secretOptions"
                    :disabled="protocolLocked"
                    :aria-label="t('authAdmin.secret')"
                    :data-testid="'auth-connection-secret-mode'"
                  />
                  <n-input
                    v-if="secretMode === 'replace'"
                    v-model:value="secret"
                    type="password"
                    show-password-on="click"
                    autocomplete="new-password"
                    :disabled="protocolLocked"
                    :input-props="
                      inputProps('auth-connection-secret', { 'aria-label': t('authAdmin.secret') })
                    "
                  />
                  <p class="text-xs text-lf-text-muted">{{ t('authAdmin.secretHint') }}</p>
                </div>
              </n-form-item>
              <n-form-item :label="t('authAdmin.evidenceAge')">
                <n-input-number
                  :value="form.evidence_max_age_seconds"
                  :min="1"
                  :max="31536000"
                  :precision="0"
                  :disabled="protocolLocked"
                  :input-props="
                    inputProps('auth-connection-evidence-max-age', {
                      'aria-label': t('authAdmin.evidenceAge'),
                    })
                  "
                  @update:value="(value) => (form.evidence_max_age_seconds = value ?? 3600)"
                />
              </n-form-item>
            </n-spin>
          </n-card>

          <n-collapse class="mb-4 scroll-mt-5" data-anchor="mappings">
            <n-collapse-item :title="t('authAdmin.mappings')" name="mappings">
              <template #header-extra>
                <n-button
                  size="small"
                  :disabled="!canCheck"
                  data-testid="auth-connection-load-attributes"
                  @click.stop="loadCandidateAttributes"
                  >{{ t('authAdmin.loadCandidateAttributes') }}</n-button
                >
              </template>
              <AuthAttributeMappings
                v-model:value="form.attribute_mappings"
                v-model:valid="mappingsValid"
                :kind="externalKind"
                :disabled="protocolLocked"
              />
            </n-collapse-item>
          </n-collapse>

          <n-card size="small" class="mb-4 scroll-mt-5" data-anchor="policy">
            <template #header>{{ t('authAdmin.policy') }}</template>
            <n-alert v-if="catalogError" type="warning" class="mb-3">{{ catalogError }}</n-alert>
            <n-spin :show="catalogLoading">
              <section
                v-for="purpose in purposes"
                :key="purpose"
                class="border-b border-lf-border py-4 first:pt-0 last:border-b-0 last:pb-0"
              >
                <div class="flex items-center justify-between gap-3">
                  <span class="text-sm font-medium">{{ t(`authAdmin.${purpose}Enabled`) }}</span>
                  <n-switch
                    v-model:value="form.policy[`${purpose}_enabled`]"
                    :disabled="protocolLocked"
                    :aria-label="t(`authAdmin.${purpose}Enabled`)"
                    :data-testid="'auth-connection-purpose-' + purpose"
                  />
                </div>
                <div class="mt-3">
                  <AuthRuleEditor
                    :fields-loading="catalogLoading"
                    v-model:value="form.policy[`${purpose}_rules`]"
                    v-model:valid="ruleValid[purpose]"
                    :fields="fields"
                    :disabled="protocolLocked || catalogLoading || Boolean(catalogError)"
                  />
                </div>
              </section>
            </n-spin>
          </n-card>

          <n-modal
            v-model:show="checkDialog"
            preset="card"
            style="width: 620px; max-width: calc(100vw - 32px)"
            :bordered="false"
            size="small"
          >
            <template #header>
              <div
                data-testid="auth-connection-check-result"
                class="flex flex-wrap items-center justify-between gap-3 pr-2"
              >
                <span>{{ t('authAdmin.checkResult') }}</span>
                <n-tag
                  v-if="check"
                  size="small"
                  :bordered="false"
                  :type="check.valid ? 'success' : 'warning'"
                >
                  {{ t(check.valid ? 'authAdmin.checkPassed' : 'authAdmin.checkFailed') }}
                </n-tag>
              </div>
            </template>
            <template v-if="check">
              <p class="mb-2 text-xs text-lf-text-muted">{{ t('authAdmin.checkHint') }}</p>
              <div v-if="check.issues.length" class="mb-1 text-sm font-semibold">
                {{ t('authAdmin.issues') }} · {{ check.issues.length }}
              </div>
              <div class="space-y-1">
                <template v-for="issue in check.issues" :key="`${issue.field}:${issue.code}`">
                  <button
                    v-if="issue.field in setupFieldAnchors"
                    type="button"
                    class="group flex w-full items-center justify-between gap-3 rounded-md px-3 py-2 text-left transition-colors hover:bg-lf-surface-muted"
                    :title="t('authAdmin.jumpToField')"
                    @click="onIssueClick(issue)"
                  >
                    <span class="text-sm">
                      <span class="font-medium">{{ setupIssueLabel(issue.field) }}</span> ·
                      <span class="text-lf-text-muted">{{ setupIssueMessage(issue) }}</span>
                    </span>
                    <span
                      class="text-xs text-lf-accent-sky opacity-0 transition-opacity group-hover:opacity-100"
                      aria-hidden="true"
                      >→</span
                    >
                  </button>
                  <div v-else class="flex items-center justify-between gap-3 px-3 py-2">
                    <span class="text-sm">
                      <span class="font-medium">{{ setupIssueLabel(issue.field) }}</span> ·
                      <span class="text-lf-text-muted">{{ setupIssueMessage(issue) }}</span>
                    </span>
                  </div>
                </template>
              </div>
              <p class="mb-1 mt-3 text-sm font-medium">{{ t('authAdmin.diagnostics') }}</p>
              <AuthDiagnostics :rules="check.rules" />
            </template>
            <template #footer>
              <div class="flex items-center justify-between gap-3">
                <span class="text-xs text-lf-text-muted">{{ t('authAdmin.checkEphemeral') }}</span>
                <n-button :disabled="busy" @click="checkDialog = false">{{
                  t('authAdmin.close')
                }}</n-button>
              </div>
            </template>
          </n-modal>
          <n-modal
            v-model:show="impactDialog"
            preset="card"
            style="width: 640px; max-width: calc(100vw - 32px)"
            :bordered="false"
            size="small"
          >
            <template #header>
              <span data-testid="auth-connection-impact-result">{{ t('authAdmin.impact') }}</span>
            </template>
            <AuthImpact v-if="impact" :impact="impact" />
            <template #footer>
              <div class="flex items-center justify-between gap-3">
                <span class="text-xs text-lf-text-muted">{{ t('authAdmin.impactEphemeral') }}</span>
                <n-button :disabled="busy" @click="impactDialog = false">{{
                  t('authAdmin.close')
                }}</n-button>
              </div>
            </template>
          </n-modal>

          <n-card size="small" class="mb-4 scroll-mt-5" data-anchor="test">
            <template #header>{{ t('authAdmin.test') }}</template>
            <p class="mb-3 text-sm text-lf-text-muted">{{ t('authAdmin.testHint') }}</p>
            <p class="mb-3 text-sm text-lf-text-muted">{{ appliedTestSummary }}</p>
            <n-alert v-if="testing" type="info" class="mb-3">
              {{ t('authAdmin.testing') }}
              <n-button
                size="small"
                data-testid="auth-connection-close-test-window"
                @click="cancelTest"
                >{{ t('authAdmin.closeTestWindow') }}</n-button
              >
            </n-alert>
            <n-alert v-if="testStale" type="warning" class="mb-3">{{
              t('authAdmin.testNeedsRetest')
            }}</n-alert>
            <n-card
              v-if="currentTestResult"
              size="small"
              class="mt-3"
              :title="t('authAdmin.testResult')"
            >
              <p class="mb-3 text-sm">
                {{
                  t(
                    currentTestResult.fresh_supported
                      ? 'authAdmin.freshSupported'
                      : 'authAdmin.freshUnsupported',
                  )
                }}
              </p>
              <AuthDiagnostics
                :rules="currentTestResult.rules"
                :attributes="currentTestResult.attributes"
              />
            </n-card>
          </n-card>

          <n-collapse v-if="record.kind === 'oidc'" class="mb-4 scroll-mt-5" data-anchor="preview">
            <n-collapse-item :title="t('authAdmin.preview')" name="preview">
              <div class="space-y-3">
                <n-alert type="info">{{ t('authAdmin.previewHint') }}</n-alert>
                <n-input
                  v-model:value="sources"
                  type="textarea"
                  :disabled="busy"
                  :autosize="{ minRows: 5, maxRows: 12 }"
                  :input-props="
                    inputProps('auth-connection-preview-sources', { spellcheck: false })
                  "
                />
                <n-button
                  :disabled="!canCheck || record.kind !== 'oidc'"
                  data-testid="auth-connection-preview"
                  @click="simulate"
                  >{{ t('authAdmin.preview') }}</n-button
                >
                <AuthDiagnostics
                  v-if="preview"
                  :rules="preview.rules"
                  :attributes="preview.attributes"
                />
              </div>
            </n-collapse-item>
          </n-collapse>
        </template>
      </n-form>
      <div v-if="!external" class="flex justify-end border-t border-lf-border pt-4">
        <n-button :disabled="busy" data-testid="auth-connection-close" @click="requestClose">{{
          t('authAdmin.close')
        }}</n-button>
      </div>
    </div>
    <nav
      v-if="external"
      class="col-start-2 row-start-1 hidden w-[200px] border-l border-lf-border lg:block"
      aria-hidden="true"
    >
      <div class="sticky top-0 py-4">
        <button
          v-for="item in tocItems"
          :key="item.id"
          type="button"
          class="block w-full border-l-2 px-3 py-1.5 text-left text-[13px] transition-colors"
          :class="
            activeAnchor === item.id
              ? 'border-lf-accent-sky bg-lf-surface-muted font-medium text-lf-text-strong'
              : 'border-transparent text-lf-text-muted hover:bg-lf-surface-muted hover:text-lf-text'
          "
          @click="scrollToAnchor(item.id)"
        >
          {{ item.label }}
        </button>
      </div>
    </nav>
    <n-card
      v-if="external"
      size="small"
      :bordered="false"
      class="col-start-1 col-end-3 row-start-2 sticky bottom-0 z-10 -mx-6 -mb-5 border-t border-lf-border"
    >
      <div class="flex flex-wrap items-center gap-3">
        <span
          class="inline-flex items-center gap-2 text-sm"
          :class="dirty ? 'text-lf-warning' : 'text-lf-text-muted'"
        >
          <i
            class="inline-block h-2 w-2 rounded-full"
            :class="dirty ? 'bg-lf-warning' : 'bg-lf-border-strong'"
          ></i>
          {{ t('authAdmin.unsavedChanges') }}
        </span>
        <n-button
          type="primary"
          :disabled="!canSaveConfiguration"
          :loading="busy"
          data-testid="auth-connection-save-configuration"
          @click="saveConfiguration"
          >{{ t('authAdmin.saveConfiguration') }}</n-button
        >
        <n-button
          :disabled="!canCheck"
          data-testid="auth-connection-check"
          @click="checkConfiguration"
          >{{ t('authAdmin.check') }}</n-button
        >
        <n-button :disabled="!canCheck" data-testid="auth-connection-impact" @click="assess">{{
          t('authAdmin.impact')
        }}</n-button>
        <n-button
          type="primary"
          :disabled="!canStartTest"
          :loading="testing"
          data-testid="auth-connection-test"
          @click="startTest"
          >{{ t('authAdmin.test') }}</n-button
        >
        <span class="min-w-4 flex-1"></span>
        <n-button
          v-if="state === 'disabled'"
          :disabled="busy || dirty"
          data-testid="auth-connection-configure"
          @click="stateAction('configure')"
          >{{ t('authAdmin.configure') }}</n-button
        >
        <n-button
          v-if="state === 'configuring'"
          type="primary"
          :disabled="busy || dirty || !record.configuration_complete || !deploymentReady"
          data-testid="auth-connection-enable"
          @click="stateAction('enable')"
          >{{ t('authAdmin.enable') }}</n-button
        >
        <n-button
          v-if="state === 'enabled'"
          :disabled="busy || dirty"
          data-testid="auth-connection-disable"
          @click="stateAction('disable')"
          >{{ t('authAdmin.disable') }}</n-button
        >
        <n-popconfirm v-if="!record.archived" @positive-click="archiveOrRestore">
          <template #trigger>
            <n-button
              type="error"
              secondary
              :disabled="busy || dirty"
              data-testid="auth-connection-archive"
              >{{ t('authAdmin.archive') }}</n-button
            >
          </template>
          {{ t('authAdmin.archiveConfirm') }}
        </n-popconfirm>
        <n-button
          v-else
          :disabled="busy || dirty"
          data-testid="auth-connection-restore"
          @click="archiveOrRestore"
          >{{ t('authAdmin.restore') }}</n-button
        >
        <n-button :disabled="busy" data-testid="auth-connection-close" @click="requestClose">{{
          t('authAdmin.close')
        }}</n-button>
      </div>
    </n-card>
  </div>
</template>
