<script setup lang="ts">
import { computed, nextTick, onMounted, onUnmounted, ref, watch } from 'vue'
import { NButton, NEmpty } from 'naive-ui'
import { useI18n } from 'vue-i18n'

import type { BatchEventMetadata, PoolEventMetadata, SSEEvent } from '@/composables/sseShared'
import {
  eventLevelType,
  formatDuration,
  formatTokens,
  getStageLabel,
  isBatchEvent,
  isPoolEvent,
  poolTimelineType,
} from '@/composables/useWorkspaceUtils'
import { formatDateTime } from '@/utils/datetime'

import BatchDetailDrawer from './BatchDetailDrawer.vue'

const { t } = useI18n()

const props = defineProps<{
  events: SSEEvent[]
  connected?: boolean
  hasOlder?: boolean
  loadingOlder?: boolean
  jobEnded?: boolean
}>()

const emit = defineEmits<{
  clear: []
  'load-older': []
}>()

const scrollContainerRef = ref<HTMLElement | null>(null)
const isNearTop = ref(false)
const isNearBottom = ref(true)
const hasNewEvents = ref(false)
const prevEventsLength = ref(0)
const tailSeq = ref(0)
const headSeq = ref(0)
// 头部前插更早事件时，记录滚动位置，前插后恢复，防止视觉跳动
const pendingScrollRestore = ref<number | null>(null)
const prevScrollHeight = ref(0)
// pull-to-load：下拉拉拽距离（px），超过阈值触发加载
const PULL_THRESHOLD = 60
const pullDistance = ref(0)
let wheelAccum = 0
let wheelActive = false
let wheelTimer: ReturnType<typeof setTimeout> | null = null
// 批次详情抽屉
const detailDrawerShow = ref(false)
const detailDrawerEvent = ref<SSEEvent | null>(null)

const canLoadOlder = computed(() => props.hasOlder && !props.loadingOlder)

const pullIndicatorLabel = computed(() => {
  if (props.loadingOlder) return t('workspace.job.events.loadingOlder')
  if (pullDistance.value >= PULL_THRESHOLD) return t('workspace.job.events.releaseToLoad')
  return t('workspace.job.events.pullToLoad')
})

const openBatchDetail = (event: SSEEvent): void => {
  detailDrawerEvent.value = event
  detailDrawerShow.value = true
}

// ── 日志行视图：每事件一行（时间 + 状态点 + 消息 + 行内元数据）──

/** 行级别：状态点颜色与消息着色的依据 */
type LogLevel = 'info' | 'success' | 'warning' | 'error' | 'dim'

interface LogRow {
  key: string
  time: string
  level: LogLevel
  message: string
  /** 右对齐淡显元数据（后端名 · Token 用量），仅批次事件有 */
  meta: string
  /** 批次事件：整行可点击打开批次详情 */
  clickable: boolean
  /** 池事件：弱化显示（小号灰字、空心点） */
  dim: boolean
  event: SSEEvent
}

const DOT_CLASS: Record<LogLevel, string> = {
  info: 'bg-brand-500',
  success: 'bg-lf-success',
  warning: 'bg-lf-warning',
  error: 'bg-lf-danger',
  dim: 'border border-lf-text-subtle bg-transparent',
}

const formatEventTime = (value: string): string => {
  return formatDateTime(value, {
    hour: '2-digit',
    minute: '2-digit',
    second: '2-digit',
  })
}

/** 批次事件摘要：「翻译 · 66 段 · 1.7s」 */
const getBatchSummary = (event: SSEEvent): string => {
  const meta = event.metadata as unknown as BatchEventMetadata | undefined
  if (!meta) return event.message
  const parts: string[] = []
  if (event.stage) parts.push(getStageLabel(event.stage))
  parts.push(t('workspace.job.events.batch.segments', { count: meta.segment_count }))
  if (meta.duration_ms) parts.push(formatDuration(meta.duration_ms))
  return parts.join(' · ')
}

/** 批次事件行右端元数据：「线衣 | DS V4F · 1.2k↑ 3.4k↓ Token · 2.0k tok/s」 */
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
  // 输出速度：输出 Token ÷ 耗时（失败批次通常无输出，自然不显示）
  if (meta.output_tokens > 0 && meta.duration_ms > 0) {
    const rate = Math.round((meta.output_tokens / meta.duration_ms) * 1000)
    parts.push(t('workspace.job.events.batch.tokenSpeed', { rate: formatTokens(rate) }))
  }
  return parts.filter(Boolean).join(' · ')
}

