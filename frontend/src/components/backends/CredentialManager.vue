<script setup lang="ts">
import { computed, onScopeDispose, ref, watch } from 'vue'
import {
  NAlert,
  NButton,
  NDivider,
  NDrawer,
  NDrawerContent,
  NEmpty,
  NForm,
  NFormItem,
  NInput,
  NSelect,
  NSpin,
  NTag,
  useDialog,
  useMessage,
  type DialogReactive,
} from 'naive-ui'
import { useI18n } from 'vue-i18n'
import { useCredentialsStore } from '@/stores/credentials'
import { captureSession, isSessionCurrent, onSessionChange } from '@/api/session-context'
import {
  canManageOrganization,
  onOrganizationInvalidated,
  organizationRoles,
} from '@/utils/organization-scope'
import { DRAWER_WIDTH } from '@/components/common/uiConstants'
import type { ApiSchemas } from '@/api/client-core'

const props = defineProps<{
  show: boolean
  orgId: number | null
  refreshBackends: () => Promise<boolean>
}>()
const emit = defineEmits<{ 'update:show': [value: boolean] }>()
const { t } = useI18n()
const store = useCredentialsStore()
const dialog = useDialog(),
  message = useMessage()
const provider = ref<ApiSchemas['Credential']['provider']>('openai')
const endpoint = ref(''),
  secret = ref(''),
  rotateSecret = ref('')
const selectedId = ref<number | null>(null)
const creating = ref(false),
  refreshing = ref(false),
  refreshError = ref(false)
const notice = ref('')
let generation = 0
let confirmation: DialogReactive | undefined
let refreshFlight: Promise<void> | null = null
let refreshQueued = false,
  pendingBackends = false,
  failedBackends = false
const pendingVersions = new Set<number>(),
  failedVersions = new Set<number>()
const canManage = computed(
  () => props.orgId === null || canManageOrganization(organizationRoles.value[props.orgId]),
)
const providerOptions = computed(() =>
  ['openai', 'anthropic', 'google'].map((value) => ({
    value,
    label: t(`backends.types.${value}`),
  })),
)
const selected = computed(() => store.items.find((item) => item.id === selectedId.value))
const versionState = computed(() =>
  selectedId.value === null ? undefined : store.versions[selectedId.value],
)
const reset = () => {
  ++generation
  secret.value = rotateSecret.value = ''
  selectedId.value = null
  creating.value = refreshing.value = refreshError.value = false
  notice.value = ''
  refreshFlight = null
  refreshQueued = pendingBackends = failedBackends = false
  pendingVersions.clear()
  failedVersions.clear()
  confirmation?.destroy()
  confirmation = undefined
}
const currentContext = () => {
  const session = captureSession(),
    version = generation,
    scope = props.orgId
  return () =>
    props.show &&
    canManage.value &&
    version === generation &&
    scope === props.orgId &&
    isSessionCurrent(session)
}
watch(
  () => [props.show, props.orgId, canManage.value] as const,
  ([show, scope, allowed]) => {
    reset()
    if (show && allowed) {
      store.setOrganization(scope)
      void store.load(scope)
    } else if (show) emit('update:show', false)
  },
  { immediate: true, flush: 'sync' },
)
watch(
  () => store.accessDenied,
  (denied) => {
    if (denied && props.show) {
      reset()
      emit('update:show', false)
      message.error(t('configurationCredentials.denied'))
    }
  },
)
watch(provider, () => {
  secret.value = ''
})
watch(endpoint, () => {
  secret.value = ''
})
watch(selectedId, () => {
  rotateSecret.value = ''
  confirmation?.destroy()
  confirmation = undefined
})
onScopeDispose(
  onSessionChange(() => {
    reset()
    emit('update:show', false)
  }),
)
onOrganizationInvalidated((id) => {
  if (id === props.orgId) {
    reset()
    emit('update:show', false)
  }
})
onScopeDispose(reset)

