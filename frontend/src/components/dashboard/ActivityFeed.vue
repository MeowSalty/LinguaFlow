<script setup lang="ts">
import { useI18n } from 'vue-i18n'

import { useStatsStore } from '@/stores/stats'

const stats = useStatsStore()
const { d, t } = useI18n()

const relativeTime = (dateStr: string): string => {
  const now = Date.now()
  const date = new Date(dateStr).getTime()
  const diff = now - date

  const seconds = Math.floor(diff / 1000)
  if (seconds < 60) return t('dashboard.activity.relativeTime.justNow')

  const minutes = Math.floor(seconds / 60)
  if (minutes < 60) return t('dashboard.activity.relativeTime.minutesAgo', { count: minutes })

  const hours = Math.floor(minutes / 60)
  if (hours < 24) return t('dashboard.activity.relativeTime.hoursAgo', { count: hours })

  const days = Math.floor(hours / 24)
  if (days < 30) return t('dashboard.activity.relativeTime.daysAgo', { count: days })

  return d(new Date(dateStr), 'short')
}

const getActionLabel = (action: string): string => {
  const key = `dashboard.activity.actions.${action}`
  const label = t(key)
  return label === key ? action : label
}
</script>

<template>
  <div class="lf-panel h-full p-5">
    <div class="flex items-center justify-between gap-3">
      <h2 class="text-sm font-semibold tracking-wide text-lf-text-strong">
        {{ t('dashboard.activity.title') }}
      </h2>
      <span class="text-xs text-lf-text-subtle">{{ stats.activities.length }}</span>
    </div>

    <div v-if="stats.activitiesLoading && stats.activities.length === 0" class="mt-4 space-y-4">
      <div v-for="i in 5" :key="i" class="flex items-start gap-3">
        <div class="mt-1 h-2 w-2 shrink-0 animate-pulse rounded-full bg-lf-border-soft" />
        <div class="flex-1 space-y-1.5">
          <div class="h-4 w-3/4 animate-pulse rounded bg-lf-border-soft" />
          <div class="h-3 w-1/3 animate-pulse rounded bg-lf-border-soft" />
        </div>
      </div>
    </div>

    <NEmpty v-else-if="stats.activitiesError" :description="stats.activitiesError" class="mt-8" />

    <NEmpty
      v-else-if="stats.activities.length === 0"
      :description="t('dashboard.activity.empty')"
      class="mt-8"
    />

    <div v-else class="relative mt-4 space-y-4">
      <div
        class="absolute top-1 bottom-1 left-[3px] w-px bg-gradient-to-b from-brand-500/40 via-lf-border-soft to-transparent"
      />
      <div
        v-for="activity in stats.activities"
        :key="activity.id"
        class="relative flex items-start gap-3 pl-1"
      >
        <div
          class="relative z-10 mt-1.5 h-2 w-2 shrink-0 rounded-full border border-brand-500 bg-lf-surface shadow-sm shadow-brand-500/20"
        />

        <div class="min-w-0 flex-1">
          <p class="text-sm leading-6 text-lf-text">
            <span v-if="activity.actor" class="font-medium text-lf-text-strong">
              {{ activity.actor.display_name?.trim() || activity.actor.username }}
            </span>
            {{ getActionLabel(activity.action) }}
            <span class="font-mono text-xs text-lf-text-muted">{{ activity.resource_type }}</span>
            <span v-if="activity.message" class="text-lf-text-muted">
              — {{ activity.message }}</span
            >
          </p>
          <time class="text-xs text-lf-text-subtle">{{ relativeTime(activity.created_at) }}</time>
        </div>
      </div>

      <div v-if="stats.hasMoreActivities" class="pt-2 text-center">
        <NButton
          quaternary
          size="small"
          :loading="stats.activitiesLoading"
          @click="stats.loadActivities(false)"
        >
          {{ t('common.loadMore') }}
        </NButton>
      </div>
    </div>
  </div>
</template>
