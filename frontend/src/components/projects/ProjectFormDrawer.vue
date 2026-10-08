<script setup lang="ts">
/**
 * 项目新建/编辑共享表单抽屉：项目列表页与工作区页共用。
 * project 为 null 时进入新建模式，否则编辑对应项目；保存成功后 emit saved。
 */
import {
  NAlert,
  NButton,
  NDrawer,
  NDrawerContent,
  NForm,
  NFormItem,
  NInput,
  NSelect,
  NSwitch,
  useMessage,
  type FormInst,
  type FormRules,
} from 'naive-ui'
import { useI18n } from 'vue-i18n'

import { type ApiSchemas } from '@/api/client'
import { useLanguageOptions } from '@/composables/useLanguageOptions'
import { useProjectsStore } from '@/stores/projects'
import { DRAWER_WIDTH } from '@/components/common/uiConstants'
import { captureSession, isSessionCurrent } from '@/api/session-context'
import { getStorageContractGate } from '@/utils/storage-contract'
import StorageTargetSelect from '@/components/storage/StorageTargetSelect.vue'
import { storageTargetContextAllowed } from '@/composables/useStorageTargets'
import { storageNeedsRefresh } from '@/api/storage-errors'
import { invalidateStorageSnapshots } from '@/utils/storage-snapshots'

type Project = ApiSchemas['Project']

interface ProjectFormModel {
  name: string
  source_lang: string
  target_lang: string
  glossary_enabled: boolean
  storage_space_id: number | null
}

const props = withDefaults(
  defineProps<{
    show: boolean
    /** 待编辑的项目；null 或缺省表示新建 */
    project?: Project | null
    orgId?: number | null
  }>(),
  { project: null, orgId: null },
)

const emit = defineEmits<{
  'update:show': [show: boolean]
  /** 保存成功（新建或编辑），携带最新项目数据 */
  saved: [project: Project]
}>()

const { t } = useI18n()
const message = useMessage()
const projects = useProjectsStore()
const { targetLanguageOptions, sourceLanguageOptions } = useLanguageOptions()

const formRef = ref<FormInst | null>(null)

const formModel = reactive<ProjectFormModel>({
  name: '',
  source_lang: 'auto',
  target_lang: 'zh-Hans',
  glossary_enabled: false,
  storage_space_id: null,
})

const isEditMode = computed(() => Boolean(props.project))
const storageDiscovery = getStorageContractGate('targetDiscovery')
const targetContext = computed(() =>
  props.show && !props.project ? { kind: 'create' as const, organizationId: props.orgId } : null,
)
const target = shallowRef<ApiSchemas['StorageOption'] | null>(null)
const canCreate = computed(
  () =>
    storageTargetContextAllowed(targetContext.value) &&
    target.value?.selectable === true &&
    target.value.space_id === formModel.storage_space_id,
)

const drawerTitle = computed(() =>
  isEditMode.value ? t('projects.actions.editTitle') : t('projects.actions.createTitle'),
)

const drawerSubtitle = computed(() =>
  isEditMode.value ? (props.project?.name ?? '') : t('projects.form.createHint'),
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
}))

// 打开时按 project 初始化表单，新建回退到默认值
watch(
  () => props.show,
  (show) => {
    if (!show) return
    formModel.name = props.project?.name ?? ''
    formModel.source_lang = props.project?.source_lang || 'auto'
    formModel.target_lang = props.project?.target_lang || 'zh-Hans'
    formModel.glossary_enabled = props.project?.glossary_enabled ?? false
    formModel.storage_space_id = null
  },
)

const close = (): void => {
  emit('update:show', false)
}

const onSubmit = async (): Promise<void> => {
  if (!props.project && !canCreate.value) return
  const session = captureSession()
  const organization = props.orgId
  const projectId = props.project?.id
  if (!props.project) projects.setOrganization(props.orgId)
  if (!projects.canEdit(props.project ?? undefined)) return
  try {
    await formRef.value?.validate()
  } catch {
    return
  }

  if (
    !isSessionCurrent(session) ||
    !props.show ||
    organization !== props.orgId ||
    projectId !== props.project?.id
  )
    return
  if (!props.project && !canCreate.value) return

  const payload: ApiSchemas['CreateProjectRequest'] = {
    name: formModel.name.trim(),
    source_lang: formModel.source_lang.trim(),
    target_lang: formModel.target_lang.trim(),
    glossary_enabled: formModel.glossary_enabled,
    ...(!props.project && formModel.storage_space_id !== null
      ? { storage_space_id: formModel.storage_space_id }
      : {}),
  }

  try {
    const project = props.project
      ? await projects.updateProject(props.project.id, payload)
      : await projects.createProject(payload)
    if (
      !isSessionCurrent(session) ||
      !props.show ||
      organization !== props.orgId ||
      projectId !== props.project?.id
    )
      return
    message.success(
      t(isEditMode.value ? 'projects.messages.updateSuccess' : 'projects.messages.createSuccess'),
    )
    emit('saved', project)
    close()
  } catch (error) {
    if (
      !isSessionCurrent(session) ||
      !props.show ||
      (error instanceof Error && error.name === 'AbortError')
    )
      return
    if (!props.project && storageNeedsRefresh(error))
      invalidateStorageSnapshots({ organizationId: organization })
    message.error(
      isEditMode.value
        ? projects.updateError || t('projects.messages.updateFailed')
        : projects.createError || t('projects.messages.createFailed'),
    )
  }
}
</script>

<template>
  <NDrawer
    :show="show"
    :width="DRAWER_WIDTH.s"
    placement="right"
    @update:show="(value: boolean) => emit('update:show', value)"
  >
    <NDrawerContent closable>
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
        <NAlert v-if="!isEditMode && !storageDiscovery.available" type="info" class="mb-4">{{
          t('storage.discoveryPending')
        }}</NAlert>
        <NFormItem v-if="!isEditMode" :label="t('storage.selectSpace')" required>
          <StorageTargetSelect
            v-model:value="formModel.storage_space_id"
            :context="targetContext"
            :disabled="submitting"
            @selection="target = $event"
          />
        </NFormItem>
        <NFormItem path="name" :label="t('projects.form.name')">
          <NInput
            v-model:value="formModel.name"
            :placeholder="t('projects.form.namePlaceholder')"
            maxlength="80"
            show-count
          />
        </NFormItem>

        <NFormItem path="glossary_enabled" :label="t('projects.form.glossaryToggle')">
          <div class="space-y-2">
            <NSwitch v-model:value="formModel.glossary_enabled" />
            <p class="text-xs text-lf-text-muted">{{ t('termExtraction.projectSwitchHint') }}</p>
          </div>
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
          <NButton :disabled="submitting" @click="close">
            {{ t('common.cancel') }}
          </NButton>
          <NButton
            type="primary"
            :loading="submitting"
            :disabled="!isEditMode && !canCreate"
            @click="onSubmit"
          >
            {{ submitButtonText }}
          </NButton>
        </div>
      </template>
    </NDrawerContent>
  </NDrawer>
</template>
