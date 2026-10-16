<script setup lang="ts">
import { computed, onMounted, onScopeDispose, ref, shallowRef, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import {
  NAlert,
  NButton,
  NCard,
  NCheckbox,
  NCollapse,
  NCollapseItem,
  NForm,
  NFormItem,
  NInput,
  NInputNumber,
  NPopconfirm,
  NSelect,
  NSpace,
  NSpin,
  NSwitch,
  useMessage,
} from 'naive-ui'
import type { ApiSchemas } from '@/api/client-core'
import {
  AuthConnectionError,
  fetchAuthConnection,
  updateAuthConnection,
  checkAuthConnection,
  fetchAuthConnectionImpact,
  publishAuthConnection,
  archiveAuthConnection,
  restoreAuthConnection,
  fetchAuthConnectionAttributes,
  fetchAuthConnectionLogo,
  uploadAuthConnectionLogo,
  previewAuthConnection,
  type AuthConnection,
  type AuthConnectionCheck,
  type AuthConnectionImpact,
  type AuthAttributeField,
} from '@/api/auth-connections'
import { captureSession, isSessionCurrent } from '@/api/session-context'
import { startExternalTest } from '@/auth/external-browser'
import AuthRuleEditor from './AuthRuleEditor.vue'
import AuthAttributeMappings from './AuthAttributeMappings.vue'
import AuthProtocolFields from './AuthProtocolFields.vue'
import AuthDiagnostics from './AuthDiagnostics.vue'
import AuthImpact from './AuthImpact.vue'
import {
  clone,
  connectionUpdate,
  connectionFormValid,
  hasUnsafeRuleNumbers,
  mergeDraftAttributes,
  parsePreviewSources,
  type SecretMode,
} from './auth-connection-model'
import { validateAuthAttributeMappings } from './auth-attribute-catalog'

const props = defineProps<{
  connection: AuthConnection
  testResult?: ApiSchemas['ExternalTestResponse'] | null
}>()
const emit = defineEmits<{
  changed: [record: AuthConnection]
  close: []
  denied: [detail: string]
}>()
const { t } = useI18n()
const message = useMessage()
const record = shallowRef(props.connection)
const snapshot = props.connection.draft ?? props.connection.published
const draft = ref(snapshot ? clone(snapshot) : null)
const baseline = ref(JSON.stringify(draft.value))
const secret = ref('')
const secretMode = ref<SecretMode>('keep')
const busy = ref(false)
const error = ref('')
const conflict = ref(false)
const check = shallowRef<AuthConnectionCheck | null>(null)
const impact = shallowRef<AuthConnectionImpact | null>(null)
const remediation = ref('')
const publishConfirmed = ref(false)
const catalog = shallowRef<AuthAttributeField[]>([])
const catalogError = ref('')
const catalogLoading = ref(false)
const mappingsValid = ref(true)
const ruleValid = ref({ login: true, signup: true, link: true })
const sources = ref('{\n  "oidc_id_token": {},\n  "oidc_userinfo": {}\n}')
const preview = shallowRef<ApiSchemas['AuthRulesPreview'] | null>(null)
const logoInput = ref<HTMLInputElement | null>(null)
const logoPreview = ref('')
let logoRequest = 0
function releaseLogo() {
  if (logoPreview.value) URL.revokeObjectURL(logoPreview.value)
  logoPreview.value = ''
}
let alive = true
let catalogRequest = 0
onScopeDispose(() => {
  alive = false
  secret.value = ''
  sources.value = ''
  catalogRequest++
  logoRequest++
  releaseLogo()
})
const dirty = computed(
  () => JSON.stringify(draft.value) !== baseline.value || secretMode.value !== 'keep',
)
const external = computed(() => record.value.kind !== 'password')
const revision = computed(() => record.value.aggregate_revision)
const snapshotRevision = computed(
  () =>
    record.value.draft_revision ??
    record.value.draft?.revision ??
    record.value.published_revision ??
    record.value.published?.revision,
)
const unsafeNumbers = computed(() =>
  hasUnsafeRuleNumbers(record.value.draft ?? record.value.published),
)
const locked = computed(
  () => busy.value || record.value.archived || unsafeNumbers.value || !external.value,
)
const savedCandidate = computed(
  () => !dirty.value && !locked.value && Boolean(record.value.draft) && !conflict.value,
)
const fields = computed(() =>
  mergeDraftAttributes(
    catalog.value,
    record.value.draft?.attribute_mappings ?? record.value.published?.attribute_mappings ?? [],
    draft.value?.attribute_mappings ?? [],
  ),
)
const canSave = computed(
  () =>
    draft.value &&
    !locked.value &&
    !conflict.value &&
    connectionFormValid(record.value.kind, draft.value) &&
    (record.value.kind === 'password' ||
      (!catalogLoading.value &&
        !catalogError.value &&
        mappingsValid.value &&
        validateAuthAttributeMappings(draft.value.attribute_mappings ?? [], record.value.kind)
          .valid &&
        Object.values(ruleValid.value).every(Boolean))) &&
    (secretMode.value !== 'replace' || secret.value.length > 0),
)
const canPublish = computed(
  () =>
    savedCandidate.value &&
    check.value?.valid &&
    check.value.revision === snapshotRevision.value &&
    impact.value?.can_publish &&
    (impact.value.revision === undefined || impact.value.revision === snapshotRevision.value),
)
const purposes = ['login', 'signup', 'link'] as const
const secretOptions = computed(() => [
  { label: t('authAdmin.secretKeep'), value: 'keep' },
  { label: t('authAdmin.secretReplace'), value: 'replace' },
  { label: t('authAdmin.secretClear'), value: 'clear' },
])
const currentTest = computed(() =>
  props.testResult?.connection_id === record.value.id &&
  props.testResult.revision === snapshotRevision.value
    ? props.testResult
    : null,
)

function clearAssessment() {
  check.value = null
  impact.value = null
  remediation.value = ''
  preview.value = null
  publishConfirmed.value = false
}
watch([draft, secretMode, secret], clearAssessment, { deep: true, flush: 'sync' })
watch(secretMode, (mode) => {
  if (mode !== 'replace') secret.value = ''
})

async function loadCatalog() {
  if (!external.value) return
  const context = captureSession()
  const request = ++catalogRequest
  const id = record.value.id
  const version = revision.value
  catalogLoading.value = true
  catalogError.value = ''
  try {
    const data = await fetchAuthConnectionAttributes(id, version)
    if (!alive || !isSessionCurrent(context) || request !== catalogRequest) return
    if (data.revision !== snapshotRevision.value) throw new Error(t('authAdmin.conflict'))
    catalog.value = data.fields
  } catch (cause) {
    if (alive && isSessionCurrent(context) && request === catalogRequest)
      catalogError.value = cause instanceof Error ? cause.message : t('authAdmin.catalogFailed')
  } finally {
    if (alive && isSessionCurrent(context) && request === catalogRequest)
      catalogLoading.value = false
  }
}
async function loadLogo() {
  const request = ++logoRequest
  const context = captureSession()
  releaseLogo()
  if (!external.value || !record.value.draft?.logo_url) return
  try {
    const image = await fetchAuthConnectionLogo(record.value.id, revision.value)
    if (alive && request === logoRequest && isSessionCurrent(context))
      logoPreview.value = URL.createObjectURL(image)
  } catch {
    /* An unavailable preview does not invalidate other draft settings. */
  }
}
function uploadLogo(event: Event) {
  const input = event.target as HTMLInputElement
  const file = input.files?.[0]
  input.value = ''
  if (!file || dirty.value || locked.value || conflict.value) return
  void run(
    () => uploadAuthConnectionLogo(record.value.id, revision.value, file),
    (data) => {
      accept(data as AuthConnection)
      message.success(t('authAdmin.logoSaved'))
    },
  )
}
watch(() => record.value, loadLogo, { immediate: true })
function accept(next: AuthConnection) {
  record.value = next
  draft.value = next.draft || next.published ? clone(next.draft ?? next.published!) : null
  baseline.value = JSON.stringify(draft.value)
  secret.value = ''
  secretMode.value = 'keep'
  conflict.value = false
  clearAssessment()
  emit('changed', next)
  void loadCatalog()
}
async function run(action: () => Promise<unknown>, apply: (data: unknown) => void) {
  if (busy.value) return
  const context = captureSession()
  busy.value = true
  error.value = ''
  try {
    const data = await action()
    if (alive && isSessionCurrent(context)) apply(data)
  } catch (cause) {
    if (!alive || !isSessionCurrent(context)) return
    publishConfirmed.value = false
    error.value = cause instanceof Error ? cause.message : t('authAdmin.requestFailed')
    if (cause instanceof AuthConnectionError && [403, 404].includes(cause.status ?? 0)) {
      secret.value = ''
      sources.value = ''
      draft.value = null
      emit('denied', error.value)
      return
    }
    if (cause instanceof AuthConnectionError && cause.status === 409) {
      clearAssessment()
      conflict.value = true
      impact.value = cause.problem?.impact ?? null
      remediation.value = cause.problem?.remediation ?? ''
    }
  } finally {
    if (alive && isSessionCurrent(context)) busy.value = false
  }
}
function save() {
  if (!canSave.value || !draft.value) return
  const body = connectionUpdate(record.value, draft.value, secretMode.value, secret.value)
  void run(
    () => updateAuthConnection(record.value.id, body),
    (data) => {
      accept(data as AuthConnection)
      message.success(t('authAdmin.saved'))
    },
  )
}
function reload() {
  void run(
    () => fetchAuthConnection(record.value.id),
    (data) => accept(data as AuthConnection),
  )
}
function checkConfiguration() {
  if (!savedCandidate.value) return
  clearAssessment()
  void run(
    () => checkAuthConnection(record.value.id, revision.value),
    (data) => {
      check.value = data as AuthConnectionCheck
    },
  )
}
function assess() {
  if (!savedCandidate.value) return
  impact.value = null
  publishConfirmed.value = false
  void run(
    () => fetchAuthConnectionImpact(record.value.id, revision.value),
    (data) => {
      impact.value = data as AuthConnectionImpact
    },
  )
}
function publish() {
  if (!canPublish.value || !publishConfirmed.value) return
  void run(
    () => publishAuthConnection(record.value.id, revision.value),
    (data) => {
      accept(data as AuthConnection)
      message.success(t('authAdmin.publishedDone'))
    },
  )
}
function changeArchive() {
  if (dirty.value || busy.value || !external.value || conflict.value) return
  const restoring = record.value.archived
  void run(
    () =>
      (restoring ? restoreAuthConnection : archiveAuthConnection)(record.value.id, revision.value),
    (data) => {
      accept(data as AuthConnection)
      message.success(t(restoring ? 'authAdmin.restoredDone' : 'authAdmin.archivedDone'))
    },
  )
}
function test() {
  if (!savedCandidate.value || !external.value) return
  void run(
    () =>
      startExternalTest(
        record.value.id,
        revision.value,
        record.value.draft_revision ?? record.value.draft!.revision,
      ),
    () => {},
  )
}
function simulate() {
  if (!savedCandidate.value || record.value.kind !== 'oidc') return
  let parsed: ApiSchemas['AuthRulesPreviewRequest']['sources']
  try {
    parsed = parsePreviewSources(sources.value)
  } catch {
    error.value = t('authAdmin.previewInvalid')
    return
  }
  preview.value = null
  void run(
    () =>
      previewAuthConnection(record.value.id, {
        expected_revision: revision.value,
        sources: parsed,
      }),
    (data) => {
      preview.value = data as ApiSchemas['AuthRulesPreview']
    },
  )
}
onMounted(loadCatalog)
</script>

<template>
  <div class="space-y-5">
    <n-alert v-if="!external" type="info">
      {{ t('authAdmin.passwordReadOnly') }}
      <div class="mt-2">
        <RouterLink to="/admin/settings">{{ t('authAdmin.systemSettings') }}</RouterLink>
      </div>
    </n-alert>
    <n-alert v-if="error" type="error" role="alert">{{ error }}</n-alert>
    <n-alert v-if="conflict" type="warning"
      >{{ t('authAdmin.conflict') }}
      <div class="mt-3">
        <n-popconfirm v-if="dirty" @positive-click="reload"
          ><template #trigger
            ><n-button :disabled="busy">{{ t('authAdmin.reload') }}</n-button></template
          >{{ t('authAdmin.discard') }}</n-popconfirm
        ><n-button v-else :disabled="busy" @click="reload">{{ t('authAdmin.reload') }}</n-button>
      </div></n-alert
    >
    <n-alert v-if="!draft" type="error">{{ t('authAdmin.missingSnapshot') }}</n-alert>
    <n-alert v-if="unsafeNumbers" type="error">{{ t('authAdmin.unsafeNumbers') }}</n-alert>
    <template v-if="draft">
      <div class="flex flex-wrap items-center gap-2 text-sm text-lf-text-muted">
        <span>{{ record.id }}</span
        ><span>· {{ t('authAdmin.revision', { revision }) }}</span
        ><span v-if="record.archived">· {{ t('authAdmin.archived') }}</span>
      </div>
      <n-form :disabled="locked" label-placement="top">
        <n-form-item :label="t('authAdmin.name')"
          ><n-input
            v-model:value="draft.display_name"
            :input-props="{ 'aria-label': t('authAdmin.name') }"
        /></n-form-item>
        <n-form-item :label="t('authAdmin.description')"
          ><n-input
            v-model:value="draft.description"
            type="textarea"
            :autosize="{ minRows: 2, maxRows: 4 }"
            :input-props="{ 'aria-label': t('authAdmin.description') }"
        /></n-form-item>
        <n-form-item :label="t('authAdmin.order')"
          ><n-input-number
            v-model:value="draft.display_order"
            :show-button="false"
            :precision="0"
            :update-value-on-input="false"
            :parse="Number"
            :on-update:value="(v) => draft && (draft.display_order = v ?? 0)"
        /></n-form-item>
        <template v-if="record.kind !== 'password'">
          <n-form-item :label="t('authAdmin.logo')">
            <div class="space-y-2">
              <div class="flex items-center gap-3">
                <img
                  v-if="logoPreview"
                  :src="logoPreview"
                  alt=""
                  width="40"
                  height="40"
                  class="rounded-md object-contain"
                /><n-button :disabled="dirty || locked || conflict" @click="logoInput?.click()">{{
                  t('authAdmin.logoUpload')
                }}</n-button>
              </div>
              <input
                ref="logoInput"
                type="file"
                accept="image/png,image/webp"
                class="hidden"
                :disabled="dirty || locked || conflict"
                @change="uploadLogo"
              />
              <p class="text-xs text-lf-text-muted">{{ t('authAdmin.logoHint') }}</p>
            </div>
          </n-form-item>
          <div
            v-if="record.identity_authority_id"
            class="mb-4 break-all text-sm text-lf-text-muted"
          >
            {{ t('authAdmin.authority') }}: {{ record.identity_authority_id }}
          </div>
          <AuthProtocolFields
            v-model:value="draft"
            :kind="record.kind"
            :disabled="locked"
            :immutable="record.published_revision != null"
          />
          <n-form-item :label="t('authAdmin.secret')"
            ><div class="w-full space-y-2">
              <div class="text-sm text-lf-text-muted">
                {{
                  t(draft.has_client_secret ? 'authAdmin.secretPresent' : 'authAdmin.secretAbsent')
                }}
              </div>
              <n-select v-model:value="secretMode" :options="secretOptions" /><n-input
                v-if="secretMode === 'replace'"
                v-model:value="secret"
                type="password"
                show-password-on="click"
                :input-props="{ autocomplete: 'new-password', 'aria-label': t('authAdmin.secret') }"
              />
              <p class="text-xs text-lf-text-muted">{{ t('authAdmin.secretHint') }}</p>
            </div></n-form-item
          >
          <n-collapse
            ><n-collapse-item :title="t('authAdmin.mappings')" name="mappings"
              ><AuthAttributeMappings
                :value="draft.attribute_mappings ?? []"
                v-model:valid="mappingsValid"
                :kind="record.kind"
                :disabled="locked"
                @update:value="(v) => draft && (draft.attribute_mappings = v)" /></n-collapse-item
          ></n-collapse>
          <n-form-item class="mt-4" :label="t('authAdmin.evidenceAge')"
            ><n-input-number
              :value="draft.evidence_max_age_seconds ?? 3600"
              :min="1"
              :max="31536000"
              :precision="0"
              @update:value="(v) => draft && (draft.evidence_max_age_seconds = v ?? 3600)"
          /></n-form-item>
        </template>
        <n-card :title="t('authAdmin.policy')" size="small">
          <n-form-item :label="t('authAdmin.enabled')"
            ><n-switch v-model:value="draft.policy.enabled" :aria-label="t('authAdmin.enabled')"
          /></n-form-item>
          <n-alert v-if="!external" type="info" class="mb-4">{{
            t('authAdmin.passwordRules')
          }}</n-alert>
          <n-alert v-if="catalogError" type="error" class="mb-4"
            >{{ t('authAdmin.catalogFailed') }} {{ catalogError
            }}<n-button size="small" :disabled="catalogLoading || busy" @click="loadCatalog">{{
              t('authAdmin.retry')
            }}</n-button></n-alert
          >
          <n-spin :show="catalogLoading">
            <section v-for="purpose in purposes" :key="purpose" class="mb-5 last:mb-0">
              <n-form-item :label="t(`authAdmin.${purpose}Enabled`)"
                ><n-switch
                  v-model:value="draft.policy[`${purpose}_enabled`]"
                  :aria-label="t(`authAdmin.${purpose}Enabled`)"
              /></n-form-item>
              <AuthRuleEditor
                v-model:value="draft.policy[`${purpose}_rules`]"
                v-model:valid="ruleValid[purpose]"
                :fields="external ? fields : []"
                :disabled="locked || !external || catalogLoading || Boolean(catalogError)"
              />
            </section>
          </n-spin>
        </n-card>
      </n-form>
      <n-alert v-if="dirty" type="info">{{ t('authAdmin.unsaved') }}</n-alert>
      <n-space v-if="external">
        <n-button type="primary" :loading="busy" :disabled="!canSave || !dirty" @click="save">{{
          t('authAdmin.save')
        }}</n-button>
        <n-button :disabled="!savedCandidate" @click="checkConfiguration">{{
          t('authAdmin.check')
        }}</n-button>
        <n-button :disabled="!savedCandidate" @click="assess">{{ t('authAdmin.impact') }}</n-button>
        <n-button v-if="external" :disabled="!savedCandidate" @click="test">{{
          t('authAdmin.test')
        }}</n-button>
      </n-space>
      <p v-if="external" class="text-sm text-lf-text-muted">{{ t('authAdmin.needsAssessment') }}</p>
      <template v-if="check"
        ><n-alert
          :type="check.valid ? 'success' : 'warning'"
          :title="t(check.valid ? 'authAdmin.checkPassed' : 'authAdmin.checkFailed')"
          ><p>{{ t('authAdmin.checkHint') }}</p>
          <ul>
            <li v-for="issue in check.issues" :key="issue">{{ issue }}</li>
          </ul></n-alert
        ><AuthDiagnostics :rules="check.rules"
      /></template>
      <AuthImpact v-if="impact" :impact="impact" />
      <n-alert v-if="remediation" type="warning" :title="t('authAdmin.remediation')">{{
        remediation
      }}</n-alert>
      <div v-if="canPublish" class="space-y-3 rounded-xl border border-lf-border p-4">
        <p>{{ t('authAdmin.publishHint') }}</p>
        <n-checkbox v-model:checked="publishConfirmed">{{
          t('authAdmin.publishConfirm')
        }}</n-checkbox>
      </div>
      <n-button
        v-if="external"
        type="primary"
        :disabled="!canPublish || !publishConfirmed"
        :loading="busy"
        @click="publish"
        >{{ t('authAdmin.publish') }}</n-button
      >
      <section v-if="external" class="space-y-3">
        <p class="text-sm text-lf-text-muted">{{ t('authAdmin.testHint') }}</p>
        <n-card v-if="currentTest" :title="t('authAdmin.testResult')" size="small"
          ><p class="mb-3">
            {{
              t(
                currentTest.fresh_supported
                  ? 'authAdmin.freshSupported'
                  : 'authAdmin.freshUnsupported',
              )
            }}
          </p>
          <AuthDiagnostics :rules="currentTest.rules" :attributes="currentTest.attributes"
        /></n-card>
        <n-alert v-else-if="testResult" type="warning">{{ t('authAdmin.staleTest') }}</n-alert>
        <n-collapse
          ><n-collapse-item :title="t('authAdmin.preview')" name="preview"
            ><div class="space-y-3">
              <n-alert type="info">{{ t('authAdmin.previewHint') }}</n-alert
              ><template v-if="record.kind === 'oidc'"
                ><n-input
                  v-model:value="sources"
                  type="textarea"
                  :disabled="busy"
                  :autosize="{ minRows: 5, maxRows: 16 }"
                  :input-props="{
                    'aria-label': t('authAdmin.previewSources'),
                    spellcheck: false,
                  }" /><n-button :disabled="!savedCandidate" @click="simulate">{{
                  t('authAdmin.preview')
                }}</n-button
                ><AuthDiagnostics
                  v-if="preview"
                  :rules="preview.rules"
                  :attributes="preview.attributes"
              /></template>
              <p v-else class="text-sm text-lf-text-muted">
                {{ t('authAdmin.previewUnavailable') }}
              </p>
            </div></n-collapse-item
          ></n-collapse
        >
      </section>
      <div class="flex flex-wrap justify-between gap-3 border-t border-lf-border pt-4">
        <div v-if="external">
          <n-button
            v-if="record.archived"
            :disabled="busy || dirty || conflict"
            @click="changeArchive"
            >{{ t('authAdmin.restore') }}</n-button
          ><n-popconfirm v-else @positive-click="changeArchive"
            ><template #trigger
              ><n-button :disabled="busy || dirty || conflict" type="error" secondary>{{
                t('authAdmin.archive')
              }}</n-button></template
            >{{ t('authAdmin.archiveConfirm') }}</n-popconfirm
          >
        </div>
        <n-popconfirm v-if="dirty" @positive-click="emit('close')"
          ><template #trigger
            ><n-button :disabled="busy">{{ t('authAdmin.close') }}</n-button></template
          >{{ t('authAdmin.discard') }}</n-popconfirm
        ><n-button v-else :disabled="busy" @click="emit('close')">{{
          t('authAdmin.close')
        }}</n-button>
      </div>
    </template>
  </div>
</template>
