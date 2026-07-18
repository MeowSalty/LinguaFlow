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

import { type ApiSchemas } from '@/api/client'
import { useAdminStore } from '@/stores/admin'

type User = ApiSchemas['User']

interface UserFormModel {
  username: string
  email: string
  display_name: string
  password: string
  role: 'user' | 'admin'
}

interface ResetPasswordFormModel {
  new_password: string
}

const admin = useAdminStore()
const message = useMessage()
const { t } = useI18n()
const formRef = ref<FormInst | null>(null)
const resetFormRef = ref<FormInst | null>(null)
const drawerVisible = ref(false)
const editingUser = ref<User | null>(null)
const deleteModalVisible = ref(false)
const disablingUser = ref<User | null>(null)
const resetPasswordModalVisible = ref(false)
const resettingPasswordUser = ref<User | null>(null)

const formModel = reactive<UserFormModel>({
  username: '',
  email: '',
  display_name: '',
  password: '',
  role: 'user',
})

const resetPasswordModel = reactive<ResetPasswordFormModel>({
  new_password: '',
})

const roleOptions = computed<SelectOption[]>(() => [
  { label: t('admin.users.roles.user'), value: 'user' },
  { label: t('admin.users.roles.admin'), value: 'admin' },
])

const filterRoleOptions = computed<SelectOption[]>(() => [
  { label: t('admin.users.filters.allRoles'), value: 'all' },
  ...roleOptions.value,
])

const filterActiveOptions = computed<SelectOption[]>(() => [
  { label: t('admin.users.filters.allStatuses'), value: 'all' },
  { label: t('admin.users.filters.active'), value: 'active' },
  { label: t('admin.users.filters.inactive'), value: 'inactive' },
])

const hasActiveFilters = computed(
  () =>
    admin.userSearchQuery.trim().length > 0 ||
    admin.userRoleFilter !== 'all' ||
    admin.userActiveFilter !== 'all',
)

const isEditMode = computed(() => Boolean(editingUser.value))
const drawerTitle = computed(() =>
  isEditMode.value ? t('admin.users.edit.title') : t('admin.users.create.title'),
)
const drawerDescription = computed(() =>
  isEditMode.value ? t('admin.users.edit.description') : t('admin.users.create.description'),
)
const submitting = computed(() => admin.creatingUser || admin.updatingUser)

const rules = computed<FormRules>(() => ({
  username: [
    {
      required: true,
      message: t('admin.users.validation.usernameRequired'),
      trigger: ['input', 'blur'],
    },
    {
      min: 3,
      max: 32,
      message: t('admin.users.validation.usernameLength'),
      trigger: ['input', 'blur'],
    },
  ],
  email: [
    {
      required: true,
      message: t('admin.users.validation.emailRequired'),
      trigger: ['input', 'blur'],
    },
    {
      type: 'email',
      message: t('admin.users.validation.emailInvalid'),
      trigger: ['input', 'blur'],
    },
  ],
  password: [
    {
      required: !isEditMode.value,
      message: t('admin.users.validation.passwordRequired'),
      trigger: ['input', 'blur'],
    },
    {
      min: 8,
      message: t('admin.users.validation.passwordMinLength'),
      trigger: ['input', 'blur'],
    },
  ],
  role: [
    {
      required: true,
      message: t('admin.users.validation.roleRequired'),
      trigger: ['change', 'blur'],
    },
  ],
}))

const resetPasswordRules = computed<FormRules>(() => ({
  new_password: [
    {
      required: true,
      message: t('admin.users.validation.passwordRequired'),
      trigger: ['input', 'blur'],
    },
    {
      min: 8,
      message: t('admin.users.validation.passwordMinLength'),
      trigger: ['input', 'blur'],
    },
  ],
}))

const resetForm = (): void => {
  formModel.username = ''
  formModel.email = ''
  formModel.display_name = ''
  formModel.password = ''
  formModel.role = 'user'
  editingUser.value = null
}

