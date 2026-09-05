<script setup lang="ts">
import {
  NAlert,
  NButton,
  NDrawer,
  NDrawerContent,
  NEmpty,
  NFormItem,
  NRadio,
  NRadioGroup,
  NSelect,
  useDialog,
  useMessage,
} from 'naive-ui'
import { computed, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'

import type { ApiSchemas } from '@/api/client'
import { qaRecheck } from '@/api/projects'
import { useExecutionProfilesStore } from '@/stores/executionProfiles'
import { useProjectWorkspaceStore } from '@/stores/projectWorkspace'

type QaRecheckResult = ApiSchemas['QaRecheckResult']
type QaRecheckResourceResult = ApiSchemas['QaRecheckResourceResult']

/** project：由抽屉内范围单选决定；其余为页面按触发入口传入的固定目标 */
type QaRecheckTargetMode = 'project' | 'resources' | 'chapters' | 'segments'

const props = withDefaults(
  defineProps<{
    projectId: number | null
    targetMode?: QaRecheckTargetMode
    targetResourceIds?: number[]
    targetGroupKeys?: string[]
    targetSegmentIds?: number[]
  }>(),
  {
    targetMode: 'project',
    targetResourceIds: () => [],
    targetGroupKeys: () => [],
    targetSegmentIds: () => [],
  },
)

const emit = defineEmits<{
  completed: []
}>()

const show = defineModel<boolean>('show', { default: false })

const { t } = useI18n()
const dialog = useDialog()
const message = useMessage()
const workspace = useProjectWorkspaceStore()
const profilesStore = useExecutionProfilesStore()

// ── 表单状态（project 模式下的范围单选） ──
const profileId = ref<number | null>(null)
const scope = ref<'project' | 'resource' | 'chapter' | 'selected'>('project')

// ── 请求/结果状态 ──
const submitting = ref(false)
const errorMessage = ref<string | null>(null)
const result = ref<QaRecheckResult | null>(null)

const busy = computed(() => submitting.value)
const isFixedTarget = computed(() => props.targetMode !== 'project')

// ── 执行策略选项：QA 未启用的策略置灰并标注 ──
const profileOptions = computed(() =>
  profilesStore.items.map((profile) => ({
    label: profile.config?.qa?.enabled
      ? profile.name
      : `${profile.name}（${t('workspace.qaRecheck.profileQaDisabled')}）`,
    value: profile.id,
    disabled: !profile.config?.qa?.enabled,
  })),
)

const selectedProfile = computed(
  () => profilesStore.items.find((profile) => profile.id === profileId.value) ?? null,
)

// ── 固定目标摘要（resources / chapters / segments 模式） ──
const targetSummary = computed(() => {
  switch (props.targetMode) {
    case 'resources':
      return t('workspace.qaRecheck.targetResources', { count: props.targetResourceIds.length })
    case 'chapters':
      return t('workspace.qaRecheck.targetChapters', { count: props.targetGroupKeys.length })
    case 'segments':
      return t('workspace.qaRecheck.targetSegments', { count: props.targetSegmentIds.length })
    default:
      return ''
  }
})

// ── project 模式的重检范围 ──
const hasActiveResource = computed(() => workspace.activeResourceId !== null)
const activeChapterKey = computed(() => workspace.epubActiveGroupKey)
const selectedSegmentCount = computed(() => props.targetSegmentIds.length)

const scopeOptions = computed(() => [
  { value: 'project' as const, label: t('workspace.qaRecheck.scopeProject') },
  {
    value: 'resource' as const,
    label: t('workspace.qaRecheck.scopeResource'),
    disabled: !hasActiveResource.value,
  },
  {
    value: 'chapter' as const,
    label: t('workspace.qaRecheck.scopeChapter'),
    disabled: !activeChapterKey.value,
  },
  {
    value: 'selected' as const,
    label: t('workspace.qaRecheck.scopeSelected', { count: selectedSegmentCount.value }),
    disabled: selectedSegmentCount.value === 0,
  },
])

const scopeHint = computed(() => {
  switch (scope.value) {
    case 'resource':
      return workspace.activeResource
        ? t('workspace.qaRecheck.scopeResourceHint', { name: workspace.activeResource.path })
        : ''
    case 'chapter':
      return t('workspace.qaRecheck.scopeChapterHint', { key: activeChapterKey.value ?? '' })
    case 'selected':
      return t('workspace.qaRecheck.scopeSelectedHint', { count: selectedSegmentCount.value })
    default:
      return t('workspace.qaRecheck.scopeProjectHint')
  }
})

// ── 打开时加载执行策略并重置状态 ──
watch(show, (visible) => {
  if (!visible) return
  profileId.value = null
  scope.value = 'project'
  errorMessage.value = null
  result.value = null
  if (!profilesStore.items.length) {
    void profilesStore.loadProfiles()
  }
})

// 当前选中的范围失效时回退到项目级，避免静默改变作用域
watch(
  () => [hasActiveResource.value, activeChapterKey.value, selectedSegmentCount.value] as const,
  () => {
    if (scope.value === 'resource' && !hasActiveResource.value) scope.value = 'project'
    if (scope.value === 'chapter' && !activeChapterKey.value) scope.value = 'project'
    if (scope.value === 'selected' && selectedSegmentCount.value === 0) scope.value = 'project'
  },
)

const canSubmit = computed(() => Boolean(props.projectId && profileId.value) && !busy.value)

/** 按目标模式与范围构造请求载荷（project 模式缺省为整个项目） */
const buildPayload = (): ApiSchemas['QaRecheckRequest'] | null => {
  if (!profileId.value) return null
  const payload: ApiSchemas['QaRecheckRequest'] = { profile_id: profileId.value }
  if (props.targetMode === 'resources') {
    payload.resource_ids = [...props.targetResourceIds]
  } else if (props.targetMode === 'chapters') {
    // 与任务创建一致：资源 ID + 章节分组键同时上送，由后端按优先级解析
    payload.resource_ids = [...props.targetResourceIds]
    payload.segment_group_keys = [...props.targetGroupKeys]
  } else if (props.targetMode === 'segments') {
    payload.segment_ids = [...props.targetSegmentIds]
  } else if (scope.value === 'chapter' && activeChapterKey.value) {
    // 与 chapters 入口及任务创建一致：资源 ID + 分组键配对上送，按键解析限定在当前资源内
    if (workspace.activeResourceId) payload.resource_ids = [workspace.activeResourceId]
    payload.segment_group_keys = [activeChapterKey.value]
  } else if (scope.value === 'selected' && selectedSegmentCount.value > 0) {
    payload.segment_ids = [...props.targetSegmentIds]
  } else if (scope.value === 'resource' && workspace.activeResourceId) {
    payload.resource_ids = [workspace.activeResourceId]
  }
  return payload
}

const handleSubmit = (): void => {
  if (!canSubmit.value || !props.projectId) return

  dialog.warning({
    title: t('workspace.qaRecheck.confirmTitle'),
    content: t('workspace.qaRecheck.confirmContent', {
      profile: selectedProfile.value?.name ?? '',
    }),
    positiveText: t('workspace.common.confirm'),
    negativeText: t('workspace.common.cancel'),
    onPositiveClick: () => {
      void submit()
    },
  })
}

const submit = async (): Promise<void> => {
  const payload = buildPayload()
  if (!props.projectId || !payload) return

  submitting.value = true
  errorMessage.value = null

  try {
    result.value = await qaRecheck(props.projectId, payload)
    message.success(t('workspace.qaRecheck.successToast'))
    emit('completed')
  } catch (error) {
    errorMessage.value = error instanceof Error ? error.message : t('api.errors.qaRecheckFailed')
  } finally {
    submitting.value = false
  }
}

const resourceResultLabel = (resource: QaRecheckResourceResult): string => {
  const name = resource.resource_name || `#${resource.resource_id}`
  return t('workspace.qaRecheck.resourceRow', {
    name,
    checked: resource.segments_checked,
    added: resource.issues_new,
    cleared: resource.issues_cleared,
  })
}

/** 忙碌资源仅有 resource_id，尽量映射为工作区已知资源名 */
const busyResourceLabel = (resourceId: number): string =>
  workspace.resources.find((resource) => resource.id === resourceId)?.path ?? `#${resourceId}`

const requestClose = (): void => {
  if (busy.value) return
  show.value = false
}
</script>

<template>
  <NDrawer
    :show="show"
    placement="right"
    :width="'min(520px, 100vw)'"
    :mask-closable="!busy"
    :close-on-esc="!busy"
    @update:show="(value: boolean) => (value ? (show = true) : requestClose())"
  >
    <NDrawerContent :title="t('workspace.qaRecheck.title')" closable @close="requestClose">
      <!-- 重检结果 -->
      <div v-if="result" class="space-y-4 pb-4">
        <NAlert type="success" :bordered="false">
          {{ t('workspace.qaRecheck.resultSummary', { profile: result.profile_name }) }}
        </NAlert>

        <div class="grid grid-cols-2 gap-3">
          <div class="rounded-lg bg-blue-50 p-4 text-center dark:bg-blue-500/10">
            <div class="text-2xl font-bold text-blue-600">{{ result.segments_checked }}</div>
            <div class="mt-1 text-xs text-blue-600/70">
              {{ t('workspace.qaRecheck.resultSegmentsChecked') }}
            </div>
          </div>
          <div class="rounded-lg bg-gray-50 p-4 text-center dark:bg-gray-500/10">
            <div class="text-2xl font-bold text-gray-600">{{ result.resources_checked }}</div>
            <div class="mt-1 text-xs text-gray-600/70">
              {{ t('workspace.qaRecheck.resultResourcesChecked') }}
            </div>
          </div>
          <div class="rounded-lg bg-red-50 p-4 text-center dark:bg-red-500/10">
            <div class="text-2xl font-bold text-red-600">{{ result.issues_new }}</div>
            <div class="mt-1 text-xs text-red-600/70">
              {{ t('workspace.qaRecheck.resultIssuesNew') }}
            </div>
          </div>
          <div class="rounded-lg bg-emerald-50 p-4 text-center dark:bg-emerald-500/10">
            <div class="text-2xl font-bold text-emerald-600">{{ result.issues_cleared }}</div>
            <div class="mt-1 text-xs text-emerald-600/70">
              {{ t('workspace.qaRecheck.resultIssuesCleared') }}
            </div>
          </div>
        </div>

        <div class="space-y-1.5 text-sm text-lf-text-muted">
          <div>
            {{
              t('workspace.qaRecheck.resultDispositionsInherited', {
                count: result.dispositions_inherited,
              })
            }}
          </div>
          <div v-if="result.segments_skipped_no_target > 0">
            {{
              t('workspace.qaRecheck.resultSkippedNoTarget', {
                count: result.segments_skipped_no_target,
              })
            }}
          </div>
          <div v-if="result.segments_skipped_concurrent > 0">
            {{
              t('workspace.qaRecheck.resultSkippedConcurrent', {
                count: result.segments_skipped_concurrent,
              })
            }}
          </div>
        </div>

        <NAlert v-if="result.resources_skipped_busy.length > 0" type="warning" :bordered="false">
          <template #header>
            {{ t('workspace.qaRecheck.resultSkippedBusyTitle') }}
          </template>
          <div class="space-y-1 text-sm">
            <div
              v-for="busyResource in result.resources_skipped_busy"
              :key="busyResource.resource_id"
            >
              {{
                t('workspace.qaRecheck.resultSkippedBusyItem', {
                  name: busyResourceLabel(busyResource.resource_id),
                  jobId: busyResource.active_job_id,
                })
              }}
            </div>
          </div>
        </NAlert>

        <div v-if="result.resources.length > 0" class="space-y-1.5">
          <div class="text-xs font-medium tracking-wide text-lf-text-subtle uppercase">
            {{ t('workspace.qaRecheck.resultResourceDetail') }}
          </div>
          <div
            v-for="resource in result.resources"
            :key="resource.resource_id"
            class="rounded-lg border border-lf-border-soft bg-lf-surface-muted/50 px-3 py-2 text-sm text-lf-text-muted"
          >
            {{ resourceResultLabel(resource) }}
          </div>
        </div>
      </div>

      <!-- 重检配置表单 -->
      <div v-else class="space-y-4 pb-4">
        <NAlert type="info" :bordered="false">
          {{ t('workspace.qaRecheck.intro') }}
        </NAlert>

        <!-- 固定目标摘要（由资源/章节/段落选择胶囊触发时） -->
        <NAlert v-if="isFixedTarget" type="success" :bordered="false">
          <template #header>
            {{ t('workspace.qaRecheck.targetLabel') }}
          </template>
          {{ targetSummary }}
        </NAlert>

        <NAlert v-if="profilesStore.error" type="error" :bordered="false">
          {{ profilesStore.error }}
        </NAlert>

        <NEmpty
          v-if="!profilesStore.loading && profileOptions.length === 0"
          :description="t('workspace.qaRecheck.noProfiles')"
        />

        <template v-else>
          <NFormItem :label="t('workspace.qaRecheck.profileLabel')" :show-feedback="false">
            <div class="w-full space-y-1.5">
              <NSelect
                v-model:value="profileId"
                :options="profileOptions"
                :loading="profilesStore.loading"
                :placeholder="t('workspace.qaRecheck.profilePlaceholder')"
                filterable
              />
              <div class="text-xs text-lf-text-subtle">
                {{ t('workspace.qaRecheck.profileHint') }}
              </div>
            </div>
          </NFormItem>

          <!-- 范围单选仅 project 模式显示；固定目标模式范围已由入口决定 -->
          <NFormItem
            v-if="!isFixedTarget"
            :label="t('workspace.qaRecheck.scopeLabel')"
            :show-feedback="false"
          >
            <div class="w-full space-y-2">
              <NRadioGroup v-model:value="scope">
                <div class="flex flex-col gap-2">
                  <NRadio
                    v-for="option in scopeOptions"
                    :key="option.value"
                    :value="option.value"
                    :disabled="option.disabled"
                  >
                    {{ option.label }}
                  </NRadio>
                </div>
              </NRadioGroup>
              <div class="text-xs text-lf-text-subtle">{{ scopeHint }}</div>
            </div>
          </NFormItem>
        </template>

        <NAlert v-if="errorMessage" type="error" :bordered="false">
          {{ errorMessage }}
        </NAlert>
      </div>

      <template #footer>
        <div v-if="result" class="flex justify-end">
          <NButton type="primary" @click="requestClose">
            {{ t('workspace.common.confirm') }}
          </NButton>
        </div>
        <div v-else class="flex justify-end gap-3">
          <NButton :disabled="busy" @click="requestClose">
            {{ t('workspace.common.cancel') }}
          </NButton>
          <NButton
            type="primary"
            :disabled="!canSubmit"
            :loading="submitting"
            @click="handleSubmit"
          >
            {{ t('workspace.qaRecheck.submit') }}
          </NButton>
        </div>
      </template>
    </NDrawerContent>
  </NDrawer>
</template>
