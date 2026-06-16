<script setup lang="ts">
import { NAlert, NButton, NCard, NIcon, NTabPane, NTabs } from 'naive-ui'
import { ref, computed, watch, onMounted, onBeforeUnmount } from 'vue'
import { useI18n } from 'vue-i18n'

import { type ApiSchemas } from '@/api/client'
import ResourceExplorer from '@/components/workspace/ResourceExplorer.vue'
import WorkspaceMetricsBar from '@/components/workspace/WorkspaceMetricsBar.vue'
import GlossaryPanel from '@/components/workspace/GlossaryPanel.vue'
import GlossaryDrawer from '@/components/workspace/GlossaryDrawer.vue'
import GlossaryImportModal from '@/components/workspace/GlossaryImportModal.vue'
import SegmentPanel from '@/components/workspace/SegmentPanel.vue'
import JobPanel from '@/components/workspace/JobPanel.vue'
import JobCreateDrawer from '@/components/workspace/JobCreateDrawer.vue'
import JobDetailDrawer from '@/components/workspace/JobDetailDrawer.vue'
import ConflictDialog from '@/components/workspace/ConflictDialog.vue'
import IncrementalResultModal from '@/components/workspace/IncrementalResultModal.vue'
import { useGlossaryManagement } from '@/composables/useGlossaryManagement'
import { useJobActions } from '@/composables/useJobActions'
import { useConflictHandling } from '@/composables/useConflictHandling'
import { formatDate } from '@/composables/useWorkspaceUtils'
import { useExecutionPlanTemplatesStore } from '@/stores/executionPlanTemplates'
import { useGlossaryStore } from '@/stores/glossary'
import { useProjectWorkspaceStore } from '@/stores/projectWorkspace'

type Resource = ApiSchemas['Resource']

type WorkspaceTab = 'resources' | 'segments' | 'jobs' | 'glossary'

const route = useRoute()
const router = useRouter()
const { t } = useI18n()
const workspace = useProjectWorkspaceStore()
const glossary = useGlossaryStore()
const executionPlanTemplatesStore = useExecutionPlanTemplatesStore()

const activeTab = ref<WorkspaceTab>('resources')

// ── projectId ──
const projectId = computed(() => {
  const params = route.params as Partial<Record<'projectId', string | string[]>>
  const rawValue = Array.isArray(params.projectId) ? params.projectId[0] : params.projectId
  const parsed = Number(rawValue)
  return Number.isFinite(parsed) ? parsed : null
})

// ── Composables ──
const glossaryMgmt = useGlossaryManagement(projectId)

const switchToJobsTab = async (): Promise<void> => {
  activeTab.value = 'jobs'
}

const jobMgmt = useJobActions(projectId, switchToJobsTab)

// ── 翻译内容段落数量（用于 JobCreateDrawer 摘要）──
const drawerSegmentCount = computed(() => {
  if (jobMgmt.jobTargetMode.value === 'segments') {
    return jobMgmt.jobTargetSegmentIds.value.length
  }
  // 资源模式：使用已选就绪资源的总段落数
  return workspace.selectedResources
    .filter((r) => r.status === 'ready')
    .reduce((sum, r) => sum + (r.total_segments ?? 0), 0)
})

const reloadSegments = async (): Promise<void> => {
  if (!projectId.value || !workspace.activeResourceId) {
    return
  }
  await workspace.loadSegments(projectId.value, workspace.activeResourceId)
}

const conflictMgmt = useConflictHandling()

// ── 工作区操作 ──
const reloadWorkspace = async (): Promise<void> => {
  if (!projectId.value) {
    return
  }

  await Promise.all([
    workspace.loadProject(projectId.value),
    workspace.loadResourceTree(projectId.value),
    workspace.loadResources(projectId.value),
    workspace.loadJobs(projectId.value),
    glossary.loadEntries(projectId.value),
  ])
  workspace.syncResourcesFromTree()
}

// ── ResourceExplorer 事件处理 ──
const handleExplorerOpenSegments = (resource: Resource): void => {
  workspace.setActiveResource(resource.id)
  activeTab.value = 'segments'
  void reloadSegments()
}

// ── Watchers ──
watch(
  () => route.query.tab,
  (tab) => {
    if (tab === 'segments' || tab === 'jobs' || tab === 'resources' || tab === 'glossary') {
      activeTab.value = tab
    }
  },
  { immediate: true },
)

watch(
  () => [workspace.segmentSearch, workspace.segmentStatusFilter, workspace.activeResourceId],
  () => {
    if (projectId.value && workspace.activeResourceId) {
      void workspace.loadSegments(projectId.value, workspace.activeResourceId)
    }
  },
)

watch(
  () => workspace.jobStatusFilter,
  () => {
    if (projectId.value) {
      void workspace.loadJobs(projectId.value)
    }
  },
)

watch(activeTab, (tab) => {
  if (route.query.tab !== tab) {
    void router.replace({ query: { ...route.query, tab } })
  }
})

// ── 5.1 任务进度轮询 ──
const hasRunningJobs = computed(() =>
  workspace.jobs.some((j) => j.status === 'pending' || j.status === 'running'),
)

