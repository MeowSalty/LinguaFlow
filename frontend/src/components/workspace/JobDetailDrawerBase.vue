<script setup lang="ts">
import { computed } from 'vue'
import { NDrawer, NDrawerContent, NEmpty, NSpin } from 'naive-ui'

import type { ApiSchemas } from '@/api/client'
import { useGlobalJobTrackerStore } from '@/stores/globalJobTracker'

import JobDetailContent from './JobDetailContent.vue'

type Job = ApiSchemas['Job']

defineProps<{
  show: boolean
  job: Job | null
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

const events = computed(() => tracker.getJobEvents())

const connected = computed(() => tracker.isJobSSEConnected())

const hasOlder = computed(() => tracker.hasOlder)

const loadingOlder = computed(() => tracker.loadingOlder)

const jobEnded = computed(() => tracker.jobEnded)

const clearEventsAndCache = (): void => {
  tracker.clearJobEvents()
}
</script>

<template>
  <NDrawer
    :show="show"
    :width="'min(720px, 100vw)'"
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
          :sse-connected="connected"
          :has-older="hasOlder"
          :loading-older="loadingOlder"
          :job-ended="jobEnded"
          @clear-events="clearEventsAndCache"
          @load-older="() => tracker.loadOlder()"
        />
        <NEmpty v-else :description="emptyDescription" />
      </NSpin>
      <template #footer>
        <slot name="footer" />
      </template>
    </NDrawerContent>
  </NDrawer>
</template>
