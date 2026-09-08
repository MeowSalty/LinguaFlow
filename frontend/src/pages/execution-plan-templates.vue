<script setup lang="ts">
import {
  NButton,
  NDrawer,
  NDrawerContent,
  NForm,
  NFormItem,
  NInput,
  NModal,
  NSelect,
  NTag,
  useMessage,
  type FormInst,
  type FormRules,
  type SelectOption,
} from 'naive-ui'
import { useI18n } from 'vue-i18n'

import type { ApiSchemas } from '@/api/client'
import ExecutionPlanEditor from '@/components/templates/ExecutionPlanEditor.vue'
import { useEntityCrud } from '@/composables/useEntityCrud'
import { useStoreErrorToast } from '@/composables/useStoreErrorToast'
import { useBackendsStore } from '@/stores/backends'
import { useBootstrapPromptTemplatesStore } from '@/stores/bootstrapPromptTemplates'
import { useExecutionPlanTemplatesStore } from '@/stores/executionPlanTemplates'
import { usePromptTemplatesStore } from '@/stores/promptTemplates'
import { useExecutionProfilesStore } from '@/stores/executionProfiles'
import { formatDateTime } from '@/utils/datetime'
import { DRAWER_WIDTH } from '@/components/common/uiConstants'

type ExecutionPlanTemplate = ApiSchemas['ExecutionPlanTemplate']
type ExecutionRoundConfig = ApiSchemas['ExecutionRoundConfig']
type ExecutionPlanRubyRetryConfig = ApiSchemas['ExecutionPlanRubyRetryConfig']
type CreateRequest = ApiSchemas['CreateExecutionPlanTemplateRequest']
type UpdateRequest = ApiSchemas['UpdateExecutionPlanTemplateRequest']

interface FormModel {
  name: string
  description: string
  profile_id: number | null
  ruby_retry: ExecutionPlanRubyRetryConfig
  rounds: ExecutionRoundConfig[]
}

// ── 默认值 ────────────────────────────────────────────────────

const DEFAULT_ROUND: ExecutionRoundConfig = {
  mode: 'translate',
  backend_id: 0,
  concurrency: 3,
  translate: {
    prompt_template_id: 0,
    batch_size: 10,
    max_words_per_batch: 0,
    fallback_shrink: 1,
    retry: { max_attempts: 3, backoff_ms: 2000, jitter: true },
  },
}

const DEFAULT_RUBY_RETRY: ExecutionPlanRubyRetryConfig = {
  enabled: false,
  backend_id: 0,
  max_attempts: 1,
}

function deepClone<T>(obj: T): T {
  return JSON.parse(JSON.stringify(obj))
}

// ── Store & 依赖 ──────────────────────────────────────────────

const store = useExecutionPlanTemplatesStore()
const backendsStore = useBackendsStore()
const promptTemplatesStore = usePromptTemplatesStore()
const bootstrapPromptTemplatesStore = useBootstrapPromptTemplatesStore()
const executionProfilesStore = useExecutionProfilesStore()
const message = useMessage()
const { t } = useI18n()

const {
  filterScopeOptions,
  getScopeTagType,
  deleteModalVisible,
  deletingItem,
  confirmDelete,
  executeDelete,
} = useEntityCrud<ExecutionPlanTemplate>({
  i18nPrefix: 'executionPlanTemplates',
  deleteItem: store.deleteTemplate,
  isDeleting: (id) => store.deletingIds.includes(id),
})

// ── 表单状态 ──────────────────────────────────────────────────

const formRef = ref<FormInst | null>(null)
const drawerVisible = ref(false)
const editingItem = ref<ExecutionPlanTemplate | null>(null)

const formModel = reactive<FormModel>({
  name: '',
  description: '',
  profile_id: null,
  ruby_retry: deepClone(DEFAULT_RUBY_RETRY),
  rounds: [],
})

// ── 依赖选项（供 ExecutionPlanEditor 使用） ────────────────────

const backendOptions = computed<SelectOption[]>(() =>
  backendsStore.items.map((b) => ({ label: b.name, value: b.id })),
)

const promptTemplateOptions = computed<SelectOption[]>(() =>
  promptTemplatesStore.items.map((t) => ({ label: t.name, value: t.id })),
)

