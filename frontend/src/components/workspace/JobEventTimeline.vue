<script setup lang="ts">
import { computed, nextTick, onMounted, onUnmounted, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'

import type { SSEEvent } from '@/composables/sseShared'

import BatchDetailDrawer from './BatchDetailDrawer.vue'
import TimelineRow from './TimelineRow.vue'

const { t } = useI18n()

const props = defineProps<{
  events: SSEEvent[]
  syntheticEvents?: SSEEvent[]
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

const JOB_EVENT_TYPES = new Set(['job_started', 'job_completed', 'job_failed', 'job_cancelled'])

const RESOURCE_EVENT_TYPES = new Set([
  'resource_started',
  'resource_completed',
  'resource_failed',
  'resource_cancelled',
])

const STAGE_EVENT_TYPES = new Set(['stage_start', 'stage_done'])

const filteredSyntheticEvents = computed(() => {
  const synthetic = props.syntheticEvents ?? []
  const live = props.events

  const liveJobTypes = new Set<string>()
  const liveResourceTypes = new Set<string>()
  const liveStageTypes = new Set<string>()

  for (const event of live) {
    if (JOB_EVENT_TYPES.has(event.type)) {
      liveJobTypes.add(event.type)
    } else if (RESOURCE_EVENT_TYPES.has(event.type)) {
      liveResourceTypes.add(event.type)
    } else if (STAGE_EVENT_TYPES.has(event.type)) {
      liveStageTypes.add(event.type)
    }
  }

  return synthetic.filter((event) => {
    if (JOB_EVENT_TYPES.has(event.type)) {
      return !liveJobTypes.has(event.type)
    }
    if (RESOURCE_EVENT_TYPES.has(event.type)) {
      return !liveResourceTypes.has(event.type)
    }
    if (STAGE_EVENT_TYPES.has(event.type)) {
      return !liveStageTypes.has(event.type)
    }
    return true
  })
})

const realItemCache = new WeakMap<SSEEvent, SSEEvent & { _key: string }>()

const timelineItems = computed<Array<SSEEvent & { _key: string; _synthetic?: boolean }>>(() => {
  const real = props.events.map((e) => {
    let cached = realItemCache.get(e)
    if (!cached) {
      cached = { ...e, _key: String(e.seq) }
      realItemCache.set(e, cached)
    }
    return cached
  })
  const syn = filteredSyntheticEvents.value.map((e, i) => ({
    ...e,
    _key: `syn-${i}-${e.type}`,
    _synthetic: true,
  }))
  return [...syn, ...real]
})

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

onMounted(() => {
  prevEventsLength.value = props.events.length
  tailSeq.value = props.events.at(-1)?.seq ?? 0
  headSeq.value = props.events.at(0)?.seq ?? 0
  nextTick(() => scrollToBottom())
  const el = scrollContainerRef.value
  if (el) {
    el.addEventListener('wheel', onWheel, { passive: false })
    el.addEventListener('touchstart', onTouchStart, { passive: true })
    el.addEventListener('touchmove', onTouchMove, { passive: false })
    el.addEventListener('touchend', onTouchEnd, { passive: true })
  }
})

onUnmounted(() => {
  const el = scrollContainerRef.value
  if (el) {
    el.removeEventListener('wheel', onWheel)
    el.removeEventListener('touchstart', onTouchStart)
    el.removeEventListener('touchmove', onTouchMove)
    el.removeEventListener('touchend', onTouchEnd)
  }
  if (wheelTimer) {
    clearTimeout(wheelTimer)
    wheelTimer = null
  }
})
</script>

<template>
  <div class="space-y-2">
    <div class="flex items-center justify-between">
      <h4
        class="border-l-2 border-brand-500 pl-2 text-xs font-semibold uppercase tracking-wider text-lf-text-muted"
      >
        {{ t('workspace.job.events.title') }}
      </h4>
      <div class="flex items-center gap-2">
        <span
          v-if="jobEnded"
          class="inline-flex items-center gap-1 rounded-full bg-gray-400/10 px-1.5 py-0.5 text-[10px] text-lf-text-muted"
        >
          <span class="inline-block h-1.5 w-1.5 rounded-full bg-gray-400" />
          {{ t('workspace.job.events.jobEnded') }}
        </span>
        <span
          v-else-if="connected"
          class="inline-flex items-center gap-1 rounded-full bg-green-500/10 px-1.5 py-0.5 text-[10px] text-green-500"
        >
          <span class="inline-block h-1.5 w-1.5 rounded-full bg-green-500" />
          实时
        </span>
        <span
          v-else
          class="inline-flex items-center gap-1 rounded-full bg-gray-400/10 px-1.5 py-0.5 text-[10px] text-lf-text-muted"
        >
          <span class="inline-block h-1.5 w-1.5 rounded-full bg-gray-400" />
          离线
        </span>
        <NButton quaternary size="tiny" @click="emit('clear')">
          {{ t('workspace.actions.clear') }}
        </NButton>
      </div>
    </div>

    <div class="relative min-h-50">
      <div class="rounded-lg border border-lf-border-soft bg-lf-surface/40 p-3">
        <div
          v-if="timelineItems.length > 0"
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
          <div v-for="(item, index) in timelineItems" :key="item._key">
            <TimelineRow
              :event="item"
              :is-last="index === timelineItems.length - 1"
              @open-detail="openBatchDetail"
            />
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
