<script setup lang="ts">
import type { ApiSchemas } from '@/api/client'

import PromptTemplateListPage, {
  type TemplateFormPayload,
} from '@/components/templates/PromptTemplateListPage.vue'
import { usePromptTemplatesStore } from '@/stores/promptTemplates'

type CreateRequest = ApiSchemas['CreateTranslationPromptTemplateRequest']

const store = usePromptTemplatesStore()

/** 归一化表单 → 翻译提示词请求（内容字段为 system_prompt_content） */
const save = (payload: TemplateFormPayload, id?: number): Promise<unknown> => {
  const request: CreateRequest = { name: payload.name }
  if (payload.description) request.description = payload.description
  if (payload.content) request.system_prompt_content = payload.content

  return id === undefined ? store.createTemplate(request) : store.updateTemplate(id, request)
}
</script>

<template>
  <PromptTemplateListPage
    class="lf-content-narrow"
    :store="store"
    i18n-prefix="promptTemplates"
    content-field="system_prompt_content"
    variable-set="system"
    :save="save"
  />
</template>
