<script setup lang="ts">
import { NButton } from 'naive-ui'
import { t } from '@/i18n'
import type { SegmentMatchMode } from '@/api/projects'

defineProps<{ disabled?: boolean }>()
const caseSensitive = defineModel<boolean>('caseSensitive', { required: true })
const wholeWord = defineModel<boolean>('wholeWord', { required: true })
const matchMode = defineModel<SegmentMatchMode>('matchMode', { required: true })
</script>

<template>
  <div
    class="flex shrink-0 items-center gap-0.5 rounded-lf-ctl border border-lf-border-soft bg-lf-surface p-0.5"
    role="group"
    :aria-label="t('workspace.segment.searchLocate.matchMode')"
  >
    <NButton
      size="tiny"
      quaternary
      :type="caseSensitive ? 'primary' : 'default'"
      class="min-w-7 px-1.5!"
      :class="{ 'bg-lf-brand-soft': caseSensitive }"
      :disabled="disabled"
      :aria-pressed="caseSensitive"
      :title="t('workspace.segment.searchLocate.caseSensitive')"
      :aria-label="t('workspace.segment.searchLocate.caseSensitive')"
      @mousedown.prevent
      @click="caseSensitive = !caseSensitive"
      >Aa</NButton
    >
    <NButton
      size="tiny"
      quaternary
      :type="wholeWord ? 'primary' : 'default'"
      class="min-w-7 px-1.5!"
      :class="{ 'bg-lf-brand-soft': wholeWord }"
      :disabled="disabled"
      :aria-pressed="wholeWord"
      :title="t('workspace.segment.searchLocate.wholeWordHint')"
      :aria-label="t('workspace.segment.searchLocate.wholeWord')"
      @mousedown.prevent
      @click="wholeWord = !wholeWord"
      ><span class="underline underline-offset-2">ab</span></NButton
    >
    <NButton
      size="tiny"
      quaternary
      :type="matchMode === 'regex' ? 'primary' : 'default'"
      class="min-w-7 px-1.5!"
      :class="{ 'bg-lf-brand-soft': matchMode === 'regex' }"
      :disabled="disabled"
      :aria-pressed="matchMode === 'regex'"
      :title="t('workspace.segment.searchReplace.regexHint')"
      :aria-label="t('workspace.segment.searchLocate.matchModeRegex')"
      @mousedown.prevent
      @click="matchMode = matchMode === 'regex' ? 'substring' : 'regex'"
      >.*</NButton
    >
  </div>
</template>
