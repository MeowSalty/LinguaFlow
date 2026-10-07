<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { NAlert, NButton, NModal, NSkeleton, NSwitch, useMessage } from 'naive-ui'
import { useI18n } from 'vue-i18n'
import { onBeforeRouteLeave } from 'vue-router'
import { useAdminStore } from '@/stores/admin'
import { useAuthStore } from '@/stores/auth'
import { captureSession, isSessionCurrent, onSessionChange } from '@/api/session-context'
import { useTaskRetention } from '@/composables/useTaskRetention'
import TaskRetentionCard from '@/components/admin/TaskRetentionCard.vue'

const admin = useAdminStore()
const auth = useAuthStore()
const message = useMessage()
const { t } = useI18n()
const authorized = computed(() => auth.user?.role === 'admin')
const retention = useTaskRetention(() => authorized.value)
const draft = ref<boolean | null>(null)
const registrationBase = ref<boolean | null>(null)
const registrationSaving = ref(false)
const discardVisible = ref(false)
const leaveVisible = ref(false)
type ButtonInstance = { $el: HTMLButtonElement }
const refreshButton = ref<ButtonInstance | null>(null)
const discardCancel = ref<ButtonInstance | null>(null)
const leaveCancel = ref<ButtonInstance | null>(null)
let alive = true
let requestGeneration = 0
let resolveLeave: ((leave: boolean) => void) | null = null
const busy = computed(
  () =>
    admin.settingsLoading ||
    admin.settingsSaving ||
    retention.preparing.value ||
    retention.submitting.value,
)
const hasRegistrationChanges = computed(
  () =>
    draft.value !== null &&
    registrationBase.value !== null &&
    draft.value !== registrationBase.value,
)
const hasChanges = computed(() => hasRegistrationChanges.value || retention.hasChanges.value)

// Full settings responses may confirm another card. Preserve this card's dirty draft.
watch(
  () => admin.settings,
  (confirmed) => {
    if (!confirmed || !authorized.value) {
      draft.value = null
      registrationBase.value = null
      discardVisible.value = false
      finishLeave(false)
    } else if (
      draft.value === null ||
      !hasRegistrationChanges.value ||
      draft.value === confirmed.registration_enabled
    ) {
      draft.value = confirmed.registration_enabled
      registrationBase.value = confirmed.registration_enabled
    }
  },
  { immediate: true, flush: 'sync' },
)

function finishLeave(leave: boolean) {
  leaveVisible.value = false
  resolveLeave?.(leave)
  resolveLeave = null
}
const stopSession = onSessionChange(() => {
  requestGeneration++
  draft.value = null
  registrationBase.value = null
  registrationSaving.value = false
  discardVisible.value = false
  finishLeave(false)
})
const reload = async (): Promise<void> => {
  if (busy.value || !authorized.value) return
  const context = captureSession()
  const generation = ++requestGeneration
  const loaded = await admin.loadSettings()
  if (!alive || generation !== requestGeneration || !isSessionCurrent(context)) return
  if (loaded && admin.settings) {
    draft.value = admin.settings.registration_enabled
    registrationBase.value = draft.value
    retention.resetDraft()
    retention.start()
  }
}
const refresh = (): void => {
  if (busy.value) return
  if (hasChanges.value) discardVisible.value = true
  else void reload()
}
const confirmRefresh = (): void => {
  discardVisible.value = false
  void reload()
}
const saveRegistration = async (): Promise<void> => {
  if (busy.value || !authorized.value || !hasRegistrationChanges.value || draft.value === null)
    return
  const context = captureSession()
  const generation = ++requestGeneration
  registrationSaving.value = true
  const saved = await admin.saveSettings({ registration_enabled: draft.value })
  if (!alive || generation !== requestGeneration || !isSessionCurrent(context)) return
  registrationSaving.value = false
  if (saved && admin.settings) {
    draft.value = admin.settings.registration_enabled
    registrationBase.value = draft.value
    message.success(t('configurationSettings.saveSuccess'))
  }
}
const discardRegistration = () => {
  if (busy.value || !admin.settings) return
  draft.value = admin.settings.registration_enabled
  registrationBase.value = draft.value
}
const beforeUnload = (event: BeforeUnloadEvent) => {
  if (!authorized.value || !hasChanges.value) return
  event.preventDefault()
  event.returnValue = ''
}
onBeforeRouteLeave(() => {
  if (!authorized.value || retention.revoked.value || !hasChanges.value) return true
  if (resolveLeave) return false
  leaveVisible.value = true
  return new Promise<boolean>((resolve) => {
    resolveLeave = resolve
  })
})
onMounted(async () => {
  window.addEventListener('beforeunload', beforeUnload)
  await reload()
  if (
    alive &&
    authorized.value &&
    !retention.revoked.value &&
    !retention.statusLoading.value &&
    !retention.status.value
  )
    retention.start()
})
onBeforeUnmount(() => {
  alive = false
  requestGeneration++
  stopSession()
  finishLeave(false)
  window.removeEventListener('beforeunload', beforeUnload)
})
const focus = (button: ButtonInstance | null) => {
  void nextTick(() => button?.$el?.focus())
}
</script>

