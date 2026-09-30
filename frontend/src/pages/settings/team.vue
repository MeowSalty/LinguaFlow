<script setup lang="ts">
import { computed, onMounted, onUnmounted, reactive, ref, shallowRef, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import {
  NAlert,
  NButton,
  NCard,
  NEmpty,
  NForm,
  NFormItem,
  NInput,
  NModal,
  NSelect,
  NSkeleton,
  NTag,
  useDialog,
  useMessage,
} from 'naive-ui'
import { useI18n } from 'vue-i18n'
import type { ApiSchemas } from '@/api/client'
import * as api from '@/api/organizations'
import { captureSession, getSessionUserId, isSessionCurrent } from '@/api/session-context'
import { isAccessDenied } from '@/api/utils'
import { useOrganizationsStore } from '@/stores/organizations'
import { usePreferencesStore } from '@/stores/preferences'
import { useServiceStore } from '@/stores/service'
import {
  canRemoveMember,
  parseOrganizationId,
  type OrganizationRole,
} from '@/utils/organization-scope'
import { formatDateTime } from '@/utils/datetime'

const { t } = useI18n()
const router = useRouter()
const route = useRoute()
const store = useOrganizationsStore()
const preferences = usePreferencesStore()
const service = useServiceStore()
const message = useMessage()
const dialog = useDialog()
const orgId = computed(() => parseOrganizationId(route.query.org_id))
const organization = computed(() => store.items.find((org) => org.id === orgId.value) ?? null)
const canWrite = computed(() => organization.value != null && store.canWrite(organization.value.id))
const members = shallowRef<ApiSchemas['OrganizationMember'][]>([])
const activities = shallowRef<ApiSchemas['Activity'][]>([])
const loading = ref(false)
const busy = ref(false)
const error = ref<string | null>(null)
const activityError = ref<string | null>(null)
const formVisible = ref(false)
const editing = ref(false)
const form = reactive({ name: '', slug: '', display_name: '', description: '' })
const username = ref('')
const role = ref<OrganizationRole>('member')
let generation = 0
let contentOrgId: number | null = null
const roles = computed(() =>
  (['member', 'admin', 'owner'] as const).map((value) => ({
    value,
    label: t(`team.roles.${value}`),
  })),
)
const addRoles = computed(() =>
  organization.value?.current_user_role === 'owner'
    ? roles.value
    : roles.value.filter((item) => item.value === 'member'),
)
const options = computed(() =>
  store.items.map((org) => ({ value: org.id, label: org.display_name || org.name })),
)
const resources = [
  'projects',
  'backends',
  'promptTemplates',
  'bootstrapPromptTemplates',
  'prunePromptTemplates',
  'executionProfiles',
  'executionPlanTemplates',
] as const
const paths = {
  projects: '/projects',
  backends: '/backends',
  promptTemplates: '/prompt-templates',
  bootstrapPromptTemplates: '/bootstrap-prompt-templates',
  prunePromptTemplates: '/prune-prompt-templates',
  executionProfiles: '/execution-profiles',
  executionPlanTemplates: '/execution-plan-templates',
}

async function select(id: number | null) {
  preferences.setSelectedOrg(id)
  await router.replace({ query: { ...route.query, org_id: id == null ? undefined : String(id) } })
}
async function refreshDetails() {
  const id = orgId.value
  const request = ++generation
  const session = captureSession()
  if (contentOrgId !== id) {
    members.value = []
    activities.value = []
    contentOrgId = id
  }
  error.value = null
  activityError.value = null
  if (id === null) {
    loading.value = false
    return
  }
  loading.value = true
  const current = () => generation === request && isSessionCurrent(session)
  try {
    const [org, list] = await Promise.all([
      api.fetchOrganization(id),
      api.fetchOrganizationMembers(id),
    ])
    if (!current()) return
    store.accept(org)
    members.value = list.items
    try {
      const activity = await api.fetchOrganizationActivity(id)
      if (current()) activities.value = activity.items
    } catch (cause) {
      if (current()) {
        if (isAccessDenied(cause)) {
          members.value = []
          activities.value = []
          store.remove(id)
          preferences.setSelectedOrg(null)
          await select(null)
        } else
          activityError.value = cause instanceof Error ? cause.message : t('team.errors.activity')
      }
    }
  } catch (cause) {
    if (!current()) return
    error.value = cause instanceof Error ? cause.message : t('team.errors.load')
    if (isAccessDenied(cause)) {
      members.value = []
      activities.value = []
      store.remove(id)
      preferences.setSelectedOrg(null)
      await select(null)
    }
  } finally {
    if (current()) loading.value = false
  }
}
async function refresh() {
  await store.refresh()
  const explicit = parseOrganizationId(route.query.org_id)
  const valid = store.items.some((org) => org.id === explicit)
  if (!store.error && !valid) {
    const remembered = preferences.selectedOrgId
    const previous = orgId.value
    await select(store.items.some((org) => org.id === remembered) ? remembered : null)
    if (orgId.value !== previous) return
  }
  await refreshDetails()
}
function openForm(isEdit = false) {
  editing.value = isEdit
  const org = isEdit ? organization.value : null
  Object.assign(form, {
    name: org?.name ?? '',
    slug: org?.slug ?? '',
    display_name: org?.display_name ?? '',
    description: org?.description ?? '',
  })
  formVisible.value = true
}
async function save() {
  if (!form.name.trim() || !form.slug.trim()) {
    message.error(t('team.required'))
    return
  }
  const id = orgId.value
  const session = captureSession()
  const request = generation
  busy.value = true
  try {
    const body = { ...form, name: form.name.trim(), slug: form.slug.trim() }
    const org =
      editing.value && id !== null
        ? await api.updateOrganization(id, body)
        : await api.createOrganization(body)
    if (!isSessionCurrent(session) || request !== generation) return
    store.accept(org)
    formVisible.value = false
    message.success(t('team.saved'))
    await select(org.id)
  } catch (cause) {
    if (isSessionCurrent(session) && request === generation)
      message.error(cause instanceof Error ? cause.message : t('team.errors.save'))
  } finally {
    if (isSessionCurrent(session) && request === generation) busy.value = false
  }
}
async function memberAction(action: (id: number) => Promise<unknown>, leaving = false) {
  const id = orgId.value
  if (id === null) return
  const request = generation
  const session = captureSession()
  busy.value = true
  try {
    await action(id)
    if (!isSessionCurrent(session) || request !== generation) return
    if (leaving) {
      store.remove(id)
      await select(null)
      message.success(t('team.left'))
      return
    }
    username.value = ''
    message.success(t('team.saved'))
    await refresh()
  } catch (cause) {
    if (!isSessionCurrent(session) || request !== generation) return
    const detail = cause instanceof Error ? cause.message : t('team.errors.memberSave')
    await refresh()
    if (isSessionCurrent(session) && orgId.value === id) message.error(detail)
  } finally {
    if (isSessionCurrent(session)) busy.value = false
  }
}
function confirmRole(member: ApiSchemas['OrganizationMember'], value: OrganizationRole) {
  dialog.warning({
    title: t('team.role'),
    content: t('team.roleConfirm', { name: member.user.username, role: t(`team.roles.${value}`) }),
    positiveText: t('team.save'),
    negativeText: t('team.cancel'),
    onPositiveClick: () =>
      memberAction((id) => api.updateOrganizationMember(id, member.user.id, value)),
  })
}
function confirmRemove(member: ApiSchemas['OrganizationMember']) {
  const self = member.user.id === getSessionUserId()
  dialog.warning({
    title: t(self ? 'team.leave' : 'team.remove'),
    content: self
      ? t('team.leaveConfirm')
      : t('team.removeConfirm', { name: member.user.username }),
    positiveText: t(self ? 'team.leave' : 'team.remove'),
    negativeText: t('team.cancel'),
    onPositiveClick: () =>
      memberAction((id) => api.removeOrganizationMember(id, member.user.id), self),
  })
}
const visibility = () => {
  if (document.visibilityState === 'visible') void refresh()
}
watch(orgId, () => {
  formVisible.value = false
  username.value = ''
  role.value = 'member'
  busy.value = false
  void refreshDetails()
})
onMounted(() => {
  if (service.isLocal) {
    void router.replace('/settings/profile')
    return
  }
  void refresh()
  document.addEventListener('visibilitychange', visibility)
})
onUnmounted(() => {
  ++generation
  document.removeEventListener('visibilitychange', visibility)
})
</script>

<template>
  <div class="space-y-5">
    <div class="flex flex-wrap items-center justify-between gap-3">
      <div>
        <h2 class="text-xl font-semibold text-lf-text-strong">{{ t('team.title') }}</h2>
        <p class="mt-1 text-lf-text-muted">{{ t('team.subtitle') }}</p>
      </div>
      <div class="flex gap-2">
        <NButton :loading="store.loading || loading" @click="refresh">{{
          t('team.refresh')
        }}</NButton
        ><NButton type="primary" @click="openForm()">{{ t('team.create') }}</NButton>
      </div>
    </div>
    <NSelect
      :value="orgId"
      :options="options"
      :loading="store.loading"
      clearable
      :placeholder="t('team.select')"
      :aria-label="t('team.select')"
      @update:value="select"
    />
    <NAlert v-if="store.error || error" type="error">{{ store.error || error }}</NAlert>
    <NSkeleton v-if="loading && !organization" height="180px" />
    <NEmpty v-else-if="!organization" :description="t('team.empty')" class="py-12" />
    <template v-else>
      <NCard :title="organization.display_name || organization.name" size="small"
        ><template #header-extra
          ><NTag>{{ t(`team.roles.${organization.current_user_role}`) }}</NTag></template
        >
        <p class="text-lf-text-muted">{{ organization.description || organization.slug }}</p>
        <NButton v-if="canWrite" class="mt-3" secondary @click="openForm(true)">{{
          t('team.edit')
        }}</NButton></NCard
      >
      <NAlert v-if="!canWrite" type="info">{{ t('team.readOnly') }}</NAlert>
      <NCard :title="t('team.resources')" size="small"
        ><div class="grid grid-cols-2 gap-3 sm:grid-cols-3">
          <NButton
            v-for="resource in resources"
            :key="resource"
            secondary
            @click="router.push({ path: paths[resource], query: { org_id: String(orgId) } })"
            >{{ t(`team.${resource}`) }}</NButton
          >
        </div></NCard
      >
      <NCard :title="t('team.members')" size="small">
        <form
          v-if="canWrite"
          class="mb-5 flex flex-wrap gap-3"
          @submit.prevent="
            memberAction((id) => api.addOrganizationMember(id, { username: username.trim(), role }))
          "
        >
          <NInput
            v-model:value="username"
            class="min-w-48 flex-1!"
            :placeholder="t('team.username')"
            :aria-label="t('team.username')"
          /><NSelect
            v-model:value="role"
            class="w-32!"
            :options="addRoles"
            :aria-label="t('team.role')"
          /><NButton
            attr-type="submit"
            type="primary"
            :disabled="!username.trim()"
            :loading="busy"
            >{{ t('team.addMember') }}</NButton
          >
          <p class="w-full text-xs text-lf-text-subtle">{{ t('team.usernameHint') }}</p>
        </form>
        <div
          v-for="member in members"
          :key="member.user.id"
          class="flex flex-wrap items-center justify-between gap-3 border-b border-lf-border-soft py-3 last:border-0"
        >
          <div>
            <span class="font-medium">{{ member.user.display_name || member.user.username }}</span
            ><span class="ml-2 text-xs text-lf-text-subtle">{{ member.user.username }}</span>
          </div>
          <div class="flex items-center gap-3">
            <NSelect
              v-if="organization.current_user_role === 'owner'"
              class="w-32!"
              :value="member.role"
              :options="roles"
              :disabled="busy"
              :aria-label="t('team.role')"
              @update:value="(value) => confirmRole(member, value)"
            /><NTag v-else>{{ t(`team.roles.${member.role}`) }}</NTag
            ><NButton
              v-if="
                canRemoveMember(
                  organization.current_user_role,
                  member.role,
                  member.user.id === getSessionUserId(),
                )
              "
              text
              type="error"
              :disabled="busy"
              @click="confirmRemove(member)"
              >{{
                t(member.user.id === getSessionUserId() ? 'team.leave' : 'team.remove')
              }}</NButton
            >
          </div>
        </div>
      </NCard>
      <NCard :title="t('team.activity')" size="small"
        ><NAlert v-if="activityError" type="warning">{{ activityError }}</NAlert
        ><NEmpty v-else-if="activities.length === 0" :description="t('team.activityEmpty')" />
        <div
          v-for="activity in activities"
          :key="activity.id"
          class="border-b border-lf-border-soft py-3 last:border-0"
        >
          <p>
            {{ activity.actor?.display_name || activity.actor?.username }}
            {{ activity.message || activity.action }}
          </p>
          <time class="text-xs text-lf-text-subtle">{{
            formatDateTime(activity.created_at, { dateStyle: 'medium', timeStyle: 'short' })
          }}</time>
        </div></NCard
      >
    </template>
    <NModal
      v-model:show="formVisible"
      preset="card"
      class="max-w-xl"
      :title="t(editing ? 'team.edit' : 'team.create')"
      ><NForm label-placement="top" @submit.prevent="save"
        ><NFormItem :label="t('team.name')" required><NInput v-model:value="form.name" /></NFormItem
        ><NFormItem :label="t('team.slug')" required><NInput v-model:value="form.slug" /></NFormItem
        ><NFormItem :label="t('team.displayName')"
          ><NInput v-model:value="form.display_name" /></NFormItem
        ><NFormItem :label="t('team.description')"
          ><NInput v-model:value="form.description" type="textarea" /></NFormItem></NForm
      ><template #footer
        ><div class="flex justify-end gap-3">
          <NButton @click="formVisible = false">{{ t('team.cancel') }}</NButton
          ><NButton type="primary" :loading="busy" @click="save">{{ t('team.save') }}</NButton>
        </div></template
      ></NModal
    >
  </div>
</template>
