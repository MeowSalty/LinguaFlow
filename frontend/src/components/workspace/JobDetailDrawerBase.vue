<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { NDrawer, NDrawerContent, NEmpty, NSpin } from 'naive-ui'

import type { ApiSchemas } from '@/api/client'
import { useGlobalJobTrackerStore } from '@/stores/globalJobTracker'
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

const tracker = useGlobalJobTrackerStore()

const jobId = computed(() => props.job?.id ?? null)

const events = computed(() => tracker.getJobEvents())

const connected = computed(() => tracker.isJobSSEConnected())

const syntheticEvents = ref<SyntheticEvent[]>([])

const refreshSyntheticEvents = (): void => {
  if (props.job) {
    syntheticEvents.value = buildSyntheticEvents(props.job)
  }
}

const clearEventsAndCache = (): void => {
  tracker.clearJobEvents()
}

watch(
  () => props.show,
  (visible) => {
    if (visible && jobId.value != null) {
      refreshSyntheticEvents()
    } else {
      syntheticEvents.value = []
    }
  },
  { immediate: true },
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
      :title="job && titlePrefix ? `${titlePrefix} #${job.id}` : job ? `#${job.id}` : ''"
      closable
      :header-style="{ borderBottom: '1px solid var(--lf-border-soft)' }"
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
          @clear-events="clearEventsAndCache"
        />
        <NEmpty v-else :description="emptyDescription" />
      </NSpin>
      <template #footer>
        <slot name="footer" />
      </template>
    </NDrawerContent>
  </NDrawer>
</template>
