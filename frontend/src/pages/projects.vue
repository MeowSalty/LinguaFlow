<script setup lang="ts">
import { useMessage, type DropdownOption, type FormInst, type FormRules } from 'naive-ui'
import { useI18n } from 'vue-i18n'

import { type ApiSchemas } from '@/api/client'
import { useLanguageOptions } from '@/composables/useLanguageOptions'
import { useProjectsStore } from '@/stores/projects'

type Project = ApiSchemas['Project']

interface ProjectFormModel {
  name: string
  source_lang: string
  target_lang: string
  owner_type: 'personal' | 'organization'
  glossary_enabled: boolean
}

const route = useRoute()
const router = useRouter()
const projects = useProjectsStore()
const message = useMessage()
const { targetLanguageOptions, sourceLanguageOptions } = useLanguageOptions()
const formRef = ref<FormInst | null>(null)
const drawerVisible = ref(false)
const editingProject = ref<Project | null>(null)

const formModel = reactive<ProjectFormModel>({
  name: '',
  source_lang: 'auto',
  target_lang: 'zh-Hans',
  owner_type: 'personal',
  glossary_enabled: false,
})

const hasActiveFilters = computed(() => projects.searchQuery.trim().length > 0)

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
  formModel.target_lang = 'en-US'
  formModel.owner_type = 'personal'
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
  formModel.owner_type = 'personal'
  formModel.glossary_enabled = project.glossary_enabled ?? false
  drawerVisible.value = true
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

  return new Intl.DateTimeFormat('zh-Hans', {
    year: 'numeric',
    month: '2-digit',
    day: '2-digit',
    hour: '2-digit',
    minute: '2-digit',
  }).format(new Date(value))
}

