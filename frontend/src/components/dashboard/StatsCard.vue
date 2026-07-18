<script setup lang="ts">
import { Icon as IconifyIcon } from '@iconify/vue'

withDefaults(
  defineProps<{
    title: string
    value: number | string
    icon: string
    tone?: 'brand' | 'info' | 'accent' | 'neutral'
    trend?: 'up' | 'down' | 'neutral'
    trendValue?: string
    loading?: boolean
  }>(),
  {
    tone: 'brand',
  },
)

const toneClass: Record<string, string> = {
  brand: 'bg-lf-brand-soft text-brand-600',
  info: 'bg-lf-info-soft text-lf-info',
  accent: 'bg-lf-accent-soft text-lf-accent',
  neutral: 'bg-lf-surface-muted text-lf-text-muted',
}

const trendColors: Record<string, string> = {
  up: 'text-brand-600',
  down: 'text-red-500',
  neutral: 'text-lf-text-subtle',
}

const trendIcons: Record<string, string> = {
  up: 'carbon:arrow-up',
  down: 'carbon:arrow-down',
  neutral: 'carbon:arrows-horizontal',
}
</script>

<template>
  <div class="lf-metric group relative overflow-hidden">
    <div
      class="pointer-events-none absolute -right-6 -top-6 h-20 w-20 rounded-full opacity-40 blur-2xl transition-opacity group-hover:opacity-70"
      :class="{
        'bg-brand-500/30': tone === 'brand',
        'bg-lf-info/30': tone === 'info',
        'bg-lf-accent/30': tone === 'accent',
        'bg-lf-text-subtle/20': tone === 'neutral',
      }"
    />

    <template v-if="loading">
      <div class="flex items-center justify-between">
        <div class="h-4 w-20 animate-pulse rounded bg-lf-border-soft" />
        <div class="h-10 w-10 animate-pulse rounded-xl bg-lf-border-soft" />
      </div>
      <div class="mt-4 h-8 w-24 animate-pulse rounded bg-lf-border-soft" />
    </template>

    <template v-else>
      <div class="relative flex items-center justify-between gap-3">
        <span class="lf-metric-label">{{ title }}</span>
        <div
          class="flex h-10 w-10 items-center justify-center rounded-xl text-lg"
          :class="toneClass[tone]"
        >
          <IconifyIcon :icon="icon" />
        </div>
      </div>

      <div class="relative mt-3">
        <span class="lf-metric-value !text-3xl">
          {{ typeof value === 'number' ? value.toLocaleString() : value }}
        </span>

        <div
          v-if="trend && trendValue"
          class="mt-1 flex items-center gap-1 text-xs"
          :class="trendColors[trend]"
        >
          <IconifyIcon :icon="trendIcons[trend] ?? 'carbon:arrows-horizontal'" class="text-xs" />
          <span>{{ trendValue }}</span>
        </div>
      </div>
    </template>
  </div>
</template>
