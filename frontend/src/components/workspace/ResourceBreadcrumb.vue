<script setup lang="ts">
import { NBreadcrumb, NBreadcrumbItem, NDropdown, type DropdownOption } from 'naive-ui'
import { computed, nextTick, onBeforeUnmount, onMounted, ref, shallowRef, watch } from 'vue'
import { useI18n } from 'vue-i18n'

import type { BreadcrumbItem } from '@/stores/projectWorkspace'

const props = defineProps<{
  items: BreadcrumbItem[]
  projectName: string
  /** 当前是否处于 EPUB 虚拟目录中（最后一项为 EPUB 名称，应禁用点击） */
  epubDirectoryActive?: boolean
}>()

const emit = defineEmits<{
  navigate: [path: string]
}>()

interface VisibleBreadcrumbItem extends BreadcrumbItem {
  originalIndex: number
}

const { t } = useI18n()

const overflowTolerance = 2

const containerRef = shallowRef<HTMLElement | null>(null)
const contentRef = shallowRef<HTMLElement | null>(null)
const collapsedCount = ref(0)

let resizeObserver: ResizeObserver | null = null
let measureFrame: number | null = null
let measureToken = 0

const minVisibleTailCount = 1

const maxCollapsibleCount = computed(() => Math.max(0, props.items.length - minVisibleTailCount))

const shouldCollapse = computed(() => collapsedCount.value > 0)

const visibleItems = computed<VisibleBreadcrumbItem[]>(() => {
  if (!shouldCollapse.value) {
    return props.items.map((item, index) => ({ ...item, originalIndex: index }))
  }

  const hiddenEnd = Math.min(collapsedCount.value, props.items.length - minVisibleTailCount)
  return props.items
    .map((item, index) => ({ ...item, originalIndex: index }))
    .filter((item) => item.originalIndex >= hiddenEnd)
})

const collapsedItems = computed(() => {
  if (!shouldCollapse.value) {
    return []
  }

  const hiddenEnd = Math.min(collapsedCount.value, props.items.length - minVisibleTailCount)
  return props.items.slice(0, hiddenEnd)
})

const collapsedOptions = computed<DropdownOption[]>(() =>
  collapsedItems.value.map((item) => ({
    label: item.label,
    key: item.path,
  })),
)

const isOverflowing = (): boolean => {
  const container = containerRef.value
  const content = contentRef.value

  if (!container || !content) {
    return false
  }

  return content.scrollWidth > container.clientWidth + overflowTolerance
}

const measureCollapse = async (): Promise<void> => {
  const token = ++measureToken
  const maxCount = maxCollapsibleCount.value

  for (let count = 0; count <= maxCount; count++) {
    collapsedCount.value = count
    await nextTick()

    if (token !== measureToken) {
      return
    }

    if (!isOverflowing()) {
      return
    }
  }

  collapsedCount.value = maxCount
}

const scheduleMeasure = (): void => {
  if (measureFrame !== null) {
    cancelAnimationFrame(measureFrame)
  }

  measureFrame = requestAnimationFrame(() => {
    measureFrame = null
    void measureCollapse()
  })
}

/** 判断指定 originalIndex 的项是否为 EPUB 虚拟目录末尾项（应禁用点击） */
const isEpubSuffixItem = (originalIndex: number): boolean =>
  props.epubDirectoryActive === true && originalIndex === props.items.length - 1

const navigateTo = (path: string): void => {
  emit('navigate', path)
}

const handleCollapsedSelect = (key: string | number): void => {
  navigateTo(String(key))
}

watch(
  () =>
    `${props.projectName}\n${props.items.map((item) => `${item.path}\0${item.label}`).join('\n')}`,
  scheduleMeasure,
  { flush: 'post' },
)

onMounted(() => {
  if (containerRef.value) {
    resizeObserver = new ResizeObserver(scheduleMeasure)
    resizeObserver.observe(containerRef.value)
  }

  scheduleMeasure()
})

onBeforeUnmount(() => {
  resizeObserver?.disconnect()

  if (measureFrame !== null) {
    cancelAnimationFrame(measureFrame)
  }

  measureToken++
})
</script>

<template>
  <div ref="containerRef" class="min-w-0 max-w-full overflow-hidden">
    <div ref="contentRef" class="inline-block whitespace-nowrap align-bottom">
      <NBreadcrumb class="min-w-0 text-sm">
        <NBreadcrumbItem :clickable="props.items.length > 0" @click="navigateTo('')">
          <span
            class="inline-flex max-w-[20rem] items-center gap-1.5 truncate font-medium"
            :class="props.items.length === 0 ? 'text-lf-text-strong' : 'text-lf-text-muted'"
            :title="props.projectName || t('workspace.explorer.rootLabel')"
          >
            <IconCarbonHome class="h-3.5 w-3.5 shrink-0 text-lf-text-subtle" />
            <span class="truncate">{{
              props.projectName || t('workspace.explorer.rootLabel')
            }}</span>
          </span>
        </NBreadcrumbItem>
        <template v-if="shouldCollapse">
          <NBreadcrumbItem :clickable="false">
            <NDropdown :options="collapsedOptions" trigger="click" @select="handleCollapsedSelect">
              <button
                type="button"
                class="inline-flex h-6 items-center rounded-md px-1.5 text-lf-text-muted transition-colors hover:bg-lf-surface-muted hover:text-lf-text-strong"
                :title="collapsedItems.map((item) => item.label).join(' / ')"
                @click.stop
              >
                <IconCarbonOverflowMenuHorizontal class="h-4 w-4" />
              </button>
            </NDropdown>
          </NBreadcrumbItem>
          <NBreadcrumbItem
            v-for="crumb in visibleItems"
            :key="crumb.path"
            :clickable="
              crumb.originalIndex < props.items.length - 1 && !isEpubSuffixItem(crumb.originalIndex)
            "
            @click="!isEpubSuffixItem(crumb.originalIndex) && navigateTo(crumb.path)"
          >
            <span
              class="inline-block max-w-[20rem] truncate align-bottom"
              :class="[
                crumb.originalIndex === props.items.length - 1
                  ? 'font-semibold text-lf-text-strong'
                  : 'text-lf-text-muted hover:text-lf-text-strong',
                isEpubSuffixItem(crumb.originalIndex) && 'pointer-events-none opacity-70',
              ]"
              :title="crumb.label"
            >
              {{ crumb.label }}
            </span>
          </NBreadcrumbItem>
        </template>
        <template v-else>
          <NBreadcrumbItem
            v-for="crumb in visibleItems"
            :key="crumb.path"
            :clickable="
              crumb.originalIndex < props.items.length - 1 && !isEpubSuffixItem(crumb.originalIndex)
            "
            @click="!isEpubSuffixItem(crumb.originalIndex) && navigateTo(crumb.path)"
          >
            <span
              class="inline-block max-w-[20rem] truncate align-bottom"
              :class="[
                crumb.originalIndex === props.items.length - 1
                  ? 'font-semibold text-lf-text-strong'
                  : 'text-lf-text-muted hover:text-lf-text-strong',
                isEpubSuffixItem(crumb.originalIndex) && 'pointer-events-none opacity-70',
              ]"
              :title="crumb.label"
            >
              {{ crumb.label }}
            </span>
          </NBreadcrumbItem>
        </template>
      </NBreadcrumb>
    </div>
  </div>
</template>
