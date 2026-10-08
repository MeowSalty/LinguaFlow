<script setup lang="ts">
import { computed, onMounted, onUnmounted, ref, watch } from 'vue'
import { onBeforeRouteLeave } from 'vue-router'
import { NAlert, NButton, NForm, NFormItem, NModal, useDialog, useMessage } from 'naive-ui'
import { useI18n } from 'vue-i18n'
import { captureSession, isSessionCurrent, sessionGeneration } from '@/api/session-context'
import { useStorageStore, storageScopeKey } from '@/stores/storage'
import { hasSpaceManagementActions, hasStorageSpaceQuota } from '@/utils/storage-availability'
import { quotaDraft, resolveQuota } from '@/utils/storage-quota'
import {
  createSpaceQuotaDraft,
  observeSpaceQuota,
  reviewSpaceQuota,
  spaceQuotaDirty,
  type SpaceQuotaDraft,
} from '@/utils/storage-quota-draft'
import { invalidateStorageSnapshots } from '@/utils/storage-snapshots'
import StorageQuotaInput from './StorageQuotaInput.vue'
import StorageCapacity from './StorageCapacity.vue'
import { formatStorageBytes } from './capacity'
import { storageConfirmationButtons } from './confirmation'

const props = defineProps<{ show: boolean; connectionId: number; spaceId: number }>()
const emit = defineEmits<{ 'update:show': [value: boolean] }>()
const store = useStorageStore(),
  { t } = useI18n(),
  dialog = useDialog(),
  message = useMessage()
const editor = ref<SpaceQuotaDraft | null>(null)
const saved = ref(false)
const refreshing = ref(false)
const returnFocus = ref<HTMLElement | null>(null)
const key = computed(() => `connection:${props.connectionId}`)
const recovery = computed(() => store.quotaRecoveries[props.spaceId])
const space = computed(() =>
  store.spaces[props.connectionId]?.items.find((item) => item.id === props.spaceId),
)
const resolution = computed(() =>
  editor.value ? resolveQuota(editor.value.draft) : { ok: false as const },
)
const busy = computed(() => !!store.busy[key.value])
const actionAllowed = computed(
  () =>
    !!space.value &&
    hasSpaceManagementActions(space.value, 'set_quota') &&
    space.value.management_actions.set_quota.allowed,
)
const proposedOver = computed(() => {
  const current = space.value,
    proposed = resolution.value
  if (!current || !hasStorageSpaceQuota(current) || !proposed.ok || proposed.value === null)
    return 0
  return Math.max(
    0,
    current.reserved_bytes +
      current.candidate_bytes +
      current.live_bytes +
      current.pending_delete_bytes -
      proposed.value,
  )
})
const dirty = computed(
  () => props.show && !!editor.value && spaceQuotaDirty(editor.value) && !recovery.value,
)
const allowed = computed(
  () =>
    !!editor.value &&
    resolution.value.ok &&
    !editor.value.latest &&
    !recovery.value &&
    !busy.value &&
    store.spaceAllowed(props.connectionId, props.spaceId, 'set_quota') &&
    editor.value.baseline.management_generation === space.value?.management_generation,
)
const quotaText = (value: number | null | undefined) =>
  value === null
    ? t('storageQuota.unlimited')
    : value === undefined
      ? t('storageQuota.unavailable')
      : `${formatStorageBytes(value)} (${value.toLocaleString()} B)`
