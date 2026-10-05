<script setup lang="ts">
import { computed, onUnmounted, reactive, ref, watch } from 'vue'
import {
  NAlert,
  NButton,
  NCard,
  NDrawer,
  NDrawerContent,
  NEmpty,
  NForm,
  NFormItem,
  NInput,
  NInputNumber,
  NModal,
  NSelect,
  NSkeleton,
  NSwitch,
  NTag,
  useDialog,
  useMessage,
} from 'naive-ui'
import { useI18n } from 'vue-i18n'
import { captureSession, isSessionCurrent, sessionGeneration } from '@/api/session-context'
import { storageTaskErrorMessage } from '@/api/storage-errors'
import {
  useStorageStore,
  storageScopeKey,
  type StorageScope,
  type StorageWriteResult,
} from '@/stores/storage'
import { getStorageContractGate } from '@/utils/storage-contract'
import { subscribeStorageRefresh, invalidateStorageSnapshots } from '@/utils/storage-snapshots'
import type { StorageManagementAction } from '@/utils/storage-availability'
import { formatDateTime } from '@/utils/datetime'
import StorageCapacity from './StorageCapacity.vue'
import StorageHealth from './StorageHealth.vue'

const props = defineProps<{ scope: StorageScope }>()
const { t } = useI18n()
const store = useStorageStore()
const dialog = useDialog(),
  message = useMessage()
const selectedId = ref<number | null>(null)
const selected = computed(() =>
  store.connections.items.find((item) => item.id === selectedId.value),
)
const selectedSpaces = computed(() =>
  selectedId.value === null ? undefined : store.spaces[selectedId.value],
)
const selectedChecks = computed(() =>
  selectedId.value === null ? undefined : store.checks[selectedId.value],
)
const formKind = ref<'connection' | 'space' | 'authorization' | null>(null)
const form = reactive({
  name: '',
  endpoint: '',
  region: '',
  path_style: false,
  bucket: '',
  prefix: '',
  capacity_bytes: 1024 * 1024 * 1024,
})
const secrets = reactive({ access_key_id: '', secret_access_key: '', session_token: '' })
const authorizationWrite = ref(false)
const authorizationOptions = computed(() => [
  {
    value: 'read',
    label: t('storageManagement.authorizeRead'),
    disabled: !selected.value || !store.connectionAllowed(selected.value.id, 'authorize_read'),
  },
  {
    value: 'write',
    label: t('storageManagement.authorizeWrite'),
    disabled:
      !selected.value ||
      !store.connectionAllowed(selected.value.id, 'authorize_write') ||
      !getStorageContractGate('writeCheck').available,
  },
])
const formError = ref<string | null>(null)
const writeKey = computed(() =>
  formKind.value === 'connection' ? 'create' : `connection:${selectedId.value}`,
)
const selectedBusy = computed(
  () => selectedId.value !== null && !!store.busy[`connection:${selectedId.value}`],
)
const formAllowed = computed(() =>
  formKind.value === 'connection'
    ? store.canCreateConnection
    : !!selected.value &&
      store.connectionAllowed(
        selected.value.id,
        formKind.value === 'space'
          ? 'create_space'
          : authorizationWrite.value
            ? 'authorize_write'
            : 'authorize_read',
      ) &&
      (formKind.value !== 'authorization' ||
        !authorizationWrite.value ||
        getStorageContractGate('writeCheck').available),
)
const formReason = computed(() =>
  formKind.value === 'connection'
    ? store.createConnectionReason
    : selected.value
      ? store.connectionReason(
          selected.value.id,
          formKind.value === 'space'
            ? 'create_space'
            : authorizationWrite.value
              ? 'authorize_write'
              : 'authorize_read',
        )
      : t('storage.notAvailable'),
)
const actionLabels: Record<StorageManagementAction, string> = {
  create_space: 'storage.createSpace',
  authorize_read: 'storageManagement.authorizeRead',
  authorize_write: 'storageManagement.authorizeWrite',
  check_read: 'storage.readCheck',
  check_write: 'storage.writeCheck',
  revoke_auth: 'storage.revoke',
  set_status: 'storage.state',
}
const blockedActions = computed(() =>
  selected.value
    ? (Object.keys(actionLabels) as StorageManagementAction[])
        .map((action) => ({ action, reason: store.connectionReason(selected.value!.id, action) }))
        .filter(({ reason }) => !!reason)
    : [],
)
let generation = 0,
  formGeneration = 0
