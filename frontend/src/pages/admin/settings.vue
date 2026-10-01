<script setup lang="ts">
import { NAlert, NButton, NModal, NSkeleton, NSwitch, useMessage } from 'naive-ui'
import { useI18n } from 'vue-i18n'

import { useAdminStore } from '@/stores/admin'
import { captureSession, isSessionCurrent, onSessionChange } from '@/api/session-context'

const admin = useAdminStore()
const message = useMessage()
const { t } = useI18n()
const draft = ref<boolean | null>(admin.settings?.registration_enabled ?? null)
const discardVisible = ref(false)
let alive = true
let requestGeneration = 0
const busy = computed(() => admin.settingsLoading || admin.settingsSaving)
const hasChanges = computed(
  () =>
    draft.value !== null &&
    admin.settings !== null &&
    draft.value !== admin.settings.registration_enabled,
)

// A read started by a previous page instance can finish after navigating back.
watch(
  () => admin.settings,
  (confirmed) => {
    if (draft.value === null && confirmed) draft.value = confirmed.registration_enabled
  },
)

const stopSession = onSessionChange(() => {
  requestGeneration++
  draft.value = null
  discardVisible.value = false
})
onBeforeUnmount(() => {
  alive = false
  requestGeneration++
  stopSession()
})

const reload = async (): Promise<void> => {
  if (busy.value) return
  const context = captureSession()
  const generation = ++requestGeneration
  const loaded = await admin.loadSettings()
  if (!alive || generation !== requestGeneration || !isSessionCurrent(context)) return
  if (loaded && admin.settings) draft.value = admin.settings.registration_enabled
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
const save = async (): Promise<void> => {
  if (busy.value || !hasChanges.value || draft.value === null) return
  const context = captureSession()
  const generation = ++requestGeneration
  const saved = await admin.saveSettings({ registration_enabled: draft.value })
  if (!alive || generation !== requestGeneration || !isSessionCurrent(context)) return
  if (saved && admin.settings) {
    draft.value = admin.settings.registration_enabled
    message.success(t('configurationSettings.saveSuccess'))
  }
}

onMounted(() => {
  void reload()
})
</script>

<template>
  <div class="lf-page lf-content-narrow">
    <PageHeader
      :title="t('admin.settings.title')"
      :subtitle="t('configurationSettings.description')"
    >
      <template #actions>
        <NButton secondary :loading="admin.settingsLoading" :disabled="busy" @click="refresh">
          {{ t('admin.settings.actions.refresh') }}
        </NButton>
        <NButton
          type="primary"
          :loading="admin.settingsSaving"
          :disabled="busy || !hasChanges"
          @click="save"
        >
          {{ t('admin.settings.actions.save') }}
        </NButton>
      </template>
    </PageHeader>

    <NAlert v-if="admin.settingsError || admin.settingsSaveError" type="error" role="alert">
      {{ admin.settingsSaveError || admin.settingsError }}
    </NAlert>
    <div class="lf-panel p-5 sm:p-6" :aria-busy="admin.settingsLoading">
      <NSkeleton v-if="admin.settingsLoading && draft === null" text :repeat="3" />
      <template v-else-if="admin.settings && draft !== null">
        <div class="flex items-start justify-between gap-5">
          <div class="min-w-0">
            <h2 id="registration-policy-label" class="text-base font-semibold text-lf-text-strong">
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
        <p v-if="hasChanges" class="mt-4 text-xs text-lf-text-subtle">
          {{ t('configurationSettings.unsaved') }}
        </p>
      </template>
      <p v-else class="text-sm text-lf-text-muted">{{ t('configurationSettings.unconfirmed') }}</p>
    </div>

    <NModal
      v-model:show="discardVisible"
      preset="dialog"
      type="warning"
      :title="t('configurationSettings.discardTitle')"
      :content="t('configurationSettings.discardContent')"
      :positive-text="t('configurationSettings.discardConfirm')"
      :negative-text="t('common.cancel')"
      @positive-click="confirmRefresh"
    />
  </div>
</template>