let view = 0
watch(
  () => [props.show, props.connectionId, props.spaceId] as const,
  ([show]) => {
    ++view
    saved.value = false
    refreshing.value = false
    if (!show) return
    returnFocus.value =
      document.activeElement instanceof HTMLElement ? document.activeElement : null
    const pending = recovery.value
    editor.value = pending
      ? createSpaceQuotaDraft(pending.baseline, quotaDraft(pending.submitted.capacity_bytes))
      : space.value && hasStorageSpaceQuota(space.value)
        ? createSpaceQuotaDraft(space.value)
        : null
  },
  { immediate: true },
)
watch(space, (latest) => {
  if (!props.show || !latest || !hasStorageSpaceQuota(latest) || recovery.value) return
  editor.value = editor.value
    ? observeSpaceQuota(editor.value, latest)
    : createSpaceQuotaDraft(latest)
})
watch(
  () =>
    [
      sessionGeneration.value,
      storageScopeKey(store.scope),
      store.canManage,
      !!space.value,
      actionAllowed.value,
    ] as const,
  ([, , canManage, exists, permitted], previous) => {
    if (
      previous &&
      (previous[0] !== sessionGeneration.value ||
        previous[1] !== storageScopeKey(store.scope) ||
        !canManage ||
        !exists ||
        !permitted)
    ) {
      ++view
      editor.value = null
      emit('update:show', false)
    }
  },
)
function confirmDiscard(): Promise<boolean> {
  if (busy.value) return Promise.resolve(false)
  if (!dirty.value) return Promise.resolve(true)
  const version = view
  return new Promise((resolve) => {
    dialog.warning({
      title: t('storageQuota.discardTitle'),
      content: t('storageQuota.discardBody'),
      positiveText: t('storageQuota.discard'),
      negativeText: t('storageQuota.continueEditing'),
      ...storageConfirmationButtons,
      onPositiveClick: () => resolve(version === view),
      onNegativeClick: () => resolve(false),
      onClose: () => resolve(false),
      onMaskClick: () => resolve(false),
    })
  })
}
async function close() {
  if (await confirmDiscard()) emit('update:show', false)
}
onBeforeRouteLeave(() => (props.show ? confirmDiscard() : true))
function beforeUnload(event: BeforeUnloadEvent) {
  if (props.show && (dirty.value || busy.value || recovery.value)) {
    event.preventDefault()
    event.returnValue = ''
  }
}
onMounted(() => window.addEventListener('beforeunload', beforeUnload))
onUnmounted(() => window.removeEventListener('beforeunload', beforeUnload))
function restoreFocus() {
  if (returnFocus.value?.isConnected) returnFocus.value.focus({ preventScroll: true })
  returnFocus.value = null
}
async function refresh() {
  if (busy.value || refreshing.value) return
  const version = view,
    session = captureSession(),
    owner = storageScopeKey(store.scope)
  refreshing.value = true
  try {
    await Promise.all([
      store.load(),
      store.scope.kind === 'site' ? store.loadPolicy() : Promise.resolve(),
    ])
    if (version === view && isSessionCurrent(session) && owner === storageScopeKey(store.scope))
      await store.loadSpaces(props.connectionId, true)
  } finally {
    if (version === view) refreshing.value = false
  }
}
function acknowledge() {
  const pending = recovery.value
  if (!pending || !editor.value) return
  const latest = store.acknowledgeSpaceQuotaUnknown(
    props.spaceId,
    pending.attemptId,
    pending.reviewRevision,
  )
  if (latest) editor.value = createSpaceQuotaDraft(latest, editor.value.draft)
}
async function save() {
  if (!allowed.value || !editor.value || !resolution.value.ok) return
  const version = view,
    session = captureSession(),
    connectionId = props.connectionId,
    spaceId = props.spaceId
  const result = await store.setSpaceQuota(connectionId, spaceId, {
    capacity_bytes: resolution.value.value,
    expected_generation: editor.value.baseline.management_generation,
  })
  if (version !== view || !isSessionCurrent(session)) return
  if (result.status === 'success') {
    editor.value = createSpaceQuotaDraft(result.value)
    saved.value = true
    message.success(t('storageQuota.saved'))
    invalidateStorageSnapshots(store.scope.kind === 'org' ? { organizationId: store.scope.id } : {})
  }
  if (result.status !== 'stale') await refresh()
}
</script>