const bootstrapPromptTemplateOptions = computed<SelectOption[]>(() =>
  bootstrapPromptTemplatesStore.items.map((t) => ({ label: t.name, value: t.id })),
)

const executionProfileOptions = computed<SelectOption[]>(() =>
  executionProfilesStore.items.map((p) => ({ label: p.name, value: p.id })),
)

const profileNameById = computed(
  () => new Map(executionProfilesStore.items.map((p) => [p.id, p.name])),
)

// ── 计算属性 ──────────────────────────────────────────────────

const hasActiveFilters = computed(
  () => store.searchQuery.trim().length > 0 || store.scopeFilter !== 'all',
)

const metrics = computed(() => [
  { label: t('executionPlanTemplates.stats.total'), value: store.totalCount },
  { label: t('executionPlanTemplates.stats.system'), value: store.systemCount },
  { label: t('executionPlanTemplates.stats.user'), value: store.userCount },
  { label: t('executionPlanTemplates.stats.avgRounds'), value: store.avgRoundsPerPlan },
])

const isEditMode = computed(() => Boolean(editingItem.value))
const isSystemScope = computed(() => editingItem.value?.scope === 'system')
const drawerTitle = computed(() =>
  isEditMode.value ? t('common.actions.edit') : t('executionPlanTemplates.actions.create'),
)

const rules = computed<FormRules>(() => ({
  name: [
    {
      required: true,
      message: t('executionPlanTemplates.validation.nameRequired'),
      trigger: ['input', 'blur'],
    },
  ],
  profile_id: [
    {
      required: true,
      type: 'number',
      message: t('executionPlanTemplates.validation.profileRequired'),
      trigger: ['change', 'blur'],
    },
  ],
}))

// ── 方法 ──────────────────────────────────────────────────────

// 抽屉依赖（后端/提示词/引导提示词）按需加载，首次打开抽屉时并行拉取；
// 执行策略随首屏加载（卡片标签依赖），不在此列
const dependenciesLoaded = ref(false)

const ensureDependenciesLoaded = (): void => {
  if (dependenciesLoaded.value) return
  dependenciesLoaded.value = true
  void backendsStore.loadBackends()
  void promptTemplatesStore.loadTemplates()
  void bootstrapPromptTemplatesStore.loadTemplates()
}

const resetForm = (): void => {
  formModel.name = ''
  formModel.description = ''
  formModel.profile_id = null
  formModel.ruby_retry = deepClone(DEFAULT_RUBY_RETRY)
  formModel.rounds = [deepClone(DEFAULT_ROUND)]
  editingItem.value = null
}

const openCreateDrawer = (): void => {
  resetForm()
  ensureDependenciesLoaded()
  drawerVisible.value = true
}

const openEditDrawer = (item: ExecutionPlanTemplate): void => {
  editingItem.value = item
  formModel.name = item.name
  formModel.description = item.description ?? ''
  formModel.profile_id = item.profile_id ?? null
  formModel.ruby_retry = item.ruby_retry
    ? deepClone(item.ruby_retry)
    : deepClone(DEFAULT_RUBY_RETRY)
  formModel.rounds = item.rounds?.length ? deepClone(item.rounds) : [deepClone(DEFAULT_ROUND)]
  ensureDependenciesLoaded()
  drawerVisible.value = true
}

