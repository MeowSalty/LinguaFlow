<script setup lang="ts">
import { NCheckbox, NIcon, NTooltip } from 'naive-ui'
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'

const props = defineProps<{
  name: string
  path: string
  childCount: number
  checked: boolean
  indeterminate: boolean
  disabled: boolean
}>()

const emit = defineEmits<{
  open: [path: string]
  selection: [selected: boolean]
}>()

const { t } = useI18n()

const selectionAriaLabel = computed(() =>
  props.checked
    ? t('workspace.explorer.deselectDirectoryResources', { name: props.name })
    : t('workspace.explorer.selectDirectoryResources', { name: props.name }),
)
</script>

<template>
  <div
    class="group flex min-h-11 w-full items-center gap-2.5 rounded-lg border border-transparent bg-lf-surface/80 px-3 py-2 text-left transition-all hover:border-lf-border-soft hover:bg-lf-surface-elevated hover:shadow-sm hover:shadow-lf-shadow focus:outline-none focus-visible:border-brand-500 focus-visible:ring-2 focus-visible:ring-brand-500/20"
  >
    <NCheckbox
      :checked="checked"
      :indeterminate="indeterminate"
      :disabled="disabled"
      :aria-label="selectionAriaLabel"
      class="shrink-0"
      @click.stop
      @update:checked="emit('selection', $event)"
    />
    <button
      type="button"
      class="flex min-w-0 flex-1 items-center gap-3 rounded-md text-left focus:outline-none focus-visible:ring-2 focus-visible:ring-brand-500/20"
      @click="emit('open', path)"
    >
      <div
        class="flex h-7 w-7 shrink-0 items-center justify-center rounded-md bg-amber-50 text-amber-600 dark:bg-amber-500/15 dark:text-amber-300"
      >
        <NIcon size="14"><IconCarbonFolder /></NIcon>
      </div>
      <div class="min-w-0 flex-1">
        <NTooltip trigger="hover" placement="top-start">
          <template #trigger>
            <div class="truncate text-sm font-medium text-lf-text-strong" :title="name">
              {{ name }}
            </div>
          </template>
          <span class="block max-w-xs break-all">{{ path || name }}</span>
        </NTooltip>
        <div
          class="mt-0.5 inline-flex rounded-full bg-amber-50 px-2 py-px text-xs text-amber-700 dark:bg-amber-500/15 dark:text-amber-200"
        >
          {{ t('workspace.explorer.childCount', { count: childCount }) }}
        </div>
      </div>
      <NIcon
        size="16"
        class="shrink-0 text-lf-text-muted opacity-60 transition-all group-hover:translate-x-0.5 group-hover:opacity-100"
      >
        <IconCarbonChevronRight />
      </NIcon>
    </button>
  </div>
</template>
