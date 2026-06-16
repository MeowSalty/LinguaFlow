<script setup lang="ts">
import { NAlert, NButton, NIcon, NModal, NUpload, type UploadFileInfo } from 'naive-ui'
import { useI18n } from 'vue-i18n'

import { useGlossaryStore } from '@/stores/glossary'

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
</script>

<template>
  <NModal
    v-model:show="show"
    preset="card"
    :title="t('workspace.glossary.import.title')"
    :style="{ width: '480px' }"
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
        {{ t('workspace.glossary.import.result', { added: glossary.importResult.added }) }}
        <template v-if="glossary.importResult.skipped?.length">
          ，{{
            t('workspace.glossary.import.skipped', {
              count: glossary.importResult.skipped.length,
            })
          }}
        </template>
      </NAlert>
    </div>
    <template #footer>
      <div class="flex justify-end">
        <NButton @click="show = false">
          {{ t('workspace.common.close') }}
        </NButton>
      </div>
    </template>
  </NModal>
</template>
