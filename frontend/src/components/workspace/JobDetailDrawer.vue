<script setup lang="ts">
import { NButton } from 'naive-ui'
import { useI18n } from 'vue-i18n'

import { useProjectWorkspaceStore } from '@/stores/projectWorkspace'

import JobDetailDrawerBase from './JobDetailDrawerBase.vue'

const { t } = useI18n()
const workspace = useProjectWorkspaceStore()

defineProps<{
  show: boolean
}>()

const emit = defineEmits<{
  'update:show': [value: boolean]
}>()
</script>

<template>
  <JobDetailDrawerBase
    :show="show"
    :job="workspace.selectedJob"
    :loading="workspace.loadingJobDetail"
    :error="workspace.jobDetailError"
    :title-prefix="t('workspace.job.detailFallbackTitle')"
    :empty-description="t('workspace.job.detailEmpty')"
    @update:show="(value: boolean) => emit('update:show', value)"
  >
    <template #footer>
      <div class="flex flex-wrap justify-end gap-3">
        <NButton @click="emit('update:show', false)">{{ t('workspace.common.close') }}</NButton>
      </div>
    </template>
  </JobDetailDrawerBase>
</template>
