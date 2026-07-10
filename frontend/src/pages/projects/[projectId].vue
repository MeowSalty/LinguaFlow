<script setup lang="ts">
import {
  NAlert,
  NButton,
  NCard,
  NDrawer,
  NDrawerContent,
  NForm,
  NFormItem,
  NIcon,
  NInput,
  NSelect,
  NSwitch,
  NTabPane,
  NTabs,
  useMessage,
  type FormInst,
  type FormRules,
} from 'naive-ui'
import { ref, computed, reactive, watch, onMounted, onBeforeUnmount, provide } from 'vue'
import { useI18n } from 'vue-i18n'

import { type ApiSchemas } from '@/api/client'
import { batchReviewSegments } from '@/api/projects'
import ResourceExplorer from '@/components/workspace/ResourceExplorer.vue'
import SelectionActionBar from '@/components/workspace/SelectionActionBar.vue'
import UploadPanel from '@/components/workspace/UploadPanel.vue'
import WorkspaceMetricsBar from '@/components/workspace/WorkspaceMetricsBar.vue'
import GlossaryPanel from '@/components/workspace/GlossaryPanel.vue'
import GlossaryDrawer from '@/components/workspace/GlossaryDrawer.vue'
import GlossaryImportModal from '@/components/workspace/GlossaryImportModal.vue'
import GlossarySyncDialog from '@/components/workspace/GlossarySyncDialog.vue'
import SegmentPanel from '@/components/workspace/SegmentPanel.vue'
import JobPanel from '@/components/workspace/JobPanel.vue'
import JobCreateDrawer from '@/components/workspace/JobCreateDrawer.vue'
import ConflictDialog from '@/components/workspace/ConflictDialog.vue'
import IncrementalResultModal from '@/components/workspace/IncrementalResultModal.vue'
import { useGlossaryManagement, GlossaryMgmtKey } from '@/composables/useGlossaryManagement'
import { useJobActions } from '@/composables/useJobActions'
import { useConflictHandling } from '@/composables/useConflictHandling'
import { useLanguageOptions } from '@/composables/useLanguageOptions'
import { formatDate } from '@/composables/useWorkspaceUtils'
import { useExecutionPlanTemplatesStore } from '@/stores/executionPlanTemplates'
import { useGlossaryStore } from '@/stores/glossary'
import { useProjectsStore } from '@/stores/projects'
import { useProjectWorkspaceStore } from '@/stores/projectWorkspace'

type Resource = ApiSchemas['Resource']

type WorkspaceTab = 'resources' | 'segments' | 'jobs' | 'glossary'

const route = useRoute()
const router = useRouter()
const { t } = useI18n()
const message = useMessage()
const workspace = useProjectWorkspaceStore()
const glossary = useGlossaryStore()
const executionPlanTemplatesStore = useExecutionPlanTemplatesStore()
const projectsStore = useProjectsStore()

const activeTab = ref<WorkspaceTab>('resources')
const segmentPanelRef = ref<InstanceType<typeof SegmentPanel> | null>(null)

// ── 标签页懒加载 ──
const loadedTabs = new Set<string>()

const loadTabData = async (tab: WorkspaceTab): Promise<void> => {
  if (loadedTabs.has(tab) || !projectId.value) return

  switch (tab) {
    case 'resources':
      // 资源数据已通过 syncResourcesFromTree 加载
      break
    case 'segments':
      // 段落数据在用户选择资源时按需加载
      break
    case 'jobs':
      await workspace.loadJobs(projectId.value)
      break
    case 'glossary':
      await glossary.loadEntries(projectId.value)
      break
  }

  loadedTabs.add(tab)
}

// ── 编辑项目抽屉 ──
const editDrawerVisible = ref(false)
const editFormRef = ref<FormInst | null>(null)
const editSubmitting = ref(false)

const editFormModel = reactive({
  name: '',
  source_lang: 'auto',
  target_lang: 'en-US',
  glossary_enabled: false,
})

const { targetLanguageOptions, sourceLanguageOptions } = useLanguageOptions()

const editFormRules = computed<FormRules>(() => ({
  name: [
    {
      required: true,
      message: t('projects.validation.nameRequired'),
      trigger: ['input', 'blur'],
    },
  ],
  source_lang: [
    {
      required: true,
      message: t('projects.validation.sourceLangRequired'),
      trigger: ['change', 'blur'],
    },
  ],
  target_lang: [
    {
      required: true,
      message: t('projects.validation.targetLangRequired'),
      trigger: ['change', 'blur'],
    },
  ],
}))

