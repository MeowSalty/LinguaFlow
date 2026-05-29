<script setup lang="ts">
import {
  useMessage,
  type DropdownOption,
  type FormInst,
  type FormRules,
  type SelectOption,
} from 'naive-ui'
import { useI18n } from 'vue-i18n'

import { type ApiSchemas } from '@/api/client'
import { useProjectsStore } from '@/stores/projects'

type Project = ApiSchemas['Project']
type ResourceScope = Project['resource_scope']

interface ProjectFormModel {
  name: string
  source_lang: string
  target_lang: string
  resource_scope: ResourceScope
  owner_org_id: number | null
}

const route = useRoute()
const projects = useProjectsStore()
const message = useMessage()
const { t } = useI18n()
const formRef = ref<FormInst | null>(null)
const drawerVisible = ref(false)
const editingProject = ref<Project | null>(null)

const formModel = reactive<ProjectFormModel>({
  name: '',
  source_lang: 'auto',
  target_lang: 'zh-Hans',
  resource_scope: 'project',
  owner_org_id: null,
})

const targetLanguageOptions = computed<SelectOption[]>(() => [
  { label: t('projects.languages.zhHans'), value: 'zh-Hans' },
  { label: t('projects.languages.zhHant'), value: 'zh-Hant' },
  { label: t('projects.languages.enUS'), value: 'en-US' },
  { label: t('projects.languages.enGB'), value: 'en-GB' },
  { label: t('projects.languages.ja'), value: 'ja' },
  { label: t('projects.languages.ko'), value: 'ko' },
  { label: t('projects.languages.fr'), value: 'fr' },
  { label: t('projects.languages.de'), value: 'de' },
  { label: t('projects.languages.es'), value: 'es' },
])

const sourceLanguageOptions = computed<SelectOption[]>(() => [
  { label: t('projects.languages.auto'), value: 'auto' },
  ...targetLanguageOptions.value,
])

const scopeOptions = computed<SelectOption[]>(() => [
  { label: t('projects.scopes.project'), value: 'project' },
  { label: t('projects.scopes.organization'), value: 'organization' },
])

const filterScopeOptions = computed<SelectOption[]>(() => [
  { label: t('projects.filters.allScopes'), value: 'all' },
  ...scopeOptions.value,
])

const organizationOptions = computed<SelectOption[]>(() =>
  projects.organizations.map((organization) => ({
    label: organization.display_name?.trim() || organization.name,
    value: organization.id,
  })),
)

const hasActiveFilters = computed(
  () => projects.searchQuery.trim().length > 0 || projects.scopeFilter !== 'all',
)

const isEditMode = computed(() => Boolean(editingProject.value))
const drawerTitle = computed(() =>
  isEditMode.value ? t('projects.edit.title') : t('projects.create.title'),
)
const drawerDescription = computed(() =>
  isEditMode.value ? t('projects.edit.description') : t('projects.create.description'),
)
const submitButtonText = computed(() =>
  isEditMode.value ? t('projects.actions.submitUpdate') : t('projects.actions.submitCreate'),
)
const submitting = computed(() => projects.creating || projects.updating)

const rules = computed<FormRules>(() => ({
  name: [
    {
      required: true,
      message: t('projects.validation.nameRequired'),
      trigger: ['input', 'blur'],
    },
  ],
  source_lang: [
    {
      required: true,
      message: t('projects.validation.sourceLangRequired'),
      trigger: ['change', 'blur'],
    },
  ],
  target_lang: [
    {
      required: true,
      message: t('projects.validation.targetLangRequired'),
      trigger: ['change', 'blur'],
    },
  ],
  owner_org_id: [
    {
      validator: () => formModel.resource_scope !== 'organization' || Boolean(formModel.owner_org_id),
      message: t('projects.validation.organizationRequired'),
      trigger: ['change', 'blur'],
    },
  ],
}))

const resetForm = (): void => {
  formModel.name = ''
  formModel.source_lang = 'auto'
  formModel.target_lang = 'en-US'
  formModel.resource_scope = 'project'
  formModel.owner_org_id = null
}

const ensureOrganizationsLoaded = async (): Promise<void> => {
  if (projects.organizations.length === 0 && !projects.organizationsLoading) {
    await projects.loadOrganizations()
  }
}

const openCreateDrawer = async (): Promise<void> => {
  editingProject.value = null
  resetForm()
  drawerVisible.value = true
  await ensureOrganizationsLoaded()
}

const openEditDrawer = async (project: Project): Promise<void> => {
  editingProject.value = project
  formModel.name = project.name
  formModel.source_lang = project.source_lang || 'auto'
  formModel.target_lang = project.target_lang || 'en-US'
  formModel.resource_scope = project.resource_scope
  formModel.owner_org_id = project.owner_org_id ?? null
  drawerVisible.value = true
  await ensureOrganizationsLoaded()
}

