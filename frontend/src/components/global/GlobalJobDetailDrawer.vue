<script setup lang="ts">
import { computed } from 'vue'
import { NButton, NDrawer, NDrawerContent, NEmpty, NSpin } from 'naive-ui'
import { useI18n } from 'vue-i18n'

import { useGlobalJobTrackerStore } from '@/stores/globalJobTracker'

import JobDetailContent from '@/components/workspace/JobDetailContent.vue'

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

const handleClose = (): void => {
  tracker.closeDetail()
}

const handleGoToProject = (): void => {
  if (job.value) {
    void router.push({ path: `/projects/${job.value.project_id}`, query: { tab: 'jobs' } })
    tracker.closeDetail()
  }
}

const handleRefreshEvents = (): void => {
  void tracker.refreshDetail()
}
</script>

<template>
  <NDrawer
    :show="show"
    :width="720"
    placement="right"
    @update:show="(value: boolean) => (show = value)"
  >
    <NDrawerContent
      :title="
        job
          ? t('globalJobTracker.detailTitle', { id: job.id })
          : t('globalJobTracker.detailFallbackTitle')
      "
      closable
    >
      <NSpin :show="tracker.loadingDetail && !job">
        <JobDetailContent
          v-if="job"
          :job="job"
          :project-name="projectName || `#${job.project_id}`"
          :events="tracker.detailEvents"
          :loading-events="tracker.loadingEvents"
          @refresh-events="handleRefreshEvents"
        />
        <NEmpty v-else :description="t('globalJobTracker.noTrackedJobs')" />
      </NSpin>
      <template #footer>
        <div class="flex flex-wrap justify-end gap-3">
          <NButton @click="handleClose">{{ t('globalJobTracker.close') }}</NButton>
          <NButton v-if="job" type="primary" @click="handleGoToProject">
            {{ t('globalJobTracker.goToProject') }}
          </NButton>
        </div>
      </template>
    </NDrawerContent>
  </NDrawer>
</template>