<template>
  <NModal
    :show="show"
    preset="card"
    :title="t('storageQuota.title')"
    :style="{ width: 'min(512px, calc(100vw - 32px))' }"
    :closable="!busy"
    :close-on-esc="!busy"
    :mask-closable="!busy"
    @update:show="
      (value) => {
        if (!value) close()
      }
    "
    @after-leave="restoreFocus"
  >
    <div class="min-w-0 space-y-4" data-storage-quota-dialog>
      <h3 class="text-sm font-semibold break-words [overflow-wrap:anywhere]">
        {{ space?.name ?? editor?.baseline.name }}
      </h3>
      <NAlert v-if="recovery" type="warning" :bordered="false">
        <p>{{ t('storageQuota.unknown') }}</p>
        <p class="mt-2 text-xs leading-5">{{ t('storageQuota.unknownHint') }}</p>
      </NAlert>
      <NAlert v-else-if="editor?.latest" type="warning" :bordered="false">{{
        t('storageQuota.conflict')
      }}</NAlert>
      <NAlert v-else-if="store.writeErrors[key]" type="warning" :bordered="false">{{
        store.writeErrors[key]
      }}</NAlert>
      <NAlert v-if="saved && store.spaces[connectionId]?.stale" type="info" :bordered="false">{{
        t('storageQuota.savedPending')
      }}</NAlert>
      <StorageCapacity v-if="space" :space="space" compact />
      <dl v-if="editor && (editor.latest || recovery)" class="space-y-2 text-sm">
        <div>
          <dt class="text-xs text-lf-text-muted">{{ t('storageQuota.original') }}</dt>
          <dd class="break-words">{{ quotaText(editor.baseline.capacity_bytes) }}</dd>
        </div>
        <div>
          <dt class="text-xs text-lf-text-muted">{{ t('storageQuota.current') }}</dt>
          <dd class="break-words">
            {{ quotaText((recovery?.latest ?? editor.latest)?.capacity_bytes) }}
          </dd>
        </div>
        <div>
          <dt class="text-xs text-lf-text-muted">
            {{ t(recovery ? 'storageQuota.attempted' : 'storageQuota.proposed') }}
          </dt>
          <dd class="break-words">
            {{
              quotaText(
                recovery
                  ? recovery.submitted.capacity_bytes
                  : resolution.ok
                    ? resolution.value
                    : undefined,
              )
            }}
          </dd>
        </div>
      </dl>
      <NForm v-if="editor" label-placement="top" @submit.prevent="save">
        <NFormItem :label="t('storageUi.quotaLabel')" required>
          <StorageQuotaInput
            v-model:value="editor.draft"
            :disabled="busy || !!recovery"
            :label="t('storageUi.quotaLabel')"
          />
        </NFormItem>
      </NForm>
      <p v-else class="text-sm text-lf-text-muted">{{ t('storageQuota.unavailable') }}</p>
      <p class="text-xs leading-5 text-lf-text-muted">{{ t('storageQuota.lowerHint') }}</p>
      <NAlert v-if="proposedOver > 0" type="warning" :bordered="false" role="status">
        {{
          t('storageQuota.proposedOver', {
            value: formatStorageBytes(proposedOver),
            bytes: proposedOver.toLocaleString(),
          })
        }}
      </NAlert>
      <div v-if="editor?.latest && !recovery" class="flex flex-wrap gap-2">
        <NButton size="small" @click="editor = reviewSpaceQuota(editor!, true)">{{
          t('storageQuota.keepDraft')
        }}</NButton>
        <NButton size="small" @click="editor = reviewSpaceQuota(editor!, false)">{{
          t('storageQuota.loadLatest')
        }}</NButton>
      </div>
      <template v-if="recovery">
        <p v-if="recovery.state === 'error'" role="status" class="text-sm text-lf-text-muted">
          {{ t('storageQuota.reviewFailed') }}
        </p>
        <NButton
          :disabled="recovery.state !== 'ready' || refreshing || !store.ready"
          @click="acknowledge"
          >{{ t('storageQuota.acknowledge') }}</NButton
        >
      </template>
      <p
        v-else-if="!store.spaceAllowed(connectionId, spaceId, 'set_quota')"
        class="text-xs leading-5 text-lf-text-muted"
      >
        {{ store.spaceReason(connectionId, spaceId, 'set_quota') }}
      </p>
    </div>
    <template #footer
      ><div class="flex flex-wrap justify-end gap-2">
        <NButton quaternary :loading="refreshing" :disabled="busy" @click="refresh">{{
          t('storageQuota.refresh')
        }}</NButton>
        <NButton :disabled="busy" @click="close">{{ t('storage.cancel') }}</NButton>
        <NButton type="primary" :loading="busy" :disabled="!allowed" @click="save">{{
          t('storageQuota.save')
        }}</NButton>
      </div></template
    >
  </NModal>
</template>
