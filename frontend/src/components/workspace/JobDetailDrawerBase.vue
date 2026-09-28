<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { NDrawer, NDrawerContent, NEmpty, NSpin, NTag } from 'naive-ui'

import type { ApiSchemas } from '@/api/client'
import { useGlobalJobTrackerStore } from '@/stores/globalJobTracker'
import { useLanguageOptions } from '@/composables/useLanguageOptions'
import { getJobStatusLabel, statusTagType } from '@/composables/useWorkspaceUtils'

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

const tracker = useGlobalJobTrackerStore()
const { sourceLanguageOptions } = useLanguageOptions()
const detailSession = ref(0)
watch(
  () => props.show,
  (show) => {
    if (show) detailSession.value++
  },
)

const events = computed(() => tracker.getJobEvents())

const connected = computed(() => tracker.isJobSSEConnected())

const hasOlder = computed(() => tracker.hasOlder)

const loadingOlder = computed(() => tracker.loadingOlder)

const headerTitle = computed(() =>
  props.job
    ? props.titlePrefix
      ? `${props.titlePrefix} #${props.job.id}`
      : `#${props.job.id}`
    : '',
)

const languageLabel = (value: unknown): string => {
  if (typeof value !== 'string' || !value) return '—'
  const label = String(
    sourceLanguageOptions.value.find((option) => option.value === value)?.label ?? value,
  )
  // Select options include a code suffix; the detail header only needs the localized name.
  const suffix = ` · ${value}`
  return label.endsWith(suffix) ? label.slice(0, -suffix.length) : label
}
const languagePair = computed(
  () =>
    `${languageLabel(props.job?.execution_config?.source_lang)} → ${languageLabel(props.job?.execution_config?.target_lang)}`,
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
    <NDrawerContent
      closable
      :native-scrollbar="true"
      :body-style="{ minHeight: '0' }"
      :body-content-style="{ padding: '0', overflow: 'hidden', height: '100%' }"
    >
      <template #header>
        <div class="min-w-0 space-y-2 pr-6">
          <div class="flex flex-wrap items-center gap-2.5">
            <span class="text-lg font-semibold leading-6">{{ headerTitle }}</span>
            <NTag
              v-if="job"
              size="small"
              round
              :bordered="false"
              :type="
                job.status === 'cancelled' || job.status === 'paused'
                  ? 'default'
                  : statusTagType(job.status)
              "
            >
              {{ getJobStatusLabel(job.status) }}
            </NTag>
          </div>
          <div
            v-if="job"
            class="flex flex-wrap items-center gap-x-2 gap-y-1 text-xs font-normal leading-5 text-lf-text-muted"
          >
            <span class="max-w-full truncate" :title="projectName || `#${job.project_id}`">{{
              projectName || `#${job.project_id}`
            }}</span>
            <span aria-hidden="true" class="text-lf-text-subtle">·</span>
            <span>{{ languagePair }}</span>
          </div>
        </div>
      </template>
      <JobDetailContent
        v-if="job"
        :key="`${job.id}-${detailSession}`"
        :job="job"
        :external-error="error"
        :events="events"
        :sse-connected="connected"
        :has-older="hasOlder"
        :loading-older="loadingOlder"
        @clear-events="clearEventsAndCache"
        @load-older="() => tracker.loadOlder()"
      />
      <NSpin v-else :show="loading" class="p-6">
        <NEmpty :description="emptyDescription" />
      </NSpin>
      <template #footer>
        <slot name="footer" />
      </template>
    </NDrawerContent>
  </NDrawer>
</template>
