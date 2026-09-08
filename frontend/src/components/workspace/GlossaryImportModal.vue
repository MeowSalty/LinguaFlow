<script setup lang="ts">
import { NAlert, NButton, NIcon, NModal, NUpload, type UploadFileInfo } from 'naive-ui'
import { useI18n } from 'vue-i18n'

import { useGlossaryStore } from '@/stores/glossary'
import { DRAWER_WIDTH } from '@/components/common/uiConstants'

const { t } = useI18n()
const glossary = useGlossaryStore()

const show = defineModel<boolean>('show', { default: false })

const emit = defineEmits<{
  import: [file: File]
}>()

const handleChange = (options: { file: UploadFileInfo }): void => {
  if (options.file.file) {
    emit('import', options.file.file)
  }
}

const resultText = computed(() => {
  const result = glossary.importResult
  if (!result) return ''
  const parts = [t('workspace.glossary.import.result', { added: result.added })]
  if (result.skipped?.length) {
    parts.push(
      t('workspace.glossary.import.skipped', {
        count: result.skipped.length,
        reasons: result.skipped.join('、'),
      }),
    )
  }
  return parts.join('；')
})
</script>

<template>
  <NModal
    v-model:show="show"
    preset="card"
    :title="t('workspace.glossary.import.title')"
    :style="{ width: DRAWER_WIDTH.s }"
    :bordered="false"
    :mask-closable="false"
  >
    <div class="space-y-4">
      <p class="text-sm text-lf-text-muted">
        {{ t('workspace.glossary.import.description') }}
      </p>
      <NUpload :max="1" accept=".csv" :default-upload="false" @change="handleChange">
        <NButton :loading="glossary.importing">
          <template #icon>
            <NIcon><IconCarbonUpload /></NIcon>
          </template>
          {{ t('workspace.glossary.actions.import') }}
        </NButton>
      </NUpload>
      <NAlert v-if="glossary.importResult" type="success" :bordered="false">
        {{ resultText }}
      </NAlert>
    </div>
    <template #footer>
      <div class="flex justify-end">
        <NButton @click="show = false">
          {{ t('common.close') }}
        </NButton>
      </div>
    </template>
  </NModal>
</template>
