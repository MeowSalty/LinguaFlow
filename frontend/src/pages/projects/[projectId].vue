<script setup lang="ts">
import { NAlert, NButton, NIcon, NTabPane, NTabs } from 'naive-ui'
import { ref, computed, watch, onMounted, onBeforeUnmount, provide } from 'vue'
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
import GlossarySyncDrawer from '@/components/workspace/GlossarySyncDrawer.vue'
import SegmentPanel from '@/components/workspace/SegmentPanel.vue'
import SegmentTranslationPreviewDrawer from '@/components/workspace/SegmentTranslationPreviewDrawer.vue'
import SegmentRevisionPreviewDrawer from '@/components/workspace/SegmentRevisionPreviewDrawer.vue'
import JobPanel from '@/components/workspace/JobPanel.vue'
import JobCreateDrawer from '@/components/workspace/JobCreateDrawer.vue'
import QaRecheckDrawer from '@/components/workspace/QaRecheckDrawer.vue'
import ConflictDialog from '@/components/workspace/ConflictDialog.vue'
import IncrementalResultModal from '@/components/workspace/IncrementalResultModal.vue'
import ProjectFormDrawer from '@/components/projects/ProjectFormDrawer.vue'
import { useGlossaryManagement, GlossaryMgmtKey } from '@/composables/useGlossaryManagement'
import { useJobActions } from '@/composables/useJobActions'
import { useConflictHandling } from '@/composables/useConflictHandling'
import { formatDate } from '@/composables/useWorkspaceUtils'
import { useExecutionPlanTemplatesStore } from '@/stores/executionPlanTemplates'
import { useGlossaryStore } from '@/stores/glossary'
import { useProjectWorkspaceStore } from '@/stores/projectWorkspace'

type Resource = ApiSchemas['Resource']

type WorkspaceTab = 'resources' | 'jobs' | 'glossary'

const route = useRoute()
const router = useRouter()
const { t } = useI18n()
const workspace = useProjectWorkspaceStore()
const glossary = useGlossaryStore()
const executionPlanTemplatesStore = useExecutionPlanTemplatesStore()

const activeTab = ref<WorkspaceTab>('resources')

// ── 编辑视图态（query: edit=<resourceId>&chapter=<groupKey>）──
/** 编辑视图激活：query.edit 存在即进入（resourceId 解析由下方 watcher 负责） */
const editorActive = computed(() => route.query.edit !== undefined)

/** 进入编辑视图（资源 + 可选 EPUB 章节直达） */
const enterEditor = (resourceId: number, chapterKey?: string): void => {
  const query: Record<string, string> = { ...route.query, edit: String(resourceId) }
  if (chapterKey) {
    query.chapter = chapterKey
  } else {
    delete query.chapter
  }
  void router.replace({ query })
}
const segmentPanelRef = ref<InstanceType<typeof SegmentPanel> | null>(null)
const segmentTranslationPreviewDrawerRef = ref<InstanceType<
  typeof SegmentTranslationPreviewDrawer
> | null>(null)
const segmentTranslationPreviewVisible = ref(false)
const segmentRevisionPreviewDrawerRef = ref<InstanceType<
  typeof SegmentRevisionPreviewDrawer
> | null>(null)
const segmentRevisionPreviewVisible = ref(false)

// ── 标签页懒加载 ──
const loadedTabs = new Set<string>()

