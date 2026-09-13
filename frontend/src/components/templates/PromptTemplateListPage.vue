<script lang="ts">
/**
 * 提示词模板列表页共享骨架的类型定义，供本组件与三个模板页（翻译 / 术语抽取 / 术语精简）共用。
 */

/** 提示词模板条目的公共结构；内容字段（content / system_prompt_content）经 contentField 读取 */
export interface TemplateEntity {
  id: number
  name: string
  description?: string | null
  scope: string
  created_at?: string
  updated_at?: string
}

/** 归一化表单数据，由各页的 save 适配器映射为具体的 Create/UpdateRequest */
export interface TemplateFormPayload {
  name: string
  description?: string
  content: string
}

/** 三个提示词模板 store 的共享结构（各 store 均满足该接口） */
export interface PromptTemplateStore {
  loading: boolean
  creating: boolean
  updating: boolean
  deletingIds: number[]
  error: string | null
  searchQuery: string
  scopeFilter: string
  filteredItems: TemplateEntity[]
  totalCount: number
  systemCount: number
  userCount: number
  loadTemplates(): Promise<unknown>
  resetFilters(): void
  setSearchQuery(query: string): void
  setScopeFilter(scope: string): void
  clearError(): void
  deleteTemplate(id: number): Promise<unknown>
}

/** 页面提供的保存适配器：id 为空表示创建，非空表示更新对应模板 */
export type SaveTemplate = (payload: TemplateFormPayload, id?: number) => Promise<unknown>
</script>

<script setup lang="ts">
/**
 * 提示词模板列表页共享骨架：计数分段筛选 + 搜索、卡片网格、创建/编辑/查看抽屉、删除确认。
 *
 * 翻译 / 术语抽取 / 术语精简三个模板页共用；各页通过 props 注入自己的 store、
 * i18n 命名空间、内容字段名与 save 适配器，页面文件只保留差异配置。
 */
import {
  NButton,
  NDrawer,
  NDrawerContent,
  NForm,
  NFormItem,
  NInput,
  NModal,
  NTab,
  NTabs,
  NTag,
  useMessage,
  type FormInst,
  type FormRules,
} from 'naive-ui'
import { h } from 'vue'
import { useI18n } from 'vue-i18n'

import PromptTemplateEditor from '@/components/templates/PromptTemplateEditor.vue'
import { useEntityCrud } from '@/composables/useEntityCrud'
import { useStoreErrorToast } from '@/composables/useStoreErrorToast'
import { DRAWER_WIDTH } from '@/components/common/uiConstants'
import { formatDateTime } from '@/utils/datetime'

const props = withDefaults(
  defineProps<{
    store: PromptTemplateStore
    /** i18n 命名空间前缀（title / form / messages 等文案均取自该前缀） */
    i18nPrefix: string
    /** 条目内容字段名：翻译模板为 system_prompt_content，其余为 content */
    contentField: 'content' | 'system_prompt_content'
    /** 编辑器内置变量集，决定变量按钮组与 tooltip 分组 */
    variableSet?: 'system' | 'bootstrap' | 'prune'
    /** 保存适配器：把归一化表单数据映射为具体请求并调用 store */
    save: SaveTemplate
  }>(),
  { variableSet: 'system' },
)

interface FormModel {
  name: string
  description: string
  content: string
}

// ── 依赖 ──────────────────────────────────────────────

const { t } = useI18n()
const message = useMessage()

/** 取当前命名空间下的文案（可选插值参数） */
const tx = (key: string, params?: Record<string, unknown>): string =>
  params ? t(`${props.i18nPrefix}.${key}`, params) : t(`${props.i18nPrefix}.${key}`)

const { getScopeTagType, deleteModalVisible, deletingItem, confirmDelete, executeDelete } =
  useEntityCrud<TemplateEntity>({
    i18nPrefix: props.i18nPrefix,
    deleteItem: (id) => props.store.deleteTemplate(id),
    isDeleting: (id) => props.store.deletingIds.includes(id),
  })

