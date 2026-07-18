<script setup lang="ts">
import {
  NButton,
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
import ProfileConfigEditor from '@/components/templates/ProfileConfigEditor.vue'
import { useExecutionProfilesStore } from '@/stores/executionProfiles'

type ExecutionProfile = ApiSchemas['ExecutionProfile']
type ExecutionProfileConfig = ApiSchemas['ExecutionProfileConfig']
type CreateRequest = ApiSchemas['CreateExecutionProfileRequest']
type UpdateRequest = ApiSchemas['UpdateExecutionProfileRequest']
type Scope = ExecutionProfile['scope']

interface FormModel {
  name: string
  description: string
  config: ExecutionProfileConfig
}

// ── 默认配置 ──────────────────────────────────────────────────

const CONFIG_DEFAULTS: ExecutionProfileConfig = {
  protect: { enabled: true, rules: ['code', 'link', 'placeholder', 'xml'] },
  ruby: {
    enabled: false,
    preserve_kinds: ['phonetic', 'semantic', 'creative'],
  },
  postprocess: { enabled: true, trim_spaces: true },
  repair: {
    enabled: true,
    json_structural: true,
    schema_aliases: true,
    partial: true,
    partial_threshold: 0.5,
    placeholder_normalize: true,
    prompt_upgrade: true,
  },
  glossary: {
    bootstrap: {
      enabled: false,
      max_terms_per_1000_chars: 20,
      min_source_len: 2,
      inline_conflict_strategy: 'off',
    },
  },
  context: { enabled: true, before: 1, after: 1, max_chars: 0 },
  qa: {
    enabled: false,
    auto_reject: false,
    length_method: 'char_weight',
    length_ratio_min: 0,
    length_ratio_max: 0,
  },
}

function deepClone<T>(obj: T): T {
  return JSON.parse(JSON.stringify(obj))
}

// ── Store & 依赖 ──────────────────────────────────────────────

const store = useExecutionProfilesStore()
const message = useMessage()
const { t } = useI18n()

// ── 表单状态 ──────────────────────────────────────────────────

const formRef = ref<FormInst | null>(null)
const configEditorRef = ref<InstanceType<typeof ProfileConfigEditor> | null>(null)
const drawerVisible = ref(false)
const editingItem = ref<ExecutionProfile | null>(null)
const deleteModalVisible = ref(false)
const deletingItem = ref<ExecutionProfile | null>(null)

const formModel = reactive<FormModel>({
  name: '',
  description: '',
  config: deepClone(CONFIG_DEFAULTS),
})

// ── 计算属性 ──────────────────────────────────────────────────

const filterScopeOptions = computed<SelectOption[]>(() => [
  { label: t('executionProfiles.filters.allScopes'), value: 'all' },
  { label: t('executionProfiles.scopes.system'), value: 'system' },
  { label: t('executionProfiles.scopes.user'), value: 'user' },
  { label: t('executionProfiles.scopes.org'), value: 'org' },
])

const hasActiveFilters = computed(
  () => store.searchQuery.trim().length > 0 || store.scopeFilter !== 'all',
)

const isEditMode = computed(() => Boolean(editingItem.value))
const isSystemScope = computed(() => editingItem.value?.scope === 'system')
const drawerTitle = computed(() =>
  isEditMode.value ? t('executionProfiles.actions.edit') : t('executionProfiles.actions.create'),
)

const hasConfigError = computed(() => Boolean(configEditorRef.value?.lengthRatioError))

const rules = computed<FormRules>(() => ({
  name: [
    {
      required: true,
      message: t('executionProfiles.validation.nameRequired'),
      trigger: ['input', 'blur'],
    },
  ],
}))

// ── 方法 ──────────────────────────────────────────────────────

/** 从 API 对象中提取配置，缺失字段用默认值填充 */
function extractConfig(profile: ExecutionProfile): ExecutionProfileConfig {
  const src = profile.config
  if (!src) return deepClone(CONFIG_DEFAULTS)
  return {
    protect: {
      ...CONFIG_DEFAULTS.protect,
      ...src.protect,
      rules: src.protect?.rules ?? CONFIG_DEFAULTS.protect.rules,
    },
    ruby: {
      enabled: src.ruby?.enabled ?? CONFIG_DEFAULTS.ruby!.enabled,
      preserve_kinds: src.ruby?.preserve_kinds ?? CONFIG_DEFAULTS.ruby!.preserve_kinds,
    },
    postprocess: { ...CONFIG_DEFAULTS.postprocess, ...src.postprocess },
    repair: { ...CONFIG_DEFAULTS.repair, ...src.repair },
    glossary: {
      bootstrap: { ...CONFIG_DEFAULTS.glossary.bootstrap, ...src.glossary?.bootstrap },
    },
    context: { ...CONFIG_DEFAULTS.context, ...src.context },
    qa: {
      enabled: src.qa?.enabled ?? CONFIG_DEFAULTS.qa!.enabled,
      auto_reject: src.qa?.auto_reject ?? CONFIG_DEFAULTS.qa!.auto_reject,
      length_method: src.qa?.length_method ?? CONFIG_DEFAULTS.qa!.length_method,
      length_ratio_min: src.qa?.length_ratio_min ?? CONFIG_DEFAULTS.qa!.length_ratio_min,
      length_ratio_max: src.qa?.length_ratio_max ?? CONFIG_DEFAULTS.qa!.length_ratio_max,
    },
  }
}

const resetForm = (): void => {
  formModel.name = ''
  formModel.description = ''
  formModel.config = deepClone(CONFIG_DEFAULTS)
  editingItem.value = null
}

const openCreateDrawer = (): void => {
  resetForm()
  drawerVisible.value = true
}

const openEditDrawer = (item: ExecutionProfile): void => {
  editingItem.value = item
  formModel.name = item.name
  formModel.description = item.description ?? ''
  formModel.config = extractConfig(item)
  drawerVisible.value = true
}

const buildPayload = (): CreateRequest => {
  const payload: CreateRequest = {
    name: formModel.name.trim(),
    config: deepClone(formModel.config),
  }
  if (formModel.description.trim()) {
    payload.description = formModel.description.trim()
  }
  return payload
}

const onSubmit = async (): Promise<void> => {
  try {
    await formRef.value?.validate()
  } catch {
    return
  }

  const payload = buildPayload()

  try {
    if (isEditMode.value && editingItem.value) {
      await store.updateProfile(editingItem.value.id, payload as UpdateRequest)
      message.success(t('executionProfiles.messages.updateSuccess'))
    } else {
      await store.createProfile(payload)
      message.success(t('executionProfiles.messages.createSuccess'))
    }
    drawerVisible.value = false
    resetForm()
  } catch {
    // Error is handled by the store
  }
}

const confirmDelete = (item: ExecutionProfile): void => {
  if (item.scope === 'system') {
    message.warning(t('executionProfiles.messages.systemDeleteForbidden'))
    return
  }
  deletingItem.value = item
  deleteModalVisible.value = true
}

const executeDelete = async (): Promise<void> => {
  if (!deletingItem.value) return

  try {
    await store.deleteProfile(deletingItem.value.id)
    message.success(t('executionProfiles.messages.deleteSuccess'))
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

onMounted(() => {
  store.loadProfiles()
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
            {{ t('executionProfiles.eyebrow') }}
          </div>
          <div>
            <h1 class="text-3xl font-semibold tracking-tight text-lf-text-strong">
              {{ t('executionProfiles.title') }}
            </h1>
            <p class="mt-2 max-w-2xl text-sm leading-6 text-lf-text-muted">
              {{ t('executionProfiles.subtitle') }}
            </p>
          </div>
        </div>
        <div class="flex flex-wrap gap-3">
          <NButton secondary :loading="store.loading" @click="store.loadProfiles">
            {{ t('executionProfiles.actions.refresh') }}
          </NButton>
          <NButton type="primary" @click="openCreateDrawer">
            {{ t('executionProfiles.actions.create') }}
          </NButton>
        </div>
      </div>
    </section>

    <!-- 统计卡片 -->
    <div class="grid grid-cols-1 gap-4 md:grid-cols-4">
      <div class="lf-metric">
        <div class="lf-metric-label">{{ t('executionProfiles.stats.total') }}</div>
        <div class="lf-metric-value">{{ store.totalCount }}</div>
      </div>
      <div class="lf-metric">
        <div class="lf-metric-label">{{ t('executionProfiles.stats.system') }}</div>
        <div class="lf-metric-value">{{ store.systemCount }}</div>
      </div>
      <div class="lf-metric">
        <div class="lf-metric-label">{{ t('executionProfiles.stats.user') }}</div>
        <div class="lf-metric-value">{{ store.userCount }}</div>
      </div>
      <div class="lf-metric">
        <div class="lf-metric-label">{{ t('executionProfiles.stats.org') }}</div>
        <div class="lf-metric-value">{{ store.orgCount }}</div>
      </div>
    </div>

    <div class="lf-panel px-4 py-3">
      <div class="flex flex-col gap-3 lg:flex-row lg:items-center lg:justify-between">
        <NInput
          v-model:value="store.searchQuery"
          clearable
          class="lg:max-w-sm"
          :placeholder="t('executionProfiles.filters.searchPlaceholder')"
        />
        <div class="flex flex-wrap gap-3">
          <NSelect v-model:value="store.scopeFilter" class="w-44" :options="filterScopeOptions" />
          <NButton
            v-if="hasActiveFilters"
            quaternary
            @click="((store.searchQuery = ''), (store.scopeFilter = 'all'))"
          >
            {{ t('executionProfiles.filters.reset') }}
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
          ? t('executionProfiles.empty.filtered')
          : t('executionProfiles.empty.default')
      "
    >
      <template #extra>
        <NButton
          v-if="hasActiveFilters"
          secondary
          @click="((store.searchQuery = ''), (store.scopeFilter = 'all'))"
        >
          {{ t('executionProfiles.filters.reset') }}
        </NButton>
        <NButton v-else type="primary" @click="openCreateDrawer">
          {{ t('executionProfiles.actions.createFirst') }}
        </NButton>
      </template>
    </NEmpty>

    <!-- 卡片网格 -->
    <div v-else class="grid grid-cols-1 gap-5 md:grid-cols-2 xl:grid-cols-3">
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
            {{ t(`executionProfiles.scopes.${item.scope}`) }}
          </NTag>
        </div>

        <!-- 描述 -->
        <p
          class="line-clamp-2 text-sm leading-6 text-lf-text-muted"
          :class="{ 'italic text-lf-text-subtle': !item.description }"
        >
          {{ item.description || t('executionProfiles.card.noDescription') }}
        </p>

        <!-- 专属摘要：配置特征标签 -->
        <div class="flex flex-wrap gap-1.5">
          <NTag v-if="item.config?.protect?.enabled" size="small" :bordered="false">
            {{ t('executionProfiles.feature.protect') }}:
            {{ item.config.protect.rules?.length ?? 0 }}
          </NTag>
          <NTag v-if="item.config?.repair?.enabled" size="small" :bordered="false">
            {{ t('executionProfiles.feature.repair') }}
          </NTag>
          <NTag v-if="item.config?.postprocess?.enabled" size="small" :bordered="false">
            {{ t('executionProfiles.feature.postprocess') }}
          </NTag>
          <NTag v-if="item.config?.glossary?.bootstrap?.enabled" size="small" :bordered="false">
            {{ t('executionProfiles.feature.glossary') }}
          </NTag>
          <NTag v-if="item.config?.context?.enabled" size="small" :bordered="false">
            {{ t('executionProfiles.feature.context') }}
          </NTag>
        </div>

        <!-- 底部：时间 + 操作 -->
        <div class="mt-auto border-t border-lf-border-soft pt-4">
          <div class="flex items-center justify-between gap-3">
            <span class="text-xs text-lf-text-subtle">
              {{ t('executionProfiles.card.createdAt') }} {{ formatDate(item.created_at) }}
            </span>
            <div class="flex items-center gap-2">
              <NButton
                v-if="item.scope !== 'system'"
                text
                type="primary"
                class="font-medium"
                @click="openEditDrawer(item)"
              >
                {{ t('executionProfiles.actions.edit') }}
              </NButton>
              <NButton
                v-if="item.scope !== 'system'"
                text
                type="error"
                class="font-medium"
                @click="confirmDelete(item)"
              >
                {{ t('executionProfiles.actions.delete') }}
              </NButton>
              <NButton
                v-if="item.scope === 'system'"
                text
                type="info"
                class="font-medium"
                @click="openEditDrawer(item)"
              >
                {{ t('executionProfiles.actions.view') }}
              </NButton>
            </div>
          </div>
        </div>
      </div>
    </div>

    <!-- 创建/编辑抽屉 -->
    <NDrawer v-model:show="drawerVisible" :width="640" placement="right">
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
          <NFormItem :label="t('executionProfiles.form.name')" path="name">
            <NInput
              v-model:value="formModel.name"
              :placeholder="t('executionProfiles.form.namePlaceholder')"
              :disabled="isSystemScope"
            />
          </NFormItem>

          <NFormItem :label="t('executionProfiles.form.description')" path="description">
            <NInput
              v-model:value="formModel.description"
              type="textarea"
              :placeholder="t('executionProfiles.form.descriptionPlaceholder')"
              :rows="3"
              :disabled="isSystemScope"
            />
          </NFormItem>

          <!-- 翻译配置编辑器 -->
          <div class="mb-4">
            <span class="mb-2 block text-sm font-medium text-lf-text-strong">
              {{ t('executionProfiles.form.executionConfig') }}
            </span>
            <ProfileConfigEditor
              ref="configEditorRef"
              :config="formModel.config"
              :disabled="isSystemScope"
              @update:config="formModel.config = $event"
            />
          </div>
        </NForm>

        <template #footer>
          <div class="flex justify-end gap-3">
            <NButton @click="drawerVisible = false">
              {{ t('executionProfiles.actions.cancel') }}
            </NButton>
            <NButton
              v-if="!isSystemScope"
              type="primary"
              :loading="store.creating || store.updating"
              :disabled="hasConfigError"
              @click="onSubmit"
            >
              {{
                isEditMode
                  ? t('executionProfiles.actions.submitUpdate')
                  : t('executionProfiles.actions.submitCreate')
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
      :title="t('executionProfiles.actions.confirmDelete')"
      :content="
        deletingItem ? t('executionProfiles.delete.confirm', { name: deletingItem.name }) : ''
      "
      :positive-text="t('executionProfiles.actions.confirmDelete')"
      :negative-text="t('executionProfiles.actions.cancel')"
      :loading="deletingItem ? store.deletingIds.includes(deletingItem.id) : false"
      @positive-click="executeDelete"
    />
  </div>
</template>
