<script setup lang="ts">
import { computed, nextTick, onMounted, onUnmounted, reactive, ref, watch } from 'vue'
import { NButton, NButtonGroup, NEmpty } from 'naive-ui'
import { useI18n } from 'vue-i18n'
import ChevronRight from '~icons/carbon/chevron-right'

import type { BatchEventMetadata, PoolEventMetadata, SSEEvent } from '@/composables/sseShared'
import { normalizeSSELevel } from '@/composables/sseShared'
import {
  eventLevelType,
  formatDuration,
  formatTokens,
  getStageLabel,
  isBatchEvent,
  isPoolEvent,
} from '@/composables/useWorkspaceUtils'
import { formatDateTime } from '@/utils/datetime'
import { isJobEventAnomaly, type JobEventFilter } from '@/utils/jobPresentation'

import BatchDetailDrawer from './BatchDetailDrawer.vue'

const { t, locale } = useI18n()

const props = withDefaults(
  defineProps<{
    events: SSEEvent[]
    connected?: boolean
    hasOlder?: boolean
    loadingOlder?: boolean
    jobEnded?: boolean
    active?: boolean
    filter?: JobEventFilter
  }>(),
  {
    active: true,
    filter: 'all',
  },
)

const emit = defineEmits<{
  clear: []
  'load-older': []
  'update:filter': [value: JobEventFilter]
}>()

const scrollContainerRef = ref<HTMLElement | null>(null)
const detailDrawerShow = ref(false)
const detailDrawerEvent = ref<SSEEvent | null>(null)
const canLoadOlder = computed(
  () => props.hasOlder && !props.loadingOlder && props.events.length > 0,
)

type LogLevel = 'info' | 'success' | 'warning' | 'error' | 'dim'

interface LogRow {
  key: string
  time: string
  level: LogLevel
  status: string
  message: string
  meta: string
  clickable: boolean
  event: SSEEvent
}

const DOT_CLASS: Record<LogLevel, string> = {
  info: 'bg-brand-500',
  success: 'bg-lf-success',
  warning: 'bg-lf-warning',
  error: 'bg-lf-danger',
  dim: 'border border-lf-text-subtle bg-transparent',
}

const getBatchSummary = (event: SSEEvent): string => {
  const meta = event.metadata as unknown as BatchEventMetadata | undefined
  if (!meta) return event.message
  const parts: string[] = []
  if (event.stage) parts.push(getStageLabel(event.stage))
  parts.push(t('workspace.job.events.batch.segments', { count: meta.segment_count }))
  if (meta.duration_ms) parts.push(formatDuration(meta.duration_ms))
  return parts.join(' · ')
}

const getBatchMeta = (event: SSEEvent): string => {
  const meta = event.metadata as unknown as BatchEventMetadata | undefined
  if (!meta) return ''
  const parts: string[] = [meta.backend_name]
  if (meta.input_tokens || meta.output_tokens) {
    parts.push(
      t('workspace.job.events.batch.tokens', {
        input: formatTokens(meta.input_tokens),
        output: formatTokens(meta.output_tokens),
      }),
    )
  }
  if (meta.output_tokens > 0 && meta.duration_ms > 0) {
    const rate = Math.round((meta.output_tokens / meta.duration_ms) * 1000)
    parts.push(t('workspace.job.events.batch.tokenSpeed', { rate: formatTokens(rate) }))
  }
  return parts.filter(Boolean).join(' · ')
}

const getPoolLine = (event: SSEEvent): string => {
  const meta = event.metadata as unknown as PoolEventMetadata | undefined
  const parts: string[] = []
  if (event.stage) parts.push(getStageLabel(event.stage))
  if (meta) {
    parts.push(
      t(
        meta.phase === 'pool_advance'
          ? 'workspace.job.events.pool.poolAdvance'
          : 'workspace.job.events.pool.poolStart',
      ),
    )
    parts.push(
      t('workspace.job.events.pool.progress', {
        index: meta.pool_index + 1,
        total: meta.max_pools,
      }),
    )
    parts.push(t('workspace.job.events.pool.batches', { count: meta.batches }))
    parts.push(t('workspace.job.events.pool.pending', { count: meta.pending }))
    parts.push(t('workspace.job.events.pool.shrinkRate', { rate: meta.shrink_rate.toFixed(2) }))
  }
  return parts.join(' · ')
}

