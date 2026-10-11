<script setup lang="ts">
import type { RoundDisplayState } from '@/utils/jobPresentation'

defineProps<{ state: RoundDisplayState }>()
</script>

<template>
  <span class="inline-flex shrink-0 items-center" aria-hidden="true">
    <IconCarbonCheckmarkFilled v-if="state === 'completed'" class="text-lf-success" />
    <IconCarbonErrorFilled v-else-if="state === 'failed'" class="text-lf-danger" />
    <IconCarbonInProgress
      v-else-if="state === 'running'"
      class="text-brand-500 motion-safe:animate-pulse"
    />
    <IconCarbonPauseFilled
      v-else-if="state === 'pausing'"
      class="text-lf-warning motion-safe:animate-pulse"
    />
    <IconCarbonPauseFilled v-else-if="state === 'paused'" class="text-lf-text-muted" />
    <IconCarbonStopFilled v-else-if="state === 'stopped'" class="text-lf-text-muted" />
    <IconCarbonSubtractAlt
      v-else-if="state === 'skipped' || state === 'not_run'"
      class="text-lf-text-subtle"
    />
    <IconCarbonTime v-else class="text-lf-text-subtle" />
  </span>
</template>
