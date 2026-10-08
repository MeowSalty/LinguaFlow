<script setup lang="ts">
import { computed, nextTick, ref, useId, watch } from 'vue'

const props = withDefaults(
  defineProps<{
    value: string
    label: string
    tabs: readonly { name: string; label: string }[]
    keepMounted?: boolean | readonly string[]
  }>(),
  { keepMounted: false },
)
const emit = defineEmits<{ 'update:value': [value: string] }>()
const id = useId()
const tablist = ref<HTMLElement | null>(null)
const visited = ref<string[]>([])
const active = computed(() =>
  props.tabs.some((tab) => tab.name === props.value) ? props.value : props.tabs[0]?.name,
)
watch(
  active,
  (name) => {
    if (name !== undefined && !visited.value.includes(name))
      visited.value = [...visited.value, name]
  },
  { immediate: true },
)
const tabId = (index: number) => `${id}-tab-${index}`
const panelId = (index: number) => `${id}-panel-${index}`

async function activate(name: string, index: number) {
  emit('update:value', name)
  await nextTick()
  const button = tablist.value?.querySelectorAll<HTMLButtonElement>('[role="tab"]')[index]
  button?.focus({ preventScroll: true })
  if (button && tablist.value) {
    const list = tablist.value
    const left = button.offsetLeft
    if (left < list.scrollLeft) list.scrollLeft = left
    else if (left + button.offsetWidth > list.scrollLeft + list.clientWidth)
      list.scrollLeft = left + button.offsetWidth - list.clientWidth
  }
}
function navigate(event: KeyboardEvent, index: number) {
  if (!['ArrowLeft', 'ArrowRight', 'Home', 'End'].includes(event.key)) return
  event.preventDefault()
  const count = props.tabs.length
  const next =
    event.key === 'Home'
      ? 0
      : event.key === 'End'
        ? count - 1
        : (index + (event.key === 'ArrowRight' ? 1 : -1) + count) % count
  const tab = props.tabs[next]
  if (tab) void activate(tab.name, next)
}
</script>

<template>
  <div class="storage-tabs">
    <div ref="tablist" role="tablist" :aria-label="label" class="storage-tablist">
      <button
        v-for="(tab, index) in tabs"
        :id="tabId(index)"
        :key="tab.name"
        type="button"
        role="tab"
        class="storage-tab"
        :aria-selected="active === tab.name"
        :aria-controls="panelId(index)"
        :tabindex="active === tab.name ? 0 : -1"
        @click="activate(tab.name, index)"
        @keydown="navigate($event, index)"
      >
        {{ tab.label }}
      </button>
    </div>
    <template v-for="(tab, index) in tabs" :key="tab.name">
      <section
        :id="panelId(index)"
        role="tabpanel"
        :aria-labelledby="tabId(index)"
        :hidden="active !== tab.name"
        :tabindex="active === tab.name ? 0 : -1"
        class="storage-tab-panel"
      >
        <slot
          v-if="
            active === tab.name ||
            ((keepMounted === true ||
              (Array.isArray(keepMounted) && keepMounted.includes(tab.name))) &&
              visited.includes(tab.name))
          "
          :name="tab.name"
        />
      </section>
    </template>
  </div>
</template>

<style scoped>
.storage-tabs {
  min-width: 0;
}
.storage-tablist {
  position: relative;
  display: flex;
  gap: 28px;
  max-width: 100%;
  overflow-x: auto;
  border-bottom: 1px solid var(--lf-border-soft);
  scrollbar-width: thin;
}
.storage-tab {
  position: relative;
  flex-shrink: 0;
  border: 0;
  background: transparent;
  padding: 11px 2px 12px;
  color: var(--lf-text-muted);
  font-size: 14px;
  line-height: 22px;
  white-space: nowrap;
  cursor: pointer;
}
.storage-tab:hover {
  color: var(--lf-text-strong);
}
.storage-tab[aria-selected='true'] {
  color: var(--lf-brand-600);
  font-weight: 500;
}
.storage-tab[aria-selected='true']::after {
  position: absolute;
  right: 0;
  bottom: 0;
  left: 0;
  height: 2px;
  border-radius: 2px;
  background: var(--lf-brand-500);
  content: '';
}
.storage-tab:focus-visible {
  border-radius: 4px;
  outline: 2px solid var(--lf-brand-500);
  outline-offset: -3px;
}
.storage-tab-panel {
  min-width: 0;
  padding-top: 24px;
}
.storage-tab-panel[hidden] {
  display: none;
}
.storage-tab-panel:focus-visible {
  border-radius: 8px;
  outline: 2px solid var(--lf-brand-500);
  outline-offset: 3px;
}
</style>