const formatRoundMessage = (event: SSEEvent): string => {
  if ((event.type === 'stage_start' || event.type === 'stage_done') && event.stage) {
    const match = event.message.match(/^(.*?):\s*\w+\s*\((.*)\)$/)
    if (match) return [match[1], getStageLabel(event.stage), match[2]].join(' · ')
  }
  return event.message
}

const getRowLevel = (event: SSEEvent): LogLevel => {
  const level = eventLevelType(normalizeSSELevel(event.level.toLowerCase()))
  const meta = isBatchEvent(event.type)
    ? (event.metadata as unknown as BatchEventMetadata | undefined)
    : undefined
  if (meta?.status === 'failed' || level === 'error') return 'error'
  if (meta?.status === 'partial' || level === 'warning') return 'warning'
  if (isPoolEvent(event.type)) return 'dim'
  if (
    meta?.status === 'success' ||
    ['job_completed', 'resource_completed', 'stage_done'].includes(event.type)
  ) {
    return 'success'
  }
  return level
}

const getRowStatus = (event: SSEEvent, level: LogLevel): string => {
  if (isBatchEvent(event.type) && event.metadata?.status === 'partial') {
    return t('workspace.job.detail.logPartial')
  }
  if (level === 'error') return t('workspace.job.detail.logFailed')
  if (level === 'warning') return t('workspace.job.detail.logWarning')
  return ''
}

const anomalyCount = computed(() => props.events.filter(isJobEventAnomaly).length)
let rowCache = new WeakMap<SSEEvent, LogRow>()
let cacheLocale = locale.value
const logRows = computed<LogRow[]>(() => {
  if (cacheLocale !== locale.value) {
    rowCache = new WeakMap<SSEEvent, LogRow>()
    cacheLocale = locale.value
  }
  return props.events
    .filter((event) => props.filter === 'all' || isJobEventAnomaly(event))
    .map((event) => {
      const cached = rowCache.get(event)
      if (cached) return cached
      const level = getRowLevel(event)
      const batch = isBatchEvent(event.type)
      const row = {
        key: String(event.seq),
        time: formatDateTime(event.created_at, {
          hour: '2-digit',
          minute: '2-digit',
          second: '2-digit',
        }),
        level,
        status: getRowStatus(event, level),
        message: batch
          ? getBatchSummary(event)
          : isPoolEvent(event.type)
            ? getPoolLine(event)
            : formatRoundMessage(event),
        meta: batch ? getBatchMeta(event) : '',
        clickable: batch,
        event,
      }
      rowCache.set(event, row)
      return row
    })
})

// Each filter keeps its own reading anchor. Sequence IDs remain stable when the
// store prepends history or trims its event window; array length does not.
interface ReadingPosition {
  initialized: boolean
  followTail: boolean
  top: number
  anchor: string | null
  offset: number
  unseen: number
}

const newPosition = (): ReadingPosition => ({
  initialized: false,
  followTail: true,
  top: 0,
  anchor: null,
  offset: 0,
  unseen: 0,
})
const positions = reactive<Record<JobEventFilter, ReadingPosition>>({
  all: newPosition(),
  anomalies: newPosition(),
})
const currentPosition = computed(() => positions[props.filter])
let renderedActive = false
let scrollFrame = 0
let restoreRevision = 0
let resizeObserver: ResizeObserver | undefined

const rememberPosition = (position = currentPosition.value): void => {
  if (!props.active || !renderedActive) return
  const el = scrollContainerRef.value
  if (!el) return
  position.top = el.scrollTop
  position.followTail = el.scrollTop + el.clientHeight >= el.scrollHeight - 40
  if (position.followTail) position.unseen = 0
  const rows = el.querySelectorAll<HTMLElement>('[data-event-seq]')
  const anchor = Array.from(rows).find((row) => row.offsetTop + row.offsetHeight > el.scrollTop)
  position.anchor = anchor?.dataset.eventSeq ?? null
  position.offset = anchor ? anchor.offsetTop - el.scrollTop : 0
}

