<script setup lang="ts">
import type { ApiSchemas } from '@/api/client'

import PromptTemplateListPage, {
  type TemplateFormPayload,
} from '@/components/templates/PromptTemplateListPage.vue'
import { useBootstrapPromptTemplatesStore } from '@/stores/bootstrapPromptTemplates'

type CreateRequest = ApiSchemas['CreateBootstrapPromptTemplateRequest']

const store = useBootstrapPromptTemplatesStore()

/** 归一化表单 → 术语抽取提示词请求（内容字段为 content） */
const save = (payload: TemplateFormPayload, id?: number): Promise<unknown> => {
  const request: CreateRequest = { name: payload.name }
  if (payload.description) request.description = payload.description
  if (payload.content) request.content = payload.content

  return id === undefined ? store.createTemplate(request) : store.updateTemplate(id, request)
}
</script>

<template>
  <PromptTemplateListPage
    class="lf-content-narrow"
    :store="store"
    i18n-prefix="bootstrapPromptTemplates"
    content-field="content"
    variable-set="bootstrap"
    :save="save"
  />
</template>