const validateRounds = (): boolean => {
  for (let i = 0; i < formModel.rounds.length; i++) {
    const round = formModel.rounds[i]!
    if (round.mode !== 'correct' && !round.backend_id) {
      message.error(t('executionPlanTemplates.validation.roundBackendRequired', { n: i + 1 }))
      return false
    }
    if (round.mode !== 'correct' && (!round.concurrency || round.concurrency < 1)) {
      message.error(t('executionPlanTemplates.validation.roundConcurrencyRequired', { n: i + 1 }))
      return false
    }
    if (round.mode === 'translate' && round.translate) {
      const hasBatchSize = round.translate.batch_size && round.translate.batch_size > 0
      const hasMaxWords =
        round.translate.max_words_per_batch && round.translate.max_words_per_batch > 0
      if (!hasBatchSize && !hasMaxWords) {
        message.error(t('executionPlanTemplates.validation.roundBatchConfigRequired', { n: i + 1 }))
        return false
      }
      if (
        round.translate.fallback_shrink == null ||
        round.translate.fallback_shrink <= 0 ||
        round.translate.fallback_shrink > 1
      ) {
        message.error(
          t('executionPlanTemplates.validation.roundFallbackShrinkRequired', { n: i + 1 }),
        )
        return false
      }
    }
    if (round.mode === 'adjudicate' && round.adjudicate) {
      const hasBatchSize = round.adjudicate.batch_size && round.adjudicate.batch_size > 0
      const hasMaxWords =
        round.adjudicate.max_words_per_batch && round.adjudicate.max_words_per_batch > 0
      if (!hasBatchSize && !hasMaxWords) {
        message.error(t('executionPlanTemplates.validation.roundBatchConfigRequired', { n: i + 1 }))
        return false
      }
    }
    if (round.mode === 'semantic_qa' && round.semantic_qa) {
      const hasBatchSize = round.semantic_qa.batch_size && round.semantic_qa.batch_size > 0
      const hasMaxWords =
        round.semantic_qa.max_words_per_batch && round.semantic_qa.max_words_per_batch > 0
      if (!hasBatchSize && !hasMaxWords) {
        message.error(t('executionPlanTemplates.validation.roundBatchConfigRequired', { n: i + 1 }))
        return false
      }
      if (
        round.semantic_qa.segment_scope === 'with_issue_codes' &&
        (!round.semantic_qa.issue_codes || round.semantic_qa.issue_codes.length === 0)
      ) {
        message.error(
          t('executionPlanTemplates.validation.roundSemanticQAIssueCodesRequired', { n: i + 1 }),
        )
        return false
      }
    }
    if (round.mode === 'revise' && round.revise) {
      const hasBatchSize = round.revise.batch_size && round.revise.batch_size > 0
      const hasMaxWords = round.revise.max_words_per_batch && round.revise.max_words_per_batch > 0
      if (!hasBatchSize && !hasMaxWords) {
        message.error(t('executionPlanTemplates.validation.roundBatchConfigRequired', { n: i + 1 }))
        return false
      }
      if (
        round.revise.segment_scope === 'with_issue_codes' &&
        (!round.revise.issue_codes || round.revise.issue_codes.length === 0)
      ) {
        message.error(
          t('executionPlanTemplates.validation.roundReviseIssueCodesRequired', { n: i + 1 }),
        )
        return false
      }
    }
    if (round.mode === 'correct' && round.correct) {
      const hasEnabledRule = round.correct.rules.some((r) => r.enabled)
      if (!hasEnabledRule) {
        message.error(
          t('executionPlanTemplates.validation.roundCorrectRulesRequired', { n: i + 1 }),
        )
        return false
      }
    }
  }
  return true
}

