<script setup lang="ts">
import { watch } from 'vue'
import { NButton, NDrawer, NDrawerContent, NEmpty, NSpin } from 'naive-ui'
import { useI18n } from 'vue-i18n'

import { useProjectWorkspaceStore } from '@/stores/projectWorkspace'
import { useTranslationJobStore } from '@/stores/translationJob'

import JobDetailContent from './JobDetailContent.vue'

const { t } = useI18n()
const workspace = useProjectWorkspaceStore()
const jobStore = useTranslationJobStore()

defineProps<{
  show: boolean
}>()

const emit = defineEmits<{
  'update:show': [value: boolean]
}>()

const handleRefreshEvents = (): void => {
  if (workspace.selectedJob) {
    void jobStore.loadEvents(workspace.selectedJob.id)
  }
}

watch(
  () => jobStore.selectedJob?.id,
  (newId) => {
    const job = jobStore.selectedJob
    if (newId != null && job && ['pending', 'running'].includes(job.status)) {
      void jobStore.loadEvents(newId)
    }
  },
  { immediate: true },
)
</script>

<template>
  <NDrawer
    :show="show"
    :width="720"
    placement="right"
    @update:show="(value: boolean) => emit('update:show', value)"
  >
    <NDrawerContent
      :title="
        workspace.selectedJob
          ? t('workspace.job.detailTitle', { id: workspace.selectedJob.id })
          : t('workspace.job.detailFallbackTitle')
      "
      closable
    >
      <NSpin :show="workspace.loadingJobDetail">
        <JobDetailContent
          v-if="workspace.selectedJob"
          :job="workspace.selectedJob"
          :external-error="workspace.jobDetailError"
          :events="jobStore.events"
          :loading-events="jobStore.loadingEvents"
          @refresh-events="handleRefreshEvents"
        />
        <NEmpty v-else :description="t('workspace.job.detailEmpty')" />
      </NSpin>
      <template #footer>
        <div class="flex flex-wrap justify-end gap-3">
          <NButton @click="emit('update:show', false)">{{ t('workspace.common.close') }}</NButton>
        </div>
      </template>
    </NDrawerContent>
  </NDrawer>
</template>
