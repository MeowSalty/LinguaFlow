<script setup lang="ts">
import { NIcon, NTooltip } from 'naive-ui'
import { useI18n } from 'vue-i18n'

defineProps<{
  name: string
  path: string
  childCount: number
}>()

const emit = defineEmits<{
  open: [path: string]
}>()

const { t } = useI18n()
</script>

<template>
  <button
    type="button"
    class="group flex min-h-19 w-full items-center gap-3 rounded-lg border border-transparent bg-lf-surface/80 px-4 py-3 text-left transition-all hover:border-lf-border-soft hover:bg-lf-surface-elevated hover:shadow-sm hover:shadow-lf-shadow focus:outline-none focus-visible:border-brand-500 focus-visible:ring-2 focus-visible:ring-brand-500/20"
    @click="emit('open', path)"
  >
    <div
      class="flex h-10 w-10 shrink-0 items-center justify-center rounded-lg bg-amber-50 text-amber-600 dark:bg-amber-500/15 dark:text-amber-300"
    >
      <NIcon size="18"><IconCarbonFolder /></NIcon>
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
        class="mt-1.5 inline-flex rounded-full bg-amber-50 px-2 py-0.5 text-xs text-amber-700 dark:bg-amber-500/15 dark:text-amber-200"
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
</template>
