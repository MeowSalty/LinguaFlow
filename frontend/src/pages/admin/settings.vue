<script setup lang="ts">
import { NAlert, NButton, NEmpty, NInput, NSkeleton, useMessage } from 'naive-ui'
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
</script>

<template>
  <div class="space-y-6">
    <NCard :bordered="false" class="overflow-hidden shadow-sm shadow-lf-shadow">
      <div class="flex flex-col gap-6 lg:flex-row lg:items-center lg:justify-between">
        <div class="space-y-3">
          <div
            class="inline-flex items-center rounded-full bg-lf-brand-soft px-3 py-1 text-xs font-medium text-brand-600"
          >
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
    </NCard>

    <NAlert v-if="admin.settingsError" type="error" :bordered="false">
      {{ admin.settingsError }}
    </NAlert>

    <NCard :bordered="false" class="shadow-sm shadow-lf-shadow">
      <div v-if="admin.settingsLoading" class="space-y-4">
        <NSkeleton v-for="i in 3" :key="i" text :repeat="1" class="h-16" />
      </div>

      <NEmpty
        v-else-if="editingSettings.length === 0 && !admin.settingsLoading"
        class="py-16"
        :description="t('admin.settings.empty')"
      />

      <div v-else class="space-y-4">
        <div
          v-for="(entry, index) in editingSettings"
          :key="index"
          class="flex items-start gap-4 rounded-xl border border-lf-border-soft bg-lf-surface-muted p-4"
        >
          <div class="flex-1 grid grid-cols-1 gap-3 sm:grid-cols-2">
            <div>
              <label class="mb-1 block text-xs text-lf-text-muted">
                {{ t('admin.settings.form.key') }}
              </label>
              <NInput
                v-model:value="entry.key"
                :placeholder="t('admin.settings.form.keyPlaceholder')"
              />
            </div>
            <div>
              <label class="mb-1 block text-xs text-lf-text-muted">
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
    </NCard>

    <NCard :bordered="false" class="shadow-sm shadow-lf-shadow">
      <h3 class="mb-4 text-lg font-semibold text-lf-text-strong">
        {{ t('admin.settings.actions.addSetting') }}
      </h3>
      <div class="flex items-end gap-4">
        <div class="flex-1">
          <label class="mb-1 block text-xs text-lf-text-muted">
            {{ t('admin.settings.form.key') }}
          </label>
          <NInput v-model:value="newKey" :placeholder="t('admin.settings.form.keyPlaceholder')" />
        </div>
        <div class="flex-1">
          <label class="mb-1 block text-xs text-lf-text-muted">
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
    </NCard>
  </div>
</template>
