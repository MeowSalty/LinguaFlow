<script setup lang="ts">
/**
 * 实体管理页通用骨架：页头 + 指标行 + 筛选栏 + 加载/空态 + 列表内容。
 *
 * 模板/配置类管理页（提示词、执行策略、执行计划、AI 后端等）共用此骨架，
 * 各页保留自己的 store、表单与编辑器组件，仅通过 props 与插槽填充内容。
 */

interface EntityMetric {
  label: string
  value: string | number
}

withDefaults(
  defineProps<{
    title: string
    subtitle?: string
    metrics?: EntityMetric[]
    loading?: boolean
    /** 列表为空（含筛选后为空）时展示空态，否则渲染默认插槽 */
    empty?: boolean
    emptyDescription?: string
  }>(),
  {
    subtitle: '',
    metrics: () => [],
    loading: false,
    empty: false,
    emptyDescription: '',
  },
)
</script>

<template>
  <div class="lf-page">
    <section class="lf-page-header">
      <div class="space-y-1.5">
        <h1 class="text-2xl font-semibold tracking-tight text-lf-text-strong">
          {{ title }}
        </h1>
        <p v-if="subtitle" class="max-w-2xl text-sm leading-6 text-lf-text-muted">
          {{ subtitle }}
        </p>
      </div>
      <div v-if="$slots.actions" class="flex flex-wrap items-center gap-3">
        <slot name="actions" />
      </div>
    </section>

    <div
      v-if="metrics.length > 0"
      class="grid grid-cols-1 gap-4 md:grid-cols-2"
      :class="metrics.length >= 4 ? 'xl:grid-cols-4' : 'xl:grid-cols-3'"
    >
      <div v-for="metric in metrics" :key="metric.label" class="lf-metric">
        <div class="lf-metric-label">{{ metric.label }}</div>
        <div class="lf-metric-value">{{ metric.value }}</div>
      </div>
    </div>

    <div v-if="$slots.filters" class="lf-panel px-4 py-3">
      <div class="flex flex-col gap-3 lg:flex-row lg:items-center lg:justify-between">
        <slot name="filters" />
      </div>
    </div>

    <div v-if="loading" class="grid grid-cols-1 gap-5 md:grid-cols-2 xl:grid-cols-3">
      <div v-for="index in 6" :key="index" class="lf-panel p-5">
        <NSkeleton text :repeat="4" />
      </div>
    </div>

    <NEmpty
      v-else-if="empty"
      class="lf-panel py-16"
      :description="emptyDescription"
    >
      <template v-if="$slots['empty-extra']" #extra>
        <slot name="empty-extra" />
      </template>
    </NEmpty>

    <slot v-else />
  </div>
</template>
