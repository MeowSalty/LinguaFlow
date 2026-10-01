<script setup lang="ts">
import {
  NAlert,
  NButton,
  NDrawer,
  NDrawerContent,
  NForm,
  NFormItem,
  NInput,
  NModal,
  NTag,
  useMessage,
  type FormInst,
  type FormRules,
} from 'naive-ui'
import { useI18n } from 'vue-i18n'

import { useOrganizationScope } from '@/composables/useOrganizationScope'
import OrganizationScopeSelect from '@/components/organizations/OrganizationScopeSelect.vue'
import CopyToOrganization from '@/components/organizations/CopyToOrganization.vue'
import { onOrganizationInvalidated } from '@/utils/organization-scope'
import { captureSession, isSessionCurrent, sessionGeneration } from '@/api/session-context'
import type { ApiSchemas } from '@/api/client'
import ScopeFilterTabs from '@/components/common/ScopeFilterTabs.vue'
import ProfileConfigEditor from '@/components/templates/ProfileConfigEditor.vue'
import {
  createProfileConfig,
  readProfileConfig,
  buildProfileConfigInput,
} from '@/utils/execution-profile-config'
import { useEntityCrud } from '@/composables/useEntityCrud'
import { useStoreErrorToast } from '@/composables/useStoreErrorToast'
import { useExecutionProfilesStore } from '@/stores/executionProfiles'
import { formatDateTime } from '@/utils/datetime'
import { DRAWER_WIDTH } from '@/components/common/uiConstants'

type ExecutionProfile = ApiSchemas['ExecutionProfile']
type ExecutionProfileConfig = ApiSchemas['ExecutionProfileConfig']
type CreateRequest = ApiSchemas['CreateExecutionProfileRequest']
type UpdateRequest = ApiSchemas['UpdateExecutionProfileRequest']

interface FormModel {
  name: string
  description: string
  config: ExecutionProfileConfig
}

// ── 默认配置 ──────────────────────────────────────────────────

function deepClone<T>(obj: T): T {
  return JSON.parse(JSON.stringify(obj))
}

// ── Store & 依赖 ──────────────────────────────────────────────

const store = useExecutionProfilesStore()
const { orgId, canWrite, setScope } = useOrganizationScope((id) => {
  store.setOrganization(id)
  void store.loadProfiles(id)
})
const message = useMessage()
const { t } = useI18n()

const { getScopeTagType, deleteModalVisible, deletingItem, confirmDelete, executeDelete } =
  useEntityCrud<ExecutionProfile>({
    i18nPrefix: 'executionProfiles',
    deleteItem: store.deleteProfile,
    isDeleting: (id) => store.deletingIds.includes(id),
  })

// ── 表单状态 ──────────────────────────────────────────────────

const formRef = ref<FormInst | null>(null)
const configEditorRef = ref<InstanceType<typeof ProfileConfigEditor> | null>(null)
const drawerVisible = ref(false)
const originalConfig = ref<ExecutionProfileConfig | undefined>()
const incompatibleConfig = ref(false)
const submitting = ref(false)
const pendingFormWrites = ref(0)
let formGeneration = 0
const editingItem = ref<ExecutionProfile | null>(null)

const formModel = reactive<FormModel>({
  name: '',
  description: '',
  config: createProfileConfig(),
})

// ── 计算属性 ──────────────────────────────────────────────────

const hasActiveFilters = computed(
  () => store.searchQuery.trim().length > 0 || store.scopeFilter !== 'all',
)

const filterTabs = computed(() => [
  { name: 'all', label: t('executionProfiles.filters.all'), count: store.totalCount },
  { name: 'system', label: t('executionProfiles.scopes.system'), count: store.systemCount },
  { name: 'user', label: t('executionProfiles.scopes.user'), count: store.userCount },
  { name: 'org', label: t('team.organization'), count: store.orgCount },
])

const isEditMode = computed(() => Boolean(editingItem.value))
const isSystemScope = computed(() => !store.canEdit(editingItem.value ?? undefined))
const drawerTitle = computed(() =>
  isSystemScope.value
    ? t('executionProfiles.actions.viewTitle')
    : isEditMode.value
      ? t('executionProfiles.actions.editTitle')
      : t('executionProfiles.actions.createTitle'),
)