function clearSecrets() {
  secrets.access_key_id = ''
  secrets.secret_access_key = ''
  secrets.session_token = ''
}
function closeForm() {
  formKind.value = null
  clearSecrets()
  formError.value = null
}
function openForm(kind: NonNullable<typeof formKind.value>) {
  ++formGeneration
  clearSecrets()
  Object.assign(form, {
    name: '',
    endpoint: '',
    region: '',
    path_style: false,
    bucket: '',
    prefix: '',
    capacity_bytes: 1024 * 1024 * 1024,
  })
  formError.value = null
  if (kind === 'authorization') authorizationWrite.value = false
  formKind.value = kind
}
async function refresh() {
  const id = selectedId.value
  if (await store.load(props.scope))
    if (id !== null) await Promise.all([store.loadSpaces(id), store.loadChecks(id)])
}
async function showDetails(id: number) {
  selectedId.value = id
  await Promise.all([store.loadSpaces(id), store.loadChecks(id)])
}
async function complete<T>(
  action: () => Promise<StorageWriteResult<T>>,
  success = 'storage.saved',
) {
  const session = captureSession(),
    version = generation
  const result = await action()
  if (!isSessionCurrent(session) || version !== generation) return false
  if (result.status === 'success') {
    message.success(t(success))
    invalidateStorageSnapshots(props.scope.kind === 'org' ? { organizationId: props.scope.id } : {})
    return isSessionCurrent(session) && version === generation
  }
  return false
}
async function submit() {
  if (!store.canManage || !formAllowed.value) return
  const kind = formKind.value,
    id = selectedId.value
  if (kind === null) return
  formError.value = null
  const session = captureSession(),
    version = generation,
    submittedForm = formGeneration
  try {
    if (kind === 'authorization') {
      if (!secrets.access_key_id.trim() || !secrets.secret_access_key || id === null) {
        formError.value = t('storage.required')
        return
      }
      const authorization = {
        access_key_id: secrets.access_key_id.trim(),
        secret_access_key: secrets.secret_access_key,
        ...(secrets.session_token ? { session_token: secrets.session_token } : {}),
        write_check: authorizationWrite.value,
      }
      // Release the modal immediately so emergency revocation stays reachable during a slow probe.
      closeForm()
      await complete(() => store.authorize(id, authorization))
    } else {
      if (!form.name.trim()) {
        formError.value = t('storage.required')
        return
      }
      if (kind === 'connection') {
        if (!form.region.trim()) {
          formError.value = t('storage.required')
          return
        }
        try {
          if (!['http:', 'https:'].includes(new URL(form.endpoint).protocol)) throw new Error()
        } catch {
          formError.value = t('storage.invalidEndpoint')
          return
        }
        if (
          await complete(() =>
            store.createConnection({
              name: form.name.trim(),
              endpoint: form.endpoint.trim(),
              region: form.region.trim(),
              path_style: form.path_style,
            }),
          )
        )
          closeForm()
      } else if (id !== null) {
        if (
          !form.bucket.trim() ||
          !Number.isSafeInteger(form.capacity_bytes) ||
          form.capacity_bytes < 1
        ) {
          formError.value = t('storage.required')
          return
        }
        if (
          await complete(() =>
            store.createSpace(id, {
              name: form.name.trim(),
              bucket: form.bucket.trim(),
              prefix: form.prefix,
              capacity_bytes: form.capacity_bytes,
            }),
          )
        )
          closeForm()
      }
    }
  } finally {
    if (
      kind === 'authorization' &&
      isSessionCurrent(session) &&
      version === generation &&
      submittedForm === formGeneration
    )
      clearSecrets()
  }
}
function changeConnection() {
  const item = selected.value
  if (
    !item ||
    !store.connectionAllowed(item.id, 'set_status') ||
    !['enabled', 'disabled'].includes(item.status)
  )
    return
  const state = item.status === 'enabled' ? 'disabled' : 'enabled'
  const session = captureSession(),
    version = generation,
    owner = storageScopeKey(props.scope)
  dialog.warning({
    title: t(state === 'enabled' ? 'storage.enable' : 'storage.disable'),
    content: t('storage.stateConfirm', { name: item.name }),
    positiveText: t('storage.save'),
    negativeText: t('storage.cancel'),
    onPositiveClick: () => {
      if (
        !isSessionCurrent(session) ||
        version !== generation ||
        owner !== storageScopeKey(props.scope) ||
        selected.value?.id !== item.id ||
        selected.value.management_generation !== item.management_generation ||
        !store.connectionAllowed(item.id, 'set_status')
      )
        return
      return complete(() => store.setConnectionState(item.id, state)).then(() => undefined)
    },
  })
}
function revoke() {
  const item = selected.value
  if (!item || !store.connectionAllowed(item.id, 'revoke_auth')) return
  const session = captureSession(),
    version = generation
  dialog.warning({
    title: t('storage.revoke'),
    content: t('storageManagement.revokeConfirm'),
    positiveText: t('storage.revoke'),
    negativeText: t('storage.cancel'),
    onPositiveClick: () => {
      if (
        !isSessionCurrent(session) ||
        version !== generation ||
        selected.value?.id !== item.id ||
        selected.value.management_generation !== item.management_generation ||
        !store.connectionAllowed(item.id, 'revoke_auth')
      )
        return
      closeForm()
      return complete(() => store.revoke(item.id)).then(() => undefined)
    },
  })
}
function checkLabel(group: 'checkStates' | 'checkModes' | 'cleanupStates', value: string) {
  const supported = {
    checkStates: [
      'pending',
      'running',
      'completed',
      'failed',
      'succeeded',
      'available',
      'unavailable',
      'success',
      'passed',
      'blocked',
      'skipped',
      'not_checked',
    ],
    checkModes: ['read_only', 'write'],
    cleanupStates: ['cleanup_pending', 'running', 'blocked', 'done'],
  }
  return supported[group].includes(value)
    ? t(`storageManagement.${group}.${value}`)
    : t('storage.unknown')
}
function changeSpace(id: number, status: string, name: string) {
  const connection = selectedId.value
  if (
    connection === null ||
    !store.spaceAllowed(connection, id) ||
    !['active', 'read_only', 'disabled'].includes(status)
  )
    return
  const next = status === 'active' ? 'read_only' : 'active'
  const session = captureSession(),
    version = generation,
    owner = storageScopeKey(props.scope),
    managementGeneration = selectedSpaces.value?.items.find(
      (item) => item.id === id,
    )?.management_generation
  dialog.warning({
    title: t(next === 'active' ? 'storage.makeActive' : 'storage.makeReadOnly'),
    content: t('storage.stateConfirm', { name }),
    positiveText: t('storage.save'),
    negativeText: t('storage.cancel'),
    onPositiveClick: () => {
      if (
        !isSessionCurrent(session) ||
        version !== generation ||
        owner !== storageScopeKey(props.scope) ||
        selectedId.value !== connection ||
        selectedSpaces.value?.items.find((item) => item.id === id)?.management_generation !==
          managementGeneration ||
        !store.spaceAllowed(connection, id)
      )
        return
      return complete(() => store.setSpaceState(connection, id, next)).then(() => undefined)
    },
  })
}
watch(
  () => [storageScopeKey(props.scope), sessionGeneration.value] as const,
  () => {
    ++generation
    selectedId.value = null
    closeForm()
    void store.load(props.scope)
  },
  { immediate: true },
)
watch(
  () => store.canManage,
  (value) => {
    if (!value) {
      ++generation
      selectedId.value = null
      closeForm()
    }
  },
)
watch(selectedId, () => {
  ++generation
  closeForm()
})
watch(selected, (value) => {
  if (!value) closeForm()
})
const unsubscribeRefresh = subscribeStorageRefresh({
  scope: () => (props.scope.kind === 'org' ? { organizationId: props.scope.id } : {}),
  invalidate: store.markStale,
  refresh,
})
onUnmounted(() => {
  unsubscribeRefresh()
  ++generation
  clearSecrets()
})
</script>

