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

import { useOrganizationScope } from '@/composables/useOrganizationScope'
import OrganizationScopeSelect from '@/components/organizations/OrganizationScopeSelect.vue'
import CopyToOrganization from '@/components/organizations/CopyToOrganization.vue'
import { isOrganizationDependency, onOrganizationInvalidated } from '@/utils/organization-scope'
import { clearUnavailablePlanDependencies } from '@/utils/organization-copy'
import { captureSession, isSessionCurrent, sessionGeneration } from '@/api/session-context'
import { fetchBackends } from '@/api/backends'
import { fetchPromptTemplates } from '@/api/prompt-templates'
import { fetchBootstrapPromptTemplates } from '@/api/bootstrap-prompt-templates'
import { fetchExecutionProfiles } from '@/api/execution-profiles'
import type { ApiSchemas } from '@/api/client'
import ExecutionPlanEditor from '@/components/templates/ExecutionPlanEditor.vue'
import {
  buildExecutionRoundInput,
  buildRubyRetryInput,
  validateRoundCodes,
} from '@/utils/execution-plan-config'
import type {
  ExecutionPlanFormRound,
  ExecutionPlanFormRubyRetry,
} from '@/utils/execution-plan-config'
import ScopeFilterTabs from '@/components/common/ScopeFilterTabs.vue'
import { useEntityCrud } from '@/composables/useEntityCrud'
import { useStoreErrorToast } from '@/composables/useStoreErrorToast'
import { useExecutionPlanTemplatesStore } from '@/stores/executionPlanTemplates'
import { formatDateTime } from '@/utils/datetime'
import { DRAWER_WIDTH } from '@/components/common/uiConstants'

type ExecutionPlanTemplate = ApiSchemas['ExecutionPlanTemplate']
type ExecutionRoundConfig = ApiSchemas['ExecutionRoundConfig']
type CreateRequest = ApiSchemas['CreateExecutionPlanTemplateRequest']
type UpdateRequest = ApiSchemas['UpdateExecutionPlanTemplateRequest']

interface FormModel {
  name: string
  description: string
  profile_id: number | null
  ruby_retry: ExecutionPlanFormRubyRetry
  rounds: ExecutionPlanFormRound[]
}

// ── 默认值 ────────────────────────────────────────────────────

const DEFAULT_ROUND: ExecutionPlanFormRound = {
  mode: 'translate',
  backend_id: null,
  concurrency: 3,
  translate: {
    prompt_template_id: null,
    batch_size: 10,
    max_words_per_batch: 0,
    fallback_shrink: 1,
    retry: { max_attempts: 3, backoff_ms: 2000, jitter: true },
  },
}

const DEFAULT_RUBY_RETRY: ExecutionPlanFormRubyRetry = {
  enabled: false,
  backend_id: null,
  max_attempts: 1,
}

function deepClone<T>(obj: T): T {
  return JSON.parse(JSON.stringify(obj))
}

// ── Store & 依赖 ──────────────────────────────────────────────

const store = useExecutionPlanTemplatesStore()
const { orgId, canWrite, setScope } = useOrganizationScope((id) => {
  store.setOrganization(id)
  void store.loadTemplates(id)
})
const dependencyOrgId = computed(() => editingItem.value?.owner_org_id ?? orgId.value)
const availableBackends = ref<ApiSchemas['Backend'][]>([])
const availablePrompts = ref<ApiSchemas['TranslationPromptTemplate'][]>([])
const availableBootstrap = ref<ApiSchemas['BootstrapPromptTemplate'][]>([])
const availableProfiles = ref<ApiSchemas['ExecutionProfile'][]>([])
let dependencyGeneration = 0
const message = useMessage()
const { t } = useI18n()

const { getScopeTagType, deleteModalVisible, deletingItem, confirmDelete, executeDelete } =
  useEntityCrud<ExecutionPlanTemplate>({
    i18nPrefix: 'executionPlanTemplates',
    deleteItem: store.deleteTemplate,
    isDeleting: (id) => store.deletingIds.includes(id),
  })

// ── 表单状态 ──────────────────────────────────────────────────