const drawerSubtitle = computed(() => {
  if (!editingItem.value) return t('executionProfiles.form.createHint')
  return isSystemScope.value
    ? `${t('executionProfiles.scopes.system')} · ${editingItem.value.name}`
    : editingItem.value.name
})

const hasConfigError = computed(
  () =>
    incompatibleConfig.value ||
    !readProfileConfig(formModel.config).ok ||
    Boolean(configEditorRef.value?.configError),
)

/** 卡片是否有任一启用的配置特征标签（无则展示「无启用能力」占位文案） */
const hasFeatures = (item: ExecutionProfile): boolean =>
  Boolean(
    item.config?.protect?.enabled ||
    item.config?.ruby?.enabled ||
    item.config?.repair?.enabled ||
    item.config?.postprocess?.enabled ||
    item.config?.glossary?.bootstrap?.enabled ||
    item.config?.context?.enabled ||
    item.config?.qa?.enabled,
  )

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

const resetForm = (): void => {
  formGeneration++
  submitting.value = false
  incompatibleConfig.value = false
  originalConfig.value = undefined
  formModel.name = ''
  formModel.description = ''
  formModel.config = createProfileConfig()
  editingItem.value = null
}

const openCreateDrawer = (): void => {
  resetForm()
  drawerVisible.value = true
}

const openEditDrawer = (item: ExecutionProfile): void => {
  resetForm()
  editingItem.value = item
  formModel.name = item.name
  formModel.description = item.description ?? ''
  const result = readProfileConfig(item.config)
  incompatibleConfig.value = !result.ok
  if (result.ok) {
    originalConfig.value = deepClone(result.config)
    formModel.config = result.config
  }
  drawerVisible.value = true
}

