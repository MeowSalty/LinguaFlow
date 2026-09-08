<script setup lang="ts">
import { NText, useMessage, type DropdownOption, type FormInst, type FormRules } from 'naive-ui'
import { h } from 'vue'
import { useI18n } from 'vue-i18n'

import { type ApiSchemas } from '@/api/client'
import { useLanguageOptions } from '@/composables/useLanguageOptions'
import { useStoreErrorToast } from '@/composables/useStoreErrorToast'
import { useProjectsStore } from '@/stores/projects'
import { formatRelativeTime } from '@/utils/datetime'
import { DRAWER_WIDTH } from '@/components/common/uiConstants'

type Project = ApiSchemas['Project']

interface ProjectFormModel {
  name: string
  source_lang: string
  target_lang: string
  glossary_enabled: boolean
}

const route = useRoute()
const router = useRouter()
const projects = useProjectsStore()
const message = useMessage()
const { t } = useI18n()
const { targetLanguageOptions, sourceLanguageOptions } = useLanguageOptions()
const formRef = ref<FormInst | null>(null)
const drawerVisible = ref(false)
const editingProject = ref<Project | null>(null)
const deleteConfirmVisible = ref(false)
const deletingProject = ref<Project | null>(null)

const formModel = reactive<ProjectFormModel>({
  name: '',
  source_lang: 'auto',
  target_lang: 'zh-Hans',
  glossary_enabled: false,
})

const hasActiveFilters = computed(() => projects.searchQuery.trim().length > 0)

const metrics = computed(() => [
  { label: t('projects.stats.total'), value: projects.projectCount },
  { label: t('projects.stats.languagePairs'), value: projects.languagePairCount },
  { label: t('projects.stats.glossaryEnabled'), value: projects.glossaryEnabledCount },
])

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
const isProjectListRoute = computed(() => route.path === '/projects')

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
}))

const resetForm = (): void => {
  formModel.name = ''
  formModel.source_lang = 'auto'
  formModel.target_lang = 'zh-Hans'
  formModel.glossary_enabled = false
}

const openCreateDrawer = (): void => {
  editingProject.value = null
  resetForm()
  drawerVisible.value = true
}

const openEditDrawer = (project: Project): void => {
  editingProject.value = project
  formModel.name = project.name
  formModel.source_lang = project.source_lang || 'auto'
  formModel.target_lang = project.target_lang || 'en-US'
  formModel.glossary_enabled = project.glossary_enabled ?? false
  drawerVisible.value = true
}

const closeCreateDrawer = (): void => {
  drawerVisible.value = false
  editingProject.value = null
  resetForm()
}