const buildPayload = (): CreateRequest => {
  const payload: CreateRequest = {
    name: formModel.name.trim(),
    profile_id: formModel.profile_id!,
    rounds: formModel.rounds.map((round) => {
      const base: {
        mode: typeof round.mode
        concurrency: typeof round.concurrency
        backend_id?: typeof round.backend_id
      } = {
        mode: round.mode,
        concurrency: round.mode === 'correct' ? 1 : round.concurrency,
      }
      if (round.mode !== 'correct') {
        base.backend_id = round.backend_id
      }
      if (round.mode === 'translate' && round.translate) {
        return {
          ...base,
          translate: {
            prompt_template_id: round.translate.prompt_template_id,
            batch_size: round.translate.batch_size,
            max_words_per_batch: round.translate.max_words_per_batch,
            fallback_shrink: round.translate.fallback_shrink,
            ...(round.translate.retry ? { retry: round.translate.retry } : {}),
          },
        }
      }
      if (round.mode === 'extract' && round.extract) {
        return {
          ...base,
          extract: {
            template_id: round.extract.template_id,
            batch_size: round.extract.batch_size,
            max_words_per_batch: round.extract.max_words_per_batch,
            max_terms_per_1000_chars: round.extract.max_terms_per_1000_chars,
            min_source_len: round.extract.min_source_len,
            ...(round.extract.retry ? { retry: round.extract.retry } : {}),
          },
        }
      }
      if (round.mode === 'adjudicate' && round.adjudicate) {
        return {
          ...base,
          adjudicate: {
            batch_size: round.adjudicate.batch_size,
            max_words_per_batch: round.adjudicate.max_words_per_batch,
            adjudicate_codes:
              round.adjudicate.adjudicate_codes && round.adjudicate.adjudicate_codes.length > 0
                ? round.adjudicate.adjudicate_codes
                : undefined,
            ...(round.adjudicate.retry ? { retry: round.adjudicate.retry } : {}),
          },
        }
      }
      if (round.mode === 'semantic_qa' && round.semantic_qa) {
        const segmentScope = round.semantic_qa.segment_scope ?? 'all'
        return {
          ...base,
          semantic_qa: {
            batch_size: round.semantic_qa.batch_size,
            max_words_per_batch: round.semantic_qa.max_words_per_batch,
            segment_scope: segmentScope,
            ...(segmentScope === 'with_issue_codes' &&
            round.semantic_qa.issue_codes &&
            round.semantic_qa.issue_codes.length > 0
              ? { issue_codes: round.semantic_qa.issue_codes }
              : {}),
            ...(round.semantic_qa.retry ? { retry: round.semantic_qa.retry } : {}),
          },
        }
      }
      if (round.mode === 'revise' && round.revise) {
        const segmentScope = round.revise.segment_scope ?? 'with_issues'
        return {
          ...base,
          revise: {
            batch_size: round.revise.batch_size,
            max_words_per_batch: round.revise.max_words_per_batch,
            segment_scope: segmentScope,
            ...(segmentScope === 'with_issue_codes' &&
            round.revise.issue_codes &&
            round.revise.issue_codes.length > 0
              ? { issue_codes: round.revise.issue_codes }
              : {}),
            ...(round.revise.retry ? { retry: round.revise.retry } : {}),
          },
        }
      }
      if (round.mode === 'correct' && round.correct) {
        return {
          ...base,
          correct: {
            rules: round.correct.rules,
          },
        }
      }
      return base
    }),
  }
  if (formModel.description.trim()) {
    payload.description = formModel.description.trim()
  }
  payload.ruby_retry = deepClone(formModel.ruby_retry)
  return payload
}

const onSubmit = async (): Promise<void> => {
  try {
    await formRef.value?.validate()
  } catch {
    return
  }

  if (!validateRounds()) return

  const payload = buildPayload()

  try {
    if (isEditMode.value && editingItem.value) {
      await store.updateTemplate(editingItem.value.id, payload as UpdateRequest)
      message.success(t('executionPlanTemplates.messages.updateSuccess'))
    } else {
      await store.createTemplate(payload)
      message.success(t('executionPlanTemplates.messages.createSuccess'))
    }
    drawerVisible.value = false
    resetForm()
  } catch {
    // Error is handled by the store
  }
}

const formatDate = (dateStr: string | undefined): string =>
  dateStr ? formatDateTime(dateStr, { dateStyle: 'short' }) : '—'

const modeBadgeClass = (mode: ExecutionRoundConfig['mode']): string => {
  if (mode === 'translate') return 'bg-lf-brand-soft text-brand-600'
  if (mode === 'extract') return 'bg-lf-accent-amber-soft text-lf-accent-amber'
  if (mode === 'adjudicate') return 'bg-lf-accent-violet-soft text-lf-accent-violet'
  if (mode === 'semantic_qa') return 'bg-lf-accent-emerald-soft text-lf-accent-emerald'
  if (mode === 'revise') return 'bg-lf-accent-rose-soft text-lf-accent-rose'
  return 'bg-lf-accent-sky-soft text-lf-accent-sky'
}

const modeLabel = (mode: ExecutionRoundConfig['mode']): string => {
  if (mode === 'translate') return t('executionPlanEditor.round.modeTranslate')
  if (mode === 'extract') return t('executionPlanEditor.round.modeExtract')
  if (mode === 'adjudicate') return t('executionPlanEditor.round.modeAdjudicate')
  if (mode === 'semantic_qa') return t('executionPlanEditor.round.modeSemanticQA')
  if (mode === 'revise') return t('executionPlanEditor.round.modeRevise')
  return t('executionPlanEditor.round.modeCorrect')
}

// ── 生命周期 ──────────────────────────────────────────────────

onMounted(() => {
  store.loadTemplates()
  // 卡片上的执行策略名称标签依赖 profiles，需随首屏加载
  executionProfilesStore.loadProfiles()
})

useStoreErrorToast(
  () => store.error,
  () => {
    store.error = null
  },
)
</script>

