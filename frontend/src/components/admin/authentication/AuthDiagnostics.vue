<script setup lang="ts">
import { useI18n } from 'vue-i18n'
import { NTag } from 'naive-ui'
import type { ApiSchemas } from '@/api/client-core'
defineProps<{
  rules: ApiSchemas['AuthRulesDiagnostics']
  attributes?: Record<string, ApiSchemas['AuthAttributeValue']>
}>()
const { t } = useI18n()
const purposes = ['login', 'signup', 'link'] as const
const tones = { pass: 'success', fail: 'error', unknown: 'warning' } as const
</script>
<template>
  <div class="space-y-3">
    <section
      v-for="purpose in purposes"
      :key="purpose"
      class="rounded-lg border border-lf-border p-3"
    >
      <div class="flex items-center gap-3">
        <strong>{{ t(`authAdmin.${purpose}`) }}</strong
        ><n-tag size="small" :type="tones[rules[purpose].status]">{{
          t(`authAdmin.${rules[purpose].status}`)
        }}</n-tag>
      </div>
      <ul v-if="rules[purpose].conditions.length" class="mt-2 space-y-1 text-sm">
        <li
          v-for="(condition, index) in rules[purpose].conditions"
          :key="index"
          class="break-words"
        >
          {{ condition.field_id }} · {{ condition.operator }} ·
          {{ t(`authAdmin.${condition.status}`)
          }}<span v-if="condition.reason"> — {{ condition.reason }}</span>
        </li>
      </ul>
    </section>
    <details v-if="attributes">
      <summary class="cursor-pointer text-sm">{{ t('authAdmin.attributes') }}</summary>
      <pre class="mt-2 max-h-64 overflow-auto whitespace-pre-wrap break-all text-xs">{{
        JSON.stringify(attributes, null, 2)
      }}</pre>
    </details>
  </div>
</template>