const buildProjectPayload = (): ApiSchemas['CreateProjectRequest'] => {
  return {
    name: formModel.name.trim(),
    source_lang: formModel.source_lang.trim(),
    target_lang: formModel.target_lang.trim(),
    glossary_enabled: formModel.glossary_enabled,
  }
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
        glossary_enabled: payload.glossary_enabled,
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

const openProjectWorkspace = (project: Project, tab?: string): void => {
  void router.push({
    path: `/projects/${project.id}`,
    query: tab ? { tab } : undefined,
  })
}

const openDeleteConfirm = (project: Project): void => {
  deletingProject.value = project
  deleteConfirmVisible.value = true
}

const closeDeleteConfirm = (): void => {
  deleteConfirmVisible.value = false
  deletingProject.value = null
}

const confirmDelete = async (): Promise<void> => {
  if (!deletingProject.value) return
  try {
    await projects.deleteProject(deletingProject.value.id)
    message.success(t('projects.messages.deleteSuccess'))
    closeDeleteConfirm()
  } catch (error) {
    console.error(error)
    message.error(projects.deleteError || t('projects.messages.deleteFailed'))
  }
}

const cardDropdownOptions = computed<DropdownOption[]>(() => [
  { label: t('projects.actions.details'), key: 'details' },
  { label: t('common.actions.edit'), key: 'edit' },
  { label: t('projects.actions.jobs'), key: 'jobs' },
  { label: t('projects.actions.glossary'), key: 'glossary' },
  { type: 'divider', key: 'd1' },
  {
    key: 'delete',
    label: () => h(NText, { type: 'error' }, { default: () => t('common.actions.delete') }),
  },
])

const handleCardDropdownSelect = (project: Project, key: string | number): void => {
  switch (key) {
    case 'details':
      openProjectWorkspace(project)
      break
    case 'edit':
      void openEditDrawer(project)
      break
    case 'jobs':
      openProjectWorkspace(project, 'jobs')
      break
    case 'glossary':
      openProjectWorkspace(project, 'glossary')
      break
    case 'delete':
      openDeleteConfirm(project)
      break
  }
}

watch(isProjectListRoute, (isList) => {
  if (isList) {
    projects.loadProjects()
  }
})

onMounted(() => {
  if (!isProjectListRoute.value) {
    return
  }

  projects.loadProjects()

  if (route.query.create === '1') {
    openCreateDrawer()
  }
})

useStoreErrorToast(
  () => projects.error,
  () => {
    projects.error = null
  },
)
</script>

<template>
  <RouterView v-if="!isProjectListRoute" />
  <EntityListPage
    v-else
    :title="t('projects.title')"
    :subtitle="t('projects.subtitle')"
    :metrics="metrics"
    :loading="projects.loading"
    :empty="projects.filteredItems.length === 0"
    :empty-description="
      hasActiveFilters ? t('projects.empty.filtered') : t('projects.empty.default')
    "
  >
    <template #actions>
      <NButton secondary :loading="projects.loading" @click="projects.loadProjects">
        {{ t('common.actions.refresh') }}
      </NButton>
      <NButton type="primary" @click="openCreateDrawer">
        {{ t('projects.actions.create') }}
      </NButton>
    </template>

    <template #filters>
      <NInput
        v-model:value="projects.searchQuery"
        clearable
        class="sm:max-w-sm!"
        :placeholder="t('projects.filters.searchPlaceholder')"
      />
      <NButton v-if="hasActiveFilters" quaternary @click="projects.resetFilters">
        {{ t('projects.filters.reset') }}
      </NButton>
    </template>

    <template #empty-extra>
      <NButton v-if="hasActiveFilters" secondary @click="projects.resetFilters">
        {{ t('projects.filters.reset') }}
      </NButton>
      <NButton v-else type="primary" @click="openCreateDrawer">
        {{ t('projects.actions.createFirst') }}
      </NButton>
    </template>

    <div class="grid grid-cols-1 gap-5 md:grid-cols-2 xl:grid-cols-3">
      <div
        v-for="project in projects.filteredItems"
        :key="project.id"
        class="lf-interactive-card group relative cursor-pointer overflow-hidden focus-within:border-brand-500/30 focus-within:ring-2 focus-within:ring-brand-500/15"
        :class="{ 'pointer-events-none opacity-60': projects.isDeletingProject(project.id) }"
        @click="openProjectWorkspace(project)"
      >
        <div class="flex h-full flex-col gap-4 p-5">
          <div class="flex items-start gap-3">
            <div class="min-w-0 flex-1">
              <h2
                class="truncate text-lg font-semibold tracking-tight text-lf-text-strong"
                :title="project.name"
              >
                {{ project.name }}
              </h2>
              <p class="mt-1 text-xs tabular-nums text-lf-text-subtle">
                {{ t('projects.card.projectId', { id: project.id }) }}
              </p>
            </div>

            <NTag
              size="small"
              round
              :bordered="false"
              :type="project.glossary_enabled ? 'success' : 'default'"
              class="shrink-0"
            >
              {{
                project.glossary_enabled
                  ? t('projects.form.glossaryEnabled')
                  : t('projects.form.glossaryDisabled')
              }}
            </NTag>

            <div
              class="shrink-0 opacity-60 transition-opacity duration-150 group-hover:opacity-100 group-focus-within:opacity-100"
              @click.stop
            >
              <NDropdown
                trigger="click"
                :options="cardDropdownOptions"
                placement="bottom-end"
                @select="(key: string | number) => handleCardDropdownSelect(project, key)"
              >
                <NButton quaternary circle size="tiny" :aria-label="t('common.actions.more')">
                  <template #icon>
                    <NIcon size="14">
                      <IconCarbonOverflowMenuHorizontal />
                    </NIcon>
                  </template>
                </NButton>
              </NDropdown>
            </div>
          </div>

          <div
            class="flex items-center gap-2 rounded-lf-ctl bg-lf-surface-muted px-3 py-2.5 text-sm text-lf-text-muted"
          >
            <IconCarbonLanguage class="h-4 w-4 shrink-0 text-brand-500" />
            <span class="truncate font-medium text-lf-text-strong">
              {{ project.source_lang || 'auto' }}
            </span>
            <span class="text-lf-text-subtle">→</span>
            <span class="truncate font-medium text-lf-text-strong">
              {{ project.target_lang }}
            </span>
          </div>

          <div class="mt-auto border-t border-lf-border-soft pt-4">
            <div class="flex items-center justify-between gap-3">
              <span
                class="inline-flex items-center gap-1.5 text-xs tabular-nums text-lf-text-subtle"
              >
                <IconCarbonTime class="h-3.5 w-3.5 shrink-0" />
                {{ t('projects.card.updatedAt') }}
                {{ formatRelativeTime(project.updated_at ?? project.created_at ?? null) }}
              </span>
              <span
                class="text-xs font-medium text-brand-600 opacity-0 transition-opacity duration-150 group-hover:opacity-100"
              >
                {{ t('projects.card.openWorkspace') }}
              </span>
            </div>
          </div>
        </div>

        <NSpin
          v-if="projects.isDeletingProject(project.id)"
          :show="true"
          class="absolute inset-0 flex items-center justify-center bg-lf-surface/80"
          size="medium"
        />
      </div>
    </div>
  </EntityListPage>

  <template v-if="isProjectListRoute">
    <NDrawer v-model:show="drawerVisible" :width="DRAWER_WIDTH.s" placement="right">
      <NDrawerContent :title="drawerTitle" closable>
        <div
          class="mb-6 rounded-lf-card bg-lf-surface-muted p-4 text-sm leading-6 text-lf-text-muted"
        >
          {{ drawerDescription }}
        </div>

        <NForm ref="formRef" :model="formModel" :rules="rules" label-placement="top">
          <NFormItem path="name" :label="t('projects.form.name')">
            <NInput
              v-model:value="formModel.name"
              :placeholder="t('projects.form.namePlaceholder')"
              maxlength="80"
              show-count
            />
          </NFormItem>

          <NFormItem path="glossary_enabled" :label="t('projects.form.glossaryToggle')">
            <NSwitch v-model:value="formModel.glossary_enabled" />
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
        </NForm>

        <template #footer>
          <div class="flex justify-end gap-3">
            <NButton :disabled="submitting" @click="closeCreateDrawer">
              {{ t('common.cancel') }}
            </NButton>
            <NButton type="primary" :loading="submitting" @click="submitProject">
              {{ submitButtonText }}
            </NButton>
          </div>
        </template>
      </NDrawerContent>
    </NDrawer>

    <NModal
      v-model:show="deleteConfirmVisible"
      preset="dialog"
      type="warning"
      :title="t('common.actions.confirmDelete')"
      :content="t('projects.delete.confirm', { name: deletingProject?.name ?? '' })"
      :positive-text="t('common.actions.deleteConfirmAction')"
      :negative-text="t('common.cancel')"
      :loading="projects.deletingProjectIds.length > 0"
      @positive-click="confirmDelete"
      @negative-click="closeDeleteConfirm"
    />
  </template>
</template>
