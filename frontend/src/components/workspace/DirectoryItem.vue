<script setup lang="ts">
import { NIcon } from 'naive-ui'
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
    class="group flex w-full items-center gap-3 rounded-lg border border-transparent bg-lf-surface-muted px-4 py-3 text-left transition-colors hover:border-lf-border hover:bg-lf-surface"
    @click="emit('open', path)"
  >
    <div class="flex h-9 w-9 shrink-0 items-center justify-center rounded-md bg-amber-50 text-amber-500 dark:bg-amber-500/10">
      <NIcon size="18"><IconLucideFolder /></NIcon>
    </div>
    <div class="min-w-0 flex-1">
      <div class="truncate text-sm font-medium text-lf-text-strong">{{ name }}</div>
      <div class="mt-0.5 text-xs text-lf-text-muted">
        {{ t('workspace.explorer.childCount', { count: childCount }) }}
      </div>
    </div>
    <NIcon size="16" class="shrink-0 text-lf-text-muted opacity-0 transition-opacity group-hover:opacity-100">
      <IconLucideChevronRight />
    </NIcon>
  </button>
</template>
