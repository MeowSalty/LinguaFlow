<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref, shallowRef, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { useRouter } from 'vue-router'
import { useMessage } from 'naive-ui'
import {
  fetchLoginMethods,
  setInitialPassword,
  deleteCurrentUserPassword,
  unlinkLoginBinding,
  reauthenticateWithPassword,
} from '@/api/login-methods'
import { captureSession, isSessionCurrent, sessionGeneration } from '@/api/session-context'
import {
  startExternalLink,
  startExternalReauthentication,
  takeExternalProof,
  clearExternalProof,
} from '@/auth/external-browser'
import { useAuthStore } from '@/stores/auth'
import { extractErrorMessage } from '@/utils/errors'
import { validateNewPassword } from '@/utils/password'
import {
  actionAllowed,
  bindingIdOf,
  isProofRejection,
  proofIsFresh,
  proofMatches,
  reauthCandidates,
  type LoginMethods,
  type LoginProof,
  type SecurityAction,
} from './proof'

const props = defineProps<{ disabled?: boolean }>()
const emit = defineEmits<{ busy: [value: boolean]; passwordMaintenance: [value: boolean] }>()
const { t } = useI18n()
const auth = useAuthStore()
const router = useRouter()
const message = useMessage()
const methods = shallowRef<LoginMethods | null>(null)
const loading = ref(false)
const busy = ref(false)
const error = ref('')
const actionError = ref('')
const pending = shallowRef<{ action: SecurityAction; targetId: string; label: string } | null>(null)
const proof = shallowRef<LoginProof | null>(null)
const password = ref('')
const newPassword = ref('')
const confirmation = ref('')
let revision = 0
let actionRevision = 0
let disposed = false
let returnedProof: LoginProof | null = null
let proofDeadline: ReturnType<typeof setTimeout> | undefined
const blocked = computed(() => busy.value || !!props.disabled)
const passwordMaintenance = computed(() => !!methods.value?.password.can_login)
const candidates = computed(() =>
  pending.value
    ? reauthCandidates(methods.value, pending.value.action, pending.value.targetId)
    : [],
)
const title = computed(() => {
  if (!pending.value) return ''
  const { action, label } = pending.value
  if (action === 'password_set') return t('accountAuth.security.setForm.title')
  if (action === 'password_delete') return t('accountAuth.security.confirm.removePasswordTitle')
  return t(
    action === 'link'
      ? 'accountAuth.security.confirm.linkTitle'
      : 'accountAuth.security.confirm.unlinkTitle',
    { name: label },
  )
})
const description = computed(() => {
  if (!pending.value) return ''
  switch (pending.value.action) {
    case 'password_set':
      return t('accountAuth.security.password.setHint')
    case 'password_delete':
      return t('accountAuth.security.confirm.removePasswordDescription')
    case 'unlink':
      return t('accountAuth.security.confirm.unlinkDescription')
    case 'link':
      return t('accountAuth.security.confirm.linkDescription')
  }
  return ''
})
const confirmLabel = computed(() => {
  switch (pending.value?.action) {
    case 'password_set':
      return t('accountAuth.security.setForm.submit')
    case 'password_delete':
      return t('accountAuth.security.confirm.removePassword')
    case 'unlink':
      return t('accountAuth.security.confirm.unlink')
    default:
      return t('accountAuth.security.confirm.continue')
  }
})
watch(busy, (value) => emit('busy', value), { flush: 'sync' })
watch(
  passwordMaintenance,
  (value) => {
    if (!value) clearAction()
    emit('passwordMaintenance', value)
  },
  { flush: 'sync' },
)
function dropProof(): void {
  clearTimeout(proofDeadline)
  proofDeadline = undefined
  if (proof.value) proof.value.proof = ''
  proof.value = null
}
function dropReturnedProof(): void {
  clearTimeout(proofDeadline)
  proofDeadline = undefined
  if (returnedProof) returnedProof.proof = ''
  returnedProof = null
}
function clearAction(): void {
  actionRevision++
  dropProof()
  dropReturnedProof()
  clearExternalProof()
  pending.value = null
  proof.value = null
  password.value = ''
  newPassword.value = ''
  confirmation.value = ''
  actionError.value = ''
}
function useProof(value: LoginProof): void {
  dropProof()
  proof.value = value
  proofDeadline = setTimeout(
    () => {
      if (proof.value !== value) return
      dropProof()
      newPassword.value = ''
      confirmation.value = ''
      actionError.value = t('accountAuth.security.errors.proofExpired')
    },
    Math.min(2_147_483_647, Math.max(0, Date.parse(value.expiresAt) - Date.now())),
  )
}
function reset(): void {
  revision++
  methods.value = null
  loading.value = false
  busy.value = false
  error.value = ''
  clearAction()
}
async function load(): Promise<boolean> {
  if (disposed || loading.value) return false
  const snapshot = captureSession()
  const request = ++revision
  loading.value = true
  error.value = ''
  try {
    const result = await fetchLoginMethods()
    if (disposed || request !== revision || !isSessionCurrent(snapshot)) return false
    methods.value = result
    return true
  } catch (cause) {
    if (!disposed && request === revision && isSessionCurrent(snapshot))
      error.value = extractErrorMessage(cause, t('accountAuth.security.loadFailed'))
    return false
  } finally {
    if (!disposed && request === revision && isSessionCurrent(snapshot)) loading.value = false
  }
}
function begin(action: SecurityAction, targetId: string, label = ''): void {
  if (blocked.value || !methods.value || !actionAllowed(methods.value, action, targetId)) return
  clearAction()
  pending.value = { action, targetId, label }
}
function cancel(): void {
  if (!blocked.value) clearAction()
}
async function run(operation: () => Promise<void>): Promise<void> {
  if (blocked.value || !pending.value) return
  const snapshot = captureSession()
  const request = revision
  const actionRequest = actionRevision
  busy.value = true
  actionError.value = ''
  try {
    await operation()
  } catch (cause) {
    if (
      !disposed &&
      request === revision &&
      actionRequest === actionRevision &&
      isSessionCurrent(snapshot)
    ) {
      dropProof()
      actionError.value = isProofRejection(cause)
        ? t('accountAuth.security.errors.proofExpired')
        : extractErrorMessage(cause, t('accountAuth.security.reauth.failed'))
    }
  } finally {
    if (
      !disposed &&
      request === revision &&
      actionRequest === actionRevision &&
      isSessionCurrent(snapshot)
    ) {
      password.value = ''
      busy.value = false
    }
  }
}
async function verifyPassword(): Promise<void> {
  const operation = pending.value
  if (!operation || !candidates.value.some((candidate) => candidate.kind === 'password')) return
  if (!password.value) {
    actionError.value = t('accountAuth.security.reauth.passwordRequired')
    return
  }
  const snapshot = captureSession()
  const request = actionRevision
  await run(async () => {
    const result = await reauthenticateWithPassword({
      password: password.value,
      action: operation.action,
      targetId: operation.targetId,
    })
    if (
      !disposed &&
      request === actionRevision &&
      isSessionCurrent(snapshot) &&
      pending.value === operation
    )
      useProof({
        ...operation,
        proof: result.reauthentication_proof,
        expiresAt: result.expires_at,
      })
  })
}
async function verifyExternal(bindingId: number | null): Promise<void> {
  const operation = pending.value
  if (
    !operation ||
    bindingId === null ||
    !candidates.value.some((candidate) => candidate.bindingId === bindingId)
  )
    return
  password.value = ''
  newPassword.value = ''
  confirmation.value = ''
  await run(() => startExternalReauthentication(bindingId, operation.action, operation.targetId))
}
async function execute(): Promise<void> {
  const operation = pending.value
  if (blocked.value || !operation || !methods.value) return
  if (!actionAllowed(methods.value, operation.action, operation.targetId)) {
    dropProof()
    actionError.value = t('accountAuth.security.errors.conflict')
    return
  }
  if (
    !proof.value?.proof ||
    !proofMatches(proof.value, operation.action, operation.targetId) ||
    !proofIsFresh(proof.value)
  ) {
    dropProof()
    actionError.value = t('accountAuth.security.errors.proofExpired')
    return
  }
  if (operation.action === 'password_set') {
    const issue = validateNewPassword(newPassword.value)
    if (issue || newPassword.value !== confirmation.value) {
      actionError.value = issue
        ? t(`workbench.password.${issue}`)
        : t('accountAuth.security.setForm.mismatch')
      return
    }
  }
  const snapshot = captureSession()
  const token = proof.value!.proof
  const nextPassword = newPassword.value
  dropProof()
  newPassword.value = ''
  confirmation.value = ''
  await run(async () => {
    switch (operation.action) {
      case 'link':
        await startExternalLink(operation.targetId, token, '/settings/security')
        return
      case 'password_set':
        await setInitialPassword({ new_password: nextPassword, reauthentication_proof: token })
        break
      case 'password_delete':
        await deleteCurrentUserPassword(token)
        break
      case 'unlink': {
        const id = bindingIdOf(operation.targetId)
        if (id === null) throw new Error(t('accountAuth.security.errors.proofMismatch'))
        await unlinkLoginBinding(id, token)
        break
      }
    }
    if (disposed || pending.value !== operation || !isSessionCurrent(snapshot)) return
    message.success(t('accountAuth.security.messages.removed'))
    auth.clearSession()
    await router.replace('/login')
  })
}
watch(
  sessionGeneration,
  () => {
    reset()
    if (captureSession().userId !== null) void load()
  },
  { flush: 'sync' },
)
onMounted(async () => {
  returnedProof = takeExternalProof()
  if (returnedProof)
    proofDeadline = setTimeout(
      dropReturnedProof,
      Math.min(2_147_483_647, Math.max(0, Date.parse(returnedProof.expiresAt) - Date.now())),
    )
  const snapshot = captureSession()
  if (
    !(await load()) ||
    disposed ||
    !isSessionCurrent(snapshot) ||
    !returnedProof ||
    !methods.value
  ) {
    dropReturnedProof()
    return
  }
  const returned = returnedProof
  returnedProof = null
  clearTimeout(proofDeadline)
  proofDeadline = undefined
  if (
    !actionAllowed(methods.value, returned.action, returned.targetId) ||
    !proofIsFresh(returned) ||
    !returned.proof
  ) {
    returned.proof = ''
    error.value = t('accountAuth.security.errors.proofMismatch')
    return
  }
  const label =
    returned.action === 'link'
      ? methods.value.available_connections.find((item) => item.connection_id === returned.targetId)
          ?.display_name
      : methods.value.bindings.find((item) => item.binding_id === bindingIdOf(returned.targetId))
          ?.display_name
  begin(returned.action, returned.targetId, label)
  if (pending.value) useProof(returned)
  else returned.proof = ''
})
function leaveDocument(): void {
  reset()
}
window.addEventListener('pagehide', leaveDocument)
onBeforeUnmount(() => {
  disposed = true
  window.removeEventListener('pagehide', leaveDocument)
  reset()
})
</script>

