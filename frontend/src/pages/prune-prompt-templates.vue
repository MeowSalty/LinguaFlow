<script setup lang="ts">
import type { ApiSchemas } from '@/api/client'

import PromptTemplateListPage, {
  type TemplateFormPayload,
} from '@/components/templates/PromptTemplateListPage.vue'
import { usePrunePromptTemplatesStore } from '@/stores/prunePromptTemplates'

type CreateRequest = ApiSchemas['CreatePrunePromptTemplateRequest']

const store = usePrunePromptTemplatesStore()

/** 归一化表单 → 术语精简提示词请求（内容字段为 content） */
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
    i18n-prefix="prunePromptTemplates"
    content-field="content"
    variable-set="prune"
    :save="save"
  />
</template>