const formatRelativeTime = (dateStr: string | null): string => {
  if (!dateStr) return '暂无更新'

  const now = Date.now()
  const date = new Date(dateStr).getTime()
  const diffMs = now - date

  if (diffMs < 0) return '刚刚'

  const diffSeconds = Math.floor(diffMs / 1000)
  const diffMinutes = Math.floor(diffSeconds / 60)
  const diffHours = Math.floor(diffMinutes / 60)
  const diffDays = Math.floor(diffHours / 24)

  if (diffSeconds < 60) return '刚刚'
  if (diffMinutes < 60) return `${diffMinutes} 分钟前`
  if (diffHours < 24) return `${diffHours} 小时前`
  if (diffDays < 7) return `${diffDays} 天前`

  return formatDate(dateStr)
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

const deleteSelectedProject = async (project: Project): Promise<void> => {
  try {
    await projects.deleteProject(project.id)
    message.success(t('projects.messages.deleteSuccess'))
  } catch (error) {
    console.error(error)
    message.error(projects.deleteError || t('projects.messages.deleteFailed'))
  }
}

const cardDropdownOptions: DropdownOption[] = [
  { label: '查看详情', key: 'details' },
  { label: '编辑', key: 'edit' },
  { label: '任务', key: 'jobs' },
  { label: '术语表', key: 'glossary' },
  { type: 'divider', key: 'd1' },
  { label: '删除', key: 'delete', props: { style: 'color: #e53e3e' } },
]

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
      void deleteSelectedProject(project)
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

watch(
  () => projects.error,
  (err) => {
    if (err) {
      message.error(err, { duration: 0, closable: true })
      projects.error = null
    }
  },
)
</script>

<template>
  <RouterView v-if="!isProjectListRoute" />
  <div v-else class="space-y-6">
    <NCard :bordered="false" class="overflow-hidden shadow-sm shadow-lf-shadow">
      <div class="flex flex-col gap-6 lg:flex-row lg:items-center lg:justify-between">
        <div class="space-y-3">
          <div
            class="inline-flex items-center rounded-full bg-lf-brand-soft px-3 py-1 text-xs font-medium text-brand-600"
          >
            {{ t('projects.eyebrow') }}
          </div>
          <div>
            <h1 class="text-3xl font-semibold tracking-tight text-lf-text-strong">
              {{ t('projects.title') }}
            </h1>
            <p class="mt-2 max-w-2xl text-sm leading-6 text-lf-text-muted">
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

    <div class="grid grid-cols-1 gap-4 md:grid-cols-2">
      <NCard :bordered="false" class="shadow-sm shadow-lf-shadow">
        <div class="text-xs font-medium text-lf-text-muted">
          {{ t('projects.stats.total') }}
        </div>
        <div class="mt-2 text-2xl font-semibold text-lf-text-strong">
          {{ projects.projectCount }}
        </div>
      </NCard>
      <NCard :bordered="false" class="shadow-sm shadow-lf-shadow">
        <div class="text-xs font-medium text-lf-text-muted">
          {{ t('projects.stats.languagePairs') }}
        </div>
        <div class="mt-2 text-2xl font-semibold text-lf-text-strong">
          {{ projects.languagePairCount }}
        </div>
      </NCard>
    </div>

    <NCard :bordered="false" class="shadow-sm shadow-lf-shadow">
      <div class="flex flex-col gap-3 lg:flex-row lg:items-center lg:justify-between">
        <NInput
          v-model:value="projects.searchQuery"
          clearable
          class="lg:max-w-sm"
          :placeholder="t('projects.filters.searchPlaceholder')"
        />
        <NButton v-if="hasActiveFilters" quaternary @click="projects.resetFilters">
          {{ t('projects.filters.reset') }}
        </NButton>
      </div>
    </NCard>

    <div v-if="projects.loading" class="grid grid-cols-1 gap-5 md:grid-cols-2 xl:grid-cols-3">
      <NCard v-for="index in 6" :key="index" :bordered="false" class="shadow-sm shadow-lf-shadow">
        <NSkeleton text :repeat="4" />
      </NCard>
    </div>

    <NEmpty
      v-else-if="projects.filteredItems.length === 0"
      class="rounded-2xl bg-lf-surface py-16 shadow-sm shadow-lf-shadow"
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
      <div
        v-for="project in projects.filteredItems"
        :key="project.id"
        class="group relative cursor-pointer overflow-hidden rounded-2xl border border-lf-border-soft bg-lf-surface p-5 shadow-sm shadow-lf-shadow transition-all duration-200 hover:-translate-y-0.5 hover:shadow-lg hover:shadow-lf-shadow-strong"
        @click="openProjectWorkspace(project)"
      >
        <div class="flex h-full flex-col gap-3">
          <!-- 标题行：项目名称 -->
          <div class="flex items-start justify-between gap-3">
            <h2
              class="min-w-0 flex-1 truncate text-base font-semibold text-lf-text-strong"
              :title="`Project #${project.id}`"
            >
              {{ project.name }}
            </h2>
          </div>

          <!-- 第二行：语言方向 + 更新时间 -->
          <p class="text-xs text-lf-text-muted">
            {{ project.source_lang || 'auto' }} → {{ project.target_lang }}
            <span class="mx-1.5">·</span>
            {{ formatRelativeTime(project.updated_at ?? project.created_at ?? null) }}
          </p>
        </div>

        <!-- 悬显更多菜单 -->
        <div
          class="absolute right-3 top-3 opacity-0 transition-opacity duration-150 group-hover:opacity-100"
          @click.stop
        >
          <NDropdown
            trigger="click"
            :options="cardDropdownOptions"
            placement="bottom-end"
            @select="(key: string | number) => handleCardDropdownSelect(project, key)"
          >
            <NButton quaternary circle size="small">
              <template #icon>
                <NIcon size="16">
                  <IconCarbonOverflowMenuHorizontal />
                </NIcon>
              </template>
            </NButton>
          </NDropdown>
        </div>
      </div>
    </div>

    <NDrawer v-model:show="drawerVisible" :width="480" placement="right">
      <NDrawerContent :title="drawerTitle" closable>
        <div class="mb-6 rounded-2xl bg-lf-surface-muted p-4 text-sm leading-6 text-lf-text-muted">
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

          <NFormItem path="glossary_enabled" :label="t('projects.form.glossaryEnabled')">
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

          <NFormItem :label="t('projects.form.ownerType')">
            <NRadioGroup v-model:value="formModel.owner_type">
              <NRadio value="personal">
                {{ t('projects.form.personal') }}
              </NRadio>
              <NRadio value="organization" disabled>
                {{ t('projects.form.orgOwner') }}
                <NText depth="3" style="margin-left: 4px; font-size: 12px">
                  ({{ t('projects.form.comingSoon') }})
                </NText>
              </NRadio>
            </NRadioGroup>
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
