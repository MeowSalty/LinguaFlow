<script setup lang="ts">
import type { Component } from 'vue'
import IconCarbonApi from '~icons/carbon/api'
import IconCarbonArrowDown from '~icons/carbon/arrow-down'
import IconCarbonArrowUp from '~icons/carbon/arrow-up'
import IconCarbonArrowsHorizontal from '~icons/carbon/arrows-horizontal'
import IconCarbonChartColumn from '~icons/carbon/chart-column'
import IconCarbonCloudDownload from '~icons/carbon/cloud-download'
import IconCarbonCloudUpload from '~icons/carbon/cloud-upload'
import IconCarbonWarning from '~icons/carbon/warning'
import { useI18n } from 'vue-i18n'

const { t } = useI18n()

withDefaults(
  defineProps<{
    title: string
    value: number | string
    icon: string
    tone?: 'brand' | 'info' | 'accent' | 'neutral'
    trend?: 'up' | 'down' | 'neutral'
    trendValue?: string
    loading?: boolean
    error?: string | null
  }>(),
  {
    tone: 'brand',
  },
)

const toneClass: Record<string, string> = {
  brand: 'bg-lf-brand-soft text-brand-600',
  info: 'bg-lf-info-soft text-lf-info',
  accent: 'bg-lf-brand-soft text-brand-600',
  neutral: 'bg-lf-surface-muted text-lf-text-muted',
}

const trendColors: Record<string, string> = {
  up: 'text-brand-600',
  down: 'text-lf-danger',
  neutral: 'text-lf-text-subtle',
}

const iconMap: Record<string, Component> = {
  'carbon:api': IconCarbonApi,
  'carbon:cloud-upload': IconCarbonCloudUpload,
  'carbon:cloud-download': IconCarbonCloudDownload,
  'carbon:chart-column': IconCarbonChartColumn,
}

const trendIcons: Record<string, Component> = {
  up: IconCarbonArrowUp,
  down: IconCarbonArrowDown,
  neutral: IconCarbonArrowsHorizontal,
}
</script>

<template>
  <div class="lf-metric group relative overflow-hidden">
    <div
      class="pointer-events-none absolute -right-6 -top-6 h-20 w-20 rounded-full opacity-40 blur-2xl transition-opacity group-hover:opacity-70"
      :class="{
        'bg-brand-500/30': tone === 'brand' || tone === 'accent',
        'bg-lf-info/30': tone === 'info',
        'bg-lf-text-subtle/20': tone === 'neutral',
      }"
    />

    <template v-if="loading">
      <div class="flex items-center justify-between">
        <NSkeleton width="80px" height="16px" />
        <NSkeleton width="40px" height="40px" />
      </div>
      <NSkeleton class="mt-4" width="96px" height="32px" />
    </template>

    <template v-else-if="error">
      <div class="relative flex items-center justify-between gap-3">
        <span class="lf-metric-label">{{ title }}</span>
        <div
          class="flex h-10 w-10 items-center justify-center rounded-lf-ctl bg-lf-danger-soft text-lg text-lf-danger"
        >
          <IconCarbonWarning />
        </div>
      </div>

      <div class="relative mt-3">
        <p class="text-sm text-lf-danger">{{ error }}</p>
        <p class="mt-1 text-xs text-lf-text-subtle">{{ t('stats.loadFailed') }}</p>
      </div>
    </template>

    <template v-else>
      <div class="relative flex items-center justify-between gap-3">
        <span class="lf-metric-label">{{ title }}</span>
        <div
          class="flex h-10 w-10 items-center justify-center rounded-lf-ctl text-lg"
          :class="toneClass[tone]"
        >
          <component :is="iconMap[icon]" />
        </div>
      </div>

      <div class="relative mt-3">
        <span class="lf-metric-value text-3xl">
          {{ typeof value === 'number' ? value.toLocaleString() : value }}
        </span>

        <div
          v-if="trend && trendValue"
          class="mt-1 flex items-center gap-1 text-xs"
          :class="trendColors[trend]"
        >
          <component :is="trendIcons[trend] ?? IconCarbonArrowsHorizontal" class="text-xs" />
          <span>{{ trendValue }}</span>
        </div>
      </div>
    </template>
  </div>
</template>