<template>
  <div class="space-y-7">
    <NSkeleton v-if="loading" height="100px" />
    <NAlert v-if="error" type="error">
      {{ error }}
      <NButton text :disabled="blocked || loading" @click="load">{{
        t('accountAuth.security.retry')
      }}</NButton>
    </NAlert>
    <template v-if="methods">
      <section class="space-y-4">
        <div class="flex flex-wrap items-center justify-between gap-3">
          <h3 class="font-semibold">{{ t('accountAuth.security.password.title') }}</h3>
          <div class="flex flex-wrap gap-2">
            <NTag size="small">{{
              t(
                methods.password.is_set
                  ? 'accountAuth.security.password.state.set'
                  : 'accountAuth.security.password.state.unset',
              )
            }}</NTag>
            <NTag size="small">{{
              t(
                methods.password.can_login
                  ? 'accountAuth.security.password.state.loginAvailable'
                  : 'accountAuth.security.password.state.loginUnavailable',
              )
            }}</NTag>
            <NTag v-if="methods.password.is_set" size="small">{{
              t(
                methods.password.verified
                  ? 'accountAuth.security.password.state.verified'
                  : 'accountAuth.security.password.state.unverified',
              )
            }}</NTag>
          </div>
        </div>
        <template v-if="methods.password.is_set">
          <p v-if="methods.password.can_login" class="text-sm text-lf-text-muted">
            {{ t('accountAuth.security.password.changeHint') }}
          </p>
          <slot v-if="methods.password.can_login" name="password" />
          <NButton
            v-if="methods.password.can_login || methods.password.can_delete"
            type="error"
            secondary
            :disabled="blocked || !methods.password.can_delete"
            @click="begin('password_delete', 'password')"
            >{{ t('accountAuth.security.password.remove') }}</NButton
          >
          <p
            v-if="methods.password.can_login && !methods.password.can_delete"
            class="text-xs text-lf-text-muted"
          >
            {{ t('accountAuth.security.password.removeBlocked') }}
          </p>
        </template>
        <template v-else>
          <p class="text-sm text-lf-text-muted">
            {{ t('accountAuth.security.password.emptyDescription') }}
          </p>
          <NButton
            v-if="methods.password.can_set"
            type="primary"
            :disabled="blocked || !methods.password.can_set"
            @click="begin('password_set', 'password')"
            >{{ t('accountAuth.security.password.set') }}</NButton
          >
        </template>
      </section>
      <section class="space-y-3 border-t border-lf-border-soft pt-5">
        <h3 class="font-semibold">{{ t('accountAuth.security.bindings.title') }}</h3>
        <p v-if="!methods.bindings.length" class="text-sm text-lf-text-muted">
          {{ t('accountAuth.security.bindings.empty') }}
        </p>
        <div
          v-for="binding in methods.bindings"
          :key="binding.binding_id"
          class="flex flex-wrap items-center justify-between gap-3 rounded-lg border border-lf-border-soft p-4"
        >
          <div class="min-w-0 space-y-2">
            <p class="break-words font-medium">{{ binding.display_name }}</p>
            <div class="flex flex-wrap gap-2">
              <NTag size="small">{{
                t(
                  binding.can_login
                    ? 'accountAuth.security.bindings.loginAvailable'
                    : 'accountAuth.security.bindings.loginUnavailable',
                )
              }}</NTag>
              <NTag size="small">{{
                t(
                  binding.verified
                    ? 'accountAuth.security.bindings.verified'
                    : 'accountAuth.security.bindings.unverified',
                )
              }}</NTag>
              <NTag v-if="binding.can_reauthenticate" size="small" type="success">{{
                t('accountAuth.security.bindings.reauthAvailable')
              }}</NTag>
              <NTag v-if="binding.archived" size="small">{{
                t('accountAuth.security.bindings.archived')
              }}</NTag>
              <NTag v-if="binding.login_rule_status" size="small">{{
                t(`accountAuth.security.bindings.ruleStatus.${binding.login_rule_status}`)
              }}</NTag>
            </div>
          </div>
          <NButton
            secondary
            type="error"
            :disabled="blocked || !binding.can_unlink"
            @click="begin('unlink', String(binding.binding_id), binding.display_name)"
            >{{ t('accountAuth.security.bindings.unlink') }}</NButton
          >
        </div>
      </section>
      <section class="space-y-3 border-t border-lf-border-soft pt-5">
        <h3 class="font-semibold">{{ t('accountAuth.security.connections.title') }}</h3>
        <p v-if="!methods.available_connections.length" class="text-sm text-lf-text-muted">
          {{ t('accountAuth.security.connections.empty') }}
        </p>
        <div
          v-for="connection in methods.available_connections"
          :key="connection.connection_id"
          class="flex flex-wrap items-center justify-between gap-3 rounded-lg border border-lf-border-soft p-4"
        >
          <div>
            <p class="font-medium">{{ connection.display_name }}</p>
            <p v-if="!connection.can_link" class="mt-1 text-xs text-lf-text-muted">
              {{ t('accountAuth.security.connections.disabledHint') }}
            </p>
          </div>
          <NButton
            :disabled="blocked || !connection.can_link"
            @click="begin('link', connection.connection_id, connection.display_name)"
            >{{ t('accountAuth.security.connections.link') }}</NButton
          >
        </div>
      </section>
      <section class="space-y-3 border-t border-lf-border-soft pt-5">
        <h3 class="font-semibold">{{ t('accountAuth.security.email.title') }}</h3>
        <p class="break-all text-sm">
          {{ methods.email.address ?? t('accountAuth.security.email.empty') }}
        </p>
        <div class="flex flex-wrap gap-2">
          <NTag size="small">{{
            t(
              methods.email.verified
                ? 'accountAuth.security.email.verified'
                : 'accountAuth.security.email.unverified',
            )
          }}</NTag>
          <NTag size="small">{{
            t(
              methods.email.can_recover
                ? 'accountAuth.security.email.canRecover'
                : 'accountAuth.security.email.cannotRecover',
            )
          }}</NTag>
        </div>
        <RouterLink to="/settings/profile" class="text-sm text-brand-500">{{
          t('accountAuth.security.email.manage')
        }}</RouterLink>
      </section>
    </template>
    <NModal
      :show="!!pending"
      :mask-closable="!blocked"
      :close-on-esc="!blocked"
      @update:show="
        (value: boolean) => {
          if (!value) cancel()
        }
      "
    >
      <!-- vueuc FocusTrap locates the modal body by its div root. -->
      <div
        role="dialog"
        aria-modal="true"
        :aria-label="title"
        class="lf-panel m-4 w-full max-w-lg space-y-5 p-6"
      >
        <h3 class="text-lg font-semibold">{{ title }}</h3>
        <p class="text-sm text-lf-text-muted">{{ description }}</p>
        <NAlert v-if="actionError" type="error">{{ actionError }}</NAlert>
        <template v-if="!proof">
          <p class="text-sm">{{ t('accountAuth.security.reauth.description') }}</p>
          <p v-if="pending?.action === 'password_delete'" class="text-xs text-lf-text-muted">
            {{ t('accountAuth.security.reauth.blockedPassword') }}
          </p>
          <p v-if="pending?.action === 'unlink'" class="text-xs text-lf-text-muted">
            {{ t('accountAuth.security.reauth.blockedTarget') }}
          </p>
          <NAlert v-if="!candidates.length" type="warning">{{
            t('accountAuth.security.reauth.noMethod')
          }}</NAlert>
          <form
            v-if="candidates.some((candidate) => candidate.kind === 'password')"
            class="space-y-3"
            @submit.prevent="verifyPassword"
          >
            <label for="security-proof-password" class="text-sm">{{
              t('accountAuth.security.reauth.passwordHeading')
            }}</label>
            <NInput
              v-model:value="password"
              type="password"
              :disabled="blocked"
              :placeholder="t('accountAuth.security.reauth.passwordPlaceholder')"
              :input-props="{ id: 'security-proof-password', autocomplete: 'current-password' }"
            />
            <NButton attr-type="submit" type="primary" :loading="busy" :disabled="blocked">{{
              t('accountAuth.security.reauth.passwordSubmit')
            }}</NButton>
          </form>
          <template v-for="candidate in candidates" :key="candidate.bindingId ?? 'password'">
            <NButton
              v-if="candidate.kind === 'binding'"
              block
              :disabled="blocked"
              @click="verifyExternal(candidate.bindingId)"
              >{{
                t('accountAuth.security.reauth.externalAction', { name: candidate.label })
              }}</NButton
            >
          </template>
        </template>
        <template v-else>
          <NAlert type="success">{{ t('accountAuth.security.reauth.verified') }}</NAlert>
          <form class="space-y-4" @submit.prevent="execute">
            <template v-if="pending?.action === 'password_set'">
              <label for="security-new-password" class="block text-sm">{{
                t('accountAuth.security.setForm.next')
              }}</label>
              <NInput
                v-model:value="newPassword"
                type="password"
                :disabled="blocked"
                :input-props="{ id: 'security-new-password', autocomplete: 'new-password' }"
              />
              <p class="text-xs text-lf-text-muted">{{ t('accountAuth.security.setForm.hint') }}</p>
              <label for="security-confirm-password" class="block text-sm">{{
                t('accountAuth.security.setForm.confirm')
              }}</label>
              <NInput
                v-model:value="confirmation"
                type="password"
                :disabled="blocked"
                :input-props="{ id: 'security-confirm-password', autocomplete: 'new-password' }"
              />
            </template>
            <NButton
              attr-type="submit"
              :type="
                pending?.action === 'unlink' || pending?.action === 'password_delete'
                  ? 'error'
                  : 'primary'
              "
              :loading="busy"
              :disabled="blocked"
              >{{ confirmLabel }}</NButton
            >
          </form>
        </template>
        <NButton :disabled="blocked" @click="cancel">{{
          t('accountAuth.security.confirm.cancel')
        }}</NButton>
      </div>
    </NModal>
  </div>
</template>
