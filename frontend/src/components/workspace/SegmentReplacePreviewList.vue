<script setup lang="ts">
import { computed, ref } from 'vue'
import type { ApiSchemas } from '@/api/client'
import { t } from '@/i18n'
import {
  buildSearchMatch,
  resolveSearchableText,
  type SearchMatchOptions,
} from '@/composables/useSearchHighlight'
import { makeDiffSnippet } from '@/utils/diffContext'
import { diffTextParts, mapReplacementParts } from '@/utils/textDiff'
import SegmentResultCard from './SegmentResultCard.vue'

const props = defineProps<{
  preview: ApiSchemas['SearchReplacePreviewResponse']
  textRenderMode: 'plaintext' | 'html'
  find: string
  replaceWith: string
  matchOptions: SearchMatchOptions
}>()
const emit = defineEmits<{
  locate: [segmentId: number]
  applySegment: [segmentId: number]
}>()
const expandedSegmentIds = ref(new Set<number>())
const previewItems = computed(() =>
  props.preview.items.map((item) => {
    const beforeText = resolveSearchableText(item.before, props.textRenderMode)
    const afterText = resolveSearchableText(item.after, props.textRenderMode)
    const literalMatch =
      props.matchOptions.matchMode === 'substring'
        ? mapReplacementParts(
            beforeText,
            afterText,
            buildSearchMatch(beforeText, props.find, props.matchOptions).ranges,
            props.replaceWith,
          )
        : null
    const diff = makeDiffSnippet(
      literalMatch ?? diffTextParts(beforeText, afterText),
      expandedSegmentIds.value.has(item.segment_id),
    )
    return {
      ...item,
      diffParts: diff.parts,
      truncated: diff.truncated,
      omittedDiffGroups: diff.omittedChangeGroups,
    }
  }),
)
function toggleContext(segmentId: number): void {
  const next = new Set(expandedSegmentIds.value)
  if (next.has(segmentId)) next.delete(segmentId)
  else next.add(segmentId)
  expandedSegmentIds.value = next
}
</script>

<template>
  <div>
    <div
      v-if="preview.matched_segment_count === 0"
      class="px-3 py-10 text-center text-xs text-lf-text-subtle"
    >
      {{ t('workspace.segment.findReplace.noTargetMatches') }}
    </div>
    <SegmentResultCard
      v-for="item in previewItems"
      :key="item.segment_id"
      mode="replace"
      :segment-id="item.segment_id"
      :segment-index="item.segment_index"
      :diff-parts="item.diffParts"
      :truncated="item.truncated"
      :omitted-diff-groups="item.omittedDiffGroups"
      :match-count="item.match_count"
      :expanded="expandedSegmentIds.has(item.segment_id)"
      @toggle-context="toggleContext(item.segment_id)"
      @locate="emit('locate', item.segment_id)"
      @apply-segment="emit('applySegment', item.segment_id)"
    />
    <p
      v-if="preview.matched_segment_count > preview.items.length"
      class="p-3 text-xs text-lf-text-subtle"
    >
      {{ t('workspace.segment.searchReplace.samplesTruncated', { shown: preview.items.length }) }}
    </p>
  </div>
</template>