const formRef = ref<FormInst | null>(null)
const drawerVisible = ref(false)
const submitting = ref(false)
const pendingFormWrites = ref(0)
let formGeneration = 0
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
  availableBackends.value.map((b) => ({ label: b.name, value: b.id })),
)

const promptTemplateOptions = computed<SelectOption[]>(() =>
  availablePrompts.value.map((t) => ({ label: t.name, value: t.id })),
)

const bootstrapPromptTemplateOptions = computed<SelectOption[]>(() =>
  availableBootstrap.value.map((t) => ({ label: t.name, value: t.id })),
)

const executionProfileOptions = computed<SelectOption[]>(() =>
  availableProfiles.value.map((p) => ({ label: p.name, value: p.id })),
)

const profileNameById = computed(() => new Map(availableProfiles.value.map((p) => [p.id, p.name])))

// ── 计算属性 ──────────────────────────────────────────────────

const hasActiveFilters = computed(
  () => store.searchQuery.trim().length > 0 || store.scopeFilter !== 'all',
)

const filterTabs = computed(() => [
  { name: 'all', label: t('executionPlanTemplates.filters.all'), count: store.totalCount },
  { name: 'system', label: t('executionPlanTemplates.scopes.system'), count: store.systemCount },
  { name: 'user', label: t('executionPlanTemplates.scopes.user'), count: store.userCount },
  { name: 'org', label: t('team.organization'), count: store.orgCount },
])

const isEditMode = computed(() => Boolean(editingItem.value))
const isSystemScope = computed(() => !store.canEdit(editingItem.value ?? undefined))
const drawerTitle = computed(() =>
  isSystemScope.value
    ? t('executionPlanTemplates.actions.viewTitle')
    : isEditMode.value
      ? t('executionPlanTemplates.actions.editTitle')
      : t('executionPlanTemplates.actions.createTitle'),
)
const drawerSubtitle = computed(() => {
  if (!editingItem.value) return t('executionPlanTemplates.form.createHint')
  return isSystemScope.value
    ? `${t('executionPlanTemplates.scopes.system')} · ${editingItem.value.name}`
    : editingItem.value.name
})

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

const ensureDependenciesLoaded = async (): Promise<void> => {
  const scope = dependencyOrgId.value
  const request = ++dependencyGeneration
  const session = captureSession()
  dependenciesLoaded.value = false
  availableBackends.value = []
  availablePrompts.value = []
  availableBootstrap.value = []
  availableProfiles.value = []
  try {
    const [backends, prompts, bootstrap, profiles] = await Promise.all([
      fetchBackends(undefined, scope ?? undefined),
      fetchPromptTemplates(),
      fetchBootstrapPromptTemplates(),
      fetchExecutionProfiles(),
    ])
    if (request !== dependencyGeneration || !isSessionCurrent(session)) return
    availableBackends.value = backends.items.filter((item) => isOrganizationDependency(item, scope))
    availablePrompts.value = prompts.items.filter((item) => isOrganizationDependency(item, scope))
    availableBootstrap.value = bootstrap.items.filter((item) =>
      isOrganizationDependency(item, scope),
    )
    availableProfiles.value = profiles.items.filter((item) => isOrganizationDependency(item, scope))
    dependenciesLoaded.value = true
  } catch (cause) {
    if (request === dependencyGeneration && isSessionCurrent(session))
      message.error(cause instanceof Error ? cause.message : t('team.errors.loadResource'))
  }
}

const resetForm = (): void => {
  formGeneration++
  submitting.value = false
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
  resetForm()
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
    const codeError = validateRoundCodes(round)
    if (codeError) {
      message.error(
        codeError === 'required'
          ? t('configurationProfiles.codesRequired')
          : t('configurationProfiles.invalidCodes'),
      )
      return false
    }
    if (round.mode !== 'correct' && !round.backend_id) {
      message.error(t('executionPlanTemplates.validation.roundBackendRequired', { n: i + 1 }))
      return false
    }
    if (
      round.mode === 'translate' &&
      round.translate &&
      round.translate.prompt_template_id == null
    ) {
      message.error(t('executionPlanTemplates.validation.roundPromptRequired', { n: i + 1 }))
      return false
    }
    if (round.mode === 'extract' && round.extract && round.extract.template_id == null) {
      message.error(
        t('executionPlanTemplates.validation.roundExtractTemplateRequired', { n: i + 1 }),
      )
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
    rounds: formModel.rounds.map(buildExecutionRoundInput),
  }
  if (formModel.description.trim()) {
    payload.description = formModel.description.trim()
  }
  payload.ruby_retry = buildRubyRetryInput(formModel.ruby_retry)
  return payload
}