const openEditDrawer = (): void => {
  if (!workspace.project) return
  editFormModel.name = workspace.project.name
  editFormModel.source_lang = workspace.project.source_lang || 'auto'
  editFormModel.target_lang = workspace.project.target_lang || 'en-US'
  editFormModel.glossary_enabled = workspace.project.glossary_enabled ?? false
  editDrawerVisible.value = true
}

const closeEditDrawer = (): void => {
  editDrawerVisible.value = false
}

const submitEditProject = async (): Promise<void> => {
  await editFormRef.value?.validate()
  if (!projectId.value) return

  editSubmitting.value = true
  try {
    const updated = await projectsStore.updateProject(projectId.value, {
      name: editFormModel.name.trim(),
      source_lang: editFormModel.source_lang.trim(),
      target_lang: editFormModel.target_lang.trim(),
      glossary_enabled: editFormModel.glossary_enabled,
    })
    workspace.project = updated
    message.success(t('projects.messages.updateSuccess'))
    closeEditDrawer()
  } catch (err) {
    console.error(err)
    message.error(projectsStore.updateError || t('projects.messages.updateFailed'))
  } finally {
    editSubmitting.value = false
  }
}

// ── projectId ──
const projectId = computed(() => {
  const params = route.params as Partial<Record<'projectId', string | string[]>>
  const rawValue = Array.isArray(params.projectId) ? params.projectId[0] : params.projectId
  const parsed = Number(rawValue)
  return Number.isFinite(parsed) ? parsed : null
})

// ── Composables ──
const glossaryMgmt = useGlossaryManagement(projectId)
provide(GlossaryMgmtKey, glossaryMgmt)

const switchToJobsTab = async (): Promise<void> => {
  // 任务已通过全局追踪器自动追踪，用户可留在当前页面继续工作
}

const jobMgmt = useJobActions(projectId, switchToJobsTab)

// ── 执行计划模板按需加载 ──
watch(
  () => jobMgmt.jobDrawerVisible.value,
  async (visible) => {
    if (visible && executionPlanTemplatesStore.items.length === 0) {
      await executionPlanTemplatesStore.loadTemplates()
    }
  },
)

// ── 翻译内容段落数量（用于 JobCreateDrawer 摘要）──
const drawerSegmentCount = computed(() => {
  if (jobMgmt.jobTargetMode.value === 'segments') {
    return jobMgmt.jobTargetSegmentIds.value.length
  }

  // EPUB 章节翻译模式：从 epubDirectoryChapters 按 groupKey 筛选段落数
  if (jobMgmt.jobTargetGroupKeys.value.length > 0) {
    const selectedKeys = new Set(jobMgmt.jobTargetGroupKeys.value)
    return workspace.epubDirectoryChapters
      .filter((ch) => selectedKeys.has(ch.group_key))
      .reduce((sum, ch) => sum + ch.segment_count, 0)
  }

  // 普通资源模式：使用任务目标资源 ID 列表查找总段落数
  const targetIdSet = new Set(jobMgmt.jobTargetResourceIds.value)
  return workspace.resources
    .filter((r) => targetIdSet.has(r.id))
    .reduce((sum, r) => sum + (r.total_segments ?? 0), 0)
})

const reloadSegments = async (): Promise<void> => {
  if (!projectId.value || !workspace.activeResourceId) {
    return
  }
  await workspace.loadSegments(projectId.value, workspace.activeResourceId)
}

const handleGlossarySynced = async (): Promise<void> => {
  if (projectId.value && workspace.activeResourceId) {
    await workspace.loadSegments(projectId.value, workspace.activeResourceId)
  }
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
  ])
  workspace.syncResourcesFromTree()

  // 重新加载当前标签页数据
  loadedTabs.clear()
  await loadTabData(activeTab.value)
}

// ── ResourceExplorer 事件处理 ──
const handleExplorerOpenSegments = (resource: Resource): void => {
  workspace.setActiveResource(resource.id)

  // EPUB 资源：进入 EPUB 虚拟目录（章节列表模式）
  if (resource.format === 'epub') {
    void workspace.enterEpub(projectId.value!, { id: resource.id, name: resource.name })
    return
  }

  // 非 EPUB 资源：跳转到段落编辑
  void workspace.loadSegments(projectId.value!, resource.id)
  activeTab.value = 'segments'
}

