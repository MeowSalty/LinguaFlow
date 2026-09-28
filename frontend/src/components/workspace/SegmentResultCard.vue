<script setup lang="ts">
import { NButton, NTag } from 'naive-ui'
import type { ApiSchemas } from '@/api/client'
import type { SearchSnippet } from '@/composables/useSearchHighlight'
import { t } from '@/i18n'
import { getSegmentStatusLabel, statusTagType } from '@/composables/useWorkspaceUtils'
import type { DiffSnippetPart } from '@/utils/diffContext'

type Segment = ApiSchemas['Segment']

defineProps<{
  mode: 'search' | 'replace'
  segment?: Segment
  segmentId: number
  segmentIndex: number
  chapter?: string
  displayField?: 'source' | 'target' | null
  snippet?: SearchSnippet
  targetSnippet?: SearchSnippet | null
  sourceMatchCount?: number
  targetMatchCount?: number
  selected?: boolean
  expanded?: boolean
  omittedMatchCount?: number
  replaceEnabled?: boolean
  busy?: boolean
  diffParts?: DiffSnippetPart[]
  truncated?: boolean
  omittedDiffGroups?: number
  matchCount?: number
}>()

const emit = defineEmits<{
  select: []
  toggleContext: []
  locate: []
  previewSegment: []
  applySegment: []
}>()
</script>

<template>
  <article
    class="border-b border-lf-border-soft px-3 py-2.5 transition-colors"
    :class="
      selected
        ? 'bg-lf-brand-soft/70 shadow-[inset_3px_0_var(--lf-brand-500)]'
        : 'hover:bg-lf-surface-muted/60'
    "
  >
    <div class="mb-1.5 flex items-center gap-2 text-[11px] text-lf-text-subtle">
      <span v-if="mode === 'search'" class="min-w-0 flex-1 truncate">{{
        chapter || t('workspace.segment.chapterAll')
      }}</span>
      <span v-else class="min-w-0 flex-1 truncate">{{
        chapter || t('workspace.segment.chapterAll')
      }}</span>
      <span class="shrink-0 tabular-nums">#{{ segmentIndex }}</span>
      <NTag
        v-if="mode === 'search' && segment"
        size="tiny"
        :type="statusTagType(segment.status)"
        :bordered="false"
      >
        {{ getSegmentStatusLabel(segment.status) }}
      </NTag>
      <span v-else-if="mode === 'replace'" class="shrink-0">{{
        t('workspace.segment.searchReplace.matchCount', { count: matchCount ?? 0 })
      }}</span>
    </div>

    <button
      type="button"
      class="block w-full cursor-pointer text-left"
      :aria-label="t('workspace.segment.searchLocate.jump')"
      @click="mode === 'search' ? emit('select') : emit('locate')"
    >
      <template v-if="mode === 'search' && snippet">
        <span
          class="block text-xs leading-relaxed break-words"
          :class="[
            displayField === 'source' ? 'text-lf-text-muted' : 'text-lf-text',
            snippet.matchCount ? '' : 'line-clamp-2',
          ]"
        >
          <template v-for="(part, partIndex) in snippet.parts" :key="partIndex">
            <mark v-if="part.type === 'hit'" class="search-hit">{{ part.text }}</mark>
            <span v-else :class="part.type === 'ellipsis' ? 'text-lf-text-subtle' : ''">{{
              part.text
            }}</span>
          </template>
        </span>
        <span
          v-if="targetSnippet"
          class="mt-1 block text-xs leading-relaxed break-words text-lf-text"
          :class="targetSnippet.matchCount ? '' : 'line-clamp-2'"
        >
          <template v-for="(part, partIndex) in targetSnippet.parts" :key="partIndex">
            <mark v-if="part.type === 'hit'" class="search-hit">{{ part.text }}</mark>
            <span v-else :class="part.type === 'ellipsis' ? 'text-lf-text-subtle' : ''">{{
              part.text
            }}</span>
          </template>
        </span>
      </template>
      <template v-else-if="mode === 'replace' && diffParts">
        <div class="whitespace-pre-wrap break-words text-xs leading-relaxed text-lf-text">
          <template v-for="(part, partIndex) in diffParts" :key="partIndex">
            <del
              v-if="part.type === 'delete'"
              class="rounded-sm bg-lf-danger-soft/45 px-0.5 text-lf-danger decoration-lf-danger"
              >{{ part.text }}</del
            >
            <ins
              v-else-if="part.type === 'insert'"
              class="rounded-sm bg-lf-success-soft/45 px-0.5 text-lf-success no-underline"
              >{{ part.text }}</ins
            >
            <span v-else :class="part.type === 'ellipsis' ? 'text-lf-text-subtle' : ''">{{
              part.text
            }}</span>
          </template>
        </div>
      </template>
    </button>

    <div class="mt-1 flex items-center gap-2 text-[11px] text-lf-text-subtle">
      <template v-if="mode === 'search'">
        <span v-if="sourceMatchCount">{{
          t('workspace.segment.searchLocate.sourceMatchCount', { count: sourceMatchCount })
        }}</span>
        <span v-if="targetMatchCount">{{
          t('workspace.segment.searchLocate.targetMatchCount', { count: targetMatchCount })
        }}</span>
        <NButton
          v-if="snippet && (snippet.truncated || targetSnippet?.truncated)"
          size="tiny"
          quaternary
          @click.stop="emit('toggleContext')"
        >
          {{
            expanded
              ? t('workspace.segment.findReplace.collapseContext')
              : omittedMatchCount
                ? t('workspace.segment.findReplace.expandMatches', { count: omittedMatchCount })
                : t('workspace.segment.findReplace.expandContext')
          }}
        </NButton>
        <NButton
          v-if="replaceEnabled"
          size="tiny"
          quaternary
          class="ml-auto"
          :disabled="busy"
          @click.stop="emit('previewSegment')"
        >
          {{ t('workspace.segment.findReplace.previewSegment') }}
        </NButton>
      </template>
      <template v-else>
        <NButton v-if="truncated" size="tiny" quaternary @click.stop="emit('toggleContext')">
          {{
            expanded
              ? t('workspace.segment.findReplace.collapseContext')
              : omittedDiffGroups
                ? t('workspace.segment.findReplace.expandDiffGroups', { count: omittedDiffGroups })
                : t('workspace.segment.findReplace.expandContext')
          }}
        </NButton>
        <NButton
          size="tiny"
          quaternary
          type="primary"
          class="ml-auto"
          :disabled="busy"
          @click.stop="emit('applySegment')"
        >
          {{ t('workspace.segment.findReplace.applySegment') }}
        </NButton>
      </template>
    </div>
    <p
      v-if="mode === 'replace' && !diffParts?.some((part) => part.type === 'insert')"
      class="mt-1 text-[11px] text-lf-text-muted"
    >
      {{ t('workspace.segment.findReplace.emptyReplacementResult') }}
    </p>
  </article>
</template>
