<script setup lang="ts">
import { computed } from 'vue'

import type { QualityIssue } from '@/composables/useQualityIssues'
import {
  renderQualityHighlightedHtml,
  renderQualityHighlightedText,
} from '@/composables/useQualityIssues'

const props = withDefaults(
  defineProps<{
    text: string
    issues?: QualityIssue[]
    mode: 'plaintext' | 'html'
    activeIssueIndex?: number | null
    maxLines?: number
  }>(),
  {
    issues: undefined,
    activeIssueIndex: null,
    maxLines: undefined,
  },
)

const useHtmlRenderer = computed(
  () => props.mode === 'html' && (!!props.issues?.length || /<[a-z][\s\S]*>/i.test(props.text)),
)

const vnode = computed(() => {
  if (useHtmlRenderer.value) {
    return renderQualityHighlightedHtml(
      props.text,
      props.issues,
      props.activeIssueIndex ?? null,
      props.maxLines,
    )
  }
  return renderQualityHighlightedText(props.text, props.issues)
})
</script>

<template>
  <component :is="vnode" />
</template>