const onSubmit = async (): Promise<void> => {
  const session = captureSession()
  const organization = store.orgId
  const generation = formGeneration
  const current = () =>
    isSessionCurrent(session) &&
    organization === store.orgId &&
    generation === formGeneration &&
    drawerVisible.value
  if (!store.canEdit(editingItem.value ?? undefined) || submitting.value) return
  // 只拦截"已选择但当前组织不可用"的依赖；未选择（null）由表单/轮次必填校验负责提示
  if (
    !dependenciesLoaded.value ||
    (formModel.profile_id != null &&
      !availableProfiles.value.some((item) => item.id === formModel.profile_id)) ||
    formModel.rounds.some(
      (round) =>
        (round.mode !== 'correct' &&
          round.backend_id != null &&
          !availableBackends.value.some((item) => item.id === round.backend_id)) ||
        (round.translate?.prompt_template_id != null &&
          !availablePrompts.value.some(
            (item) => item.id === round.translate?.prompt_template_id,
          )) ||
        (round.extract?.template_id != null &&
          !availableBootstrap.value.some((item) => item.id === round.extract?.template_id)),
    ) ||
    (formModel.ruby_retry.backend_id != null &&
      !availableBackends.value.some((item) => item.id === formModel.ruby_retry.backend_id))
  ) {
    message.error(t('team.errors.dependencies'))
    return
  }
  try {
    await formRef.value?.validate()
  } catch {
    return
  }

  if (!validateRounds()) return

  if (!current()) return
  const payload = buildPayload()
  submitting.value = true
  pendingFormWrites.value++

  try {
    if (isEditMode.value && editingItem.value) {
      await store.updateTemplate(editingItem.value.id, payload as UpdateRequest)
      if (!current()) return
      message.success(t('executionPlanTemplates.messages.updateSuccess'))
    } else {
      await store.createTemplate(payload)
      if (!current()) return
      message.success(t('executionPlanTemplates.messages.createSuccess'))
    }
    drawerVisible.value = false
    resetForm()
  } catch (cause) {
    // Consume the shared store error before releasing the suppression guard.
    // A closed/replaced draft must not receive the old write's failure toast.
    if (isSessionCurrent(session) && organization === store.orgId) store.error = null
    if (current()) {
      message.error(cause instanceof Error ? cause.message : t('team.errors.saveResource'), {
        duration: 0,
        closable: true,
      })
    }
  } finally {
    pendingFormWrites.value--
    if (current()) submitting.value = false
  }
}

const cardDate = (item: ExecutionPlanTemplate): string => {
  const value = item.updated_at ?? item.created_at
  return value ? formatDateTime(value, { dateStyle: 'short' }) : '—'
}

const cardDateTitle = (item: ExecutionPlanTemplate): string => {
  const value = item.updated_at ?? item.created_at
  return value ? formatDateTime(value, { dateStyle: 'medium', timeStyle: 'short' }) : ''
}

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
  void ensureDependenciesLoaded()
})
watch(
  sessionGeneration,
  () => {
    drawerVisible.value = false
    deleteModalVisible.value = false
    resetForm()
  },
  { flush: 'sync' },
)
onBeforeUnmount(() => {
  formGeneration++
})
watch(drawerVisible, (visible) => {
  if (!visible) {
    formGeneration++
    submitting.value = false
  }
})
watch(orgId, () => {
  resetForm()
  drawerVisible.value = false
  deleteModalVisible.value = false
  editingItem.value = null
  void ensureDependenciesLoaded()
})
onUnmounted(() => {
  ++dependencyGeneration
})
onOrganizationInvalidated((id) => {
  if (id === orgId.value || id === editingItem.value?.owner_org_id) {
    ++dependencyGeneration
    drawerVisible.value = false
    deleteModalVisible.value = false
    resetForm()
    availableBackends.value = []
    availablePrompts.value = []
    availableBootstrap.value = []
    availableProfiles.value = []
    dependenciesLoaded.value = false
  }
})
const copyToOrganization = async (item: ExecutionPlanTemplate, target: number) => {
  const session = captureSession()
  await setScope(target)
  if (!isSessionCurrent(session) || orgId.value !== target) return
  store.setOrganization(target)
  openEditDrawer(item)
  editingItem.value = null
  await ensureDependenciesLoaded()
  if (!isSessionCurrent(session) || orgId.value !== target || !drawerVisible.value) return
  Object.assign(
    formModel,
    clearUnavailablePlanDependencies(formModel, {
      profiles: availableProfiles.value,
      backends: availableBackends.value,
      prompts: availablePrompts.value,
      bootstrap: availableBootstrap.value,
    }),
  )
  message.info(t('team.copyDependencies'))
}