/** 池事件单行：「质量裁决 · 池开始 · 池 1/4 · 1 批 · 1 段待处理 · 缩放 1.00」 */
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

/** 轮次开始/完成消息内联阶段名：「轮次开始: adjudicate (1 段)」→「轮次开始 · 质量裁决 · 1 段」 */
const formatRoundMessage = (event: SSEEvent): string => {
  if ((event.type === 'stage_start' || event.type === 'stage_done') && event.stage) {
    const match = event.message.match(/^(.*?):\s*\w+\s*\((.*)\)$/)
    if (match) return [match[1], getStageLabel(event.stage), match[2]].join(' · ')
  }
  return event.message
}

const getRowLevel = (event: SSEEvent): LogLevel => {
  if (isPoolEvent(event.type)) {
    const meta = event.metadata as unknown as PoolEventMetadata | undefined
    return poolTimelineType(meta?.phase, event.level) === 'error' ? 'error' : 'dim'
  }
  if (isBatchEvent(event.type)) {
    const meta = event.metadata as unknown as BatchEventMetadata | undefined
    if (meta?.status === 'failed') return 'error'
    if (meta?.status === 'partial') return 'warning'
    if (meta?.status === 'success') return 'success'
    return eventLevelType(event.level)
  }
  // 完成类事件用成功色，其余按事件级别
  if (
    event.type === 'job_completed' ||
    event.type === 'resource_completed' ||
    event.type === 'stage_done'
  ) {
    return 'success'
  }
  return eventLevelType(event.level)
}

const buildLogRow = (event: SSEEvent): LogRow => {
  const batch = isBatchEvent(event.type)
  const pool = isPoolEvent(event.type)
  return {
    key: String(event.seq),
    time: formatEventTime(event.created_at),
    level: getRowLevel(event),
    message: batch ? getBatchSummary(event) : pool ? getPoolLine(event) : formatRoundMessage(event),
    meta: batch ? getBatchMeta(event) : '',
    clickable: batch,
    dim: pool,
    event,
  }
}

const rowCache = new WeakMap<SSEEvent, LogRow>()

const logRows = computed<LogRow[]>(() =>
  props.events.map((event) => {
    let cached = rowCache.get(event)
    if (!cached) {
      cached = buildLogRow(event)
      rowCache.set(event, cached)
    }
    return cached
  }),
)

let scrollTicking = false

const onScroll = (e: Event): void => {
  if (scrollTicking) return
  scrollTicking = true
  requestAnimationFrame(() => {
    scrollTicking = false
    const el = e.target as HTMLElement
    if (!el) return
    isNearTop.value = el.scrollTop <= 50
    isNearBottom.value = el.scrollTop + el.clientHeight >= el.scrollHeight - 50
  })
}

const triggerLoad = (): void => {
  if (!canLoadOlder.value) return
  hasNewEvents.value = false
  emit('load-older')
}

// 桌面端：wheel 在顶部继续上滚 → 累积拉拽距离，松手（停止滚动）后判定
const onWheel = (e: WheelEvent): void => {
  if (e.deltaY > 0) {
    wheelAccum = 0
    wheelActive = false
    pullDistance.value = 0
    if (wheelTimer) {
      clearTimeout(wheelTimer)
      wheelTimer = null
    }
    return
  }
  const el = scrollContainerRef.value
  if (!el || el.scrollTop > 0) {
    wheelAccum = 0
    wheelActive = false
    pullDistance.value = 0
    if (wheelTimer) {
      clearTimeout(wheelTimer)
      wheelTimer = null
    }
    return
  }
  // 已在顶部 + 向上滚动
  e.preventDefault()
  wheelActive = true
  wheelAccum += Math.abs(e.deltaY)
  pullDistance.value = Math.min(wheelAccum * 0.5, PULL_THRESHOLD * 1.4)
  // 停止滚动一段时间（视为松手）→ 达到阈值则触发
  if (wheelTimer) clearTimeout(wheelTimer)
  wheelTimer = setTimeout(() => {
    if (pullDistance.value >= PULL_THRESHOLD) {
      triggerLoad()
    }
    wheelAccum = 0
    wheelActive = false
    pullDistance.value = 0
    wheelTimer = null
  }, 140)
}

const endWheel = (): void => {
  if (wheelActive) {
    wheelActive = false
    wheelAccum = 0
    pullDistance.value = 0
  }
  if (wheelTimer) {
    clearTimeout(wheelTimer)
    wheelTimer = null
  }
}

