<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { NDrawer, NDrawerContent, NEmpty, NSpin } from 'naive-ui'

import type { ApiSchemas } from '@/api/client'
import { useJobSSE } from '@/composables/useJobSSE'
import { buildSyntheticEvents, type SyntheticEvent } from '@/composables/useSyntheticEvents'

import JobDetailContent from './JobDetailContent.vue'

type TranslationJob = ApiSchemas['TranslationJob']

const props = defineProps<{
  show: boolean
  job: TranslationJob | null
  loading: boolean
  error?: string | null
  projectName?: string
  titlePrefix?: string
  emptyDescription?: string
}>()

const emit = defineEmits<{
  'update:show': [value: boolean]
}>()

const jobId = computed(() => props.job?.id ?? null)

const { events, connected, connect, disconnect, clearEvents } = useJobSSE(jobId)

const syntheticEvents = ref<SyntheticEvent[]>([])

const refreshSyntheticEvents = (): void => {
  if (props.job) {
    syntheticEvents.value = buildSyntheticEvents(props.job)
  }
}

watch(
  () => props.show,
  (visible) => {
    if (visible && jobId.value != null) {
      connect()
      refreshSyntheticEvents()
    } else {
      disconnect()
      clearEvents()
      syntheticEvents.value = []
    }
  },
)

watch(
  () => props.loading,
  (loading, wasLoading) => {
    if (wasLoading && !loading && props.show) {
      refreshSyntheticEvents()
    }
  },
)

watch(jobId, (newId, oldId) => {
  if (props.show && newId != null && newId !== oldId) {
    clearEvents()
    connect()
    refreshSyntheticEvents()
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
        job && titlePrefix
          ? `${titlePrefix} #${job.id}`
          : job
            ? `#${job.id}`
            : ''
      "
      closable
    >
      <NSpin :show="loading && !job">
        <JobDetailContent
          v-if="job"
          :job="job"
          :external-error="error"
          :project-name="projectName"
          :events="events"
          :synthetic-events="syntheticEvents"
          :sse-connected="connected"
          @clear-events="clearEvents"
        />
        <NEmpty v-else :description="emptyDescription" />
      </NSpin>
      <template #footer>
        <slot name="footer" />
      </template>
    </NDrawerContent>
  </NDrawer>
</template>