const closeCreateDrawer = (): void => {
  drawerVisible.value = false
  editingProject.value = null
  resetForm()
}

const formatDate = (value?: string): string => {
  if (!value) {
    return t('projects.card.noDate')
  }

  return new Intl.DateTimeFormat('zh-CN', {
    year: 'numeric',
    month: '2-digit',
    day: '2-digit',
    hour: '2-digit',
    minute: '2-digit',
  }).format(new Date(value))
}

const getOrganizationName = (orgId?: number): string => {
  const organization = projects.organizations.find((item) => item.id === orgId)
  return organization?.display_name?.trim() || organization?.name || t('projects.card.unknownOrganization')
}

const buildProjectPayload = (): ApiSchemas['CreateProjectRequest'] => {
  const payload: ApiSchemas['CreateProjectRequest'] = {
    name: formModel.name.trim(),
    source_lang: formModel.source_lang.trim(),
    target_lang: formModel.target_lang.trim(),
    resource_scope: formModel.resource_scope,
  }

  if (formModel.resource_scope === 'organization' && formModel.owner_org_id) {
    payload.owner_org_id = formModel.owner_org_id
  }

  return payload
}

const submitProject = async (): Promise<void> => {
  await formRef.value?.validate()

  try {
    if (editingProject.value) {
      const payload = buildProjectPayload()
      await projects.updateProject(editingProject.value.id, {
        name: payload.name,
        source_lang: payload.source_lang,
        target_lang: payload.target_lang,
        resource_scope: payload.resource_scope,
      })
      message.success(t('projects.messages.updateSuccess'))
    } else {
      await projects.createProject(buildProjectPayload())
      message.success(t('projects.messages.createSuccess'))
    }

    closeCreateDrawer()
  } catch (error) {
    console.error(error)
    message.error(
      editingProject.value
        ? projects.updateError || t('projects.messages.updateFailed')
        : projects.createError || t('projects.messages.createFailed'),
    )
  }
}

const moreActionOptions = computed<DropdownOption[]>(() => [
  { label: t('projects.actions.jobs'), key: 'jobs' },
  { label: t('projects.actions.glossary'), key: 'glossary' },
  { label: t('projects.actions.backends'), key: 'backends' },
])

const selectMoreAction = (key: string | number): void => {
  const featureKeyMap: Record<string, string> = {
    jobs: 'projects.features.jobs',
    glossary: 'projects.features.glossary',
    backends: 'projects.features.backends',
  }
  showPlaceholder(featureKeyMap[String(key)] ?? 'projects.features.details')
}

const deleteSelectedProject = async (project: Project): Promise<void> => {
  try {
    await projects.deleteProject(project.id)
    message.success(t('projects.messages.deleteSuccess'))
  } catch (error) {
    console.error(error)
    message.error(projects.deleteError || t('projects.messages.deleteFailed'))
  }
}

const showPlaceholder = (featureKey: string): void => {
  message.info(t('projects.messages.featureComingSoon', { feature: t(featureKey) }))
}

watch(
  () => formModel.resource_scope,
  (scope) => {
    if (scope === 'project') {
      formModel.owner_org_id = null
    }
  },
)

onMounted(() => {
  projects.loadProjects()

  if (route.query.create === '1') {
    openCreateDrawer()
  }
})
</script>