<template>
  <div class="lf-page lf-content-narrow">
    <PageHeader :title="t('admin.settings.title')" :subtitle="t('taskRetention.pageDescription')">
      <template #actions>
        <NButton
          ref="refreshButton"
          secondary
          :loading="admin.settingsLoading"
          :disabled="busy || !authorized"
          @click="refresh"
          >{{ t('admin.settings.actions.refresh') }}</NButton
        >
      </template>
    </PageHeader>
    <NAlert v-if="!authorized || retention.revoked.value" type="error" role="alert">{{
      t('configurationSettings.accessDenied')
    }}</NAlert>
    <NAlert v-else-if="admin.settingsError || admin.settingsSaveError" type="error" role="alert">{{
      admin.settingsSaveError || admin.settingsError
    }}</NAlert>
    <template v-if="authorized && !retention.revoked.value">
      <section
        class="lf-panel p-5 sm:p-6"
        :aria-busy="admin.settingsLoading"
        aria-labelledby="registration-policy-label"
      >
        <NSkeleton v-if="admin.settingsLoading && draft === null" text :repeat="3" />
        <template v-else-if="admin.settings && draft !== null">
          <div class="flex items-start justify-between gap-5">
            <div class="min-w-0">
              <h2
                id="registration-policy-label"
                class="text-base font-semibold text-lf-text-strong"
              >
                {{ t('configurationSettings.registrationEnabled') }}
              </h2>
              <p
                id="registration-policy-effect"
                class="mt-2 text-sm leading-relaxed text-lf-text-muted"
              >
                {{ t('configurationSettings.effect') }}
              </p>
            </div>
            <NSwitch
              v-model:value="draft"
              :disabled="busy"
              :aria-disabled="busy"
              aria-labelledby="registration-policy-label"
              aria-describedby="registration-policy-effect"
            />
          </div>
          <p v-if="hasRegistrationChanges" class="mt-4 text-xs text-lf-text-subtle">
            {{ t('configurationSettings.unsaved') }}
          </p>
          <div class="mt-5 flex flex-wrap gap-2">
            <NButton
              type="primary"
              :loading="registrationSaving"
              :disabled="busy || !hasRegistrationChanges"
              @click="saveRegistration"
              >{{ t('taskRetention.saveRegistration') }}</NButton
            >
            <NButton :disabled="busy || !hasRegistrationChanges" @click="discardRegistration">{{
              t('taskRetention.discard')
            }}</NButton>
          </div>
        </template>
        <p v-else class="text-sm text-lf-text-muted">
          {{ t('configurationSettings.unconfirmed') }}
        </p>
      </section>
      <TaskRetentionCard :controller="retention" />
    </template>
    <NModal
      v-model:show="discardVisible"
      preset="card"
      :title="t('configurationSettings.discardTitle')"
      style="width: min(460px, calc(100vw - 32px))"
      :auto-focus="false"
      @after-enter="focus(discardCancel)"
      @after-leave="focus(refreshButton)"
    >
      <p class="text-sm leading-relaxed">{{ t('configurationSettings.discardContent') }}</p>
      <template #footer
        ><div class="flex flex-wrap justify-end gap-2">
          <NButton ref="discardCancel" @click="discardVisible = false">{{
            t('common.cancel')
          }}</NButton>
          <NButton type="warning" @click="confirmRefresh">{{
            t('configurationSettings.discardConfirm')
          }}</NButton>
        </div></template
      >
    </NModal>
    <NModal
      :show="leaveVisible"
      preset="card"
      :title="t('taskRetention.leaveTitle')"
      style="width: min(460px, calc(100vw - 32px))"
      :auto-focus="false"
      @update:show="!$event && finishLeave(false)"
      @after-enter="focus(leaveCancel)"
    >
      <p class="text-sm leading-relaxed">{{ t('taskRetention.leaveDescription') }}</p>
      <template #footer
        ><div class="flex flex-wrap justify-end gap-2">
          <NButton ref="leaveCancel" @click="finishLeave(false)">{{ t('common.cancel') }}</NButton>
          <NButton type="warning" @click="finishLeave(true)">{{
            t('taskRetention.leaveConfirm')
          }}</NButton>
        </div></template
      >
    </NModal>
  </div>
</template>