const restorePosition = (preserveAnchor = false): void => {
  if (!props.active || !renderedActive) return
  const el = scrollContainerRef.value
  if (!el) return
  const position = currentPosition.value
  if (!position.initialized || (position.followTail && !preserveAnchor)) {
    el.scrollTop = el.scrollHeight
    position.unseen = 0
  } else {
    const anchor = position.anchor
      ? el.querySelector<HTMLElement>(`[data-event-seq="${position.anchor}"]`)
      : null
    // A trimmed reading anchor no longer exists: show the earliest retained row.
    el.scrollTop = anchor ? anchor.offsetTop - position.offset : position.anchor ? 0 : position.top
  }
  position.initialized = true
  rememberPosition(position)
}

const scheduleRestore = (preserveAnchor = false): void => {
  const revision = ++restoreRevision
  void nextTick(() => {
    if (revision === restoreRevision) restorePosition(preserveAnchor)
  })
}

const onScroll = (): void => {
  if (!props.active || scrollFrame) return
  scrollFrame = requestAnimationFrame(() => {
    scrollFrame = 0
    rememberPosition()
  })
}

const scrollToBottom = (): void => {
  currentPosition.value.followTail = true
  scheduleRestore()
}

const openBatchDetail = (event: SSEEvent): void => {
  detailDrawerEvent.value = event
  detailDrawerShow.value = true
}

watch(
  () => props.filter,
  (_, previous) => {
    rememberPosition(positions[previous])
    scheduleRestore()
  },
)

watch(
  () => props.active,
  (active) => {
    renderedActive = false
    ++restoreRevision
    if (active) {
      void nextTick(() => {
        renderedActive = props.active
        restorePosition()
      })
    }
  },
  { flush: 'sync' },
)

watch(
  () => [props.events[0]?.seq, props.events.at(-1)?.seq, props.events.length] as const,
  ([head, tail, length], [previousHead, previousTail]) => {
    rememberPosition()
    if (!length) {
      Object.assign(positions.all, newPosition())
      Object.assign(positions.anomalies, newPosition())
      detailDrawerShow.value = false
      detailDrawerEvent.value = null
      scheduleRestore()
      return
    }
    if (previousTail !== undefined && tail !== undefined && tail > previousTail) {
      const appended = props.events.filter((event) => event.seq > previousTail)
      for (const filter of ['all', 'anomalies'] as const) {
        const position = positions[filter]
        if (position.initialized && !position.followTail) {
          position.unseen +=
            filter === 'all' ? appended.length : appended.filter(isJobEventAnomaly).length
        }
      }
    }
    const onlyPrepending =
      head !== undefined &&
      previousHead !== undefined &&
      head < previousHead &&
      tail === previousTail
    scheduleRestore(onlyPrepending)
  },
)

onMounted(() => {
  void nextTick(() => {
    renderedActive = props.active
    restorePosition()
    const el = scrollContainerRef.value
    if (el) {
      resizeObserver = new ResizeObserver(() => {
        if (props.active) scheduleRestore()
      })
      resizeObserver.observe(el)
    }
  })
})

onUnmounted(() => {
  ++restoreRevision
  if (scrollFrame) cancelAnimationFrame(scrollFrame)
  resizeObserver?.disconnect()
})
</script>

