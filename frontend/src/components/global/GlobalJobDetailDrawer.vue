<script setup lang="ts">
import { computed } from 'vue'
import { NButton } from 'naive-ui'
import { useI18n } from 'vue-i18n'

import { useGlobalJobTrackerStore } from '@/stores/globalJobTracker'

import JobDetailDrawerBase from '@/components/workspace/JobDetailDrawerBase.vue'

const { t } = useI18n()
const router = useRouter()
const tracker = useGlobalJobTrackerStore()

const show = computed({
  get: () => tracker.drawerJobId != null,
  set: (value: boolean) => {
    if (!value) tracker.closeDetail()
  },
})

const job = computed(() => tracker.detailJob)

const projectName = computed(() => {
  if (!job.value) return undefined
  return tracker.trackedJobs.find((j) => j.id === job.value!.id)?.project_name
})

const handleGoToProject = (): void => {
  if (job.value) {
    void router.push({ path: `/projects/${job.value.project_id}`, query: { tab: 'jobs' } })
    tracker.closeDetail()
  }
}
</script>

<template>
  <JobDetailDrawerBase
    :show="show"
    :job="job"
    :loading="tracker.loadingDetail"
    :project-name="projectName || (job ? `#${job.project_id}` : undefined)"
    :title-prefix="t('globalJobTracker.detailFallbackTitle')"
    :empty-description="t('globalJobTracker.noTrackedJobs')"
    @update:show="(value: boolean) => (show = value)"
  >
    <template #footer>
      <div class="flex flex-wrap justify-end gap-3">
        <NButton @click="tracker.closeDetail()">{{ t('globalJobTracker.close') }}</NButton>
        <NButton v-if="job" type="primary" @click="handleGoToProject">
          {{ t('globalJobTracker.goToProject') }}
        </NButton>
      </div>
    </template>
  </JobDetailDrawerBase>
</template>
