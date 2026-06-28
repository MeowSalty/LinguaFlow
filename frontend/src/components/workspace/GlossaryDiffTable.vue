<script setup lang="ts">
import { useI18n } from 'vue-i18n'

const { t } = useI18n()

defineProps<{
  usedGlossary: Array<{ source: string; target: string }>
  addedGlossary: Array<{ source: string; target: string }>
}>()
</script>

<template>
  <div v-if="usedGlossary.length > 0 || addedGlossary.length > 0" class="space-y-3">
    <div v-if="usedGlossary.length > 0">
      <div class="mb-1.5 text-xs font-medium text-lf-text-strong">
        {{ t('workspace.job.events.batch.usedGlossary') }}
      </div>
      <table class="w-full text-xs">
        <thead>
          <tr class="border-b border-lf-border-soft text-lf-text-muted">
            <th class="py-1 pr-2 text-left font-medium">
              {{ t('workspace.glossary.columns.source') }}
            </th>
            <th class="py-1 text-left font-medium">{{ t('workspace.glossary.columns.target') }}</th>
          </tr>
        </thead>
        <tbody>
          <tr
            v-for="(item, idx) in usedGlossary"
            :key="`used-${idx}`"
            class="border-b border-lf-border-soft/50"
          >
            <td class="py-1 pr-2 text-lf-text">{{ item.source }}</td>
            <td class="py-1 text-lf-text">{{ item.target }}</td>
          </tr>
        </tbody>
      </table>
    </div>

    <div v-if="addedGlossary.length > 0">
      <div class="mb-1.5 text-xs font-medium text-lf-text-strong">
        {{ t('workspace.job.events.batch.addedGlossary') }}
      </div>
      <table class="w-full text-xs">
        <thead>
          <tr class="border-b border-lf-border-soft text-lf-text-muted">
            <th class="py-1 pr-2 text-left font-medium">
              {{ t('workspace.glossary.columns.source') }}
            </th>
            <th class="py-1 text-left font-medium">{{ t('workspace.glossary.columns.target') }}</th>
          </tr>
        </thead>
        <tbody>
          <tr
            v-for="(item, idx) in addedGlossary"
            :key="`added-${idx}`"
            class="border-b border-lf-border-soft/50"
          >
            <td class="py-1 pr-2 text-lf-text">{{ item.source }}</td>
            <td class="py-1 text-lf-text">{{ item.target }}</td>
          </tr>
        </tbody>
      </table>
    </div>
  </div>
</template>
