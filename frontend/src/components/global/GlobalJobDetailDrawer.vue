<script setup lang="ts">
import { computed, ref } from 'vue'
import { NButton, useMessage } from 'naive-ui'
import { useI18n } from 'vue-i18n'

import { pauseJob, resumeJob } from '@/api/client'
import { useGlobalJobTrackerStore } from '@/stores/globalJobTracker'

import JobDetailDrawerBase from '@/components/workspace/JobDetailDrawerBase.vue'

const { t } = useI18n()
const route = useRoute()
const router = useRouter()
const tracker = useGlobalJobTrackerStore()
const message = useMessage()

const pausing = ref(false)
const resuming = ref(false)

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

const isInProjectPage = computed(() => {
  if (!job.value) return false
  return route.path === `/projects/${job.value.project_id}`
})

const handleGoToProject = (): void => {
  if (job.value) {
    void router.push({ path: `/projects/${job.value.project_id}`, query: { tab: 'jobs' } })
    tracker.closeDetail()
  }
}

const handlePause = async (): Promise<void> => {
  const currentJob = job.value
  if (!currentJob || pausing.value) return
  pausing.value = true
  try {
    await pauseJob(currentJob.id)
    message.success(t('workspace.messages.jobPaused'))
    await tracker.refreshDetail()
  } catch (error) {
    console.error(error)
    message.error(error instanceof Error ? error.message : t('workspace.messages.jobPauseFailed'))
  } finally {
    pausing.value = false
  }
}

const handleResume = async (): Promise<void> => {
  const currentJob = job.value
  if (!currentJob || resuming.value) return
  resuming.value = true
  try {
    await resumeJob(currentJob.id)
    message.success(t('workspace.messages.jobResumed'))
    await tracker.refreshDetail()
  } catch (error) {
    console.error(error)
    message.error(error instanceof Error ? error.message : t('workspace.messages.jobResumeFailed'))
  } finally {
    resuming.value = false
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
        <NButton
          v-if="job && (job.status === 'pending' || job.status === 'running')"
          :loading="pausing"
          @click="handlePause"
        >
          {{ t('workspace.job.actions.pause') }}
        </NButton>
        <NButton v-if="job && job.status === 'paused'" :loading="resuming" @click="handleResume">
          {{ t('workspace.job.actions.resume') }}
        </NButton>
        <NButton @click="tracker.closeDetail()">{{ t('globalJobTracker.close') }}</NButton>
        <NButton v-if="job && !isInProjectPage" type="primary" @click="handleGoToProject">
          {{ t('globalJobTracker.goToProject') }}
        </NButton>
      </div>
    </template>
  </JobDetailDrawerBase>
</template>
