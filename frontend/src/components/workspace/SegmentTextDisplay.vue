<script setup lang="ts">
import { computed } from 'vue'

import type { QualityIssue } from '@/composables/useQualityIssues'
import {
  renderCombinedHighlightedHtml,
  renderCombinedHighlightedText,
  renderHighlightedHtmlRanges,
  renderHighlightedTextRanges,
  type SearchHighlightRange,
  type SearchMatchOptions,
} from '@/composables/useSearchHighlight'

const props = withDefaults(
  defineProps<{
    text: string
    issues?: QualityIssue[]
    mode: 'plaintext' | 'html'
    activeIssueIndex?: number | null
    maxLines?: number
    searchQuery?: string
    searchMatchOptions?: SearchMatchOptions
    highlightRanges?: SearchHighlightRange[]
  }>(),
  {
    issues: undefined,
    activeIssueIndex: null,
    maxLines: undefined,
    searchQuery: '',
    searchMatchOptions: undefined,
  },
)

const useHtmlRenderer = computed(
  () => props.mode === 'html' && (!!props.issues?.length || /<[a-z][\s\S]*>/i.test(props.text)),
)

const vnode = computed(() => {
  if (props.highlightRanges) {
    if (useHtmlRenderer.value) return renderHighlightedHtmlRanges(props.text, props.highlightRanges)
    return renderHighlightedTextRanges(props.text, props.highlightRanges)
  }

  const query = props.searchQuery.trim()
  if (useHtmlRenderer.value) {
    return renderCombinedHighlightedHtml(
      props.text,
      query,
      props.searchMatchOptions,
      props.issues,
      props.activeIssueIndex ?? null,
      props.maxLines,
    )
  }
  return renderCombinedHighlightedText(props.text, query, props.searchMatchOptions, props.issues)
})
</script>

<template>
  <component :is="vnode" />
</template>