// 移动端：touch 在顶部继续下拉，松手后判定是否触发
let touchStartY = 0
const onTouchStart = (e: TouchEvent): void => {
  touchStartY = e.touches[0]?.clientY ?? 0
}
const onTouchMove = (e: TouchEvent): void => {
  const el = scrollContainerRef.value
  if (!el || el.scrollTop > 0) {
    pullDistance.value = 0
    return
  }
  const currentY = e.touches[0]?.clientY
  if (currentY == null) return
  const dy = currentY - touchStartY
  if (dy <= 0) {
    pullDistance.value = 0
    return
  }
  e.preventDefault()
  pullDistance.value = Math.min(dy * 0.5, PULL_THRESHOLD * 1.4)
}
const onTouchEnd = (): void => {
  if (pullDistance.value >= PULL_THRESHOLD) {
    triggerLoad()
  }
  pullDistance.value = 0
}

const scrollToBottom = (): void => {
  prevEventsLength.value = props.events.length
  const el = scrollContainerRef.value
  if (el) el.scrollTop = el.scrollHeight
  hasNewEvents.value = false
}

watch(
  () => props.events.length,
  (newLen) => {
    const newTail = props.events.at(-1)?.seq ?? 0
    const newHead = props.events.at(0)?.seq ?? 0

    // 头部前插（loadOlder）：头部 seq 变小 → 记录滚动位置和内容高度，前插后恢复
    if (newHead < headSeq.value) {
      const el = scrollContainerRef.value
      if (el) {
        prevScrollHeight.value = el.scrollHeight
        pendingScrollRestore.value = el.scrollTop
      }
    } else if (newTail > tailSeq.value) {
      // 尾部推进（新事件）：自动滚底或提示
      tailSeq.value = newTail
      if (isNearBottom.value) {
        prevEventsLength.value = newLen
        nextTick(() => {
          scrollToBottom()
        })
      } else {
        hasNewEvents.value = true
      }
    }

    headSeq.value = newHead
    if (newTail > tailSeq.value) tailSeq.value = newTail
  },
)

// 前插更早事件后，恢复原滚动位置（补偿新增内容高度），消除跳动
watch(
  () => props.events.length,
  () => {
    const saved = pendingScrollRestore.value
    if (saved == null) return
    nextTick(() => {
      const el = scrollContainerRef.value
      if (el) el.scrollTop = saved + (el.scrollHeight - prevScrollHeight.value)
      pendingScrollRestore.value = null
    })
  },
)

const attachScrollListeners = (el: HTMLElement): void => {
  el.addEventListener('wheel', onWheel, { passive: false })
  el.addEventListener('touchstart', onTouchStart, { passive: true })
  el.addEventListener('touchmove', onTouchMove, { passive: false })
  el.addEventListener('touchend', onTouchEnd, { passive: true })
}

const detachScrollListeners = (el: HTMLElement): void => {
  el.removeEventListener('wheel', onWheel)
  el.removeEventListener('touchstart', onTouchStart)
  el.removeEventListener('touchmove', onTouchMove)
  el.removeEventListener('touchend', onTouchEnd)
}

let currentScrollEl: HTMLElement | null = null

// 滚动容器可能在 onMounted 之后才渲染（事件到达后 v-if 才为 true），
// 用 watch 确保监听器在容器出现时挂载、消失时卸载
watch(scrollContainerRef, (el, oldEl) => {
  if (oldEl) detachScrollListeners(oldEl)
  if (el) {
    attachScrollListeners(el)
    currentScrollEl = el
  } else {
    currentScrollEl = null
  }
})

onMounted(() => {
  prevEventsLength.value = props.events.length
  tailSeq.value = props.events.at(-1)?.seq ?? 0
  headSeq.value = props.events.at(0)?.seq ?? 0
  nextTick(() => scrollToBottom())
})

onUnmounted(() => {
  if (currentScrollEl) detachScrollListeners(currentScrollEl)
  if (wheelTimer) {
    clearTimeout(wheelTimer)
    wheelTimer = null
  }
})
</script>