// ── 筛选 ──────────────────────────────────────────────

interface ScopeFilterTab {
  name: 'all' | 'system' | 'user'
  label: string
  count: number
}

const filterTabs = computed<ScopeFilterTab[]>(() => [
  { name: 'all', label: tx('filters.all'), count: props.store.totalCount },
  { name: 'system', label: tx('scopes.system'), count: props.store.systemCount },
  { name: 'user', label: tx('scopes.user'), count: props.store.userCount },
])

const renderFilterTab = (tab: ScopeFilterTab): ReturnType<typeof h> =>
  h('span', { class: 'inline-flex items-baseline gap-1.5' }, [
    tab.label,
    h(
      'span',
      { class: 'hidden text-xs font-normal text-lf-text-subtle sm:inline' },
      String(tab.count),
    ),
  ])

const hasActiveFilters = computed(
  () => props.store.searchQuery.trim().length > 0 || props.store.scopeFilter !== 'all',
)

// ── 表单状态 ──────────────────────────────────────────

const formRef = ref<FormInst | null>(null)
const drawerVisible = ref(false)
const editingItem = ref<TemplateEntity | null>(null)

const formModel = reactive<FormModel>({ name: '', description: '', content: '' })

const isEditMode = computed(() => Boolean(editingItem.value))
const isSystemScope = computed(() => editingItem.value?.scope === 'system')

const drawerTitle = computed(() =>
  isSystemScope.value
    ? tx('actions.viewTitle')
    : isEditMode.value
      ? tx('actions.editTitle')
      : tx('actions.createTitle'),
)

const drawerSubtitle = computed(() => {
  if (!editingItem.value) return tx('form.createHint')
  return isSystemScope.value
    ? `${tx('scopes.system')} · ${editingItem.value.name}`
    : editingItem.value.name
})

const rules = computed<FormRules>(() => ({
  name: [{ required: true, message: tx('validation.nameRequired'), trigger: ['input', 'blur'] }],
}))

// ── 方法 ──────────────────────────────────────────────

const getContent = (item: TemplateEntity): string => {
  const raw = (item as unknown as Record<string, unknown>)[props.contentField]
  return typeof raw === 'string' ? raw : ''
}

const cardDate = (item: TemplateEntity): string => {
  const value = item.updated_at ?? item.created_at
  return value ? formatDateTime(value, { dateStyle: 'short' }) : '—'
}

const cardDateTitle = (item: TemplateEntity): string => {
  const value = item.updated_at ?? item.created_at
  return value ? formatDateTime(value, { dateStyle: 'medium', timeStyle: 'short' }) : ''
}

const resetForm = (): void => {
  formModel.name = ''
  formModel.description = ''
  formModel.content = ''
  editingItem.value = null
}

const openCreateDrawer = (): void => {
  resetForm()
  drawerVisible.value = true
}

const openEditDrawer = (item: TemplateEntity): void => {
  editingItem.value = item
  formModel.name = item.name
  formModel.description = item.description ?? ''
  formModel.content = getContent(item)
  drawerVisible.value = true
}

const onSubmit = async (): Promise<void> => {
  try {
    await formRef.value?.validate()
  } catch {
    return
  }

  const payload: TemplateFormPayload = {
    name: formModel.name.trim(),
    content: formModel.content.trim(),
  }
  const description = formModel.description.trim()
  if (description) payload.description = description

  try {
    await props.save(payload, editingItem.value?.id)
    message.success(tx(isEditMode.value ? 'messages.updateSuccess' : 'messages.createSuccess'))
    drawerVisible.value = false
    resetForm()
  } catch {
    // 错误由 store 经 useStoreErrorToast 统一提示
  }
}

// ── 生命周期 ──────────────────────────────────────────

onMounted(() => {
  props.store.loadTemplates()
})

useStoreErrorToast(
  () => props.store.error,
  () => props.store.clearError(),
)
</script>

