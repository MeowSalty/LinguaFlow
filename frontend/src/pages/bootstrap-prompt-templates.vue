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
} from 'naive-ui'
import { useI18n } from 'vue-i18n'

import type { ApiSchemas } from '@/api/client'
import PromptTemplateEditor from '@/components/templates/PromptTemplateEditor.vue'
import { useEntityCrud } from '@/composables/useEntityCrud'
import { useStoreErrorToast } from '@/composables/useStoreErrorToast'
import { useBootstrapPromptTemplatesStore } from '@/stores/bootstrapPromptTemplates'
import { formatDateTime } from '@/utils/datetime'
import { DRAWER_WIDTH } from '@/components/common/uiConstants'

type BootstrapPromptTemplate = ApiSchemas['BootstrapPromptTemplate']
type CreateRequest = ApiSchemas['CreateBootstrapPromptTemplateRequest']
type UpdateRequest = ApiSchemas['UpdateBootstrapPromptTemplateRequest']

interface FormModel {
  name: string
  description: string
  content: string
}

// ── Store & 依赖 ──────────────────────────────────────────────

const store = useBootstrapPromptTemplatesStore()
const message = useMessage()
const { t } = useI18n()

const {
  filterScopeOptions,
  getScopeTagType,
  deleteModalVisible,
  deletingItem,
  confirmDelete,
  executeDelete,
} = useEntityCrud<BootstrapPromptTemplate>({
  i18nPrefix: 'bootstrapPromptTemplates',
  deleteItem: store.deleteTemplate,
  isDeleting: (id) => store.deletingIds.includes(id),
})

// ── 表单状态 ──────────────────────────────────────────────────

const formRef = ref<FormInst | null>(null)
const drawerVisible = ref(false)
const editingItem = ref<BootstrapPromptTemplate | null>(null)

const formModel = reactive<FormModel>({
  name: '',
  description: '',
  content: '',
})

// ── 计算属性 ──────────────────────────────────────────────────

const hasActiveFilters = computed(
  () => store.searchQuery.trim().length > 0 || store.scopeFilter !== 'all',
)

const metrics = computed(() => [
  { label: t('bootstrapPromptTemplates.stats.total'), value: store.totalCount },
  { label: t('bootstrapPromptTemplates.stats.system'), value: store.systemCount },
  { label: t('bootstrapPromptTemplates.stats.user'), value: store.userCount },
])

const isEditMode = computed(() => Boolean(editingItem.value))
const isSystemScope = computed(() => editingItem.value?.scope === 'system')
const drawerTitle = computed(() =>
  isEditMode.value ? t('common.actions.edit') : t('bootstrapPromptTemplates.actions.create'),
)

const rules = computed<FormRules>(() => ({
  name: [
    {
      required: true,
      message: t('bootstrapPromptTemplates.validation.nameRequired'),
      trigger: ['input', 'blur'],
    },
  ],
}))

// ── 方法 ──────────────────────────────────────────────────────

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

const openEditDrawer = (item: BootstrapPromptTemplate): void => {
  editingItem.value = item
  formModel.name = item.name
  formModel.description = item.description ?? ''
  formModel.content = item.content ?? ''
  drawerVisible.value = true
}

const buildPayload = (): CreateRequest => {
  const payload: CreateRequest = { name: formModel.name.trim() }
  if (formModel.description.trim()) {
    payload.description = formModel.description.trim()
  }
  if (formModel.content.trim()) {
    payload.content = formModel.content.trim()
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
      await store.updateTemplate(editingItem.value.id, payload as UpdateRequest)
      message.success(t('bootstrapPromptTemplates.messages.updateSuccess'))
    } else {
      await store.createTemplate(payload)
      message.success(t('bootstrapPromptTemplates.messages.createSuccess'))
    }
    drawerVisible.value = false
    resetForm()
  } catch {
    // Error is handled by the store
  }
}

const formatDate = (dateStr: string | undefined): string =>
  dateStr ? formatDateTime(dateStr, { dateStyle: 'short' }) : '—'

// ── 生命周期 ──────────────────────────────────────────────────