useStoreErrorToast(
  () => (pendingFormWrites.value > 0 ? null : store.error),
  () => {
    store.error = null
  },
)
</script>

<template>
  <EntityListPage
    class="lf-content-narrow"
    :title="t('executionPlanTemplates.title')"
    :subtitle="t('executionPlanTemplates.subtitle')"
    :loading="store.loading"
    :empty="store.filteredItems.length === 0"
    :empty-description="
      hasActiveFilters
        ? t('executionPlanTemplates.empty.filtered')
        : t('executionPlanTemplates.empty.default')
    "
  >
    <template #actions>
      <NButton secondary :loading="store.loading" @click="store.loadTemplates(orgId)">
        {{ t('common.actions.refresh') }}
      </NButton>
      <NButton v-if="canWrite" type="primary" @click="openCreateDrawer">
        {{ t('executionPlanTemplates.actions.create') }}
      </NButton>
    </template>

    <template #filters>
      <OrganizationScopeSelect :value="orgId" @update:value="setScope" />
      <ScopeFilterTabs
        :tabs="filterTabs"
        :value="store.scopeFilter"
        @update:value="
          (v: string) => (store.scopeFilter = v as ExecutionPlanTemplate['scope'] | 'all')
        "
      />
      <NInput
        v-model:value="store.searchQuery"
        clearable
        class="lg:max-w-sm!"
        :placeholder="t('executionPlanTemplates.filters.searchPlaceholder')"
      />
    </template>

    <template #empty-extra>
      <NButton v-if="hasActiveFilters" secondary @click="store.resetFilters()">
        {{ t('executionPlanTemplates.filters.reset') }}
      </NButton>
      <NButton v-else-if="canWrite" type="primary" @click="openCreateDrawer">
        {{ t('executionPlanTemplates.actions.createFirst') }}
      </NButton>
    </template>

    <!-- 卡片网格 -->
    <div class="grid grid-cols-1 gap-5 md:grid-cols-2 xl:grid-cols-3">
      <div
        v-for="item in store.filteredItems"
        :key="item.id"
        class="lf-interactive-card flex h-full cursor-pointer flex-col gap-4 p-5"
        @click="openEditDrawer(item)"
      >
        <!-- 头部：名称 + 编号 + 作用域标签 -->
        <div class="flex items-start justify-between gap-4">
          <div class="min-w-0">
            <h2
              class="truncate text-lg font-semibold tracking-tight text-lf-text-strong"
              :title="item.name"
            >
              {{ item.name }}
            </h2>
            <p class="mt-1 font-mono text-xs text-lf-text-subtle">#{{ item.id }}</p>
          </div>
          <NTag round size="small" :bordered="false" :type="getScopeTagType(item.scope)">
            {{
              item.scope === 'org'
                ? t('team.organization')
                : t(`executionPlanTemplates.scopes.${item.scope}`)
            }}
          </NTag>
        </div>

        <!-- 描述 -->
        <p
          class="line-clamp-2 text-sm leading-6"
          :class="item.description ? 'text-lf-text-muted' : 'text-lf-text-subtle'"
        >
          {{ item.description || t('executionPlanTemplates.card.noDescription') }}
        </p>

        <!-- 策略信息行 -->
        <div class="flex items-baseline gap-3">
          <span class="w-14 shrink-0 text-xs text-lf-text-subtle">
            {{ t('executionPlanTemplates.card.profile') }}
          </span>
          <span
            class="min-w-0 flex-1 truncate text-[13px] text-lf-text"
            :title="profileNameById.get(item.profile_id) ?? ''"
          >
            {{ profileNameById.get(item.profile_id) ?? '—' }}
          </span>
        </div>

        <!-- 轮次徽章 -->
        <div v-if="item.rounds?.length" class="flex flex-wrap gap-1.5">
          <span
            v-for="(round, idx) in item.rounds"
            :key="idx"
            class="inline-flex items-center gap-1.5 rounded-full py-0.5 pl-0.5 pr-2.5 text-xs font-medium"
            :class="modeBadgeClass(round.mode)"
          >
            <span
              class="inline-flex h-5 w-5 items-center justify-center rounded-full bg-lf-surface text-[11px] font-bold"
            >
              {{ idx + 1 }}
            </span>
            {{ modeLabel(round.mode) }}
          </span>
        </div>

        <!-- 底部：更新时间 + 操作 -->
        <div class="mt-auto border-t border-lf-border-soft pt-4">
          <div class="flex items-center justify-between gap-3">
            <span class="text-xs text-lf-text-subtle" :title="cardDateTitle(item)">
              {{ t('executionPlanTemplates.card.updatedAt') }} {{ cardDate(item) }}
            </span>
            <div class="flex items-center gap-2" @click.stop>
              <CopyToOrganization @copy="(target) => copyToOrganization(item, target)" />
              <template v-if="store.canEdit(item)">
                <NButton text type="primary" class="font-medium" @click="openEditDrawer(item)">
                  {{ t('common.actions.edit') }}
                </NButton>
                <NButton text type="error" class="font-medium" @click="confirmDelete(item)">
                  {{ t('common.actions.delete') }}
                </NButton>
              </template>
              <NButton v-else text type="info" class="font-medium" @click="openEditDrawer(item)">
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
        <DrawerHeader :title="drawerTitle" :subtitle="drawerSubtitle" />
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
            :disabled="isSystemScope || submitting"
          />
        </NFormItem>

        <NFormItem :label="t('executionPlanTemplates.form.description')" path="description">
          <NInput
            v-model:value="formModel.description"
            type="textarea"
            :placeholder="t('executionPlanTemplates.form.descriptionPlaceholder')"
            :rows="3"
            :disabled="isSystemScope || submitting"
          />
        </NFormItem>

        <NFormItem :label="t('executionPlanTemplates.form.profile')" path="profile_id">
          <div class="w-full">
            <!-- 说明置于控件上方，让校验错误紧贴控件（NFormItem 的反馈区渲染在默认插槽之后） -->
            <div class="mb-1 text-xs text-lf-text-subtle">
              {{ t('executionPlanTemplates.form.profileHint') }}
            </div>
            <NSelect
              v-model:value="formModel.profile_id"
              :options="executionProfileOptions"
              :placeholder="t('executionPlanTemplates.form.profilePlaceholder')"
              :disabled="isSystemScope || submitting"
            />
          </div>
        </NFormItem>

        <!-- 轮次编辑器 -->
        <div class="mb-4">
          <span class="mb-2 block text-sm font-medium text-lf-text-strong">
            {{ t('executionPlanTemplates.form.rounds') }}
          </span>
          <p class="mb-4 text-xs text-lf-text-subtle">{{ t('configurationProfiles.effect') }}</p>
          <ExecutionPlanEditor
            :key="formGeneration"
            :rounds="formModel.rounds"
            :ruby-retry="formModel.ruby_retry"
            :backends="backendOptions"
            :prompt-templates="promptTemplateOptions"
            :bootstrap-prompt-templates="bootstrapPromptTemplateOptions"
            :disabled="isSystemScope || submitting"
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
            :loading="submitting"
            :disabled="
              submitting ||
              !dependenciesLoaded ||
              formModel.rounds.some((round) => Boolean(validateRoundCodes(round)))
            "
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
