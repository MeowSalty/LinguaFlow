<script setup lang="ts">
import {
  NAlert,
  NButton,
  NCard,
  NDrawer,
  NDrawerContent,
  NEmpty,
  NForm,
  NFormItem,
  NInput,
  NModal,
  NSelect,
  NSkeleton,
  NTag,
  useMessage,
  type FormInst,
  type FormRules,
  type SelectOption,
} from 'naive-ui'
import { useI18n } from 'vue-i18n'

import type { ApiSchemas } from '@/api/client'
import ExecutionPlanEditor from '@/components/templates/ExecutionPlanEditor.vue'
import { useBackendsStore } from '@/stores/backends'
import { useExecutionPlanTemplatesStore } from '@/stores/executionPlanTemplates'
import { usePromptTemplatesStore } from '@/stores/promptTemplates'
import { useTranslationProfilesStore } from '@/stores/translationProfiles'

type ExecutionPlanTemplate = ApiSchemas['ExecutionPlanTemplate']
type ExecutionRoundConfig = ApiSchemas['ExecutionRoundConfig']
type ExecutionPlanBootstrapConfig = ApiSchemas['ExecutionPlanBootstrapConfig']
type ExecutionPlanRubyRetryConfig = ApiSchemas['ExecutionPlanRubyRetryConfig']
type CreateRequest = ApiSchemas['CreateExecutionPlanTemplateRequest']
type UpdateRequest = ApiSchemas['UpdateExecutionPlanTemplateRequest']
type Scope = ExecutionPlanTemplate['scope']

interface FormModel {
  name: string
  description: string
  bootstrap: ExecutionPlanBootstrapConfig
  ruby_retry: ExecutionPlanRubyRetryConfig
  rounds: ExecutionRoundConfig[]
}

// ── 默认值 ────────────────────────────────────────────────────

const DEFAULT_ROUND: ExecutionRoundConfig = {
  backend_id: 0,
  prompt_template_id: 0,
  profile_id: 0,
  batch_size: 10,
  concurrency: 3,
  fallback_shrink: 0,
  rate_limit_per_sec: 0,
  retry: { max_attempts: 3, backoff_ms: 2000, jitter: true },
}

const DEFAULT_BOOTSTRAP: ExecutionPlanBootstrapConfig = {
  enabled: false,
  backend_id: 0,
  prompt_template_id: 0,
  batch_size: 20,
  concurrency: 2,
  max_terms_per_batch: 20,
  min_source_len: 2,
}

const DEFAULT_RUBY_RETRY: ExecutionPlanRubyRetryConfig = {
  enabled: false,
  backend_id: 0,
}

function deepClone<T>(obj: T): T {
  return JSON.parse(JSON.stringify(obj))
}

// ── Store & 依赖 ──────────────────────────────────────────────

const store = useExecutionPlanTemplatesStore()
const backendsStore = useBackendsStore()
const promptTemplatesStore = usePromptTemplatesStore()
const translationProfilesStore = useTranslationProfilesStore()
const message = useMessage()
const { t } = useI18n()

// ── 表单状态 ──────────────────────────────────────────────────

const formRef = ref<FormInst | null>(null)
const drawerVisible = ref(false)
const editingItem = ref<ExecutionPlanTemplate | null>(null)
const deleteModalVisible = ref(false)
const deletingItem = ref<ExecutionPlanTemplate | null>(null)