onMounted(() => {
  store.loadTemplates()
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
    :title="t('bootstrapPromptTemplates.title')"
    :subtitle="t('bootstrapPromptTemplates.subtitle')"
    :metrics="metrics"
    :loading="store.loading"
    :empty="store.filteredItems.length === 0"
    :empty-description="
      hasActiveFilters
        ? t('bootstrapPromptTemplates.empty.filtered')
        : t('bootstrapPromptTemplates.empty.default')
    "
  >
    <template #actions>
      <NButton secondary :loading="store.loading" @click="store.loadTemplates">
        <template #icon><IconCarbonRenew /></template>
        {{ t('common.actions.refresh') }}
      </NButton>
      <NButton type="primary" @click="openCreateDrawer">
        <template #icon><IconCarbonAdd /></template>
        {{ t('bootstrapPromptTemplates.actions.create') }}
      </NButton>
    </template>

    <template #filters>
      <NInput
        v-model:value="store.searchQuery"
        clearable
        class="lg:max-w-sm!"
        :placeholder="t('bootstrapPromptTemplates.filters.searchPlaceholder')"
      />
      <div class="flex flex-wrap gap-3">
        <NSelect v-model:value="store.scopeFilter" class="w-44!" :options="filterScopeOptions" />
        <NButton v-if="hasActiveFilters" quaternary @click="store.resetFilters()">
          {{ t('bootstrapPromptTemplates.filters.reset') }}
        </NButton>
      </div>
    </template>

    <template #empty-extra>
      <NButton v-if="hasActiveFilters" secondary @click="store.resetFilters()">
        {{ t('bootstrapPromptTemplates.filters.reset') }}
      </NButton>
      <NButton v-else type="primary" @click="openCreateDrawer">
        {{ t('bootstrapPromptTemplates.actions.createFirst') }}
      </NButton>
    </template>

    <!-- 卡片网格 -->
    <div class="grid grid-cols-1 gap-5 md:grid-cols-2 xl:grid-cols-3">
      <div
        v-for="item in store.filteredItems"
        :key="item.id"
        class="lf-interactive-card flex h-full flex-col gap-4 p-5"
        @click="openEditDrawer(item)"
      >
        <!-- 头部：名称 + 作用域标签 -->
        <div class="flex items-start justify-between gap-4">
          <div class="min-w-0">
            <h2 class="truncate text-lg font-semibold text-lf-text-strong">
              {{ item.name }}
            </h2>
          </div>
          <NTag round size="small" :type="getScopeTagType(item.scope)">
            {{ t(`bootstrapPromptTemplates.scopes.${item.scope}`) }}
          </NTag>
        </div>

        <!-- 描述 -->
        <p
          class="line-clamp-2 text-sm leading-6 text-lf-text-muted"
          :class="{ 'italic text-lf-text-subtle': !item.description }"
        >
          {{ item.description || t('bootstrapPromptTemplates.card.noDescription') }}
        </p>

        <!-- 专属摘要：内容预览 -->
        <div v-if="item.content" class="lf-code-panel line-clamp-3">
          {{ item.content }}
        </div>
        <p v-else class="text-xs italic text-lf-text-subtle">
          {{ t('bootstrapPromptTemplates.card.noContent') }}
        </p>

        <!-- 底部：时间 + 操作 -->
        <div class="mt-auto border-t border-lf-border-soft pt-4">
          <div class="flex items-center justify-between gap-3">
            <span class="text-xs text-lf-text-subtle">
              {{ t('bootstrapPromptTemplates.card.updatedAt') }} {{ formatDate(item.updated_at) }}
            </span>
            <div class="flex items-center gap-2" @click.stop>
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
                @click="confirmDelete(item, $event)"
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
  <NDrawer v-model:show="drawerVisible" :width="DRAWER_WIDTH.m" placement="right">
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
        <NFormItem :label="t('bootstrapPromptTemplates.form.name')" path="name">
          <NInput
            v-model:value="formModel.name"
            :placeholder="t('bootstrapPromptTemplates.form.namePlaceholder')"
            :disabled="isSystemScope"
          />
        </NFormItem>

        <NFormItem :label="t('bootstrapPromptTemplates.form.description')" path="description">
          <NInput
            v-model:value="formModel.description"
            type="textarea"
            :placeholder="t('bootstrapPromptTemplates.form.descriptionPlaceholder')"
            :rows="3"
            :disabled="isSystemScope"
          />
        </NFormItem>

        <NFormItem :label="t('bootstrapPromptTemplates.form.content')" path="content">
          <PromptTemplateEditor
            v-model="formModel.content"
            :disabled="isSystemScope"
            :rows="8"
            variable-set="bootstrap"
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
            {{
              isEditMode
                ? t('bootstrapPromptTemplates.actions.submitUpdate')
                : t('bootstrapPromptTemplates.actions.submitCreate')
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
      deletingItem ? t('bootstrapPromptTemplates.delete.confirm', { name: deletingItem.name }) : ''
    "
    :positive-text="t('common.actions.deleteConfirmAction')"
    :negative-text="t('common.cancel')"
    :loading="deletingItem ? store.deletingIds.includes(deletingItem.id) : false"
    @positive-click="executeDelete"
  />
</template>
