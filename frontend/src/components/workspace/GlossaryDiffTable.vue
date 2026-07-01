<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'

const { t } = useI18n()

interface RawGlossaryItem {
  source?: string
  target?: string
  Source?: string
  Target?: string
}

const props = defineProps<{
  usedGlossary: RawGlossaryItem[]
  addedGlossary: RawGlossaryItem[]
}>()

const normalize = (items: RawGlossaryItem[]): Array<{ source: string; target: string }> =>
  items.map((item) => ({
    source: item.source ?? item.Source ?? '',
    target: item.target ?? item.Target ?? '',
  }))

const used = computed(() => normalize(props.usedGlossary))
const added = computed(() => normalize(props.addedGlossary))
</script>

<template>
  <div v-if="used.length > 0 || added.length > 0" class="space-y-3">
    <div v-if="used.length > 0">
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
            v-for="(item, idx) in used"
            :key="`used-${idx}`"
            class="border-b border-lf-border-soft/50"
          >
            <td class="py-1 pr-2 text-lf-text">{{ item.source }}</td>
            <td class="py-1 text-lf-text">{{ item.target }}</td>
          </tr>
        </tbody>
      </table>
    </div>

    <div v-if="added.length > 0">
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
            v-for="(item, idx) in added"
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