/** 处理 EPUB 章节点击：进入章节段落编辑视图 */
const handleOpenEpubSegments = (resourceId: number, groupKey: string): void => {
  const groupTitle =
    workspace.epubDirectoryChapters.find((g) => g.group_key === groupKey)?.group_title ?? groupKey
  workspace.enterChapter(groupKey, groupTitle)
  void workspace.loadSegments(projectId.value!, resourceId, false, groupKey)
  activeTab.value = 'segments'
}

/** EPUB 章节选中数量 */
const epubSelectedChapterCount = computed(() => workspace.epubSelectedGroupKeys.size)

/** 翻译选中的 EPUB 章节：使用 EPUB 资源 ID 打开任务创建抽屉 */
const handleTranslateEpubChapters = (): void => {
  const epubResourceId = workspace.epubDirectoryResourceId
  if (!epubResourceId) return
  const groupKeys = [...workspace.epubSelectedGroupKeys]
  console.debug('[projectId] handleTranslateEpubChapters:', {
    epubResourceId,
    groupKeys,
    setBeforeClear: [...workspace.epubSelectedGroupKeys],
  })
  jobMgmt.openResourceJobDrawerWithIds([epubResourceId], groupKeys)
  workspace.epubSelectedGroupKeys = new Set()
  console.debug('[projectId] after clear:', {
    setAfterClear: [...workspace.epubSelectedGroupKeys],
  })
}

/** 清除 EPUB 章节选中 */
const handleClearEpubChapterSelection = (): void => {
  workspace.epubSelectedGroupKeys = new Set()
}

// ── 段落选择操作 ──
const selectedSegmentCount = computed(() => segmentPanelRef.value?.selectedSegmentIds.length ?? 0)

const handleTranslateSelectedSegments = (): void => {
  const ids = segmentPanelRef.value?.selectedSegmentIds as number[] | undefined
  if (!ids || ids.length === 0) return
  jobMgmt.openSegmentJobDrawerWithIds(ids)
  segmentPanelRef.value?.clearSelectedSegments()
}

const handleClearSelectedSegments = (): void => {
  segmentPanelRef.value?.clearSelectedSegments()
}

const handleBatchReview = async (action: 'approve' | 'reject'): Promise<void> => {
  if (!projectId.value || !workspace.activeResourceId) return
  const segmentIds = segmentPanelRef.value?.selectedSegmentIds as number[] | undefined
  if (!segmentIds || segmentIds.length === 0) return

  await batchReviewSegments(projectId.value, workspace.activeResourceId, segmentIds, action)
  await reloadSegments()
  segmentPanelRef.value?.clearSelectedSegments()
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
  (newVal, oldVal) => {
    if (!projectId.value || !workspace.activeResourceId) return

    const resourceIdChanged = newVal[2] !== oldVal?.[2]

    // EPUB 资源切换时加载章节数据
    if (resourceIdChanged && workspace.isEpubResource) {
      void workspace.loadEpubData(projectId.value, workspace.activeResourceId)
    }

    // 加载段落数据（EPUB "全部章节"视图加载全部段落，章节视图加载对应章节段落）
    void workspace.loadSegments(
      projectId.value,
      workspace.activeResourceId,
      false,
      workspace.epubActiveGroupKey ?? undefined,
    )
  },
)

watch(
  () => workspace.jobStatusFilter,
  () => {
    if (projectId.value && loadedTabs.has('jobs')) {
      void workspace.loadJobs(projectId.value)
    }
  },
)

watch(activeTab, (tab) => {
  if (route.query.tab !== tab) {
    void router.replace({ query: { ...route.query, tab } })
  }
  void loadTabData(tab)
})

onBeforeUnmount(() => {
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
          if (workspace.isEpubResource) {
            // 刷新章节分组进度
            void workspace.refreshChapterGroups(projectId.value, workspace.activeResourceId)
            // 如果在章节内容视图中，重新加载当前章节
            if (workspace.epubActiveGroupKey) {
              void workspace.loadSegments(
                projectId.value,
                workspace.activeResourceId,
                false,
                workspace.epubActiveGroupKey,
              )
            }
          } else {
            // 非 EPUB 资源：正常刷新 segments
            void workspace.loadSegments(projectId.value, workspace.activeResourceId)
          }
        }
        break
      }
    }
  },
)

