<script setup lang="ts">
import { NButton, NInput, NModal, NTag, useMessage } from 'naive-ui'
import { useI18n } from 'vue-i18n'

import { type ApiSchemas } from '@/api/client'
import ProjectFormDrawer from '@/components/projects/ProjectFormDrawer.vue'
import ScopeFilterTabs from '@/components/common/ScopeFilterTabs.vue'
import { useStoreErrorToast } from '@/composables/useStoreErrorToast'
import { useProjectsStore, type GlossaryFilter } from '@/stores/projects'
import { formatDateTime } from '@/utils/datetime'

type Project = ApiSchemas['Project']

const route = useRoute()
const router = useRouter()
const projects = useProjectsStore()
const message = useMessage()
const { t } = useI18n()

const formDrawerVisible = ref(false)
const editingProject = ref<Project | null>(null)
const deleteConfirmVisible = ref(false)
const deletingProject = ref<Project | null>(null)

const hasActiveFilters = computed(
  () => projects.searchQuery.trim().length > 0 || projects.glossaryFilter !== 'all',
)

const filterTabs = computed(() => [
  { name: 'all', label: t('projects.filters.all'), count: projects.totalCount },
  {
    name: 'enabled',
    label: t('projects.filters.glossaryEnabled'),
    count: projects.glossaryEnabledCount,
  },
  {
    name: 'disabled',
    label: t('projects.filters.glossaryDisabled'),
    count: projects.glossaryDisabledCount,
  },
])

const isProjectListRoute = computed(() => route.path === '/projects')

const cardDate = (project: Project): string => {
  const value = project.updated_at ?? project.created_at
  return value ? formatDateTime(value, { dateStyle: 'short' }) : '—'
}

const cardDateTitle = (project: Project): string => {
  const value = project.updated_at ?? project.created_at
  return value ? formatDateTime(value, { dateStyle: 'medium', timeStyle: 'short' }) : ''
}

const openCreateDrawer = (): void => {
  editingProject.value = null
  formDrawerVisible.value = true
}

const openEditDrawer = (project: Project): void => {
  editingProject.value = project
  formDrawerVisible.value = true
}

const openProjectWorkspace = (project: Project): void => {
  void router.push(`/projects/${project.id}`)
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
    class="lf-content-narrow"
    :title="t('projects.title')"
    :subtitle="t('projects.subtitle')"
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
      <ScopeFilterTabs
        :tabs="filterTabs"
        :value="projects.glossaryFilter"
        @update:value="(value: string) => projects.setGlossaryFilter(value as GlossaryFilter)"
      />
      <NInput
        v-model:value="projects.searchQuery"
        clearable
        class="lg:max-w-sm!"
        :placeholder="t('projects.filters.searchPlaceholder')"
      />
    </template>

    <template #empty-extra>
      <NButton v-if="hasActiveFilters" secondary @click="projects.resetFilters">
        {{ t('projects.filters.reset') }}
      </NButton>
      <NButton v-else type="primary" @click="openCreateDrawer">
        {{ t('projects.actions.createFirst') }}
      </NButton>
    </template>

    <!-- 卡片网格 -->
    <div class="grid grid-cols-1 gap-5 md:grid-cols-2 xl:grid-cols-3">
      <div
        v-for="project in projects.filteredItems"
        :key="project.id"
        class="lf-interactive-card relative flex h-full cursor-pointer flex-col gap-4 overflow-hidden p-5"
        :class="{ 'pointer-events-none opacity-60': projects.isDeletingProject(project.id) }"
        @click="openProjectWorkspace(project)"
      >
        <!-- 头部：名称 + 编号 + 术语表状态标签 -->
        <div class="flex items-start justify-between gap-4">
          <div class="min-w-0">
            <h2
              class="truncate text-lg font-semibold tracking-tight text-lf-text-strong"
              :title="project.name"
            >
              {{ project.name }}
            </h2>
            <p class="mt-1 font-mono text-xs text-lf-text-subtle">#{{ project.id }}</p>
          </div>
          <NTag
            round
            size="small"
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
        </div>

        <!-- 语言方向 -->
        <div class="flex items-center gap-2 rounded-lf-ctl bg-lf-surface-muted px-3 py-2.5 text-sm">
          <IconCarbonLanguage class="h-4 w-4 shrink-0 text-brand-500" />
          <span class="truncate font-medium text-lf-text-strong">
            {{ project.source_lang || 'auto' }}
          </span>
          <span class="text-lf-text-subtle">→</span>
          <span class="truncate font-medium text-lf-text-strong">
            {{ project.target_lang }}
          </span>
        </div>

        <!-- 底部：更新时间 + 操作 -->
        <div class="mt-auto border-t border-lf-border-soft pt-4">
          <div class="flex items-center justify-between gap-3">
            <span class="text-xs text-lf-text-subtle" :title="cardDateTitle(project)">
              {{ t('projects.card.updatedAt') }} {{ cardDate(project) }}
            </span>
            <div class="flex items-center gap-2" @click.stop>
              <NButton text type="primary" class="font-medium" @click="openEditDrawer(project)">
                {{ t('common.actions.edit') }}
              </NButton>
              <NButton text type="error" class="font-medium" @click="openDeleteConfirm(project)">
                {{ t('common.actions.delete') }}
              </NButton>
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
    <!-- 新建/编辑项目抽屉 -->
    <ProjectFormDrawer v-model:show="formDrawerVisible" :project="editingProject" />

    <!-- 删除确认弹窗 -->
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