<template>
  <EntityListPage
    :title="tx('title')"
    :subtitle="tx('subtitle')"
    :loading="store.loading"
    :empty="store.filteredItems.length === 0"
    :empty-description="tx(hasActiveFilters ? 'empty.filtered' : 'empty.default')"
  >
    <template #actions>
      <NButton secondary :loading="store.loading" @click="store.loadTemplates">
        {{ t('common.actions.refresh') }}
      </NButton>
      <NButton type="primary" @click="openCreateDrawer">
        {{ tx('actions.create') }}
      </NButton>
    </template>

    <template #filters>
      <NTabs
        :value="store.scopeFilter"
        type="segment"
        size="small"
        class="min-w-0"
        @update:value="(value: string | number) => store.setScopeFilter(String(value))"
      >
        <NTab
          v-for="tab in filterTabs"
          :key="tab.name"
          :name="tab.name"
          :tab="() => renderFilterTab(tab)"
        />
      </NTabs>
      <NInput
        :value="store.searchQuery"
        clearable
        class="lg:max-w-sm!"
        :placeholder="tx('filters.searchPlaceholder')"
        @update:value="store.setSearchQuery"
      />
    </template>

    <template #empty-extra>
      <NButton v-if="hasActiveFilters" secondary @click="store.resetFilters()">
        {{ tx('filters.reset') }}
      </NButton>
      <NButton v-else type="primary" @click="openCreateDrawer">
        {{ tx('actions.createFirst') }}
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
            {{ tx(`scopes.${item.scope}`) }}
          </NTag>
        </div>

        <!-- 描述 -->
        <p
          class="line-clamp-2 text-sm leading-6"
          :class="item.description ? 'text-lf-text-muted' : 'text-lf-text-subtle'"
        >
          {{ item.description || tx('card.noDescription') }}
        </p>

        <!-- 提示词内容预览 -->
        <div v-if="getContent(item)" class="lf-code-panel line-clamp-3">
          {{ getContent(item) }}
        </div>
        <p v-else class="text-xs text-lf-text-subtle">
          {{ tx('card.noContent') }}
        </p>

        <!-- 底部：更新时间 + 操作 -->
        <div class="mt-auto border-t border-lf-border-soft pt-4">
          <div class="flex items-center justify-between gap-3">
            <span class="text-xs text-lf-text-subtle" :title="cardDateTitle(item)">
              {{ tx('card.updatedAt') }} {{ cardDate(item) }}
            </span>
            <div class="flex items-center gap-2" @click.stop>
              <template v-if="item.scope !== 'system'">
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

  <!-- 创建/编辑/查看抽屉 -->
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
        <NFormItem :label="tx('form.name')" path="name">
          <NInput
            v-model:value="formModel.name"
            :placeholder="tx('form.namePlaceholder')"
            :disabled="isSystemScope"
          />
        </NFormItem>

        <NFormItem :label="tx('form.description')" path="description">
          <NInput
            v-model:value="formModel.description"
            type="textarea"
            :placeholder="tx('form.descriptionPlaceholder')"
            :rows="3"
            :disabled="isSystemScope"
          />
        </NFormItem>

        <NFormItem :label="tx('form.content')" path="content">
          <PromptTemplateEditor
            v-model="formModel.content"
            :disabled="isSystemScope"
            :rows="12"
            :variable-set="variableSet"
            :placeholder="tx('form.contentPlaceholder')"
            :insert-label="tx('form.insertBuiltinVar')"
          />
        </NFormItem>
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
            {{ tx(isEditMode ? 'actions.submitUpdate' : 'actions.submitCreate') }}
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
    :content="deletingItem ? tx('delete.confirm', { name: deletingItem.name }) : ''"
    :positive-text="t('common.actions.deleteConfirmAction')"
    :negative-text="t('common.cancel')"
    :loading="deletingItem ? store.deletingIds.includes(deletingItem.id) : false"
    @positive-click="executeDelete"
  />
</template>
