<script setup lang="ts">
/**
 * 配置分区面板：头部（标题 + 可选说明 + 右侧操作区插槽）+ 仅在启用时渲染的内容区。
 * 执行策略 / 执行计划编辑器共用；头部操作区放主开关或轮次操作按钮，由调用方决定。
 * 分区关闭时收起字段，字段值仍保留在配置对象中，重新开启不丢失。
 */
withDefaults(
  defineProps<{
    /** 头部标题文本；提供 #title 插槽时可省略（如轮次卡片的自定义徽章标题） */
    title?: string
    description?: string
    /** 内容区是否展开；轮次卡片等常驻分区不传此 prop */
    enabled?: boolean
  }>(),
  { title: '', description: '', enabled: true },
)
</script>

<template>
  <div class="overflow-hidden rounded-lf-ctl border border-lf-border-soft bg-lf-surface">
    <div class="flex items-center justify-between gap-4 px-4 py-3">
      <div class="min-w-0">
        <slot name="title">
          <div class="text-sm font-medium text-lf-text-strong">{{ title }}</div>
          <div v-if="description" class="mt-0.5 text-xs leading-4 text-lf-text-subtle">
            {{ description }}
          </div>
        </slot>
      </div>
      <div v-if="$slots.actions" class="flex shrink-0 items-center gap-1">
        <slot name="actions" />
      </div>
    </div>
    <div v-if="enabled" class="space-y-3 border-t border-lf-border-soft px-4 py-3">
      <slot />
    </div>
  </div>
</template>
