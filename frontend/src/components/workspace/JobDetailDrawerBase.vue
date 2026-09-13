<script setup lang="ts">
import { computed } from 'vue'
import { NDrawer, NDrawerContent, NEmpty, NSpin } from 'naive-ui'
import { useI18n } from 'vue-i18n'

import type { ApiSchemas } from '@/api/client'
import { useGlobalJobTrackerStore } from '@/stores/globalJobTracker'

import JobDetailContent from './JobDetailContent.vue'
import { DRAWER_WIDTH } from '@/components/common/uiConstants'

type Job = ApiSchemas['Job']

const props = defineProps<{
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

const { t } = useI18n()
const tracker = useGlobalJobTrackerStore()

const events = computed(() => tracker.getJobEvents())

const connected = computed(() => tracker.isJobSSEConnected())

const hasOlder = computed(() => tracker.hasOlder)

const loadingOlder = computed(() => tracker.loadingOlder)

const jobEnded = computed(() => tracker.jobEnded)

const headerTitle = computed(() =>
  props.job
    ? props.titlePrefix
      ? `${props.titlePrefix} #${props.job.id}`
      : `#${props.job.id}`
    : '',
)

const headerSubtitle = computed(() =>
  props.job ? t(`workspace.job.statusSubtitle.${props.job.status}`) : undefined,
)

const clearEventsAndCache = (): void => {
  tracker.clearJobEvents()
}
</script>

<template>
  <NDrawer
    :show="show"
    :width="DRAWER_WIDTH.l"
    placement="right"
    @update:show="(value: boolean) => emit('update:show', value)"
  >
    <NDrawerContent closable>
      <template #header>
        <DrawerHeader :title="headerTitle" :subtitle="headerSubtitle" />
      </template>
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
