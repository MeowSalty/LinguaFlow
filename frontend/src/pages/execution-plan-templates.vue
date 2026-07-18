<script setup lang="ts">
import {
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
import { useBootstrapPromptTemplatesStore } from '@/stores/bootstrapPromptTemplates'
import { useExecutionPlanTemplatesStore } from '@/stores/executionPlanTemplates'
import { usePromptTemplatesStore } from '@/stores/promptTemplates'
import { useExecutionProfilesStore } from '@/stores/executionProfiles'

type ExecutionPlanTemplate = ApiSchemas['ExecutionPlanTemplate']
type ExecutionRoundConfig = ApiSchemas['ExecutionRoundConfig']
type ExecutionPlanRubyRetryConfig = ApiSchemas['ExecutionPlanRubyRetryConfig']
type CreateRequest = ApiSchemas['CreateExecutionPlanTemplateRequest']
type UpdateRequest = ApiSchemas['UpdateExecutionPlanTemplateRequest']
type Scope = ExecutionPlanTemplate['scope']

interface FormModel {
  name: string
  description: string
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
    profile_id: 0,
    batch_size: 10,
    max_words_per_batch: 0,
    fallback_shrink: undefined,
    retry: { max_attempts: 3, backoff_ms: 2000, jitter: true },
  },
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
const bootstrapPromptTemplatesStore = useBootstrapPromptTemplatesStore()
const executionProfilesStore = useExecutionProfilesStore()
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
  formModel.ruby_retry = item.ruby_retry
    ? deepClone(item.ruby_retry)
    : deepClone(DEFAULT_RUBY_RETRY)
  formModel.rounds = item.rounds?.length ? deepClone(item.rounds) : [deepClone(DEFAULT_ROUND)]
  drawerVisible.value = true
}

const validateRounds = (): boolean => {
  for (let i = 0; i < formModel.rounds.length; i++) {
    const round = formModel.rounds[i]!
    if (!round.backend_id) {
      message.error(t('executionPlanTemplates.validation.roundBackendRequired', { n: i + 1 }))
      return false
    }
    if (!round.concurrency || round.concurrency < 1) {
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
    }
  }
  return true
}

const buildPayload = (): CreateRequest => {
  const payload: CreateRequest = {
    name: formModel.name.trim(),
    rounds: formModel.rounds.map((round) => {
      const base = {
        mode: round.mode,
        backend_id: round.backend_id,
        concurrency: round.concurrency,
      }
      if (round.mode === 'translate' && round.translate) {
        return {
          ...base,
          translate: {
            prompt_template_id: round.translate.prompt_template_id,
            profile_id: round.translate.profile_id,
            batch_size: round.translate.batch_size,
            max_words_per_batch: round.translate.max_words_per_batch,
            fallback_shrink: round.translate.fallback_shrink ?? undefined,
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
          },
        }
      }
      return base
    }),
  }
  if (formModel.description.trim()) {
    payload.description = formModel.description.trim()
  }
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

// ── 生命周期 ──────────────────────────────────────────────────

onMounted(async () => {
  await Promise.all([
    store.loadTemplates(),
    backendsStore.loadBackends(),
    promptTemplatesStore.loadTemplates(),
    bootstrapPromptTemplatesStore.loadTemplates(),
    executionProfilesStore.loadProfiles(),
  ])
})

watch(
  () => store.error,
  (err) => {
    if (err) {
      message.error(err, { duration: 0, closable: true })
      store.error = null
    }
  },
)
</script>

<template>
  <div class="lf-page">
    <!-- 页面头部 -->
    <section class="lf-page-header">
      <div class="flex flex-col gap-6 lg:flex-row lg:items-center lg:justify-between">
        <div class="space-y-3">
          <div class="lf-eyebrow">
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
    </section>

    <!-- 统计卡片 -->
    <div class="grid grid-cols-1 gap-4 md:grid-cols-4">
      <div class="lf-metric">
        <div class="lf-metric-label">{{ t('executionPlanTemplates.stats.total') }}</div>
        <div class="lf-metric-value">{{ store.totalCount }}</div>
      </div>
      <div class="lf-metric">
        <div class="lf-metric-label">{{ t('executionPlanTemplates.stats.system') }}</div>
        <div class="lf-metric-value">{{ store.systemCount }}</div>
      </div>
      <div class="lf-metric">
        <div class="lf-metric-label">{{ t('executionPlanTemplates.stats.user') }}</div>
        <div class="lf-metric-value">{{ store.userCount }}</div>
      </div>
      <div class="lf-metric">
        <div class="lf-metric-label">{{ t('executionPlanTemplates.stats.avgRounds') }}</div>
        <div class="lf-metric-value">{{ store.avgRoundsPerPlan }}</div>
      </div>
    </div>

    <div class="lf-panel px-4 py-3">
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
    </div>

    <!-- 加载骨架屏 -->
    <div v-if="store.loading" class="grid grid-cols-1 gap-5 md:grid-cols-2 xl:grid-cols-3">
      <div v-for="index in 6" :key="index" class="lf-panel p-5">
        <NSkeleton text :repeat="4" />
      </div>
    </div>

    <!-- 空状态 -->
    <NEmpty
      v-else-if="store.filteredItems.length === 0"
      class="lf-panel py-16"
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
        class="lf-interactive-card group"
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

          <!-- 轮次概览 -->
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
                  class="inline-flex h-5 w-5 items-center justify-center rounded-full text-[10px] font-semibold"
                  :class="
                    round.mode === 'translate'
                      ? 'bg-lf-brand-soft text-brand-600'
                      : 'bg-amber-50 text-amber-600'
                  "
                >
                  {{ idx + 1 }}
                </span>
                <span class="truncate">
                  {{
                    round.mode === 'translate'
                      ? t('executionPlanEditor.round.modeTranslate')
                      : t('executionPlanEditor.round.modeExtract')
                  }}
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
              :execution-profiles="executionProfileOptions"
              :disabled="isSystemScope"
              @update:rounds="formModel.rounds = $event"
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