<template>
  <div class="space-y-6">
    <NCard :bordered="false" class="overflow-hidden shadow-sm shadow-slate-200/60">
      <div class="flex flex-col gap-6 lg:flex-row lg:items-center lg:justify-between">
        <div class="space-y-3">
          <div
            class="inline-flex items-center rounded-full bg-brand-50 px-3 py-1 text-xs font-medium text-brand-600"
          >
            {{ t('projects.eyebrow') }}
          </div>
          <div>
            <h1 class="text-3xl font-semibold tracking-tight text-slate-950">
              {{ t('projects.title') }}
            </h1>
            <p class="mt-2 max-w-2xl text-sm leading-6 text-slate-500">
              {{ t('projects.subtitle') }}
            </p>
          </div>
        </div>
        <div class="flex flex-wrap gap-3">
          <NButton secondary :loading="projects.loading" @click="projects.loadProjects">
            {{ t('projects.actions.refresh') }}
          </NButton>
          <NButton type="primary" @click="openCreateDrawer">
            {{ t('projects.actions.create') }}
          </NButton>
        </div>
      </div>
    </NCard>

    <div class="grid grid-cols-1 gap-4 md:grid-cols-4">
      <NCard :bordered="false" class="shadow-sm shadow-slate-200/60">
        <div class="text-xs font-medium text-slate-500">{{ t('projects.stats.total') }}</div>
        <div class="mt-2 text-2xl font-semibold text-slate-950">{{ projects.projectCount }}</div>
      </NCard>
      <NCard :bordered="false" class="shadow-sm shadow-slate-200/60">
        <div class="text-xs font-medium text-slate-500">{{ t('projects.stats.personal') }}</div>
        <div class="mt-2 text-2xl font-semibold text-slate-950">
          {{ projects.personalProjectCount }}
        </div>
      </NCard>
      <NCard :bordered="false" class="shadow-sm shadow-slate-200/60">
        <div class="text-xs font-medium text-slate-500">{{ t('projects.stats.organization') }}</div>
        <div class="mt-2 text-2xl font-semibold text-slate-950">
          {{ projects.organizationProjectCount }}
        </div>
      </NCard>
      <NCard :bordered="false" class="shadow-sm shadow-slate-200/60">
        <div class="text-xs font-medium text-slate-500">{{ t('projects.stats.languagePairs') }}</div>
        <div class="mt-2 text-2xl font-semibold text-slate-950">
          {{ projects.languagePairCount }}
        </div>
      </NCard>
    </div>

    <NCard :bordered="false" class="shadow-sm shadow-slate-200/60">
      <div class="flex flex-col gap-3 lg:flex-row lg:items-center lg:justify-between">
        <NInput
          v-model:value="projects.searchQuery"
          clearable
          class="lg:max-w-sm"
          :placeholder="t('projects.filters.searchPlaceholder')"
        />
        <div class="flex flex-wrap gap-3">
          <NSelect
            v-model:value="projects.scopeFilter"
            class="w-44"
            :options="filterScopeOptions"
          />
          <NButton v-if="hasActiveFilters" quaternary @click="projects.resetFilters">
            {{ t('projects.filters.reset') }}
          </NButton>
        </div>
      </div>
    </NCard>

    <NAlert v-if="projects.error" type="error" :bordered="false">
      {{ projects.error }}
    </NAlert>

    <div v-if="projects.loading" class="grid grid-cols-1 gap-5 md:grid-cols-2 xl:grid-cols-3">
      <NCard v-for="index in 6" :key="index" :bordered="false" class="shadow-sm shadow-slate-200/60">
        <NSkeleton text :repeat="4" />
      </NCard>
    </div>

    <NEmpty
      v-else-if="projects.filteredItems.length === 0"
      class="rounded-2xl bg-white py-16 shadow-sm shadow-slate-200/60"
      :description="hasActiveFilters ? t('projects.empty.filtered') : t('projects.empty.default')"
    >
      <template #extra>
        <NButton v-if="hasActiveFilters" secondary @click="projects.resetFilters">
          {{ t('projects.filters.reset') }}
        </NButton>
        <NButton v-else type="primary" @click="openCreateDrawer">
          {{ t('projects.actions.createFirst') }}
        </NButton>
      </template>
    </NEmpty>

    <div v-else class="grid grid-cols-1 gap-5 md:grid-cols-2 xl:grid-cols-3">
      <NCard
        v-for="project in projects.filteredItems"
        :key="project.id"
        hoverable
        :bordered="false"
        class="group shadow-sm shadow-slate-200/60 transition-all duration-200 hover:-translate-y-0.5 hover:shadow-lg hover:shadow-slate-200/80"
      >
        <div class="flex h-full flex-col gap-5">
          <div class="flex items-start justify-between gap-4">
            <div class="min-w-0">
              <h2 class="truncate text-lg font-semibold text-slate-950">
                {{ project.name }}
              </h2>
              <p class="mt-1 text-xs text-slate-400">
                {{ t('projects.card.projectId', { id: project.id }) }}
              </p>
            </div>
            <NTag
              round
              size="small"
              :type="project.resource_scope === 'organization' ? 'info' : 'success'"
            >
              {{ t(`projects.scopes.${project.resource_scope}`) }}
            </NTag>
          </div>

          <div class="rounded-2xl bg-slate-50 p-4">
            <div class="flex items-center justify-between gap-3">
              <div class="min-w-0">
                <div class="text-xs text-slate-400">{{ t('projects.card.source') }}</div>
                <div class="mt-1 truncate text-base font-semibold text-slate-800">
                  {{ project.source_lang || '-' }}
                </div>
              </div>
              <div class="rounded-full bg-white px-3 py-1 text-sm text-brand-500 shadow-sm">→</div>
              <div class="min-w-0 text-right">
                <div class="text-xs text-slate-400">{{ t('projects.card.target') }}</div>
                <div class="mt-1 truncate text-base font-semibold text-slate-800">
                  {{ project.target_lang || '-' }}
                </div>
              </div>
            </div>
          </div>

          <div class="mt-auto space-y-2 text-xs text-slate-500">
            <div v-if="project.resource_scope === 'organization'" class="flex justify-between gap-3">
              <span>{{ t('projects.card.organization') }}</span>
              <span class="truncate font-medium text-slate-700">
                {{ getOrganizationName(project.owner_org_id) }}
              </span>
            </div>
            <div class="flex justify-between gap-3">
              <span>{{ t('projects.card.updatedAt') }}</span>
              <span class="font-medium text-slate-700">
                {{ formatDate(project.updated_at ?? project.created_at) }}
              </span>
            </div>
          </div>

          <div class="border-t border-slate-100 pt-4">
            <div class="flex items-center justify-between gap-3">
              <NButton
                text
                type="primary"
                class="font-medium"
                @click="showPlaceholder('projects.features.details')"
              >
                {{ t('projects.actions.details') }}
              </NButton>
              <div class="flex items-center gap-1.5">
                <NButton quaternary size="small" @click="openEditDrawer(project)">
                  {{ t('projects.actions.edit') }}
                </NButton>
                <NDropdown
                  trigger="click"
                  :options="moreActionOptions"
                  placement="bottom-end"
                  @select="selectMoreAction"
                >
                  <NButton quaternary size="small">
                    {{ t('projects.actions.more') }}
                  </NButton>
                </NDropdown>
                <NPopconfirm
                  :positive-text="t('projects.actions.confirmDelete')"
                  :negative-text="t('projects.actions.cancel')"
                  @positive-click="deleteSelectedProject(project)"
                >
                  <template #trigger>
                    <NButton
                      quaternary
                      size="small"
                      type="error"
                      :loading="projects.isDeletingProject(project.id)"
                    >
                      {{ t('projects.actions.delete') }}
                    </NButton>
                  </template>
                  {{ t('projects.delete.confirm', { name: project.name }) }}
                </NPopconfirm>
              </div>
            </div>
          </div>
        </div>
      </NCard>
    </div>

    <NDrawer v-model:show="drawerVisible" :width="480" placement="right">
      <NDrawerContent :title="drawerTitle" closable>
        <div class="mb-6 rounded-2xl bg-slate-50 p-4 text-sm leading-6 text-slate-500">
          {{ drawerDescription }}
        </div>

        <NAlert v-if="projects.organizationsError" type="warning" :bordered="false" class="mb-4">
          {{ projects.organizationsError }}
        </NAlert>

        <NForm ref="formRef" :model="formModel" :rules="rules" label-placement="top">
          <NFormItem path="name" :label="t('projects.form.name')">
            <NInput
              v-model:value="formModel.name"
              :placeholder="t('projects.form.namePlaceholder')"
              maxlength="80"
              show-count
            />
          </NFormItem>

          <div class="grid grid-cols-1 gap-3 sm:grid-cols-2">
            <NFormItem path="source_lang" :label="t('projects.form.sourceLang')">
              <NSelect
                v-model:value="formModel.source_lang"
                filterable
                tag
                :options="sourceLanguageOptions"
                :placeholder="t('projects.form.languagePlaceholder')"
              />
            </NFormItem>
            <NFormItem path="target_lang" :label="t('projects.form.targetLang')">
              <NSelect
                v-model:value="formModel.target_lang"
                filterable
                tag
                :options="targetLanguageOptions"
                :placeholder="t('projects.form.languagePlaceholder')"
              />
            </NFormItem>
          </div>

          <NFormItem path="resource_scope" :label="t('projects.form.scope')">
            <NRadioGroup v-model:value="formModel.resource_scope">
              <NSpace>
                <NRadioButton value="project">
                  {{ t('projects.scopes.project') }}
                </NRadioButton>
                <NRadioButton value="organization">
                  {{ t('projects.scopes.organization') }}
                </NRadioButton>
              </NSpace>
            </NRadioGroup>
          </NFormItem>

          <NFormItem
            v-if="formModel.resource_scope === 'organization'"
            path="owner_org_id"
            :label="t('projects.form.organization')"
          >
            <NSelect
              v-model:value="formModel.owner_org_id"
              filterable
              clearable
              :loading="projects.organizationsLoading"
              :options="organizationOptions"
              :placeholder="t('projects.form.organizationPlaceholder')"
            />
          </NFormItem>
        </NForm>

        <template #footer>
          <div class="flex justify-end gap-3">
            <NButton :disabled="submitting" @click="closeCreateDrawer">
              {{ t('projects.actions.cancel') }}
            </NButton>
            <NButton type="primary" :loading="submitting" @click="submitProject">
              {{ submitButtonText }}
            </NButton>
          </div>
        </template>
      </NDrawerContent>
    </NDrawer>
  </div>
</template>
