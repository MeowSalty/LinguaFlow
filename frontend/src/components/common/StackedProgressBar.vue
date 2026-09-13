<script setup lang="ts">
import { computed } from 'vue'

/**
 * 通用堆叠进度条。
 * 主段（value）自左向右；subValue 覆盖绘制在主段起点之上（如"已批准"叠在"已翻译"）；
 * errorValue / skippedValue 依次紧接主段之后（失败段为弱化红色、跳过段为中性弱化色）。
 */
const props = withDefaults(
  defineProps<{
    /** 主段百分比（0–100） */
    value: number
    /** 次段百分比：自主段起点覆盖绘制 */
    subValue?: number
    /** 失败段百分比：紧接主段之后 */
    errorValue?: number
    /** 跳过段百分比：紧接失败段之后 */
    skippedValue?: number
    /** 失败态：主段使用危险色 */
    error?: boolean
    /** 主段色调（error 为 true 时强制危险色） */
    tone?: 'brand' | 'success' | 'warning'
    /** 轨道高度 */
    height?: string
  }>(),
  {
    subValue: 0,
    errorValue: 0,
    skippedValue: 0,
    error: false,
    tone: 'brand',
    height: '6px',
  },
)

const clamp = (value: number): number => Math.min(Math.max(value, 0), 100)

const mainPct = computed(() => clamp(props.value))
const subPct = computed(() => clamp(props.subValue))
const errorPct = computed(() => clamp(props.errorValue))
const skippedPct = computed(() => clamp(props.skippedValue))

const TONE_CLASS: Record<'brand' | 'success' | 'warning', string> = {
  brand: 'bg-brand-500',
  success: 'bg-lf-success',
  warning: 'bg-lf-warning',
}

const mainFillClass = computed(() => (props.error ? 'bg-lf-danger' : TONE_CLASS[props.tone]))
</script>

<template>
  <div
    class="relative overflow-hidden rounded-full border border-lf-border-soft bg-lf-surface-muted"
    :style="{ height }"
  >
    <!-- 主段 -->
    <div
      class="absolute inset-y-0 left-0 transition-all duration-300"
      :class="mainFillClass"
      :style="{ width: `${mainPct}%` }"
    />
    <!-- 失败段（紧接主段） -->
    <div
      v-if="errorPct > 0"
      class="absolute inset-y-0 bg-lf-danger/60 transition-all duration-300"
      :style="{ left: `${mainPct}%`, width: `${errorPct}%` }"
    />
    <!-- 跳过段（紧接失败段） -->
    <div
      v-if="skippedPct > 0"
      class="absolute inset-y-0 bg-lf-text-muted/40 transition-all duration-300"
      :style="{ left: `${mainPct + errorPct}%`, width: `${skippedPct}%` }"
    />
    <!-- 叠加次段（覆盖主段起点） -->
    <div
      v-if="subPct > 0"
      class="absolute inset-y-0 left-0 bg-brand-500/40 transition-all duration-300"
      :style="{ width: `${subPct}%` }"
    />
  </div>
</template>