const formModel = reactive<FormModel>({
  name: '',
  description: '',
  bootstrap: deepClone(DEFAULT_BOOTSTRAP),
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

const translationProfileOptions = computed<SelectOption[]>(() =>
  translationProfilesStore.items.map((p) => ({ label: p.name, value: p.id })),
)

// ── 计算属性 ──────────────────────────────────────────────────

const filterScopeOptions = computed<SelectOption[]>(() => [
  { label: t('executionPlanTemplates.filters.allScopes'), value: 'all' },
  { label: t('executionPlanTemplates.scopes.system'), value: 'system' },
  { label: t('executionPlanTemplates.scopes.user'), value: 'user' },
  { label: t('executionPlanTemplates.scopes.org'), value: 'org' },
])

const hasActiveFilters = computed(
  () => store.searchQuery.trim().length > 0 || store.scopeFilter !== 'all',
)

const isEditMode = computed(() => Boolean(editingItem.value))
const isSystemScope = computed(() => editingItem.value?.scope === 'system')
const drawerTitle = computed(() =>
  isEditMode.value
    ? t('executionPlanTemplates.actions.edit')
    : t('executionPlanTemplates.actions.create'),
)

const rules = computed<FormRules>(() => ({
  name: [
    {
      required: true,
      message: t('executionPlanTemplates.validation.nameRequired'),
      trigger: ['input', 'blur'],
    },
  ],
}))

// ── 方法 ──────────────────────────────────────────────────────

const resetForm = (): void => {
  formModel.name = ''
  formModel.description = ''
  formModel.bootstrap = deepClone(DEFAULT_BOOTSTRAP)
  formModel.ruby_retry = deepClone(DEFAULT_RUBY_RETRY)
  formModel.rounds = [deepClone(DEFAULT_ROUND)]
  editingItem.value = null
}

const openCreateDrawer = (): void => {
  resetForm()
  drawerVisible.value = true
}

const openEditDrawer = (item: ExecutionPlanTemplate): void => {
  editingItem.value = item
  formModel.name = item.name
  formModel.description = item.description ?? ''
  formModel.bootstrap = item.bootstrap ? deepClone(item.bootstrap) : deepClone(DEFAULT_BOOTSTRAP)
  formModel.ruby_retry = item.ruby_retry
    ? deepClone(item.ruby_retry)
    : deepClone(DEFAULT_RUBY_RETRY)
  formModel.rounds = item.rounds?.length ? deepClone(item.rounds) : [deepClone(DEFAULT_ROUND)]
  drawerVisible.value = true
}

/** 轮次级别校验 */
const validateRounds = (): boolean => {
  for (let i = 0; i < formModel.rounds.length; i++) {
    const round = formModel.rounds[i]!
    if (!round.backend_id) {
      message.error(t('executionPlanTemplates.validation.roundBackendRequired', { n: i + 1 }))
      return false
    }
    if (!round.prompt_template_id) {
      message.error(t('executionPlanTemplates.validation.roundPromptRequired', { n: i + 1 }))
      return false
    }
    if (!round.profile_id) {
      message.error(t('executionPlanTemplates.validation.roundProfileRequired', { n: i + 1 }))
      return false
    }
    if (!round.batch_size || round.batch_size < 1) {
      message.error(t('executionPlanTemplates.validation.roundBatchSizeRequired', { n: i + 1 }))
      return false
    }
    if (!round.concurrency || round.concurrency < 1) {
      message.error(t('executionPlanTemplates.validation.roundConcurrencyRequired', { n: i + 1 }))
      return false
    }
  }
  return true
}

const buildPayload = (): CreateRequest => {
  const payload: CreateRequest = {
    name: formModel.name.trim(),
    rounds: formModel.rounds.map((round) => ({
      name: round.name?.trim() || undefined,
      backend_id: round.backend_id,
      prompt_template_id: round.prompt_template_id,
      profile_id: round.profile_id,
      batch_size: round.batch_size,
      concurrency: round.concurrency,
      fallback_shrink: round.fallback_shrink ?? 0,
      rate_limit_per_sec: round.rate_limit_per_sec ?? 0,
      ...(round.retry ? { retry: round.retry } : {}),
    })),
  }
  if (formModel.description.trim()) {
    payload.description = formModel.description.trim()
  }
  // 仅当 bootstrap.enabled 为 true 时才包含 bootstrap 配置
  if (formModel.bootstrap.enabled) {
    payload.bootstrap = deepClone(formModel.bootstrap)
  }
  // 仅当 ruby_retry.enabled 为 true 时才包含 ruby_retry 配置
  if (formModel.ruby_retry.enabled) {
    payload.ruby_retry = deepClone(formModel.ruby_retry)
  }
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

const confirmDelete = (item: ExecutionPlanTemplate): void => {
  if (item.scope === 'system') {
    message.warning(t('executionPlanTemplates.messages.systemDeleteForbidden'))
    return
  }
  deletingItem.value = item
  deleteModalVisible.value = true
}

const executeDelete = async (): Promise<void> => {
  if (!deletingItem.value) return

  try {
    await store.deleteTemplate(deletingItem.value.id)
    message.success(t('executionPlanTemplates.messages.deleteSuccess'))
    deleteModalVisible.value = false
    deletingItem.value = null
  } catch {
    // Error is handled by the store
  }
}

const getScopeTagType = (scope: Scope): 'default' | 'info' | 'success' => {
  switch (scope) {
    case 'system':
      return 'default'
    case 'user':
      return 'info'
    case 'org':
      return 'success'
    default:
      return 'default'
  }
}

const formatDate = (dateStr: string | undefined): string => {
  if (!dateStr) return '—'
  return new Date(dateStr).toLocaleDateString()
}

// ── 生命周期：并行加载四个 Store ───────────────────────────────

onMounted(async () => {
  await Promise.all([
    store.loadTemplates(),
    backendsStore.loadBackends(),
    promptTemplatesStore.loadTemplates(),
    translationProfilesStore.loadProfiles(),
  ])
})
</script>

<template>
  <div class="space-y-6">
    <!-- 页面头部 -->
    <NCard :bordered="false" class="overflow-hidden shadow-sm shadow-lf-shadow">
      <div class="flex flex-col gap-6 lg:flex-row lg:items-center lg:justify-between">
        <div class="space-y-3">
          <div
            class="inline-flex items-center rounded-full bg-lf-brand-soft px-3 py-1 text-xs font-medium text-brand-600"
          >
            {{ t('executionPlanTemplates.eyebrow') }}
          </div>
          <div>
            <h1 class="text-3xl font-semibold tracking-tight text-lf-text-strong">
              {{ t('executionPlanTemplates.title') }}
            </h1>
            <p class="mt-2 max-w-2xl text-sm leading-6 text-lf-text-muted">
              {{ t('executionPlanTemplates.subtitle') }}
            </p>
          </div>
        </div>
        <div class="flex flex-wrap gap-3">
          <NButton secondary :loading="store.loading" @click="store.loadTemplates">
            {{ t('executionPlanTemplates.actions.refresh') }}
          </NButton>
          <NButton type="primary" @click="openCreateDrawer">
            {{ t('executionPlanTemplates.actions.create') }}
          </NButton>
        </div>
      </div>
    </NCard>

    <!-- 统计卡片（第 4 个为平均轮次） -->
    <div class="grid grid-cols-1 gap-4 md:grid-cols-4">
      <NCard :bordered="false" class="shadow-sm shadow-lf-shadow">
        <div class="text-xs font-medium text-lf-text-muted">
          {{ t('executionPlanTemplates.stats.total') }}
        </div>
        <div class="mt-2 text-2xl font-semibold text-lf-text-strong">
          {{ store.totalCount }}
        </div>
      </NCard>
      <NCard :bordered="false" class="shadow-sm shadow-lf-shadow">
        <div class="text-xs font-medium text-lf-text-muted">
          {{ t('executionPlanTemplates.stats.system') }}
        </div>
        <div class="mt-2 text-2xl font-semibold text-lf-text-strong">
          {{ store.systemCount }}
        </div>
      </NCard>
      <NCard :bordered="false" class="shadow-sm shadow-lf-shadow">
        <div class="text-xs font-medium text-lf-text-muted">
          {{ t('executionPlanTemplates.stats.user') }}
        </div>
        <div class="mt-2 text-2xl font-semibold text-lf-text-strong">
          {{ store.userCount }}
        </div>
      </NCard>
      <NCard :bordered="false" class="shadow-sm shadow-lf-shadow">
        <div class="text-xs font-medium text-lf-text-muted">
          {{ t('executionPlanTemplates.stats.avgRounds') }}
        </div>
        <div class="mt-2 text-2xl font-semibold text-lf-text-strong">
          {{ store.avgRoundsPerPlan }}
        </div>
      </NCard>
    </div>

    <!-- 筛选栏 -->
    <NCard :bordered="false" class="shadow-sm shadow-lf-shadow">
      <div class="flex flex-col gap-3 lg:flex-row lg:items-center lg:justify-between">
        <NInput
          v-model:value="store.searchQuery"
          clearable
          class="lg:max-w-sm"
          :placeholder="t('executionPlanTemplates.filters.searchPlaceholder')"
        />
        <div class="flex flex-wrap gap-3">
          <NSelect v-model:value="store.scopeFilter" class="w-44" :options="filterScopeOptions" />
          <NButton
            v-if="hasActiveFilters"
            quaternary
            @click="((store.searchQuery = ''), (store.scopeFilter = 'all'))"
          >
            {{ t('executionPlanTemplates.filters.reset') }}
          </NButton>
        </div>
      </div>
    </NCard>

    <!-- 错误提示（抽屉关闭时显示在主页面） -->
    <NAlert v-if="store.error && !drawerVisible" type="error" :bordered="false">
      {{ store.error }}
    </NAlert>

    <!-- 加载骨架屏 -->
    <div v-if="store.loading" class="grid grid-cols-1 gap-5 md:grid-cols-2 xl:grid-cols-3">
      <NCard v-for="index in 6" :key="index" :bordered="false" class="shadow-sm shadow-lf-shadow">
        <NSkeleton text :repeat="4" />
      </NCard>
    </div>

    <!-- 空状态 -->
    <NEmpty
      v-else-if="store.filteredItems.length === 0"
      class="rounded-2xl bg-lf-surface py-16 shadow-sm shadow-lf-shadow"
      :description="
        hasActiveFilters
          ? t('executionPlanTemplates.empty.filtered')
          : t('executionPlanTemplates.empty.default')
      "
    >
      <template #extra>
        <NButton
          v-if="hasActiveFilters"
          secondary
          @click="((store.searchQuery = ''), (store.scopeFilter = 'all'))"
        >
          {{ t('executionPlanTemplates.filters.reset') }}
        </NButton>
        <NButton v-else type="primary" @click="openCreateDrawer">
          {{ t('executionPlanTemplates.actions.createFirst') }}
        </NButton>
      </template>
    </NEmpty>

    <!-- 卡片网格 -->
    <div v-else class="grid grid-cols-1 gap-5 md:grid-cols-2 xl:grid-cols-3">
      <NCard
        v-for="item in store.filteredItems"
        :key="item.id"
        hoverable
        :bordered="false"
        class="group shadow-sm shadow-lf-shadow transition-all duration-200 hover:-translate-y-0.5 hover:shadow-lg hover:shadow-lf-shadow-strong"
      >
        <div class="flex h-full flex-col gap-4">
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

          <!-- 专属摘要：轮次概览 -->
          <div class="space-y-2">
            <div class="flex items-center gap-2">
              <NTag size="small" type="info" :bordered="false">
                {{ item.rounds?.length ?? 0 }} {{ t('executionPlanTemplates.card.rounds') }}
              </NTag>
            </div>
            <div v-if="item.rounds?.length" class="space-y-1">
              <div
                v-for="(round, idx) in item.rounds.slice(0, 3)"
                :key="idx"
                class="flex items-center gap-2 text-xs text-lf-text-muted"
              >
                <span
                  class="inline-flex h-5 w-5 items-center justify-center rounded-full bg-lf-brand-soft text-[10px] font-semibold text-brand-600"
                >
                  {{ idx + 1 }}
                </span>
                <span class="truncate">
                  {{ round.name || `round-${idx + 1}` }}
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
                  {{ t('executionPlanTemplates.actions.edit') }}
                </NButton>
                <NButton
                  v-if="item.scope !== 'system'"
                  text
                  type="error"
                  class="font-medium"
                  @click="confirmDelete(item)"
                >
                  {{ t('executionPlanTemplates.actions.delete') }}
                </NButton>
                <NButton
                  v-if="item.scope === 'system'"
                  text
                  type="info"
                  class="font-medium"
                  @click="openEditDrawer(item)"
                >
                  {{ t('executionPlanTemplates.actions.view') }}
                </NButton>
              </div>
            </div>
          </div>
        </div>
      </NCard>
    </div>

    <!-- 创建/编辑抽屉 -->
    <NDrawer v-model:show="drawerVisible" :width="720" placement="right">
      <NDrawerContent :native-scrollbar="false">
        <template #header>
          <div>
            <div class="text-lg font-semibold">{{ drawerTitle }}</div>
          </div>
        </template>

        <!-- 错误提示（抽屉打开时显示在抽屉内部） -->
        <NAlert v-if="store.error && drawerVisible" type="error" class="mb-4">
          {{ store.error }}
        </NAlert>

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

          <!-- Bootstrap + 轮次编辑器 -->
          <div class="mb-4">
            <span class="mb-2 block text-sm font-medium text-lf-text-strong">
              {{ t('executionPlanTemplates.form.rounds') }}
            </span>
            <ExecutionPlanEditor
              :rounds="formModel.rounds"
              :bootstrap="formModel.bootstrap"
              :ruby-retry="formModel.ruby_retry"
              :backends="backendOptions"
              :prompt-templates="promptTemplateOptions"
              :translation-profiles="translationProfileOptions"
              :disabled="isSystemScope"
              @update:rounds="formModel.rounds = $event"
              @update:bootstrap="formModel.bootstrap = $event"
              @update:ruby-retry="formModel.ruby_retry = $event"
            />
          </div>
        </NForm>

        <template #footer>
          <div class="flex justify-end gap-3">
            <NButton @click="drawerVisible = false">
              {{ t('executionPlanTemplates.actions.cancel') }}
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
      :title="t('executionPlanTemplates.actions.confirmDelete')"
      :content="
        deletingItem ? t('executionPlanTemplates.delete.confirm', { name: deletingItem.name }) : ''
      "
      :positive-text="t('executionPlanTemplates.actions.confirmDelete')"
      :negative-text="t('executionPlanTemplates.actions.cancel')"
      :loading="deletingItem ? store.deletingIds.includes(deletingItem.id) : false"
      @positive-click="executeDelete"
    />
  </div>
</template>
