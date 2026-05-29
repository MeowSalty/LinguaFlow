<script setup lang="ts">
defineProps<{
  title: string
  value: number | string
  icon: string
  trend?: 'up' | 'down' | 'neutral'
  trendValue?: string
  loading?: boolean
}>()

const trendColors: Record<string, string> = {
  up: 'text-green-600',
  down: 'text-red-600',
  neutral: 'text-lf-text-subtle',
}

const trendIcons: Record<string, string> = {
  up: '↑',
  down: '↓',
  neutral: '→',
}
</script>

<template>
  <div class="rounded-xl bg-lf-surface p-6 shadow-sm transition-shadow hover:shadow-md">
    <!-- 加载骨架屏 -->
    <template v-if="loading">
      <div class="flex items-center justify-between">
        <div class="h-4 w-20 animate-pulse rounded bg-lf-border" />
        <div class="h-10 w-10 animate-pulse rounded-full bg-lf-border" />
      </div>
      <div class="mt-4 h-8 w-24 animate-pulse rounded bg-lf-border" />
    </template>

    <!-- 正常内容 -->
    <template v-else>
      <div class="flex items-center justify-between">
        <span class="text-sm text-lf-text-muted">{{ title }}</span>
        <div
          class="flex h-10 w-10 items-center justify-center rounded-full bg-lf-brand-soft text-lg"
        >
          {{ icon }}
        </div>
      </div>

      <div class="mt-4">
        <span class="text-3xl font-bold text-lf-text-strong">
          {{ typeof value === 'number' ? value.toLocaleString() : value }}
        </span>

        <div
          v-if="trend && trendValue"
          class="mt-1 flex items-center gap-1 text-xs"
          :class="trendColors[trend]"
        >
          <span>{{ trendIcons[trend] }}</span>
          <span>{{ trendValue }}</span>
        </div>
      </div>
    </template>
  </div>
</template>
