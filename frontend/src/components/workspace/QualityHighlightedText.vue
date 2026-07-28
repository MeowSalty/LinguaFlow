<script setup lang="ts">
import { computed } from 'vue'

import type { ApiSchemas } from '@/api/client'
import { renderQualityHighlightedText } from '@/composables/useQualityIssues'

const props = defineProps<{
  text: string
  issues?: ApiSchemas['QualityIssue'][]
}>()

const vnode = computed(() => renderQualityHighlightedText(props.text, props.issues))
</script>

<template>
  <component :is="vnode" />
</template>
