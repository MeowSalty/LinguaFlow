<script setup lang="ts">
import { NAlert, NCollapse, NCollapseItem, NTag } from 'naive-ui'
import { useI18n } from 'vue-i18n'
import type { ApiSchemas } from '@/api/client-core'
import { retentionCountLabel } from '@/api/task-history'
import { retentionDate, retentionReason } from '@/composables/taskRetentionPresentation'

const props = defineProps<{ preview: ApiSchemas['TaskRetentionPreview'] }>()
const { t } = useI18n()
const types = ['translation', 'glossary_sync'] as const
const groups = (value: ApiSchemas['RetentionTypePreview']) => [
  {
    title: 'terminal',
    values: {
      active: value.active,
      terminal: value.terminal,
      missing_anchor: value.missing_anchor,
      not_expired: value.not_expired,
      blocked: value.expired.blocked,
      busy: value.expired.busy,
      deletable: value.expired.deletable,
    },
  },
  { title: 'timing', values: value.timing_sources },
  { title: 'age', values: value.terminal_age },
  { title: 'dependencies', values: value.deletable_dependencies },
]
</script>

<template>
  <section class="space-y-4" :aria-label="t('taskRetention.previewTitle')">
    <div class="flex flex-wrap items-center justify-between gap-2">
      <p class="font-medium">
        {{ t('taskRetention.previewDays', { days: props.preview.retention_days }) }}
      </p>
      <NTag size="small" :type="props.preview.partial ? 'warning' : 'success'">
        {{ t(props.preview.partial ? 'taskRetention.partialBadge' : 'taskRetention.complete') }}
      </NTag>
    </div>
    <NAlert v-if="props.preview.partial" type="warning">
      <p>{{ t('taskRetention.partial') }}</p>
      <ul v-if="props.preview.incomplete_reasons.length" class="mt-2 list-inside list-disc text-sm">
        <li v-for="(code, index) in props.preview.incomplete_reasons" :key="index">
          {{ retentionReason(code) }}
        </li>
      </ul>
    </NAlert>
    <dl class="grid gap-2 text-xs text-lf-text-muted sm:grid-cols-2">
      <div>
        <dt>{{ t('taskRetention.asOf') }}</dt>
        <dd>{{ retentionDate(props.preview.as_of) }}</dd>
      </div>
      <div>
        <dt>{{ t('taskRetention.cutoff') }}</dt>
        <dd>{{ retentionDate(props.preview.cutoff) }}</dd>
      </div>
    </dl>
    <div class="grid gap-3 sm:grid-cols-2">
      <div v-for="kind in types" :key="kind" class="rounded-lg border border-lf-border p-3">
        <h4 class="text-sm font-medium">{{ t(`taskRetention.${kind}`) }}</h4>
        <p class="mt-2 text-sm text-lf-text-muted">{{ t('taskRetention.deletable') }}</p>
        <p class="mt-1 text-lg font-semibold tabular-nums">
          {{ retentionCountLabel(props.preview.by_type[kind].expired.deletable) }}
        </p>
        <dl class="mt-3 space-y-1 text-xs text-lf-text-muted">
          <div
            v-for="field in ['sse_events', 'job_round_segments'] as const"
            :key="field"
            class="flex flex-wrap justify-between gap-2"
          >
            <dt>{{ t(`taskRetention.${field}`) }}</dt>
            <dd class="tabular-nums">
              {{ retentionCountLabel(props.preview.by_type[kind].deletable_dependencies[field]) }}
            </dd>
          </div>
        </dl>
      </div>
    </div>
    <p class="text-xs leading-relaxed text-lf-text-muted">{{ t('taskRetention.previewHint') }}</p>
    <NCollapse>
      <NCollapseItem :title="t('taskRetention.details')" name="details">
        <div class="grid gap-5 sm:grid-cols-2">
          <section v-for="kind in types" :key="kind" class="min-w-0 space-y-4">
            <h4 class="font-medium">{{ t(`taskRetention.${kind}`) }}</h4>
            <div v-for="group in groups(props.preview.by_type[kind])" :key="group.title">
              <h5 class="mb-2 text-xs font-semibold text-lf-text-muted">
                {{ t(`taskRetention.${group.title}`) }}
              </h5>
              <dl class="space-y-2 text-sm">
                <div
                  v-for="(value, field) in group.values"
                  :key="field"
                  class="flex flex-wrap justify-between gap-2"
                >
                  <dt>{{ t(`taskRetention.${field}`) }}</dt>
                  <dd class="tabular-nums">{{ retentionCountLabel(value) }}</dd>
                </div>
              </dl>
            </div>
          </section>
        </div>
        <p class="mt-4 text-xs text-lf-text-muted">{{ t('taskRetention.timingHint') }}</p>
      </NCollapseItem>
    </NCollapse>
  </section>
</template>