const openCreateDrawer = (): void => {
  resetForm()
  drawerVisible.value = true
}

const openEditDrawer = (user: User): void => {
  editingUser.value = user
  formModel.username = user.username
  formModel.email = user.email
  formModel.display_name = user.display_name ?? ''
  formModel.password = ''
  formModel.role = user.role as 'user' | 'admin'
  drawerVisible.value = true
}

const onSubmit = async (): Promise<void> => {
  try {
    await formRef.value?.validate()
  } catch {
    return
  }

  try {
    if (isEditMode.value && editingUser.value) {
      await admin.updateUser(editingUser.value.id, {
        display_name: formModel.display_name || undefined,
        email: formModel.email,
        role: formModel.role,
      })
      message.success(t('admin.users.messages.updateSuccess'))
    } else {
      await admin.createUser({
        username: formModel.username,
        email: formModel.email,
        password: formModel.password,
        display_name: formModel.display_name || undefined,
        role: formModel.role,
      })
      message.success(t('admin.users.messages.createSuccess'))
    }
    drawerVisible.value = false
    resetForm()
  } catch {
    // Error is handled by the store
  }
}

const confirmDisable = (user: User): void => {
  disablingUser.value = user
  deleteModalVisible.value = true
}

const executeDisable = async (): Promise<void> => {
  if (!disablingUser.value) return

  try {
    await admin.disableUser(disablingUser.value.id)
    message.success(t('admin.users.messages.disableSuccess'))
    deleteModalVisible.value = false
    disablingUser.value = null
  } catch {
    // Error is handled by the store
  }
}

const openResetPasswordModal = (user: User): void => {
  resettingPasswordUser.value = user
  resetPasswordModel.new_password = ''
  resetPasswordModalVisible.value = true
}

const executeResetPassword = async (): Promise<void> => {
  try {
    await resetFormRef.value?.validate()
  } catch {
    return
  }

  if (!resettingPasswordUser.value) return

  try {
    await admin.resetPassword(resettingPasswordUser.value.id, {
      new_password: resetPasswordModel.new_password,
    })
    message.success(t('admin.users.messages.resetPasswordSuccess'))
    resetPasswordModalVisible.value = false
    resettingPasswordUser.value = null
  } catch {
    // Error is handled by the store
  }
}

const resetFilters = (): void => {
  admin.userSearchQuery = ''
  admin.userRoleFilter = 'all'
  admin.userActiveFilter = 'all'
}

onMounted(() => {
  admin.loadUsers()
})

watch(
  () => admin.usersError,
  (err) => {
    if (err) {
      message.error(err, { duration: 0, closable: true })
      admin.usersError = null
    }
  },
)
</script>