const buildPayload = (): CreateRequest => {
  const payload: CreateRequest = {
    name: formModel.name.trim(),
    config: buildProfileConfigInput(
      formModel.config,
      editingItem.value ? originalConfig.value : undefined,
    ),
  }
  if (formModel.description.trim()) {
    payload.description = formModel.description.trim()
  }
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
  if (!store.canEdit(editingItem.value ?? undefined) || hasConfigError.value || submitting.value)
    return
  try {
    await formRef.value?.validate()
  } catch {
    return
  }

  if (!current() || hasConfigError.value) return
  const payload = buildPayload()
  submitting.value = true
  pendingFormWrites.value++

  try {
    if (isEditMode.value && editingItem.value) {
      await store.updateProfile(editingItem.value.id, payload as UpdateRequest)
      if (!current()) return
      message.success(t('executionProfiles.messages.updateSuccess'))
    } else {
      await store.createProfile(payload)
      if (!current()) return
      message.success(t('executionProfiles.messages.createSuccess'))
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

const cardDate = (item: ExecutionProfile): string => {
  const value = item.updated_at ?? item.created_at
  return value ? formatDateTime(value, { dateStyle: 'short' }) : '—'
}

const cardDateTitle = (item: ExecutionProfile): string => {
  const value = item.updated_at ?? item.created_at
  return value ? formatDateTime(value, { dateStyle: 'medium', timeStyle: 'short' }) : ''
}

// ── 生命周期 ──────────────────────────────────────────────────

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
})
onOrganizationInvalidated((id) => {
  if (id === orgId.value || id === editingItem.value?.owner_org_id) {
    drawerVisible.value = false
    deleteModalVisible.value = false
    resetForm()
  }
})
const copyToOrganization = async (item: ExecutionProfile, target: number) => {
  if (!readProfileConfig(item.config).ok) {
    message.error(t('configurationProfiles.incompatible'))
    return
  }
  const session = captureSession()
  await setScope(target)
  if (!isSessionCurrent(session) || orgId.value !== target) return
  store.setOrganization(target)
  openEditDrawer(item)
  editingItem.value = null
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
    :title="t('executionProfiles.title')"
    :subtitle="t('executionProfiles.subtitle')"
    :loading="store.loading"
    :empty="store.filteredItems.length === 0"
    :empty-description="
      hasActiveFilters
        ? t('executionProfiles.empty.filtered')
        : t('executionProfiles.empty.default')
    "
  >
    <template #actions>
      <NButton secondary :loading="store.loading" @click="store.loadProfiles(orgId)">
        {{ t('common.actions.refresh') }}
      </NButton>
      <NButton v-if="canWrite" type="primary" @click="openCreateDrawer">
        {{ t('executionProfiles.actions.create') }}
      </NButton>
    </template>

    <template #filters>
      <OrganizationScopeSelect :value="orgId" @update:value="setScope" />
      <ScopeFilterTabs
        :tabs="filterTabs"
        :value="store.scopeFilter"
        @update:value="(v: string) => (store.scopeFilter = v as ExecutionProfile['scope'] | 'all')"
      />
      <NInput
        v-model:value="store.searchQuery"
        clearable
        class="lg:max-w-sm!"
        :placeholder="t('executionProfiles.filters.searchPlaceholder')"
      />
    </template>

    <template #empty-extra>
      <NButton v-if="hasActiveFilters" secondary @click="store.resetFilters()">
        {{ t('executionProfiles.filters.reset') }}
      </NButton>
      <NButton v-else-if="canWrite" type="primary" @click="openCreateDrawer">
        {{ t('executionProfiles.actions.createFirst') }}
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
                : t(`executionProfiles.scopes.${item.scope}`)
            }}
          </NTag>
        </div>

        <!-- 描述 -->
        <p
          class="line-clamp-2 text-sm leading-6"
          :class="item.description ? 'text-lf-text-muted' : 'text-lf-text-subtle'"
        >
          {{ item.description || t('executionProfiles.card.noDescription') }}
        </p>

        <!-- 专属摘要：配置特征标签 -->
        <div v-if="hasFeatures(item)" class="flex flex-wrap gap-1.5">
          <NTag v-if="item.config?.protect?.enabled" size="small" :bordered="false">
            {{ t('executionProfiles.feature.protect') }}:
            {{ item.config.protect.rules?.length ?? 0 }}
          </NTag>
          <NTag v-if="item.config?.ruby?.enabled" size="small" :bordered="false">
            {{ t('executionProfiles.feature.ruby') }}
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
          <NTag v-if="item.config?.qa?.enabled" size="small" :bordered="false">
            {{ t('executionProfiles.feature.qa') }}
          </NTag>
        </div>
        <p v-else class="text-xs text-lf-text-subtle">
          {{ t('executionProfiles.card.noFeatures') }}
        </p>

        <!-- 底部：更新时间 + 操作 -->
        <div class="mt-auto border-t border-lf-border-soft pt-4">
          <div class="flex items-center justify-between gap-3">
            <span class="text-xs text-lf-text-subtle" :title="cardDateTitle(item)">
              {{ t('executionProfiles.card.updatedAt') }} {{ cardDate(item) }}
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
  <NDrawer v-model:show="drawerVisible" :width="DRAWER_WIDTH.m" placement="right">
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
        <NFormItem :label="t('executionProfiles.form.name')" path="name">
          <NInput
            v-model:value="formModel.name"
            :placeholder="t('executionProfiles.form.namePlaceholder')"
            :disabled="isSystemScope || incompatibleConfig || submitting"
          />
        </NFormItem>

        <NFormItem :label="t('executionProfiles.form.description')" path="description">
          <NInput
            v-model:value="formModel.description"
            type="textarea"
            :placeholder="t('executionProfiles.form.descriptionPlaceholder')"
            :rows="3"
            :disabled="isSystemScope || incompatibleConfig || submitting"
          />
        </NFormItem>

        <NAlert v-if="incompatibleConfig" type="error" :bordered="false" class="mb-4">
          {{ t('configurationProfiles.incompatible') }}
        </NAlert>
        <p class="mb-4 text-xs text-lf-text-subtle">{{ t('configurationProfiles.effect') }}</p>
        <!-- 翻译配置编辑器 -->
        <ProfileConfigEditor
          v-if="!incompatibleConfig"
          :key="formGeneration"
          :allow-checks-default="!editingItem || originalConfig?.qa?.checks === undefined"
          ref="configEditorRef"
          :config="formModel.config"
          :disabled="isSystemScope || incompatibleConfig || submitting"
          @update:config="formModel.config = $event"
        />
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
            :disabled="hasConfigError || submitting"
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
    :title="t('common.actions.confirmDelete')"
    :content="
      deletingItem ? t('executionProfiles.delete.confirm', { name: deletingItem.name }) : ''
    "
    :positive-text="t('common.actions.deleteConfirmAction')"
    :negative-text="t('common.cancel')"
    :loading="deletingItem ? store.deletingIds.includes(deletingItem.id) : false"
    @positive-click="executeDelete"
  />
</template>