function refresh(id?: number, reloadBackends = false): Promise<void> {
  if (!canManage.value || !props.show) return Promise.resolve()
  if (id !== undefined) pendingVersions.add(id)
  pendingBackends ||= reloadBackends
  refreshQueued = true
  if (refreshFlight) return refreshFlight
  const current = currentContext()
  refreshing.value = true
  const work = async () => {
    try {
      while (refreshQueued && current()) {
        refreshQueued = false
        const ids = new Set([...pendingVersions, ...failedVersions])
        const withBackends = pendingBackends || failedBackends
        pendingVersions.clear()
        pendingBackends = false
        const [listOk, backendsOk] = await Promise.all([
          store.load(props.orgId),
          withBackends ? props.refreshBackends().catch(() => false) : Promise.resolve(true),
        ])
        if (!current()) return
        failedBackends = !backendsOk
        const results = await Promise.all(
          [...ids].map(async (credentialId) => ({
            id: credentialId,
            ok:
              listOk &&
              (!store.items.some((item) => item.id === credentialId) ||
                (await store.loadVersions(credentialId))),
          })),
        )
        if (!current()) return
        for (const result of results) {
          if (result.ok) failedVersions.delete(result.id)
          else failedVersions.add(result.id)
        }
        refreshError.value = !listOk || failedBackends || failedVersions.size > 0
      }
    } finally {
      if (current()) {
        refreshing.value = false
        refreshFlight = null
      }
    }
  }
  refreshFlight = work()
  return refreshFlight
}
async function select(id: number): Promise<void> {
  selectedId.value = id
  await store.loadVersions(id)
}
function confirm(content: string, action: () => Promise<void>): void {
  const current = currentContext()
  confirmation = dialog.warning({
    title: t('common.confirm'),
    content,
    positiveText: t('common.confirm'),
    negativeText: t('common.cancel'),
    onPositiveClick: async () => {
      if (current()) await action()
    },
  })
}
function toggleCreate(): void {
  creating.value = !creating.value
  secret.value = ''
}
async function create(retry = false): Promise<void> {
  if (!secret.value.trim() || store.creation.busy || !canManage.value) return
  if (store.creation.unknown && !retry) {
    confirm(t('configurationCredentials.retryWriteConfirm'), () => create(true))
    return
  }
  const current = currentContext()
  const result = await store.create(
    {
      provider: provider.value,
      ...(endpoint.value.trim() ? { endpoint: endpoint.value.trim() } : {}),
      secret: secret.value.trim(),
    },
    retry,
  )
  if (!current() || result.status !== 'success') return
  secret.value = ''
  creating.value = false
  notice.value = t('configurationCredentials.success')
  message.success(notice.value)
  await refresh(undefined, false)
}
function rotate(id: number): void {
  if (!rotateSecret.value.trim() || !store.canMutate(id)) return
  const retry = !!store.writes[id]?.unknown
  confirm(
    `${t('configurationCredentials.rotateConfirm', { id })}${retry ? ` ${t('configurationCredentials.retryWriteConfirm')}` : ''}`,
    async () => {
      const current = currentContext()
      const result = await store.rotate(id, { secret: rotateSecret.value.trim() }, retry)
      if (!current() || result.status !== 'success') return
      if (selectedId.value === id) rotateSecret.value = ''
      notice.value = t('configurationCredentials.success')
      message.success(notice.value)
      await refresh(id, true)
    },
  )
}
function revoke(id: number, version: number): void {
  if (!store.canMutate(id)) return
  const retry = !!store.writes[id]?.unknown
  confirm(
    `${t('configurationCredentials.revokeConfirm', { id, version })}${retry ? ` ${t('configurationCredentials.retryWriteConfirm')}` : ''}`,
    async () => {
      const current = currentContext()
      const result = await store.revoke(id, version, retry)
      if (!current() || result.status !== 'success') return
      notice.value = t('configurationCredentials.success')
      await refresh(id, false)
    },
  )
}
function collect(id: number): void {
  if (!store.canMutate(id)) return
  const retry = !!store.writes[id]?.unknown
  confirm(
    `${t('configurationCredentials.collectConfirm', { id })}${retry ? ` ${t('configurationCredentials.retryWriteConfirm')}` : ''}`,
    async () => {
      const current = currentContext()
      const result = await store.collect(id, retry)
      if (!current() || result.status !== 'success') return
      notice.value = t('configurationCredentials.collected', {
        count: result.value.deleted_versions,
      })
      await refresh(id, false)
    },
  )
}
</script>