<template>
  <div class="flex h-full min-h-0 flex-col">
    <div class="shrink-0 space-y-2 border-b border-lf-border-soft pb-3">
      <div class="flex flex-wrap items-center justify-between gap-2">
        <NButtonGroup>
          <NButton
            size="small"
            :type="filter === 'all' ? 'primary' : 'default'"
            :secondary="filter === 'all'"
            :aria-pressed="filter === 'all'"
            @click="emit('update:filter', 'all')"
          >
            {{ t('workspace.job.detail.logAll') }}
          </NButton>
          <NButton
            size="small"
            :type="filter === 'anomalies' ? 'primary' : 'default'"
            :secondary="filter === 'anomalies'"
            :aria-pressed="filter === 'anomalies'"
            @click="emit('update:filter', 'anomalies')"
          >
            {{ t('workspace.job.detail.logAnomalies') }}
          </NButton>
        </NButtonGroup>
        <div class="flex flex-wrap items-center gap-2">
          <span class="inline-flex items-center gap-1.5 text-xs text-lf-text-muted">
            <span
              class="h-1.5 w-1.5 rounded-full"
              :class="!jobEnded && connected ? 'bg-lf-success' : 'bg-lf-text-subtle'"
            />
            {{
              t(
                jobEnded
                  ? 'workspace.job.detail.logHistory'
                  : connected
                    ? 'workspace.job.detail.logLive'
                    : 'workspace.job.detail.logDisconnected',
              )
            }}
          </span>
          <NButton quaternary size="tiny" :disabled="events.length === 0" @click="emit('clear')">
            {{ t('workspace.job.detail.logClear') }}
          </NButton>
        </div>
      </div>
      <p class="text-xs text-lf-text-muted" aria-live="polite">
        {{
          filter === 'anomalies'
            ? t('workspace.job.detail.logAnomalyScope', { count: anomalyCount })
            : t('workspace.job.detail.logLoadedCount', { count: events.length })
        }}
      </p>
    </div>

    <div class="relative min-h-0 flex-1">
      <div
        ref="scrollContainerRef"
        class="relative h-full overflow-y-auto overscroll-contain py-2"
        style="overflow-anchor: none"
        @scroll="onScroll"
      >
        <div class="flex justify-center py-2">
          <NButton
            v-if="hasOlder || loadingOlder"
            size="tiny"
            quaternary
            :loading="loadingOlder"
            :disabled="!canLoadOlder"
            @click="emit('load-older')"
          >
            {{ t('workspace.job.detail.logLoadOlder') }}
          </NButton>
          <span v-else-if="events.length" class="text-xs text-lf-text-subtle">
            {{ t('workspace.job.events.reachedOldest') }}
          </span>
        </div>

        <NEmpty
          v-if="!logRows.length"
          size="small"
          class="py-10"
          :description="
            t(
              filter === 'anomalies'
                ? 'workspace.job.detail.logNoAnomalies'
                : 'workspace.job.events.empty',
            )
          "
        />
        <component
          :is="row.clickable ? 'button' : 'div'"
          v-for="row in logRows"
          :key="row.key"
          :type="row.clickable ? 'button' : undefined"
          :data-event-seq="row.key"
          :title="row.clickable ? t('workspace.job.detail.logBatchDetail') : undefined"
          class="group flex w-full items-start gap-2 rounded-lf-ctl px-1.5 py-1.5 text-left text-xs sm:gap-2.5"
          :class="
            row.clickable
              ? 'cursor-pointer hover:bg-lf-hover focus-visible:outline-2 focus-visible:outline-brand-500'
              : ''
          "
          @click="row.clickable && openBatchDetail(row.event)"
        >
          <span
            class="w-14 shrink-0 font-mono text-[11px] leading-5 tabular-nums text-lf-text-subtle"
            >{{ row.time }}</span
          >
          <span
            class="mt-1.5 h-[7px] w-[7px] shrink-0 rounded-full"
            :class="DOT_CLASS[row.level]"
          />
          <span class="flex min-w-0 flex-1 flex-wrap items-start gap-x-3 gap-y-0.5 leading-5">
            <span
              class="min-w-0 flex-1 basis-36 [overflow-wrap:anywhere]"
              :class="row.level === 'dim' ? 'text-lf-text-subtle' : 'text-lf-text'"
            >
              <span
                v-if="row.status"
                class="mr-1.5 font-medium"
                :class="row.level === 'error' ? 'text-lf-danger' : 'text-lf-warning'"
                >{{ row.status }}</span
              >
              {{ row.message }}
            </span>
            <span
              v-if="row.meta"
              class="w-full min-w-0 font-mono text-[11px] tabular-nums text-lf-text-subtle [overflow-wrap:anywhere] sm:w-auto sm:max-w-[45%]"
              >{{ row.meta }}</span
            >
          </span>
          <ChevronRight
            v-if="row.clickable"
            class="mt-1 h-3 w-3 shrink-0 text-lf-text-subtle"
            aria-hidden="true"
          />
        </component>
      </div>

      <NButton
        v-if="currentPosition.unseen > 0 && !currentPosition.followTail"
        type="primary"
        size="small"
        round
        class="!absolute bottom-4 left-1/2 -translate-x-1/2 shadow-lg"
        @click="scrollToBottom"
      >
        {{ t('workspace.job.detail.logNewEvents', { count: currentPosition.unseen }) }}
      </NButton>
    </div>

    <BatchDetailDrawer v-model:show="detailDrawerShow" :event="detailDrawerEvent" />
  </div>
</template>