const loadTabData = async (tab: WorkspaceTab): Promise<void> => {
  if (loadedTabs.has(tab) || !projectId.value) return

  switch (tab) {
    case 'resources':
      // 资源数据已随资源树加载同步
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

// ── 编辑项目抽屉（表单在共享 ProjectFormDrawer 内） ──
const editDrawerVisible = ref(false)

const openEditDrawer = (): void => {
  if (!workspace.project) return
  editDrawerVisible.value = true
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

  // 重新加载当前标签页数据
  loadedTabs.clear()
  await loadTabData(activeTab.value)
}

// ── ResourceExplorer 事件处理 ──
// 进入编辑视图只做状态对齐（query 写入 → 深链 watcher 设 store），
// 段落/章节数据由 SegmentPanel 的筛选 watcher 统一加载。
const handleExplorerOpenSegments = (resource: Resource): void => {
  workspace.setActiveResource(resource.id)

  // EPUB 资源：进入 EPUB 虚拟目录（章节列表模式）
  if (resource.format === 'epub') {
    void workspace.enterEpub(projectId.value!, { id: resource.id, name: resource.name })
    return
  }

  // 非 EPUB 资源：进入编辑视图
  enterEditor(resource.id)
}

/** 处理 EPUB 章节点击：进入章节编辑视图 */
const handleOpenEpubSegments = (resourceId: number, groupKey: string): void => {
  const groupTitle =
    workspace.epubDirectoryChapters.find((g) => g.group_key === groupKey)?.group_title ?? groupKey
  workspace.enterChapter(groupKey, groupTitle)
  enterEditor(resourceId, groupKey)
}

/** EPUB 章节选中数量 */
const epubSelectedChapterCount = computed(() => workspace.epubSelectedGroupKeys.size)

/** 翻译选中的 EPUB 章节：使用 EPUB 资源 ID 打开任务创建抽屉 */
const handleTranslateEpubChapters = (): void => {
  const epubResourceId = workspace.epubDirectoryResourceId
  if (!epubResourceId) return
  const groupKeys = [...workspace.epubSelectedGroupKeys]
  jobMgmt.openResourceJobDrawerWithIds([epubResourceId], groupKeys)
  workspace.epubSelectedGroupKeys = new Set()
}

/** 清除 EPUB 章节选中 */
const handleClearEpubChapterSelection = (): void => {
  workspace.epubSelectedGroupKeys = new Set()
}

// ── 段落选择操作 ──
const selectedSegmentCount = computed(() => segmentPanelRef.value?.selectedSegmentIds.length ?? 0)

// ── QA 重检 ──
// project 模式由抽屉内范围单选决定；其余模式由触发入口传入固定目标
interface QaRecheckTarget {
  mode: 'project' | 'resources' | 'chapters' | 'segments'
  resourceIds: number[]
  groupKeys: string[]
  segmentIds: number[]
}

const EMPTY_QA_RECHECK_TARGET: QaRecheckTarget = {
  mode: 'project',
  resourceIds: [],
  groupKeys: [],
  segmentIds: [],
}

const qaRecheckDrawerVisible = ref(false)
const qaRecheckTarget = ref<QaRecheckTarget>({ ...EMPTY_QA_RECHECK_TARGET })

const openQaRecheckDrawer = (target: QaRecheckTarget): void => {
  qaRecheckTarget.value = target
  qaRecheckDrawerVisible.value = true
}

/** 段落工具栏入口：项目级重检，范围由抽屉内单选决定（含当前段落选中） */
const handleQaRecheckProject = (): void => {
  openQaRecheckDrawer({
    mode: 'project',
    resourceIds: [],
    groupKeys: [],
    segmentIds: [...(segmentPanelRef.value?.selectedSegmentIds ?? [])],
  })
}

/** 资源胶囊入口：重检选中的资源 */
const handleQaRecheckSelectedResources = (): void => {
  const resourceIds = [...jobMgmt.selectedResourceIds.value]
  if (resourceIds.length === 0) return
  openQaRecheckDrawer({ mode: 'resources', resourceIds, groupKeys: [], segmentIds: [] })
}

/** EPUB 章节胶囊入口：重检选中的章节（与任务创建一致，同时上送资源 ID 与分组键） */
const handleQaRecheckSelectedChapters = (): void => {
  const epubResourceId = workspace.epubDirectoryResourceId
  if (!epubResourceId) return
  const groupKeys = [...workspace.epubSelectedGroupKeys]
  openQaRecheckDrawer({
    mode: 'chapters',
    resourceIds: [epubResourceId],
    groupKeys,
    segmentIds: [],
  })
  workspace.epubSelectedGroupKeys = new Set()
}

/** 段落胶囊入口：重检选中的段落 */
const handleQaRecheckSelectedSegments = (): void => {
  const ids = segmentPanelRef.value?.selectedSegmentIds as number[] | undefined
  if (!ids || ids.length === 0) return
  openQaRecheckDrawer({
    mode: 'segments',
    resourceIds: [],
    groupKeys: [],
    segmentIds: [...ids],
  })
  segmentPanelRef.value?.clearSelectedSegments()
}

/** 重检仅更新 quality_issues，重载当前视图的段落以刷新高亮与筛选 */
const handleQaRecheckCompleted = (): void => {
  if (!projectId.value || !workspace.activeResourceId) return
  void workspace.loadSegments(
    projectId.value,
    workspace.activeResourceId,
    false,
    workspace.epubActiveGroupKey ?? undefined,
  )
}

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

const handlePreviewTranslation = (segment: ApiSchemas['Segment']): void => {
  if (!workspace.activeResourceId) return
  segmentTranslationPreviewDrawerRef.value?.open(segment, workspace.activeResourceId)
}

const handlePreviewRevision = (segment: ApiSchemas['Segment']): void => {
  if (!workspace.activeResourceId) return
  segmentRevisionPreviewDrawerRef.value?.open(segment, workspace.activeResourceId)
}

const handlePreviewApplied = async (payload: {
  segment: ApiSchemas['Segment']
  resourceId: number
}): Promise<void> => {
  if (!projectId.value) return

  const refreshes: Promise<void>[] = [
    workspace.loadSegments(
      projectId.value,
      payload.resourceId,
      false,
      workspace.epubActiveGroupKey ?? undefined,
    ),
    workspace.loadResourceTree(projectId.value),
  ]

  if (workspace.isEpubResource) {
    refreshes.push(workspace.refreshChapterGroups(projectId.value, payload.resourceId))
    if (workspace.isInEpubDirectory) {
      refreshes.push(
        workspace.refreshEpubChapters(projectId.value).catch((error) => {
          console.error(error)
        }),
      )
    }
  }

  await Promise.all(refreshes)
}

// ── Watchers ──
watch(
  () => route.query.tab,
  (tab) => {
    if (tab === 'jobs' || tab === 'resources' || tab === 'glossary') {
      activeTab.value = tab
    } else if (tab === 'segments') {
      // 旧链接兼容：段落编辑已从 Tab 拆出为独立编辑视图
      activeTab.value = 'resources'
    }
  },
  { immediate: true },
)

// ── 编辑视图深链恢复：query.edit / query.chapter → store 状态对齐 ──
// 职责收敛：这里只把 activeResourceId / epubActiveGroupKey 对齐到路由（含资源树、
// 章节数据异步就位后的二次校准），段落数据统一由 SegmentPanel 的筛选 watcher 加载，
// 避免两处 watcher 各自 loadSegments 造成重复请求。
watch(
  () =>
    [
      route.query.edit,
      route.query.chapter,
      workspace.resources.length,
      workspace.segmentGroups.length,
    ] as const,
  ([editRaw, chapterRaw]) => {
    if (!projectId.value) return
    const editId = Number(editRaw)
    if (!Number.isFinite(editId)) return

    const resource = workspace.resources.find((r) => r.id === editId)
    if (!resource) return

    if (workspace.activeResourceId !== editId) {
      workspace.setActiveResource(editId)
    }

    // EPUB 资源需要章节数据；非 EPUB 直接忽略 chapter 参数
    if (resource.format === 'epub') {
      // 仅当章节数据尚未加载时拉取（此 watcher 依赖 segmentGroups.length，
      // 无条件拉取会与依赖变化形成循环）
      if (workspace.segmentGroups.length === 0) {
        void workspace.loadEpubData(projectId.value, editId)
        return // 章节数据就位后本 watcher 重跑，再对齐章节
      }
      if (typeof chapterRaw === 'string' && chapterRaw) {
        const title =
          workspace.segmentGroups.find((g) => g.group_key === chapterRaw)?.group_title ?? chapterRaw
        // key 或标题未对齐时进入章节（含 fallback 标题 → 真实标题的二次校准）。
        // 注意：同 key 同标题时不再调 enterChapter，否则会触发
        // SegmentPanel 的章节路由同步 watcher → router.replace → 本 watcher 无限循环。
        if (
          workspace.epubActiveGroupKey !== chapterRaw ||
          workspace.epubActiveGroupTitle !== title
        ) {
          workspace.enterChapter(chapterRaw, title)
        }
        return
      }
      workspace.exitChapter()
    }
  },
  { immediate: true },
)

// 退出编辑视图时清空段落下拉/选择，避免浏览态残留编辑上下文
watch(editorActive, (active) => {
  if (!active && workspace.activeResourceId) {
    workspace.setActiveResource(null)
  }
})

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
  <div class="flex h-[calc(100vh-7rem)] min-h-0 flex-col gap-4">
    <!-- ── 编辑视图：沉浸式三栏（席位常驻），独立于浏览态 Tab ── -->
    <template v-if="editorActive">
      <SegmentPanel
        ref="segmentPanelRef"
        :project-id="projectId"
        @preview-translation="handlePreviewTranslation"
        @preview-revision="handlePreviewRevision"
        @refresh="reloadSegments"
        @qa-recheck="handleQaRecheckProject"
      />
    </template>

    <!-- ── 浏览态：页头 + 指标 + Tab（恢复 8b6445a7 之前的全局 1100px 居中帽）── -->
    <template v-else>
      <div class="mx-auto flex w-full max-w-275 flex-1 min-h-0 flex-col gap-4">
        <section class="flex shrink-0 flex-wrap items-center justify-between gap-x-4 gap-y-2">
          <div class="flex flex-col gap-3 sm:flex-row sm:items-center sm:justify-between">
            <div class="flex min-w-0 flex-wrap items-center gap-x-2.5 gap-y-1.5">
              <NButton quaternary size="small" @click="router.push('/projects')">
                <template #icon>
                  <NIcon><IconCarbonArrowLeft /></NIcon>
                </template>
              </NButton>

              <h1 class="truncate text-lg font-semibold tracking-tight text-lf-text-strong">
                {{ workspace.project?.name || t('workspace.loadingProject') }}
              </h1>

              <NButton
                v-if="workspace.project"
                quaternary
                circle
                size="tiny"
                :title="t('common.actions.edit')"
                @click="openEditDrawer"
              >
                <template #icon>
                  <NIcon size="14"><IconCarbonEdit /></NIcon>
                </template>
              </NButton>

              <span class="hidden h-4 w-px bg-lf-border-soft sm:inline-block" />
              <span
                class="hidden items-center gap-1.5 rounded-md bg-lf-surface-muted px-2 py-0.5 text-xs text-lf-text-muted sm:inline-flex"
              >
                <IconCarbonLanguage class="h-3.5 w-3.5 text-brand-500" />
                <span class="font-medium text-lf-text-strong">{{
                  workspace.project?.source_lang || '-'
                }}</span>
                <span class="text-lf-text-subtle">→</span>
                <span class="font-medium text-lf-text-strong">{{
                  workspace.project?.target_lang || '-'
                }}</span>
              </span>
              <span class="hidden h-4 w-px bg-lf-border-soft md:inline-block" />
              <span class="hidden items-center gap-1.5 text-xs text-lf-text-muted md:inline-flex">
                <IconCarbonTime class="h-3.5 w-3.5 text-lf-text-subtle" />
                {{
                  t('workspace.updatedAt', {
                    time: formatDate(
                      workspace.project?.updated_at ?? workspace.project?.created_at,
                    ),
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
                {{ t('common.actions.refresh') }}
              </NButton>
            </div>
          </div>
        </section>

        <NAlert v-if="workspace.projectError" class="shrink-0" type="error" :bordered="false">
          {{ workspace.projectError }}
        </NAlert>

        <NAlert v-if="workspace.resourceTreeError" class="shrink-0" type="error" :bordered="false">
          {{ workspace.resourceTreeError }}
        </NAlert>

        <NAlert v-if="workspace.segmentsError" class="shrink-0" type="error" :bordered="false">
          {{ workspace.segmentsError }}
        </NAlert>

        <WorkspaceMetricsBar
          class="shrink-0"
          :total-resources="workspace.resources.length"
          :total-segments="workspace.totalSegmentCount"
          :translated-segments="workspace.totalTranslatedSegments"
          :approved-segments="workspace.totalApprovedSegments"
          :running-jobs="workspace.runningJobCount"
        />

        <!-- 定高工作台：Tab 条常驻，Section 区内部滚动 -->
        <div class="lf-panel flex min-h-0 flex-1 flex-col overflow-hidden">
          <NTabs
            v-model:value="activeTab"
            type="line"
            animated
            class="workspace-tabs flex h-full min-h-0 flex-col px-3 pt-1 sm:px-4"
          >
            <NTabPane name="resources" :tab="t('workspace.tabs.resources')">
              <div
                :class="[
                  'h-full w-full overflow-y-auto pb-3 pt-2',
                  workspace.uploadTasks.length > 0 ? 'pb-20 sm:pb-0' : '',
                ]"
              >
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

            <NTabPane name="jobs" :tab="t('workspace.tabs.jobs')">
              <div class="h-full w-full overflow-y-auto pb-3 pt-2">
                <JobPanel
                  :project-id="projectId"
                  @detail="(job) => jobMgmt.openJobDetail(job)"
                  @cancel="(job) => jobMgmt.cancelJob(job)"
                  @retry="(job) => jobMgmt.retryJob(job)"
                  @pause="(job) => jobMgmt.pauseJob(job)"
                  @resume="(job) => jobMgmt.resumeJob(job)"
                />
              </div>
            </NTabPane>

            <NTabPane name="glossary" :tab="t('workspace.tabs.glossary')">
              <div class="h-full w-full overflow-y-auto pb-3 pt-2">
                <GlossaryPanel :project-id="projectId" />
              </div>
            </NTabPane>
          </NTabs>
        </div>
      </div>
    </template>

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
      :segment-filter="jobMgmt.jobForm.segment_filter"
      :form-rules="jobMgmt.jobFormRules.value"
      :execution-plan-options="jobMgmt.executionPlanOptions.value"
      :submitting="workspace.creatingJob"
      :segment-count="drawerSegmentCount"
      :selected-plan-template="jobMgmt.selectedPlanTemplate.value"
      @update:execution-plan-id="(val) => (jobMgmt.jobForm.execution_plan_id = val)"
      @update:auto-approve="(val) => (jobMgmt.jobForm.auto_approve = val)"
      @update:segment-filter="(val) => (jobMgmt.jobForm.segment_filter = val)"
      @submit="jobMgmt.submitJob()"
      @close="jobMgmt.closeJobDrawer()"
    />

    <!-- QA 重检抽屉 -->
    <QaRecheckDrawer
      v-model:show="qaRecheckDrawerVisible"
      :project-id="projectId"
      :target-mode="qaRecheckTarget.mode"
      :target-resource-ids="qaRecheckTarget.resourceIds"
      :target-group-keys="qaRecheckTarget.groupKeys"
      :target-segment-ids="qaRecheckTarget.segmentIds"
      @completed="handleQaRecheckCompleted"
    />

    <SegmentTranslationPreviewDrawer
      ref="segmentTranslationPreviewDrawerRef"
      v-model:show="segmentTranslationPreviewVisible"
      :project-id="projectId"
      :text-render-mode="
        workspace.activeResource?.format === 'epub' ||
        workspace.activeResource?.format === 'docx' ||
        workspace.activeResource?.format === 'html'
          ? 'html'
          : 'plaintext'
      "
      @applied="handlePreviewApplied"
    />

    <SegmentRevisionPreviewDrawer
      ref="segmentRevisionPreviewDrawerRef"
      v-model:show="segmentRevisionPreviewVisible"
      :project-id="projectId"
      :text-render-mode="
        workspace.activeResource?.format === 'epub' ||
        workspace.activeResource?.format === 'docx' ||
        workspace.activeResource?.format === 'html'
          ? 'html'
          : 'plaintext'
      "
      @applied="handlePreviewApplied"
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
      @update:form-forbidden="(val) => (glossaryMgmt.glossaryForm.forbidden = val)"
      @update:form-mandatory="(val) => (glossaryMgmt.glossaryForm.mandatory = val)"
      @update:form-case-sensitive="(val) => (glossaryMgmt.glossaryForm.case_sensitive = val)"
      @update:form-notes="(val) => (glossaryMgmt.glossaryForm.notes = val)"
    />

    <!-- 术语表导入弹窗 -->
    <GlossaryImportModal
      v-model:show="glossaryMgmt.glossaryImportVisible.value"
      @import="(file) => glossaryMgmt.handleGlossaryImport(file)"
    />

    <!-- 术语表同步抽屉 -->
    <GlossarySyncDrawer
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
      show-qa-recheck
      @translate="jobMgmt.openResourceJobDrawer()"
      @qa-recheck="handleQaRecheckSelectedResources"
      @clear="jobMgmt.clearResourceSelection()"
    />

    <!-- 浮动操作岛 - EPUB 章节选择 -->
    <SelectionActionBar
      v-show="activeTab === 'resources' && workspace.isInEpubDirectory"
      :count="epubSelectedChapterCount"
      :can-translate="epubSelectedChapterCount > 0"
      show-qa-recheck
      @translate="handleTranslateEpubChapters"
      @qa-recheck="handleQaRecheckSelectedChapters"
      @clear="handleClearEpubChapterSelection"
    />

    <!-- 浮动操作岛 - 段落选择（编辑视图） -->
    <SelectionActionBar
      v-show="editorActive"
      :count="selectedSegmentCount"
      :can-translate="selectedSegmentCount > 0"
      :show-review="true"
      :can-review="selectedSegmentCount > 0"
      show-qa-recheck
      @translate="handleTranslateSelectedSegments"
      @qa-recheck="handleQaRecheckSelectedSegments"
      @clear="handleClearSelectedSegments"
      @approve="handleBatchReview('approve')"
      @reject="handleBatchReview('reject')"
    />

    <!-- 编辑项目抽屉 -->
    <ProjectFormDrawer
      v-model:show="editDrawerVisible"
      :project="workspace.project"
      @saved="(project) => (workspace.project = project)"
    />
  </div>
</template>