<template>
  <EntityListPage
    :title="t('executionPlanTemplates.title')"
    :subtitle="t('executionPlanTemplates.subtitle')"
    :metrics="metrics"
    :loading="store.loading"
    :empty="store.filteredItems.length === 0"
    :empty-description="
      hasActiveFilters
        ? t('executionPlanTemplates.empty.filtered')
        : t('executionPlanTemplates.empty.default')
    "
  >
    <template #actions>
      <NButton secondary :loading="store.loading" @click="store.loadTemplates">
        {{ t('common.actions.refresh') }}
      </NButton>
      <NButton type="primary" @click="openCreateDrawer">
        {{ t('executionPlanTemplates.actions.create') }}
      </NButton>
    </template>

    <template #filters>
      <NInput
        v-model:value="store.searchQuery"
        clearable
        class="lg:max-w-sm!"
        :placeholder="t('executionPlanTemplates.filters.searchPlaceholder')"
      />
      <div class="flex flex-wrap gap-3">
        <NSelect v-model:value="store.scopeFilter" class="w-44!" :options="filterScopeOptions" />
        <NButton v-if="hasActiveFilters" quaternary @click="store.resetFilters()">
          {{ t('executionPlanTemplates.filters.reset') }}
        </NButton>
      </div>
    </template>

    <template #empty-extra>
      <NButton v-if="hasActiveFilters" secondary @click="store.resetFilters()">
        {{ t('executionPlanTemplates.filters.reset') }}
      </NButton>
      <NButton v-else type="primary" @click="openCreateDrawer">
        {{ t('executionPlanTemplates.actions.createFirst') }}
      </NButton>
    </template>

    <!-- 卡片网格 -->
    <div class="grid grid-cols-1 gap-5 md:grid-cols-2 xl:grid-cols-3">
      <div
        v-for="item in store.filteredItems"
        :key="item.id"
        class="lf-interactive-card group flex h-full flex-col gap-4 p-5"
      >
        <!-- 头部：名称 + 作用域标签 -->
        <div class="flex items-start justify-between gap-4">
          <div class="min-w-0">
            <h2 class="truncate text-lg font-semibold text-lf-text-strong">
              {{ item.name }}
            </h2>
          </div>
          <NTag round size="small" :type="getScopeTagType(item.scope)">
            {{ t(`executionPlanTemplates.scopes.${item.scope}`) }}
          </NTag>
        </div>

        <!-- 描述 -->
        <p
          class="line-clamp-2 text-sm leading-6 text-lf-text-muted"
          :class="{ 'italic text-lf-text-subtle': !item.description }"
        >
          {{ item.description || t('executionPlanTemplates.card.noDescription') }}
        </p>

        <!-- 轮次概览 -->
        <div class="space-y-2">
          <div class="flex items-center gap-2">
            <NTag size="small" type="info" :bordered="false">
              {{ item.rounds?.length ?? 0 }} {{ t('executionPlanTemplates.card.rounds') }}
            </NTag>
            <NTag
              v-if="profileNameById.get(item.profile_id)"
              size="small"
              :bordered="false"
              class="max-w-[160px]"
            >
              <span class="truncate">
                {{ t('executionPlanTemplates.card.profile') }}:
                {{ profileNameById.get(item.profile_id) }}
              </span>
            </NTag>
          </div>
          <div v-if="item.rounds?.length" class="space-y-1">
            <div
              v-for="(round, idx) in item.rounds.slice(0, 3)"
              :key="idx"
              class="flex items-center gap-2 text-xs text-lf-text-muted"
            >
              <span
                class="inline-flex h-5 w-5 items-center justify-center rounded-full text-[10px] font-semibold"
                :class="modeBadgeClass(round.mode)"
              >
                {{ idx + 1 }}
              </span>
              <span class="truncate">
                {{ modeLabel(round.mode) }}
              </span>
            </div>
            <div v-if="item.rounds.length > 3" class="text-xs text-lf-text-subtle">
              +{{ item.rounds.length - 3 }} {{ t('executionPlanTemplates.card.moreRounds') }}
            </div>
          </div>
        </div>

        <!-- 底部：时间 + 操作 -->
        <div class="mt-auto border-t border-lf-border-soft pt-4">
          <div class="flex items-center justify-between gap-3">
            <span class="text-xs text-lf-text-subtle">
              {{ t('executionPlanTemplates.card.createdAt') }} {{ formatDate(item.created_at) }}
            </span>
            <div class="flex items-center gap-2">
              <NButton
                v-if="item.scope !== 'system'"
                text
                type="primary"
                class="font-medium"
                @click="openEditDrawer(item)"
              >
                {{ t('common.actions.edit') }}
              </NButton>
              <NButton
                v-if="item.scope !== 'system'"
                text
                type="error"
                class="font-medium"
                @click="confirmDelete(item)"
              >
                {{ t('common.actions.delete') }}
              </NButton>
              <NButton
                v-if="item.scope === 'system'"
                text
                type="info"
                class="font-medium"
                @click="openEditDrawer(item)"
              >
                {{ t('common.actions.view') }}
              </NButton>
            </div>
          </div>
        </div>
      </div>
    </div>
  </EntityListPage>

  <!-- 创建/编辑抽屉 -->
  <NDrawer v-model:show="drawerVisible" :width="DRAWER_WIDTH.l" placement="right">
    <NDrawerContent :native-scrollbar="false">
      <template #header>
        <DrawerHeader :title="drawerTitle" />
      </template>

      <NForm
        ref="formRef"
        :model="formModel"
        :rules="rules"
        label-placement="top"
        require-mark-placement="right-hanging"
      >
        <NFormItem :label="t('executionPlanTemplates.form.name')" path="name">
          <NInput
            v-model:value="formModel.name"
            :placeholder="t('executionPlanTemplates.form.namePlaceholder')"
            :disabled="isSystemScope"
          />
        </NFormItem>

        <NFormItem :label="t('executionPlanTemplates.form.description')" path="description">
          <NInput
            v-model:value="formModel.description"
            type="textarea"
            :placeholder="t('executionPlanTemplates.form.descriptionPlaceholder')"
            :rows="3"
            :disabled="isSystemScope"
          />
        </NFormItem>

        <NFormItem :label="t('executionPlanTemplates.form.profile')" path="profile_id">
          <div class="w-full">
            <NSelect
              v-model:value="formModel.profile_id"
              :options="executionProfileOptions"
              :placeholder="t('executionPlanTemplates.form.profilePlaceholder')"
              :disabled="isSystemScope"
            />
            <div class="mt-1 text-[11px] leading-4 text-lf-text-subtle">
              {{ t('executionPlanTemplates.form.profileHint') }}
            </div>
          </div>
        </NFormItem>

        <!-- 轮次编辑器 -->
        <div class="mb-4">
          <span class="mb-2 block text-sm font-medium text-lf-text-strong">
            {{ t('executionPlanTemplates.form.rounds') }}
          </span>
          <ExecutionPlanEditor
            :rounds="formModel.rounds"
            :ruby-retry="formModel.ruby_retry"
            :backends="backendOptions"
            :prompt-templates="promptTemplateOptions"
            :bootstrap-prompt-templates="bootstrapPromptTemplateOptions"
            :disabled="isSystemScope"
            @update:rounds="formModel.rounds = $event"
            @update:ruby-retry="formModel.ruby_retry = $event"
          />
        </div>
      </NForm>

      <template #footer>
        <div class="flex justify-end gap-3">
          <NButton @click="drawerVisible = false">
            {{ t('common.cancel') }}
          </NButton>
          <NButton
            v-if="!isSystemScope"
            type="primary"
            :loading="store.creating || store.updating"
            @click="onSubmit"
          >
            {{
              isEditMode
                ? t('executionPlanTemplates.actions.submitUpdate')
                : t('executionPlanTemplates.actions.submitCreate')
            }}
          </NButton>
        </div>
      </template>
    </NDrawerContent>
  </NDrawer>

  <!-- 删除确认弹窗 -->
  <NModal
    v-model:show="deleteModalVisible"
    preset="dialog"
    type="warning"
    :title="t('common.actions.confirmDelete')"
    :content="
      deletingItem ? t('executionPlanTemplates.delete.confirm', { name: deletingItem.name }) : ''
    "
    :positive-text="t('common.actions.deleteConfirmAction')"
    :negative-text="t('common.cancel')"
    :loading="deletingItem ? store.deletingIds.includes(deletingItem.id) : false"
    @positive-click="executeDelete"
  />
</template>