<template>
  <section class="space-y-4">
    <NAlert v-if="scope.kind !== 'site'" type="info">{{ t('storage.preparationHint') }}</NAlert>
    <div class="flex flex-wrap items-center justify-between gap-3">
      <h2 class="text-lg font-semibold text-lf-text-strong">
        {{
          t(
            scope.kind === 'site'
              ? 'storage.site'
              : scope.kind === 'org'
                ? 'storage.organization'
                : 'storage.personal',
          )
        }}
      </h2>
      <div class="flex gap-2">
        <NButton :loading="store.connections.loading" @click="refresh">{{
          t('storage.refreshStorage')
        }}</NButton>
        <NButton
          v-if="scope.kind !== 'site' && store.canManage"
          type="primary"
          :disabled="!store.canCreateConnection"
          @click="openForm('connection')"
          >{{ t('storage.createConnection') }}</NButton
        >
      </div>
    </div>
    <NAlert v-if="store.runtime?.deployment_enabled === false" type="info">{{
      t('storageManagement.runtimeDisabled')
    }}</NAlert>
    <NAlert v-if="store.runtime?.maintenance" type="info">{{
      t('storageManagement.runtimeMaintenance')
    }}</NAlert>
    <p
      v-if="scope.kind !== 'site' && store.createConnectionReason"
      class="text-sm text-lf-text-muted"
    >
      {{ t('storage.createConnection') }}：{{ store.createConnectionReason }}
    </p>
    <NAlert v-if="store.connections.error" type="error">{{ store.connections.error }}</NAlert>
    <NAlert v-if="store.connections.stale" type="warning">{{ t('storage.stale') }}</NAlert>
    <p v-if="store.updatedAt" class="text-xs text-lf-text-subtle">
      {{
        t('storage.updatedAt', {
          time: formatDateTime(new Date(store.updatedAt).toISOString(), {
            dateStyle: 'medium',
            timeStyle: 'short',
          }),
        })
      }}
    </p>
    <NSkeleton v-if="store.connections.loading && !store.connections.loaded" height="150px" />
    <NEmpty
      v-else-if="store.connections.loaded && !store.connections.items.length"
      :description="t('storage.empty')"
      class="py-10"
    />
    <div v-else class="grid gap-3">
      <NCard v-for="item in store.connections.items" :key="item.id" size="small">
        <div class="flex flex-wrap items-center justify-between gap-3">
          <div>
            <p class="font-medium">{{ item.name }}</p>
            <div class="mt-2 flex flex-wrap items-center gap-2">
              <NTag size="small">{{
                ['enabled', 'disabled'].includes(item.status)
                  ? t(`storage.states.${item.status}`)
                  : t('storage.unknown')
              }}</NTag
              ><StorageHealth :value="item.health" /><span class="text-xs text-lf-text-subtle">{{
                item.driver
              }}</span>
            </div>
          </div>
          <NButton secondary @click="showDetails(item.id)">{{ t('storage.details') }}</NButton>
        </div>
      </NCard>
    </div>
    <NDrawer
      :show="selectedId !== null && !!selected"
      :width="580"
      placement="right"
      class="max-w-full"
      @update:show="
        (value: boolean) => {
          if (!value) selectedId = null
        }
      "
    >
      <NDrawerContent :title="selected?.name" closable>
        <div v-if="selected" class="space-y-5">
          <NAlert v-if="store.writeErrors[`connection:${selected.id}`]" type="warning">{{
            store.writeErrors[`connection:${selected.id}`]
          }}</NAlert>
          <NAlert v-if="store.writeErrors[`revoke:${selected.id}`]" type="warning">{{
            store.writeErrors[`revoke:${selected.id}`]
          }}</NAlert>
          <dl class="grid grid-cols-1 gap-3 text-sm">
            <div>
              <dt class="text-lf-text-subtle">{{ t('storage.endpoint') }}</dt>
              <dd class="break-all">{{ selected.endpoint }}</dd>
            </div>
            <div>
              <dt class="text-lf-text-subtle">{{ t('storage.region') }}</dt>
              <dd>{{ selected.region }}</dd>
            </div>
            <div>
              <dt class="text-lf-text-subtle">{{ t('storage.lastChecked') }}</dt>
              <dd>
                {{
                  selected.checked_at
                    ? formatDateTime(selected.checked_at, {
                        dateStyle: 'medium',
                        timeStyle: 'short',
                      })
                    : t('storage.neverChecked')
                }}
              </dd>
            </div>
          </dl>
          <div class="flex flex-wrap gap-2">
            <StorageHealth :value="selected.health" /><NTag>{{
              t(selected.has_auth ? 'storage.hasAuth' : 'storage.noAuth')
            }}</NTag>
          </div>
          <div class="flex flex-wrap gap-2">
            <NButton
              :disabled="!store.connectionAllowed(selected.id, 'check_read')"
              :loading="selectedBusy"
              @click="complete(() => store.check(selected!.id), 'storage.checked')"
              >{{ t('storage.readCheck') }}</NButton
            >
            <NButton
              :disabled="
                !store.connectionAllowed(selected.id, 'check_write') ||
                !getStorageContractGate('writeCheck').available
              "
              :title="
                getStorageContractGate('writeCheck').available
                  ? undefined
                  : t('storage.writeCheckPending')
              "
              @click="complete(() => store.check(selected!.id, true))"
              >{{ t('storage.writeCheck') }}</NButton
            >
            <NButton
              :disabled="
                !store.connectionAllowed(selected.id, 'authorize_read') &&
                !store.connectionAllowed(selected.id, 'authorize_write')
              "
              @click="openForm('authorization')"
              >{{ t('storage.authorize') }}</NButton
            >
            <NButton
              :disabled="!store.connectionAllowed(selected.id, 'revoke_auth')"
              :loading="!!store.busy[`revoke:${selected.id}`]"
              @click="revoke"
              >{{ t('storage.revoke') }}</NButton
            >
            <NButton
              :disabled="
                !store.connectionAllowed(selected.id, 'set_status') ||
                !['enabled', 'disabled'].includes(selected.status)
              "
              @click="changeConnection"
              >{{
                t(selected.status === 'enabled' ? 'storage.disable' : 'storage.enable')
              }}</NButton
            >
          </div>
          <div v-if="blockedActions.length" class="space-y-1 text-sm text-lf-text-muted">
            <p v-for="item in blockedActions" :key="item.action">
              {{ t(actionLabels[item.action]) }}：{{ item.reason }}
            </p>
          </div>
          <p class="text-xs text-lf-text-subtle">{{ t('storage.checkHint') }}</p>
          <section class="space-y-3">
            <div class="flex items-center justify-between gap-3">
              <h3 class="font-semibold">{{ t('storageManagement.checks') }}</h3>
              <NButton
                size="small"
                :loading="selectedChecks?.loading"
                @click="store.loadChecks(selected!.id)"
                >{{ t('storage.refreshStorage') }}</NButton
              >
            </div>
            <p class="text-xs text-lf-text-subtle">{{ t('storageManagement.checksHint') }}</p>
            <NAlert v-if="selectedChecks?.error" type="warning">{{ selectedChecks.error }}</NAlert>
            <NSkeleton v-if="selectedChecks?.loading && !selectedChecks.loaded" height="80px" />
            <NEmpty
              v-else-if="selectedChecks?.loaded && !selectedChecks.items.length"
              :description="t('storageManagement.checksEmpty')"
            />
            <NCard v-for="check in selectedChecks?.items ?? []" :key="check.check_id" size="small">
              <div class="flex flex-wrap items-center gap-2">
                <span class="text-sm font-medium"
                  >#{{ check.check_id }} · {{ checkLabel('checkModes', check.mode) }}</span
                >
                <NTag size="small">{{ checkLabel('checkStates', check.status) }}</NTag>
                <NTag size="small" :type="check.authorization_activated ? 'success' : 'default'">{{
                  t(
                    check.authorization_activated
                      ? 'storageManagement.authorizationActivated'
                      : 'storageManagement.authorizationNotActivated',
                  )
                }}</NTag>
              </div>
              <p class="mt-2 text-xs text-lf-text-subtle">
                {{ formatDateTime(check.created_at, { dateStyle: 'medium', timeStyle: 'short' }) }}
              </p>
              <p v-if="check.error_code" class="mt-2 text-sm text-lf-text-muted">
                {{ storageTaskErrorMessage(check.error_code) }}
              </p>
              <ul class="mt-3 space-y-1 text-sm">
                <li v-for="result in check.results" :key="result.space_id">
                  {{ t('storageManagement.spaceResult', { id: result.space_id }) }} ·
                  {{ checkLabel('checkStates', result.status)
                  }}<span v-if="result.error_code">
                    · {{ storageTaskErrorMessage(result.error_code) }}</span
                  >
                </li>
              </ul>
              <p class="mt-3 text-xs text-lf-text-muted">
                {{ checkLabel('cleanupStates', check.cleanup_status) }} ·
                {{ t('storageManagement.probeAccounted') }}:
                {{
                  Number.isSafeInteger(check.accounted_bytes) && check.accounted_bytes >= 0
                    ? t('storage.bytes', { value: check.accounted_bytes.toLocaleString() })
                    : t('storage.unknown')
                }}
              </p>
            </NCard>
            <NButton
              v-if="selectedChecks?.nextCursor"
              size="small"
              :disabled="selectedChecks.loading"
              @click="store.loadChecks(selected!.id, true)"
              >{{ t('storageManagement.loadMoreChecks') }}</NButton
            >
          </section>
          <div class="flex items-center justify-between gap-3">
            <h3 class="font-semibold">{{ t('storage.spaces') }}</h3>
            <NButton
              :disabled="!store.connectionAllowed(selected.id, 'create_space')"
              @click="openForm('space')"
              >{{ t('storage.createSpace') }}</NButton
            >
          </div>
          <NAlert v-if="selectedSpaces?.error" type="error">{{ selectedSpaces.error }}</NAlert>
          <NSkeleton v-if="selectedSpaces?.loading && !selectedSpaces.loaded" height="130px" />
          <NEmpty
            v-else-if="selectedSpaces?.loaded && !selectedSpaces.items.length"
            :description="t('storage.spacesEmpty')"
          />
          <NCard
            v-for="space in selectedSpaces?.items ?? []"
            :key="space.id"
            :title="space.name"
            size="small"
          >
            <div class="mb-3 flex flex-wrap gap-2">
              <NTag size="small">{{
                ['active', 'read_only', 'disabled'].includes(space.status)
                  ? t(`storage.states.${space.status}`)
                  : t('storage.unknown')
              }}</NTag
              ><NTag size="small">{{
                t(space.verified ? 'storage.verified' : 'storage.unverified')
              }}</NTag>
            </div>
            <p v-if="space.bucket" class="mb-3 break-all text-xs text-lf-text-muted">
              {{ space.bucket }} / {{ space.prefix }}
            </p>
            <StorageCapacity :space="space" />
            <NButton
              class="mt-3"
              :disabled="
                !store.spaceAllowed(selected.id, space.id) ||
                selectedSpaces?.stale ||
                !['active', 'read_only', 'disabled'].includes(space.status)
              "
              @click="changeSpace(space.id, space.status, space.name)"
              >{{
                t(space.status === 'active' ? 'storage.makeReadOnly' : 'storage.makeActive')
              }}</NButton
            >
            <p
              v-if="store.spaceReason(selected.id, space.id)"
              class="mt-2 text-xs text-lf-text-muted"
            >
              {{ store.spaceReason(selected.id, space.id) }}
            </p>
          </NCard>
        </div>
      </NDrawerContent>
    </NDrawer>
    <NModal
      :show="formKind !== null"
      preset="card"
      class="max-w-lg"
      :title="
        t(
          formKind === 'connection'
            ? 'storage.createConnection'
            : formKind === 'space'
              ? 'storage.createSpace'
              : 'storage.authorization',
        )
      "
      @update:show="
        (value: boolean) => {
          if (!value) closeForm()
        }
      "
    >
      <NAlert v-if="formError || store.writeErrors[writeKey]" type="error" class="mb-4">{{
        formError || store.writeErrors[writeKey]
      }}</NAlert>
      <NForm label-placement="top" @submit.prevent="submit">
        <template v-if="formKind === 'authorization'">
          <NFormItem :label="t('storageManagement.authorizationPurpose')"
            ><NSelect
              :value="authorizationWrite ? 'write' : 'read'"
              :options="authorizationOptions"
              @update:value="(value) => (authorizationWrite = value === 'write')"
          /></NFormItem>
          <p class="mb-3 text-sm text-lf-text-muted">
            {{ t('storageManagement.authorizationPurposeHint') }}
          </p>
          <p class="mb-4 text-sm text-lf-text-muted">{{ t('storage.secretHint') }}</p>
          <NFormItem :label="t('storage.accessKey')" required
            ><NInput v-model:value="secrets.access_key_id" :input-props="{ autocomplete: 'off' }"
          /></NFormItem>
          <NFormItem :label="t('storage.secretKey')" required
            ><NInput
              v-model:value="secrets.secret_access_key"
              type="password"
              show-password-on="click"
              :input-props="{ autocomplete: 'new-password' }"
          /></NFormItem>
          <NFormItem :label="t('storage.sessionToken')"
            ><NInput
              v-model:value="secrets.session_token"
              type="password"
              :input-props="{ autocomplete: 'new-password' }"
          /></NFormItem>
        </template>
        <template v-else>
          <NFormItem :label="t('storage.name')" required
            ><NInput v-model:value="form.name"
          /></NFormItem>
          <template v-if="formKind === 'connection'">
            <NFormItem :label="t('storage.endpoint')" required
              ><NInput v-model:value="form.endpoint" placeholder="https://s3.example.com"
            /></NFormItem>
            <NFormItem :label="t('storage.region')" required
              ><NInput v-model:value="form.region"
            /></NFormItem>
            <NFormItem :label="t('storage.pathStyle')"
              ><NSwitch v-model:value="form.path_style"
            /></NFormItem>
          </template>
          <template v-else>
            <NFormItem :label="t('storage.bucket')" required
              ><NInput v-model:value="form.bucket"
            /></NFormItem>
            <NFormItem :label="t('storage.prefix')"
              ><NInput v-model:value="form.prefix"
            /></NFormItem>
            <NFormItem :label="t('storage.capacityBytes')" required
              ><NInputNumber
                :value="form.capacity_bytes"
                :min="1"
                :max="Number.MAX_SAFE_INTEGER"
                :precision="0"
                class="w-full"
                @update:value="(value) => (form.capacity_bytes = value ?? 0)"
            /></NFormItem>
          </template>
        </template>
      </NForm>
      <p v-if="!formAllowed" class="mt-3 text-sm text-lf-text-muted">{{ formReason }}</p>
      <template #footer
        ><div class="flex justify-end gap-2">
          <NButton @click="closeForm">{{ t('storage.cancel') }}</NButton
          ><NButton
            type="primary"
            :loading="!!store.busy[writeKey]"
            :disabled="!formAllowed || !!store.unknownWrites[writeKey]"
            @click="submit"
            >{{ t('storage.save') }}</NButton
          >
        </div></template
      >
    </NModal>
  </section>
</template>
