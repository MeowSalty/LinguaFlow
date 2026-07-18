<script setup lang="ts">
import { NButton, NEmpty, NInput, NSkeleton, useMessage } from 'naive-ui'
import { useI18n } from 'vue-i18n'

import { useAdminStore } from '@/stores/admin'

const admin = useAdminStore()
const message = useMessage()
const { t } = useI18n()

interface SettingEntry {
  key: string
  value: string
  originalKey: string
}

const editingSettings = ref<SettingEntry[]>([])
const newKey = ref('')
const newValue = ref('')

const buildEditingSettings = (): void => {
  editingSettings.value = Object.entries(admin.settings).map(([key, value]) => ({
    key,
    value,
    originalKey: key,
  }))
}

watch(
  () => admin.settings,
  () => {
    buildEditingSettings()
  },
  { immediate: true },
)

const addSetting = (): void => {
  if (!newKey.value.trim()) return

  const exists = editingSettings.value.some((s) => s.key === newKey.value.trim())
  if (exists) return

  editingSettings.value.push({
    key: newKey.value.trim(),
    value: newValue.value,
    originalKey: newKey.value.trim(),
  })

  newKey.value = ''
  newValue.value = ''
}

const removeSetting = (index: number): void => {
  editingSettings.value.splice(index, 1)
}

const saveSettings = async (): Promise<void> => {
  const settings: Record<string, string> = {}
  for (const entry of editingSettings.value) {
    settings[entry.key] = entry.value
  }

  try {
    await admin.saveSettings(settings)
    message.success(t('admin.settings.messages.saveSuccess'))
  } catch {
    // Error is handled by the store
  }
}

const hasChanges = computed(() => {
  const currentKeys = Object.keys(admin.settings)
  const editKeys = editingSettings.value.map((s) => s.key)

  if (currentKeys.length !== editKeys.length) return true

  for (const entry of editingSettings.value) {
    if (admin.settings[entry.key] !== entry.value) return true
    if (entry.key !== entry.originalKey) return true
  }

  return false
})

onMounted(() => {
  admin.loadSettings()
})

watch(
  () => admin.settingsError,
  (err) => {
    if (err) {
      message.error(err, { duration: 0, closable: true })
      admin.settingsError = null
    }
  },
)
</script>

<template>
  <div class="lf-page">
    <section class="lf-page-header">
      <div class="flex flex-col gap-6 lg:flex-row lg:items-center lg:justify-between">
        <div class="space-y-3">
          <div class="lf-eyebrow">
            {{ t('admin.eyebrow') }}
          </div>
          <div>
            <h1 class="text-3xl font-semibold tracking-tight text-lf-text-strong">
              {{ t('admin.settings.title') }}
            </h1>
            <p class="mt-2 max-w-2xl text-sm leading-6 text-lf-text-muted">
              {{ t('admin.settings.description') }}
            </p>
          </div>
        </div>
        <div class="flex flex-wrap gap-3">
          <NButton secondary :loading="admin.settingsLoading" @click="admin.loadSettings">
            {{ t('admin.settings.actions.refresh') }}
          </NButton>
          <NButton
            type="primary"
            :loading="admin.settingsSaving"
            :disabled="!hasChanges"
            @click="saveSettings"
          >
            {{ t('admin.settings.actions.save') }}
          </NButton>
        </div>
      </div>
    </section>

    <div class="lf-panel p-5">
      <div class="mb-4 flex items-center justify-between gap-3">
        <h2 class="text-sm font-semibold tracking-wide text-lf-text-strong">
          {{ t('admin.settings.title') }}
        </h2>
        <span class="text-xs text-lf-text-subtle"> {{ editingSettings.length }} keys </span>
      </div>

      <div v-if="admin.settingsLoading" class="space-y-3">
        <NSkeleton v-for="i in 3" :key="i" text :repeat="1" class="h-16" />
      </div>

      <NEmpty
        v-else-if="editingSettings.length === 0 && !admin.settingsLoading"
        class="py-12"
        :description="t('admin.settings.empty')"
      />

      <div v-else class="space-y-3">
        <div
          v-for="(entry, index) in editingSettings"
          :key="index"
          class="flex items-start gap-3 rounded-xl border border-lf-border-soft bg-lf-surface-muted p-3.5 sm:gap-4 sm:p-4"
        >
          <div class="grid flex-1 grid-cols-1 gap-3 sm:grid-cols-2">
            <div>
              <label class="mb-1.5 block text-xs font-medium text-lf-text-muted">
                {{ t('admin.settings.form.key') }}
              </label>
              <NInput
                v-model:value="entry.key"
                class="font-mono"
                :placeholder="t('admin.settings.form.keyPlaceholder')"
              />
            </div>
            <div>
              <label class="mb-1.5 block text-xs font-medium text-lf-text-muted">
                {{ t('admin.settings.form.value') }}
              </label>
              <NInput
                v-model:value="entry.value"
                :placeholder="t('admin.settings.form.valuePlaceholder')"
              />
            </div>
          </div>
          <NButton quaternary type="error" class="mt-6" @click="removeSetting(index)">
            <template #icon>
              <IconCarbonClose />
            </template>
          </NButton>
        </div>
      </div>
    </div>

    <div class="lf-panel p-5">
      <h3 class="mb-4 text-sm font-semibold tracking-wide text-lf-text-strong">
        {{ t('admin.settings.actions.addSetting') }}
      </h3>
      <div class="flex flex-col items-stretch gap-3 sm:flex-row sm:items-end">
        <div class="min-w-0 flex-1">
          <label class="mb-1.5 block text-xs font-medium text-lf-text-muted">
            {{ t('admin.settings.form.key') }}
          </label>
          <NInput
            v-model:value="newKey"
            class="font-mono"
            :placeholder="t('admin.settings.form.keyPlaceholder')"
          />
        </div>
        <div class="min-w-0 flex-1">
          <label class="mb-1.5 block text-xs font-medium text-lf-text-muted">
            {{ t('admin.settings.form.value') }}
          </label>
          <NInput
            v-model:value="newValue"
            :placeholder="t('admin.settings.form.valuePlaceholder')"
          />
        </div>
        <NButton type="primary" @click="addSetting">
          {{ t('admin.settings.actions.addSetting') }}
        </NButton>
      </div>
    </div>
  </div>
</template>