<template>
  <div class="space-y-2">
    <div class="flex items-center justify-between">
      <h4 class="text-[11px] font-medium tracking-wide uppercase text-lf-text-subtle">
        {{ t('workspace.job.events.title') }}
      </h4>
      <div class="flex items-center gap-2">
        <span
          v-if="jobEnded"
          class="inline-flex items-center gap-1 rounded-full bg-lf-text-subtle/10 px-1.5 py-0.5 text-[10px] text-lf-text-muted"
        >
          <span class="inline-block h-1.5 w-1.5 rounded-full bg-lf-text-subtle" />
          {{ t('workspace.job.events.jobEnded') }}
        </span>
        <span
          v-else-if="connected"
          class="inline-flex items-center gap-1 rounded-full bg-lf-success-soft px-1.5 py-0.5 text-[10px] text-lf-success"
        >
          <span class="inline-block h-1.5 w-1.5 rounded-full bg-lf-success" />
          {{ t('workspace.job.events.live') }}
        </span>
        <span
          v-else
          class="inline-flex items-center gap-1 rounded-full bg-lf-text-subtle/10 px-1.5 py-0.5 text-[10px] text-lf-text-muted"
        >
          <span class="inline-block h-1.5 w-1.5 rounded-full bg-lf-text-subtle" />
          {{ t('workspace.job.events.offline') }}
        </span>
        <NButton quaternary size="tiny" @click="emit('clear')">
          {{ t('workspace.actions.clear') }}
        </NButton>
      </div>
    </div>

    <div class="relative min-h-50">
      <div class="rounded-lg border border-lf-border-soft bg-lf-surface/40 p-3">
        <div
          v-if="logRows.length > 0"
          ref="scrollContainerRef"
          class="max-h-[60vh] overflow-y-auto"
          style="overflow-anchor: none"
          @scroll="onScroll"
          @mouseleave="endWheel"
        >
          <!-- Top: reached oldest or pull-to-load indicator -->
          <div
            v-if="!hasOlder && !loadingOlder"
            class="py-2 text-center text-xs text-lf-text-muted"
          >
            {{ t('workspace.job.events.reachedOldest') }}
          </div>
          <div
            v-else
            class="flex items-center justify-center overflow-hidden text-xs text-lf-text-muted transition-[height] duration-150"
            :style="{ height: pullDistance + 'px' }"
          >
            <span v-if="pullDistance > 0 || isNearTop">{{ pullIndicatorLabel }}</span>
          </div>
          <!-- 控制台式日志流：时间 + 状态点 + 消息 + 行内元数据 -->
          <div>
            <div
              v-for="row in logRows"
              :key="row.key"
              class="group flex items-start gap-2.5 rounded-md px-2"
              :class="[
                row.clickable ? 'cursor-pointer hover:bg-lf-hover' : '',
                row.dim ? 'py-px text-[11.5px]' : 'py-0.5 text-[12.5px]',
              ]"
              @click="row.clickable && openBatchDetail(row.event)"
            >
              <span
                class="w-14 shrink-0 pt-px font-mono text-[11px] leading-5 tabular-nums text-lf-text-subtle"
              >
                {{ row.time }}
              </span>
              <span
                class="mt-1.5 h-[7px] w-[7px] shrink-0 rounded-full"
                :class="DOT_CLASS[row.level]"
              />
              <span
                class="min-w-0 flex-1 break-words leading-5"
                :class="
                  row.level === 'error'
                    ? 'text-lf-danger'
                    : row.dim
                      ? 'text-lf-text-subtle'
                      : 'text-lf-text'
                "
              >
                {{ row.message }}
              </span>
              <span
                v-if="row.meta"
                class="hidden shrink-0 pl-3 pt-px font-mono text-[11px] leading-5 tabular-nums text-lf-text-subtle sm:inline"
              >
                {{ row.meta }}
              </span>
              <span
                v-if="row.clickable"
                class="shrink-0 pt-px text-[11px] leading-5 text-lf-text-subtle opacity-0 transition-opacity group-hover:opacity-100"
              >
                ›
              </span>
            </div>
          </div>
        </div>
        <div v-else class="py-6 text-center">
          <NEmpty size="small" :description="t('workspace.job.events.empty')" />
        </div>
      </div>

      <!-- Floating "new events" button -->
      <Transition
        enter-active-class="transition-opacity duration-200"
        leave-active-class="transition-opacity duration-200"
        enter-from-class="opacity-0"
        leave-to-class="opacity-0"
      >
        <button
          v-if="hasNewEvents && !isNearBottom"
          class="absolute bottom-4 left-1/2 -translate-x-1/2 rounded-full bg-brand-500 px-4 py-1.5 text-xs font-medium text-white shadow-lg hover:bg-brand-600"
          @click="scrollToBottom"
        >
          {{ t('workspace.job.events.newEvents', { count: events.length - prevEventsLength + 1 }) }}
        </button>
      </Transition>
    </div>

    <BatchDetailDrawer v-model:show="detailDrawerShow" :event="detailDrawerEvent" />
  </div>
</template>