<template>
  <div class="lf-page">
    <section class="lf-page-header">
      <div class="flex flex-col gap-6 lg:flex-row lg:items-center lg:justify-between">
        <div class="space-y-3">
          <div class="lf-eyebrow">
            {{ t('admin.eyebrow') }}
          </div>
          <div>
            <h1 class="text-3xl font-semibold tracking-tight text-lf-text-strong">
              {{ t('admin.users.title') }}
            </h1>
            <p class="mt-2 max-w-2xl text-sm leading-6 text-lf-text-muted">
              {{ t('admin.users.description') }}
            </p>
          </div>
        </div>
        <div class="flex flex-wrap gap-3">
          <NButton secondary :loading="admin.usersLoading" @click="admin.loadUsers">
            {{ t('admin.users.actions.refresh') }}
          </NButton>
          <NButton type="primary" @click="openCreateDrawer">
            {{ t('admin.users.actions.create') }}
          </NButton>
        </div>
      </div>
    </section>

    <div class="lf-panel px-4 py-3">
      <div class="flex flex-col gap-3 lg:flex-row lg:items-center lg:justify-between">
        <NInput
          v-model:value="admin.userSearchQuery"
          clearable
          class="lg:max-w-sm"
          :placeholder="t('admin.users.filters.searchPlaceholder')"
        />
        <div class="flex flex-wrap gap-3">
          <NSelect v-model:value="admin.userRoleFilter" class="w-36" :options="filterRoleOptions" />
          <NSelect
            v-model:value="admin.userActiveFilter"
            class="w-36"
            :options="filterActiveOptions"
          />
          <NButton v-if="hasActiveFilters" quaternary @click="resetFilters">
            {{ t('admin.users.filters.reset') }}
          </NButton>
        </div>
      </div>
    </div>

    <div v-if="admin.usersLoading" class="grid grid-cols-1 gap-5 md:grid-cols-2 xl:grid-cols-3">
      <div v-for="index in 6" :key="index" class="lf-panel p-5">
        <NSkeleton text :repeat="4" />
      </div>
    </div>

    <NEmpty
      v-else-if="admin.filteredUsers.length === 0"
      class="lf-panel py-16"
      :description="
        hasActiveFilters ? t('admin.users.empty.filtered') : t('admin.users.empty.default')
      "
    >
      <template #extra>
        <NButton v-if="hasActiveFilters" secondary @click="resetFilters">
          {{ t('admin.users.filters.reset') }}
        </NButton>
        <NButton v-else type="primary" @click="openCreateDrawer">
          {{ t('admin.users.actions.create') }}
        </NButton>
      </template>
    </NEmpty>

    <div v-else class="grid grid-cols-1 gap-5 md:grid-cols-2 xl:grid-cols-3">
      <div
        v-for="user in admin.filteredUsers"
        :key="user.id"
        class="lf-interactive-card group flex h-full flex-col gap-4 p-5"
      >
        <div class="flex items-start justify-between gap-4">
          <div class="min-w-0">
            <h2 class="truncate text-lg font-semibold tracking-tight text-lf-text-strong">
              {{ user.display_name || user.username }}
            </h2>
            <p class="mt-0.5 font-mono text-xs text-lf-text-subtle">@{{ user.username }}</p>
          </div>
          <div class="flex shrink-0 flex-wrap justify-end gap-2">
            <NTag
              round
              size="small"
              :bordered="false"
              :type="user.role === 'admin' ? 'warning' : 'default'"
            >
              {{ t(`admin.users.roles.${user.role}`) }}
            </NTag>
            <NTag round size="small" :bordered="false" :type="user.active ? 'success' : 'error'">
              {{
                user.active ? t('admin.users.filters.active') : t('admin.users.filters.inactive')
              }}
            </NTag>
          </div>
        </div>

        <div class="rounded-xl border border-lf-border-soft bg-lf-surface-muted px-3.5 py-3">
          <div class="text-[11px] font-medium tracking-wide text-lf-text-subtle uppercase">
            {{ t('admin.users.columns.email') }}
          </div>
          <div class="mt-1 truncate text-sm text-lf-text-strong">{{ user.email }}</div>
        </div>

        <div class="mt-auto border-t border-lf-border-soft pt-4">
          <div class="flex items-center justify-between gap-2">
            <NButton text type="primary" class="font-medium" @click="openEditDrawer(user)">
              {{ t('admin.users.actions.edit') }}
            </NButton>
            <div class="flex gap-2">
              <NButton text size="small" @click="openResetPasswordModal(user)">
                {{ t('admin.users.actions.resetPassword') }}
              </NButton>
              <NButton
                v-if="user.active"
                text
                type="error"
                size="small"
                :loading="admin.disablingUserIds.includes(user.id)"
                @click="confirmDisable(user)"
              >
                {{ t('admin.users.actions.disable') }}
              </NButton>
            </div>
          </div>
        </div>
      </div>
    </div>

    <NDrawer v-model:show="drawerVisible" :width="480" placement="right">
      <NDrawerContent :title="drawerTitle" :native-scrollbar="false">
        <template #header>
          <div>
            <div class="text-lg font-semibold">{{ drawerTitle }}</div>
            <div class="mt-1 text-xs text-lf-text-muted">{{ drawerDescription }}</div>
          </div>
        </template>

        <NForm
          ref="formRef"
          :model="formModel"
          :rules="rules"
          label-placement="top"
          require-mark-placement="right-hanging"
        >
          <NFormItem :label="t('admin.users.form.username')" path="username">
            <NInput
              v-model:value="formModel.username"
              :disabled="isEditMode"
              :placeholder="t('admin.users.form.usernamePlaceholder')"
            />
          </NFormItem>

          <NFormItem :label="t('admin.users.form.email')" path="email">
            <NInput
              v-model:value="formModel.email"
              :placeholder="t('admin.users.form.emailPlaceholder')"
            />
          </NFormItem>

          <NFormItem :label="t('admin.users.form.displayName')" path="display_name">
            <NInput
              v-model:value="formModel.display_name"
              :placeholder="t('admin.users.form.displayNamePlaceholder')"
            />
          </NFormItem>

          <NFormItem v-if="!isEditMode" :label="t('admin.users.form.password')" path="password">
            <NInput
              v-model:value="formModel.password"
              type="password"
              show-password-on="click"
              :placeholder="t('admin.users.form.passwordPlaceholder')"
            />
          </NFormItem>

          <NFormItem :label="t('admin.users.form.role')" path="role">
            <NSelect
              v-model:value="formModel.role"
              :options="roleOptions"
              :placeholder="t('admin.users.form.rolePlaceholder')"
            />
          </NFormItem>
        </NForm>

        <template #footer>
          <div class="flex justify-end gap-3">
            <NButton @click="drawerVisible = false">
              {{ t('workspace.common.cancel') }}
            </NButton>
            <NButton type="primary" :loading="submitting" @click="onSubmit">
              {{ t('workspace.common.save') }}
            </NButton>
          </div>
        </template>
      </NDrawerContent>
    </NDrawer>

    <NModal
      v-model:show="deleteModalVisible"
      preset="dialog"
      type="warning"
      :title="t('admin.users.actions.disable')"
      :content="
        disablingUser
          ? t('admin.users.disable.confirm', {
              name: disablingUser.display_name || disablingUser.username,
            })
          : ''
      "
      :positive-text="t('workspace.common.confirm')"
      :negative-text="t('workspace.common.cancel')"
      :loading="disablingUser ? admin.disablingUserIds.includes(disablingUser.id) : false"
      @positive-click="executeDisable"
    />

    <NModal v-model:show="resetPasswordModalVisible" preset="card" style="max-width: 420px">
      <template #header>
        <div class="text-lg font-semibold">{{ t('admin.users.resetPassword.title') }}</div>
      </template>

      <p class="mb-4 text-sm text-lf-text-muted">
        {{ t('admin.users.resetPassword.description') }}
      </p>

      <NForm
        ref="resetFormRef"
        :model="resetPasswordModel"
        :rules="resetPasswordRules"
        label-placement="top"
      >
        <NFormItem :label="t('admin.users.resetPassword.newPassword')" path="new_password">
          <NInput
            v-model:value="resetPasswordModel.new_password"
            type="password"
            show-password-on="click"
            :placeholder="t('admin.users.resetPassword.newPasswordPlaceholder')"
          />
        </NFormItem>
      </NForm>

      <template #footer>
        <div class="flex justify-end gap-3">
          <NButton @click="resetPasswordModalVisible = false">
            {{ t('workspace.common.cancel') }}
          </NButton>
          <NButton
            type="primary"
            :loading="
              resettingPasswordUser
                ? admin.resettingPasswordUserIds.includes(resettingPasswordUser.id)
                : false
            "
            @click="executeResetPassword"
          >
            {{ t('admin.users.resetPassword.confirm') }}
          </NButton>
        </div>
      </template>
    </NModal>
  </div>
</template>