const pollingTimer = ref<ReturnType<typeof setInterval> | null>(null)

watch(
  hasRunningJobs,
  (running) => {
    if (running && !pollingTimer.value) {
      pollingTimer.value = setInterval(() => {
        if (projectId.value) {
          void workspace.loadJobs(projectId.value)
        }
      }, 5000)
    } else if (!running && pollingTimer.value) {
      clearInterval(pollingTimer.value)
      pollingTimer.value = null
    }
  },
  { immediate: true },
)

onBeforeUnmount(() => {
  if (pollingTimer.value) {
    clearInterval(pollingTimer.value)
    pollingTimer.value = null
  }
  workspace.reset()
  glossary.reset()
})

// ── 5.2 段落状态联动刷新 ──
watch(
  () => workspace.jobs.map((j) => `${j.id}:${j.status}`),
  (newVal, oldVal) => {
    if (!oldVal) return
    // 检测到任务状态从 running/pending 变为其他状态
    for (let i = 0; i < newVal.length; i++) {
      const newStatus = newVal[i]!.split(':')[1]
      const oldStatus = oldVal[i]?.split(':')[1]
      if (
        oldStatus &&
        (oldStatus === 'running' || oldStatus === 'pending') &&
        newStatus !== 'running' &&
        newStatus !== 'pending'
      ) {
        // 任务完成或取消，刷新段落
        if (projectId.value && workspace.activeResourceId) {
          void workspace.loadSegments(projectId.value, workspace.activeResourceId)
        }
        break
      }
    }
  },
)

onMounted(() => {
  workspace.reset()
  glossary.reset()
  void reloadWorkspace()
  void executionPlanTemplatesStore.loadTemplates()
})
</script>