<template>
  <NDrawer
    :show="show"
    :width="DRAWER_WIDTH.l"
    @update:show="(value) => emit('update:show', value)"
  >
    <NDrawerContent :title="t('configurationCredentials.title')" closable>
      <div class="space-y-5" data-testid="credential-manager">
        <p class="text-sm leading-6 text-lf-text-muted">
          {{ t('configurationCredentials.subtitle') }}
        </p>
        <NTag size="small">{{
          t(
            orgId === null
              ? 'configurationCredentials.personal'
              : 'configurationCredentials.organization',
          )
        }}</NTag>
        <div class="flex flex-wrap gap-2">
          <NButton
            :loading="store.loading || refreshing"
            @click="refresh(selectedId ?? undefined)"
            >{{ t('configurationCredentials.refresh') }}</NButton
          >
          <NButton
            type="primary"
            :disabled="!canManage || store.creation.busy"
            @click="toggleCreate"
            >{{ t('configurationCredentials.create') }}</NButton
          >
        </div>
        <NAlert v-if="notice" type="success">{{ notice }}</NAlert>
        <NAlert v-if="refreshError" type="warning"
          >{{
            t(
              notice
                ? 'configurationCredentials.refreshFailed'
                : 'configurationCredentials.readFailure',
            )
          }}
          <NButton text @click="refresh()">{{
            t('configurationCredentials.retryRead')
          }}</NButton></NAlert
        >
        <NAlert v-if="store.error" type="error">{{ store.error }}</NAlert>
        <NAlert v-if="store.stale" type="warning">{{ t('configurationCredentials.stale') }}</NAlert>
        <NForm
          v-if="creating"
          label-placement="top"
          :disabled="store.creation.busy || !canManage"
          @submit.prevent="create()"
        >
          <NFormItem :label="t('configurationCredentials.provider')"
            ><NSelect v-model:value="provider" :options="providerOptions"
          /></NFormItem>
          <NFormItem :label="t('configurationCredentials.endpoint')"
            ><NInput
              v-model:value="endpoint"
              :placeholder="t('configurationCredentials.endpointDefault')"
          /></NFormItem>
          <NFormItem :label="t('configurationCredentials.secret')"
            ><NInput
              v-model:value="secret"
              type="password"
              autocomplete="new-password"
              :placeholder="t('configurationCredentials.secretPlaceholder')"
          /></NFormItem>
          <NAlert
            v-if="store.creation.error"
            class="mb-3"
            :type="store.creation.unknown ? 'warning' : 'error'"
            >{{ store.creation.error }}</NAlert
          >
          <NButton
            type="primary"
            :loading="store.creation.busy"
            :disabled="!secret.trim()"
            @click="create()"
            >{{
              t(
                store.creation.unknown
                  ? 'configurationCredentials.retryWrite'
                  : 'configurationCredentials.create',
              )
            }}</NButton
          >
        </NForm>
        <NSpin
          v-if="store.loading && !store.loaded"
          :description="t('configurationCredentials.loading')"
        />
        <NEmpty
          v-else-if="store.loaded && !store.items.length"
          :description="t('configurationCredentials.empty')"
        />
        <div
          v-for="credential in store.items"
          :key="credential.id"
          class="rounded-xl border border-lf-border p-4 space-y-3"
        >
          <div class="flex flex-wrap items-start justify-between gap-3">
            <div class="min-w-0">
              <div class="font-medium">
                {{ t(`backends.types.${credential.provider}`) }}
                <span class="text-lf-text-muted">#{{ credential.id }}</span>
              </div>
              <p class="mt-1 break-all text-xs text-lf-text-muted">{{ credential.endpoint }}</p>
            </div>
            <NTag size="small">{{
              t('configurationCredentials.current', { version: credential.current_version })
            }}</NTag>
          </div>
          <NButton size="small" :disabled="!store.ready" @click="select(credential.id)">{{
            t('configurationCredentials.versions')
          }}</NButton>
          <template v-if="selected?.id === credential.id">
            <NDivider />
            <NAlert
              v-if="store.writes[credential.id]?.error"
              :type="store.writes[credential.id]?.unknown ? 'warning' : 'error'"
              >{{ store.writes[credential.id]?.error }}</NAlert
            >
            <NAlert v-if="versionState?.error" type="error"
              >{{ versionState.error }}
              <NButton text @click="store.loadVersions(credential.id)">{{
                t('configurationCredentials.retryRead')
              }}</NButton></NAlert
            >
            <NAlert v-if="versionState?.stale" type="warning">{{
              t('configurationCredentials.stale')
            }}</NAlert>
            <NSpin v-if="versionState?.loading" />
            <NEmpty
              v-if="versionState?.loaded && !versionState.items.length"
              :description="t('configurationCredentials.versionsEmpty')"
            />
            <div
              v-for="version in versionState?.items ?? []"
              :key="version.version"
              class="flex flex-wrap items-center justify-between gap-3 text-sm"
            >
              <div>
                <span>{{
                  t('configurationCredentials.version', { version: version.version })
                }}</span
                ><NTag class="ml-2" size="small" :type="version.revoked ? 'error' : 'default'">{{
                  t(
                    version.revoked
                      ? 'configurationCredentials.revoked'
                      : 'configurationCredentials.notRevoked',
                  )
                }}</NTag>
                <p class="mt-1 text-xs text-lf-text-muted">{{ version.created_at }}</p>
              </div>
              <NButton
                size="small"
                type="error"
                secondary
                :disabled="version.revoked || !store.canMutate(credential.id) || refreshing"
                @click="revoke(credential.id, version.version)"
                >{{ t('configurationCredentials.revoke') }}</NButton
              >
            </div>
            <NForm label-placement="top" :disabled="!store.canMutate(credential.id) || refreshing">
              <NFormItem :label="t('configurationCredentials.secret')"
                ><NInput
                  v-model:value="rotateSecret"
                  type="password"
                  autocomplete="new-password"
                  :placeholder="t('configurationCredentials.secretPlaceholder')"
              /></NFormItem>
              <div class="flex flex-wrap gap-2">
                <NButton
                  :loading="store.writes[credential.id]?.busy"
                  :disabled="!rotateSecret.trim() || !store.canMutate(credential.id) || refreshing"
                  @click="rotate(credential.id)"
                  >{{ t('configurationCredentials.rotate') }}</NButton
                ><NButton
                  :disabled="!store.canMutate(credential.id) || refreshing"
                  @click="collect(credential.id)"
                  >{{ t('configurationCredentials.collect') }}</NButton
                >
              </div>
            </NForm>
          </template>
        </div>
      </div>
    </NDrawerContent>
  </NDrawer>
</template>
