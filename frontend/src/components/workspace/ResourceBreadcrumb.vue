<script setup lang="ts">
import { NBreadcrumb, NBreadcrumbItem } from 'naive-ui'
import { useI18n } from 'vue-i18n'

import type { BreadcrumbItem } from '@/stores/projectWorkspace'

const props = defineProps<{
  items: BreadcrumbItem[]
  projectName: string
}>()

const emit = defineEmits<{
  navigate: [path: string]
}>()

const { t } = useI18n()
</script>

<template>
  <NBreadcrumb>
    <NBreadcrumbItem @click="emit('navigate', '')">
      <span class="inline-flex items-center gap-1.5">
        <IconLucideHome class="h-3.5 w-3.5" />
        {{ props.projectName || t('workspace.explorer.rootLabel') }}
      </span>
    </NBreadcrumbItem>
    <NBreadcrumbItem
      v-for="(crumb, index) in props.items"
      :key="crumb.path"
      :clickable="index < props.items.length - 1"
      @click="emit('navigate', crumb.path)"
    >
      {{ crumb.label }}
    </NBreadcrumbItem>
  </NBreadcrumb>
</template>