<template>
  <div class="space-y-6">
    <NCard :bordered="false" class="overflow-hidden shadow-sm shadow-lf-shadow">
      <div class="flex flex-wrap items-center justify-between gap-3">
        <div class="flex min-w-0 flex-wrap items-center gap-x-3 gap-y-2">
          <NButton quaternary size="small" @click="router.push('/projects')">
            <template #icon>
              <NIcon><IconCarbonArrowLeft /></NIcon>
            </template>
          </NButton>
          <h1 class="truncate text-lg font-semibold tracking-tight text-lf-text-strong">
            {{ workspace.project?.name || t('workspace.loadingProject') }}
          </h1>
          <span class="inline-block h-4 w-px bg-lf-border-soft" />
          <span class="inline-flex items-center gap-1.5 text-sm text-lf-text-muted">
            <IconCarbonLanguage class="h-3.5 w-3.5 text-lf-text-subtle" />
            {{ workspace.project?.source_lang || '-' }} →
            {{ workspace.project?.target_lang || '-' }}
          </span>
          <span class="hidden h-4 w-px bg-lf-border-soft md:inline-block" />
          <span class="hidden items-center gap-1.5 text-sm text-lf-text-muted md:inline-flex">
            <IconCarbonTime class="h-3.5 w-3.5 text-lf-text-subtle" />
            {{
              t('workspace.updatedAt', {
                time: formatDate(workspace.project?.updated_at ?? workspace.project?.created_at),
              })
            }}
          </span>
        </div>
        <div class="flex shrink-0 flex-wrap gap-3">
          <NButton
            secondary
            :loading="
              workspace.loadingProject || workspace.loadingResourceTree || workspace.loadingJobs
            "
            @click="reloadWorkspace"
          >
            <template #icon>
              <NIcon><IconCarbonRenew /></NIcon>
            </template>
            {{ t('workspace.actions.refresh') }}
          </NButton>
          <NButton
            type="primary"
            :disabled="!jobMgmt.canCreateResourceJob.value"
            @click="jobMgmt.openResourceJobDrawer()"
          >
            <template #icon>
              <NIcon><IconCarbonMagicWand /></NIcon>
            </template>
            {{ t('workspace.job.actions.createFromResources') }}
          </NButton>
        </div>
      </div>
    </NCard>

    <NAlert v-if="workspace.projectError" type="error" :bordered="false">
      {{ workspace.projectError }}
    </NAlert>

    <WorkspaceMetricsBar
      :total-resources="workspace.resources.length"
      :ready-resources="workspace.readyResourceCount"
      :total-segments="workspace.totalSegmentCount"
      :translated-segments="workspace.translatedSegmentCount"
      :running-jobs="workspace.runningJobCount"
    />

    <NCard :bordered="false" class="shadow-sm shadow-lf-shadow">
      <div
        v-if="activeTab === 'resources' && jobMgmt.selectedReadyResourceIds.value.length > 0"
        class="mb-3 inline-flex items-center gap-2 rounded-full bg-lf-surface-muted px-3 py-1.5 text-xs text-lf-text-muted"
      >
        <IconCarbonSelect-01 class="h-3.5 w-3.5" />
        {{
          t('workspace.content.selectedResources', {
            count: jobMgmt.selectedReadyResourceIds.value.length,
          })
        }}
      </div>

      <NTabs v-model:value="activeTab" animated>
        <NTabPane
          name="resources"
          :tab="`${t('workspace.tabs.resources')} (${workspace.resources.length})`"
        >
          <div class="pt-3">
            <ResourceExplorer
              v-if="projectId"
              :project-id="projectId"
              @open-segments="handleExplorerOpenSegments"
              @conflict="conflictMgmt.handleExplorerConflict"
              @incremental-result="conflictMgmt.handleExplorerIncrementalResult"
            />
          </div>
        </NTabPane>

        <NTabPane
          name="segments"
          :tab="`${t('workspace.tabs.segments')} (${workspace.totalSegmentCount})`"
        >
          <SegmentPanel
            :project-id="projectId"
            @translate="(segment) => jobMgmt.openSegmentJobDrawer(segment)"
            @refresh="reloadSegments"
          />
        </NTabPane>

        <NTabPane name="jobs" :tab="`${t('workspace.tabs.jobs')} (${workspace.jobs.length})`">
          <JobPanel
            :project-id="projectId"
            @create="jobMgmt.openResourceJobDrawer()"
            @detail="(job) => jobMgmt.openJobDetail(job)"
          />
        </NTabPane>

        <NTabPane name="glossary" :tab="`${t('workspace.tabs.glossary')} (${glossary.entryCount})`">
          <GlossaryPanel
            :project-id="projectId"
            @create="glossaryMgmt.openCreateGlossaryDrawer()"
            @import="glossaryMgmt.glossaryImportVisible.value = true"
          />
        </NTabPane>
      </NTabs>
    </NCard>

    <!-- 创建任务抽屉 -->
    <JobCreateDrawer
      v-model:show="jobMgmt.jobDrawerVisible.value"
      :form-ref="jobMgmt.jobFormRef.value"
      :target-mode="jobMgmt.jobTargetMode.value"
      :target-resource-ids="jobMgmt.jobTargetResourceIds.value"
      :target-segment-ids="jobMgmt.jobTargetSegmentIds.value"
      :execution-plan-id="jobMgmt.jobForm.execution_plan_id"
      :form-rules="jobMgmt.jobFormRules.value"
      :execution-plan-options="jobMgmt.executionPlanOptions.value"
      :submitting="workspace.creatingJob"
      :segment-count="drawerSegmentCount"
      :selected-plan-template="jobMgmt.selectedPlanTemplate.value"
      @update:execution-plan-id="(val) => (jobMgmt.jobForm.execution_plan_id = val)"
      @submit="jobMgmt.submitJob()"
      @close="jobMgmt.closeJobDrawer()"
    />

    <!-- 任务详情抽屉 -->
    <JobDetailDrawer
      v-model:show="jobMgmt.jobDetailDrawerVisible.value"
      @download="(job) => jobMgmt.downloadJob(job)"
    />

    <!-- 冲突对话框 -->
    <ConflictDialog
      v-model:show="conflictMgmt.conflictDialogVisible.value"
      :resource-name="conflictMgmt.conflictResource.value?.name ?? ''"
      :loading="conflictMgmt.replacingResourceId.value !== null"
      @replace="
        conflictMgmt.handleConflictReplace(projectId!, reloadSegments, (id) =>
          workspace.loadResourceTree(id),
        )
      "
      @incremental="
        conflictMgmt.handleConflictIncremental(projectId!, reloadSegments, (id) =>
          workspace.loadResourceTree(id),
        )
      "
    />

    <!-- 增量结果弹窗 -->
    <IncrementalResultModal
      v-model:show="conflictMgmt.incrementalResultVisible.value"
      :result="conflictMgmt.incrementalResult.value"
      @confirm="conflictMgmt.confirmIncrementalResult()"
    />

    <!-- 术语表新增/编辑抽屉 -->
    <GlossaryDrawer
      v-model:show="glossaryMgmt.glossaryDrawerVisible.value"
      :is-edit-mode="glossaryMgmt.isGlossaryEditMode.value"
      :drawer-title="glossaryMgmt.glossaryDrawerTitle.value"
      :form-ref="glossaryMgmt.glossaryFormRef.value"
      :form="glossaryMgmt.glossaryForm"
      :form-rules="glossaryMgmt.glossaryRules.value"
      :submitting="glossary.creating || glossary.updating"
      :error="glossaryMgmt.isGlossaryEditMode.value ? glossary.updateError : glossary.createError"
      @submit="glossaryMgmt.submitGlossaryEntry()"
      @close="glossaryMgmt.closeGlossaryDrawer()"
      @update:form-source="(val) => (glossaryMgmt.glossaryForm.source = val)"
      @update:form-target="(val) => (glossaryMgmt.glossaryForm.target = val)"
      @update:form-case-sensitive="(val) => (glossaryMgmt.glossaryForm.case_sensitive = val)"
      @update:form-notes="(val) => (glossaryMgmt.glossaryForm.notes = val)"
    />

    <!-- 术语表导入弹窗 -->
    <GlossaryImportModal
      v-model:show="glossaryMgmt.glossaryImportVisible.value"
      @import="(file) => glossaryMgmt.handleGlossaryImport(file)"
    />
  </div>
</template>