onMounted(() => {
  workspace.reset()
  glossary.reset()
  loadedTabs.clear()
  void reloadWorkspace()
})
</script>

<template>
  <div class="space-y-5">
    <!-- 项目头部 -->
    <NCard :bordered="false" class="overflow-hidden shadow-sm shadow-lf-shadow">
      <div class="flex flex-col gap-4 sm:flex-row sm:items-center sm:justify-between">
        <div class="flex min-w-0 flex-wrap items-center gap-x-3 gap-y-2">
          <NButton quaternary size="small" @click="router.push('/projects')">
            <template #icon>
              <NIcon><IconCarbonArrowLeft /></NIcon>
            </template>
          </NButton>

          <h1 class="truncate text-xl font-bold tracking-tight text-lf-text-strong">
            {{ workspace.project?.name || t('workspace.loadingProject') }}
          </h1>

          <!-- 编辑按钮 -->
          <NButton
            v-if="workspace.project"
            quaternary
            circle
            size="tiny"
            :title="t('projects.actions.edit')"
            @click="openEditDrawer"
          >
            <template #icon>
              <NIcon size="14"><IconCarbonEdit /></NIcon>
            </template>
          </NButton>

          <span class="hidden h-4 w-px bg-lf-border-soft sm:inline-block" />
          <span class="hidden items-center gap-1.5 text-sm text-lf-text-muted sm:inline-flex">
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

        <div class="flex shrink-0 items-center gap-2">
          <NButton
            secondary
            size="small"
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
        </div>
      </div>
    </NCard>

    <NAlert v-if="workspace.projectError" type="error" :bordered="false">
      {{ workspace.projectError }}
    </NAlert>

    <NAlert v-if="workspace.resourceTreeError" type="error" :bordered="false">
      {{ workspace.resourceTreeError }}
    </NAlert>

    <NAlert v-if="workspace.segmentsError" type="error" :bordered="false">
      {{ workspace.segmentsError }}
    </NAlert>

    <!-- 统计指标栏 -->
    <WorkspaceMetricsBar
      :total-resources="workspace.resources.length"
      :total-segments="workspace.totalSegmentCount"
      :translated-segments="workspace.totalTranslatedSegments"
      :approved-segments="workspace.totalApprovedSegments"
      :running-jobs="workspace.runningJobCount"
    />

    <!-- 标签页 -->
    <NCard :bordered="false" class="shadow-sm shadow-lf-shadow">
      <NTabs v-model:value="activeTab" animated>
        <NTabPane name="resources" :tab="t('workspace.tabs.resources')">
          <div class="pt-3">
            <ResourceExplorer
              v-if="projectId"
              :project-id="projectId"
              @open-segments="handleExplorerOpenSegments"
              @open-epub-segments="handleOpenEpubSegments"
              @conflict="conflictMgmt.handleExplorerConflict"
              @incremental-result="conflictMgmt.handleExplorerIncrementalResult"
            />
          </div>
        </NTabPane>

        <NTabPane name="segments" :tab="t('workspace.tabs.segments')">
          <SegmentPanel
            ref="segmentPanelRef"
            :project-id="projectId"
            @translate="(segment) => jobMgmt.openSegmentJobDrawer(segment)"
            @refresh="reloadSegments"
          />
        </NTabPane>

        <NTabPane name="jobs" :tab="t('workspace.tabs.jobs')">
          <JobPanel
            :project-id="projectId"
            @detail="(job) => jobMgmt.openJobDetail(job)"
            @cancel="(job) => jobMgmt.cancelJob(job)"
            @retry="(job) => jobMgmt.retryJob(job)"
          />
        </NTabPane>

        <NTabPane name="glossary" :tab="t('workspace.tabs.glossary')">
          <GlossaryPanel :project-id="projectId" />
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
      :target-group-keys="jobMgmt.jobTargetGroupKeys.value"
      :execution-plan-id="jobMgmt.jobForm.execution_plan_id"
      :auto-approve="jobMgmt.jobForm.auto_approve"
      :overwrite-mode="jobMgmt.jobForm.overwrite_mode"
      :form-rules="jobMgmt.jobFormRules.value"
      :execution-plan-options="jobMgmt.executionPlanOptions.value"
      :submitting="workspace.creatingJob"
      :segment-count="drawerSegmentCount"
      :selected-plan-template="jobMgmt.selectedPlanTemplate.value"
      @update:execution-plan-id="(val) => (jobMgmt.jobForm.execution_plan_id = val)"
      @update:auto-approve="(val) => (jobMgmt.jobForm.auto_approve = val)"
      @update:overwrite-mode="(val) => (jobMgmt.jobForm.overwrite_mode = val)"
      @submit="jobMgmt.submitJob()"
      @close="jobMgmt.closeJobDrawer()"
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

    <!-- 术语表同步对话框 -->
    <GlossarySyncDialog
      v-model:show="glossaryMgmt.syncDialogVisible.value"
      :project-id="projectId!"
      @close="glossaryMgmt.closeSyncDialog"
      @synced="handleGlossarySynced"
    />

    <!-- 上传面板 -->
    <UploadPanel
      v-show="workspace.uploadTasks.length > 0"
      :project-id="projectId!"
      @refresh="() => workspace.loadResourceTree(projectId!)"
    />

    <!-- 浮动操作岛 - 资源选择（非 EPUB 目录时显示） -->
    <SelectionActionBar
      v-show="activeTab === 'resources' && !workspace.isInEpubDirectory"
      :count="jobMgmt.selectedResourceIds.value.length"
      :can-translate="jobMgmt.canCreateResourceJob.value"
      @translate="jobMgmt.openResourceJobDrawer()"
      @clear="jobMgmt.clearResourceSelection()"
    />

    <!-- 浮动操作岛 - EPUB 章节选择 -->
    <SelectionActionBar
      v-show="activeTab === 'resources' && workspace.isInEpubDirectory"
      :count="epubSelectedChapterCount"
      :can-translate="epubSelectedChapterCount > 0"
      @translate="handleTranslateEpubChapters"
      @clear="handleClearEpubChapterSelection"
    />

    <!-- 浮动操作岛 - 段落选择 -->
    <SelectionActionBar
      v-show="activeTab === 'segments'"
      :count="selectedSegmentCount"
      :can-translate="selectedSegmentCount > 0"
      :show-review="true"
      :can-review="selectedSegmentCount > 0"
      @translate="handleTranslateSelectedSegments"
      @clear="handleClearSelectedSegments"
      @approve="handleBatchReview('approve')"
      @reject="handleBatchReview('reject')"
    />

    <!-- 编辑项目抽屉 -->
    <NDrawer v-model:show="editDrawerVisible" :width="420" placement="right">
      <NDrawerContent :title="t('projects.edit.title')" closable>
        <div class="mb-4 rounded-lg bg-lf-surface-muted p-3 text-sm leading-6 text-lf-text-muted">
          {{ t('projects.edit.description') }}
        </div>

        <NForm
          ref="editFormRef"
          :model="editFormModel"
          :rules="editFormRules"
          label-placement="top"
        >
          <NFormItem path="name" :label="t('projects.form.name')">
            <NInput
              v-model:value="editFormModel.name"
              :placeholder="t('projects.form.namePlaceholder')"
              maxlength="80"
              show-count
            />
          </NFormItem>

          <NFormItem path="glossary_enabled" :label="t('projects.form.glossaryEnabled')">
            <NSwitch v-model:value="editFormModel.glossary_enabled" />
          </NFormItem>

          <div class="grid grid-cols-1 gap-3 sm:grid-cols-2">
            <NFormItem path="source_lang" :label="t('projects.form.sourceLang')">
              <NSelect
                v-model:value="editFormModel.source_lang"
                filterable
                tag
                :options="sourceLanguageOptions"
                :placeholder="t('projects.form.languagePlaceholder')"
              />
            </NFormItem>
            <NFormItem path="target_lang" :label="t('projects.form.targetLang')">
              <NSelect
                v-model:value="editFormModel.target_lang"
                filterable
                tag
                :options="targetLanguageOptions"
                :placeholder="t('projects.form.languagePlaceholder')"
              />
            </NFormItem>
          </div>
        </NForm>

        <template #footer>
          <div class="flex justify-end gap-3">
            <NButton :disabled="editSubmitting" @click="closeEditDrawer">
              {{ t('projects.actions.cancel') }}
            </NButton>
            <NButton type="primary" :loading="editSubmitting" @click="submitEditProject">
              {{ t('projects.actions.submitUpdate') }}
            </NButton>
          </div>
        </template>
      </NDrawerContent>
    </NDrawer>
  </div>
</template>
