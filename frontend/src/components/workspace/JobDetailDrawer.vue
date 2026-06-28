<script setup lang="ts">
import { computed, watch } from 'vue'
import { NButton, NDrawer, NDrawerContent, NEmpty, NSpin } from 'naive-ui'
import { useI18n } from 'vue-i18n'

import { useJobSSE } from '@/composables/useJobSSE'
import { useProjectWorkspaceStore } from '@/stores/projectWorkspace'

import JobDetailContent from './JobDetailContent.vue'

const { t } = useI18n()
const workspace = useProjectWorkspaceStore()

const props = defineProps<{
  show: boolean
}>()

const emit = defineEmits<{
  'update:show': [value: boolean]
}>()

const selectedJobId = computed(() => workspace.selectedJob?.id ?? null)

const { events, connected, connect, disconnect, clearEvents } = useJobSSE(selectedJobId)

watch(
  () => props.show,
  (visible) => {
    if (visible && selectedJobId.value != null) {
      connect()
    } else {
      disconnect()
      clearEvents()
    }
  },
)

watch(selectedJobId, (newId) => {
  if (props.show && newId != null) {
    clearEvents()
    connect()
  }
})
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
          :events="events"
          :sse-connected="connected"
          @clear-events="clearEvents"
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
