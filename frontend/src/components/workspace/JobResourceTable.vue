<script setup lang="ts">
import { computed, onBeforeUnmount, ref, watch } from 'vue'
import { NTag, NTooltip } from 'naive-ui'
import { useI18n } from 'vue-i18n'

import type { ApiSchemas } from '@/api/client'
import { formatDetailDuration } from '@/composables/useJobDetailPresentation'
import {
  estimateRoundRemaining,
  formatCompactTime,
  getJobStatusLabel,
  getResourceWorkTotals,
  getRoundError,
  getStageLabel,
  statusTagType,
} from '@/composables/useWorkspaceUtils'
import {
  getDetailRoundSeconds,
  getJobRoundSkipReason,
  getResourceRoundSummary,
  getRoundDisplayState,
  selectResourceRound,
  type RoundDisplayState,
} from '@/utils/jobPresentation'
import JobRoundStateIcon from './JobRoundStateIcon.vue'

type Job = ApiSchemas['Job']
type Resource = ApiSchemas['JobResource']
type Round = ApiSchemas['JobResourceRound']

const props = defineProps<{ job: Job }>()
const { t, locale } = useI18n()
const expandedIds = ref<number[]>([])
const expandedErrors = ref<Record<string, boolean>>({})
const mobileQuery = typeof window !== 'undefined' ? window.matchMedia('(max-width: 767px)') : null
const isMobile = ref(mobileQuery?.matches ?? false)
const updateMobile = (): void => {
  isMobile.value = mobileQuery?.matches ?? false
}
mobileQuery?.addEventListener('change', updateMobile)
onBeforeUnmount(() => mobileQuery?.removeEventListener('change', updateMobile))
const numberFormat = computed(() => new Intl.NumberFormat(locale.value))
const formatNumber = (value: number): string => numberFormat.value.format(value)

watch(
  () => props.job.id,
  () => {
    expandedIds.value = []
    expandedErrors.value = {}
  },
)

const stateLabel = (state: RoundDisplayState): string =>
  state === 'pausing' || state === 'paused' || state === 'stopped' || state === 'not_run'
    ? t(`workspace.job.detail.roundState.${state}`)
    : t(`workspace.job.round.status.${state}`)

const segmentText = (round: Round): string =>
  t('workspace.job.round.segments', {
    completed: formatNumber(round.segment_completed),
    total: formatNumber(round.segment_total),
  })

const resourceStatus = (resource: Resource) => {
  if (resource.status === 'pending') {
    if (props.job.status === 'paused')
      return { label: stateLabel('paused'), type: 'default' as const }
    if (['completed', 'failed', 'cancelled'].includes(props.job.status)) {
      return { label: stateLabel('not_run'), type: 'default' as const }
    }
  }
  // A resource can retain running while its parent drains, pauses or stops.
  if (resource.status === 'running' && props.job.status !== 'running') {
    const state = getRoundDisplayState(props.job.status, resource.status, 'running')
    return {
      label: stateLabel(state),
      type: state === 'pausing' ? ('warning' as const) : ('default' as const),
    }
  }
  return {
    label: getJobStatusLabel(resource.status),
    type:
      resource.status === 'cancelled'
        ? ('default' as const)
        : resource.warning_message?.trim() && resource.status === 'completed'
          ? ('warning' as const)
          : statusTagType(resource.status),
  }
}

const stageSummary = (resource: Resource) => {
  const summary = getResourceRoundSummary(resource)
  const round = selectResourceRound(resource, props.job.status)
  if (summary.kind === 'legacy' || !round) {
    return { state: null, headline: t('workspace.job.round.legacyHint'), detail: '' }
  }
  if (summary.kind === 'completed') {
    return {
      state: 'completed' as const,
      headline: t('workspace.job.round.allRoundsDone', { total: summary.total }),
      detail: '',
    }
  }
  if (summary.kind === 'mixed') {
    return {
      state: 'completed' as const,
      headline: t('workspace.job.detail.mixedRounds', summary),
      detail: '',
    }
  }
  if (summary.kind === 'ended') {
    return {
      state: 'stopped' as const,
      headline: t('workspace.job.detail.executionEnded'),
      detail: '',
    }
  }
  if (
    (resource.status === 'cancelled' || props.job.status === 'cancelled') &&
    resource.rounds.every((item) => item.status === 'pending' && !item.started_at)
  ) {
    return { state: 'not_run' as const, headline: stateLabel('not_run'), detail: '' }
  }
  const roundState = getRoundDisplayState(props.job.status, resource.status, round.status)
  // The summary describes where this resource stopped; expanded rounds keep their true outcomes.
  const state: RoundDisplayState =
    resource.status === 'cancelled' || props.job.status === 'cancelled'
      ? 'stopped'
      : props.job.status === 'paused' &&
          (resource.status === 'pending' || resource.status === 'running')
        ? 'paused'
        : roundState
  const stage = getStageLabel(round.mode)
  const headline =
    state === 'pausing'
      ? t('workspace.job.detail.pausingAt', { stage })
      : state === 'paused'
        ? t('workspace.job.detail.pausedAt', { stage })
        : state === 'stopped'
          ? t('workspace.job.detail.stoppedAt', { stage })
          : stage
  const details = [
    roundState === 'pending' || roundState === 'not_run'
      ? stateLabel(roundState)
      : segmentText(round),
  ]
  if (state === 'running') {
    const remaining = estimateRoundRemaining(round)
    if (remaining != null)
      details.push(
        t('workspace.job.round.remainingShort', {
          duration: formatDetailDuration(remaining),
        }),
      )
  }
  return {
    state,
    headline: `${headline} · ${t('workspace.job.round.of', {
      index: round.round_index + 1,
      total: summary.total,
    })}`,
    detail: details.join(' · '),
  }
}

const rows = computed(() =>
  (props.job.job_resources ?? []).map((resource) => {
    const name = resource.resource?.name || `#${resource.resource_id}`
    const characters = Array.from(name)
    const tailLength = characters.length > 20 ? Math.min(18, Math.ceil(characters.length / 3)) : 0
    const { completed, total } = getResourceWorkTotals(resource)
    const rounds = [...(resource.rounds ?? [])]
      .sort((a, b) => a.round_index - b.round_index)
      .map((round) => {
        const state = getRoundDisplayState(props.job.status, resource.status, round.status)
        const skipReason = getJobRoundSkipReason(props.job, round)
        const remaining = state === 'running' ? estimateRoundRemaining(round) : null
        return {
          ...round,
          state,
          stateLabel: skipReason ? t('termExtraction.skippedGlossaryDisabled') : stateLabel(state),
          segments: segmentText(round),
          duration: formatDetailDuration(getDetailRoundSeconds(round, props.job)),
          remaining:
            remaining == null
              ? ''
              : t('workspace.job.round.remainingShort', {
                  duration: formatDetailDuration(remaining),
                }),
          times: [
            round.started_at ? formatCompactTime(round.started_at) : null,
            round.finished_at ? formatCompactTime(round.finished_at) : null,
          ]
            .filter(Boolean)
            .join(' → '),
          errorKey: `round-${resource.id}-${round.round_index}`,
        }
      })
    const resourceErrors = [
      resource.error_message &&
      !rounds.some((round) => round.error_message === resource.error_message)
        ? { tone: 'danger', message: resource.error_message, key: `error-${resource.id}` }
        : null,
      resource.warning_message?.trim()
        ? { tone: 'warning', message: resource.warning_message, key: `warning-${resource.id}` }
        : null,
    ].filter((issue) => issue != null)
    const issueMessage =
      resource.error_message || resource.warning_message || getRoundError(resource)
    return {
      ...resource,
      name,
      nameStart: tailLength ? characters.slice(0, -tailLength).join('') : name,
      nameEnd: tailLength ? characters.slice(-tailLength).join('') : '',
      rounds,
      statusView: resourceStatus(resource),
      stage: stageSummary(resource),
      workload: `${formatNumber(completed)} / ${formatNumber(total)}`,
      skipped:
        resource.skipped_segments > 0
          ? t('workspace.job.detail.skippedSegments', {
              count: formatNumber(resource.skipped_segments),
            })
          : '',
      resourceErrors,
      issueMessage,
      issueTone: resource.error_message || getRoundError(resource) ? 'danger' : 'warning',
    }
  }),
)

const toggleRow = (id: number): void => {
  expandedIds.value = expandedIds.value.includes(id)
    ? expandedIds.value.filter((value) => value !== id)
    : [...expandedIds.value, id]
}
</script>

<template>
  <section class="resource-section" :aria-label="t('workspace.job.resourcesTitle')">
    <div class="section-heading">
      <h3>{{ t('workspace.job.resourcesTitle') }}</h3>
      <span>{{ t('workspace.job.detail.resourceCount', { count: rows.length }) }}</span>
    </div>
    <div v-if="rows.length === 0" class="empty-resources">
      {{ t('workspace.job.detail.noResources') }}
    </div>
    <table v-else class="resource-table">
      <colgroup>
        <col class="expand-col" />
        <col />
        <col v-if="!isMobile" class="status-col" />
        <col class="stage-col" />
        <col v-if="!isMobile" class="work-col" />
      </colgroup>
      <thead>
        <tr>
          <th scope="col">
            <span class="sr-only">{{ t('workspace.job.round.expandHint') }}</span>
          </th>
          <th scope="col">{{ t('workspace.resource.columns.name') }}</th>
          <th v-if="!isMobile" scope="col">{{ t('workspace.job.columns.status') }}</th>
          <th scope="col">{{ t('workspace.job.detail.executionStage') }}</th>
          <th v-if="!isMobile" scope="col" class="workload-cell">
            {{ t('workspace.job.columns.workload') }}
          </th>
        </tr>
      </thead>
      <tbody v-for="row in rows" :key="row.id">
        <tr
          class="resource-row"
          :class="{ expandable: row.rounds.length }"
          @click="row.rounds.length && toggleRow(row.id)"
        >
          <td class="expand-cell">
            <button
              v-if="row.rounds.length"
              type="button"
              class="expand-button"
              :aria-expanded="expandedIds.includes(row.id)"
              :aria-controls="`job-${job.id}-resource-${row.id}`"
              :aria-label="
                t(
                  expandedIds.includes(row.id)
                    ? 'workspace.job.detail.collapseResource'
                    : 'workspace.job.detail.expandResource',
                  { name: row.name },
                )
              "
              @click.stop="toggleRow(row.id)"
            >
              <IconCarbonChevronDown v-if="expandedIds.includes(row.id)" />
              <IconCarbonChevronRight v-else />
            </button>
          </td>
          <td class="name-cell">
            <div class="name-line">
              <NTooltip :style="{ maxWidth: 'min(480px, 80vw)', overflowWrap: 'anywhere' }">
                <template #trigger>
                  <span class="resource-name" :aria-label="row.name">
                    <span class="name-start">{{ row.nameStart }}</span
                    ><span v-if="row.nameEnd" class="name-end"
                      ><span>{{ row.nameEnd }}</span></span
                    >
                  </span>
                </template>
                {{ row.name }}
              </NTooltip>
              <NTooltip
                v-if="row.issueMessage"
                :style="{ maxWidth: 'min(420px, 80vw)', overflowWrap: 'anywhere' }"
              >
                <template #trigger>
                  <span class="issue-icon" :class="row.issueTone" :aria-label="row.issueMessage">
                    <IconCarbonWarningAltFilled />
                  </span>
                </template>
                {{ row.issueMessage }}
              </NTooltip>
            </div>
            <NTag
              class="mobile-status"
              :type="row.statusView.type"
              :bordered="false"
              round
              size="tiny"
              >{{ row.statusView.label }}</NTag
            >
          </td>
          <td v-if="!isMobile">
            <NTag :type="row.statusView.type" :bordered="false" round size="tiny">{{
              row.statusView.label
            }}</NTag>
          </td>
          <td class="stage-cell">
            <div class="stage-summary">
              <JobRoundStateIcon
                v-if="row.stage.state"
                :state="row.stage.state"
                class="stage-icon"
              />
              <div>
                <div class="stage-headline" :class="{ 'legacy-stage': !row.stage.state }">
                  {{ row.stage.headline }}
                </div>
                <div v-if="row.stage.detail" class="stage-detail">{{ row.stage.detail }}</div>
              </div>
            </div>
          </td>
          <td v-if="!isMobile" class="workload-cell">
            <span class="workload-number">{{ row.workload }}</span>
            <div v-if="row.skipped" class="stage-detail">{{ row.skipped }}</div>
          </td>
        </tr>
        <tr
          v-show="expandedIds.includes(row.id) || (!row.rounds.length && row.resourceErrors.length)"
          class="expanded-row"
        >
          <td :colspan="isMobile ? 3 : 5">
            <div :id="`job-${job.id}-resource-${row.id}`" class="resource-details">
              <div class="full-name">{{ row.name }}</div>
              <div class="mobile-workload">
                <span>{{
                  t(
                    row.rounds.length
                      ? 'workspace.job.detail.workload'
                      : 'workspace.resource.columns.segments',
                  )
                }}</span>
                <span class="workload-number">{{ row.workload }}</span>
                <span v-if="row.skipped">{{ row.skipped }}</span>
              </div>
              <div
                v-for="issue in row.resourceErrors"
                :key="issue.key"
                class="issue-message"
                :class="issue.tone"
              >
                <p :class="{ clamped: !expandedErrors[issue.key] }">{{ issue.message }}</p>
                <button
                  type="button"
                  class="text-button"
                  :aria-expanded="!!expandedErrors[issue.key]"
                  @click="expandedErrors[issue.key] = !expandedErrors[issue.key]"
                >
                  {{
                    t(
                      expandedErrors[issue.key]
                        ? 'workspace.job.detail.collapseError'
                        : 'workspace.job.detail.showFullError',
                    )
                  }}
                </button>
              </div>
              <div v-if="row.rounds.length" class="round-list">
                <div class="round-heading">
                  <span>{{ t('workspace.job.detail.executionStage') }}</span>
                  <span>{{ t('workspace.job.columns.workload') }}</span>
                  <span class="duration-heading">{{ t('workspace.job.columns.duration') }}</span>
                </div>
                <div v-for="round in row.rounds" :key="round.round_index" class="round-item">
                  <div class="round-stage">
                    <JobRoundStateIcon :state="round.state" class="round-icon" />
                    <div>
                      <div class="round-mode">{{ getStageLabel(round.mode) }}</div>
                      <div class="round-state">
                        {{
                          t('workspace.job.round.timelineRound', { index: round.round_index + 1 })
                        }}
                        · {{ round.stateLabel }}
                      </div>
                    </div>
                  </div>
                  <div
                    class="round-workload"
                    :class="{ 'running-workload': round.state === 'running' }"
                  >
                    {{
                      round.state === 'pending' || round.state === 'not_run'
                        ? round.stateLabel
                        : round.segments
                    }}
                  </div>
                  <div class="round-duration">
                    <span>{{ round.duration }}</span>
                    <span v-if="round.remaining" class="round-remaining">{{
                      round.remaining
                    }}</span>
                  </div>
                  <div v-if="round.times" class="round-times">{{ round.times }}</div>
                  <div v-if="round.error_message" class="issue-message round-error danger">
                    <p :class="{ clamped: !expandedErrors[round.errorKey] }">
                      {{ round.error_message }}
                    </p>
                    <button
                      type="button"
                      class="text-button"
                      :aria-expanded="!!expandedErrors[round.errorKey]"
                      @click="expandedErrors[round.errorKey] = !expandedErrors[round.errorKey]"
                    >
                      {{
                        t(
                          expandedErrors[round.errorKey]
                            ? 'workspace.job.detail.collapseError'
                            : 'workspace.job.detail.showFullError',
                        )
                      }}
                    </button>
                  </div>
                </div>
              </div>
            </div>
          </td>
        </tr>
      </tbody>
    </table>
  </section>
</template>

<style scoped>
.section-heading {
  display: flex;
  align-items: baseline;
  gap: 8px;
  margin-bottom: 10px;
}
.section-heading h3 {
  margin: 0;
  color: var(--lf-text-strong);
  font-size: 13px;
  font-weight: 600;
}
.section-heading > span,
.empty-resources {
  color: var(--lf-text-subtle);
  font-size: 12px;
}
.empty-resources {
  padding: 28px 16px;
  text-align: center;
}
.resource-table {
  width: 100%;
  table-layout: fixed;
  border: 1px solid var(--lf-border-soft);
  border-collapse: separate;
  border-spacing: 0;
  border-radius: var(--radius-lf-card);
  overflow: hidden;
  font-size: 12px;
}
.expand-col {
  width: 36px;
}
.status-col {
  width: 76px;
}
.stage-col {
  width: 176px;
}
.work-col {
  width: 116px;
}
th {
  padding: 11px 8px;
  background: var(--lf-surface-muted);
  color: var(--lf-text-muted);
  font-size: 12px;
  font-weight: 500;
  text-align: left;
}
td {
  padding: 14px 8px;
  vertical-align: middle;
}
tbody + tbody .resource-row td {
  border-top: 1px solid var(--lf-border-soft);
}
.resource-row.expandable {
  cursor: pointer;
}
.resource-row.expandable:hover {
  background: var(--lf-hover);
}
.expand-cell {
  padding-right: 0;
  padding-left: 5px;
}
.expand-button {
  display: inline-flex;
  width: 28px;
  height: 28px;
  align-items: center;
  justify-content: center;
  border: 0;
  border-radius: 6px;
  background: transparent;
  color: var(--lf-text-muted);
  cursor: pointer;
}
.expand-button:hover {
  background: var(--lf-hover);
  color: var(--lf-text-strong);
}
.expand-button:focus-visible,
.text-button:focus-visible {
  outline: 2px solid var(--lf-brand-500);
  outline-offset: 2px;
}
.name-line {
  display: flex;
  min-width: 0;
  align-items: center;
  gap: 6px;
}
.resource-name {
  display: inline-flex;
  min-width: 0;
  max-width: 100%;
  color: var(--lf-text-strong);
  font-size: 13px;
  font-weight: 500;
  white-space: nowrap;
}
.name-start {
  min-width: 0;
  overflow: hidden;
  text-overflow: ellipsis;
}
.name-end {
  display: inline-flex;
  justify-content: flex-end;
  flex: 0 0 auto;
  max-width: 55%;
  overflow: hidden;
}
.name-end > span {
  flex: 0 0 auto;
}
.issue-icon {
  display: inline-flex;
  flex-shrink: 0;
  font-size: 13px;
}
.danger {
  color: var(--lf-danger);
}
.warning {
  color: var(--lf-warning);
}
.stage-summary {
  display: flex;
  min-width: 0;
  align-items: flex-start;
  gap: 6px;
}
.stage-summary > div {
  min-width: 0;
}
.stage-icon {
  margin-top: 3px;
  font-size: 13px;
}
.stage-headline {
  color: var(--lf-text);
  line-height: 1.6;
  overflow-wrap: anywhere;
}
.stage-detail {
  margin-top: 3px;
  color: var(--lf-text-subtle);
  font-size: 11px;
  line-height: 1.5;
  font-variant-numeric: tabular-nums;
  overflow-wrap: anywhere;
}
.legacy-stage {
  color: var(--lf-text-subtle);
}
.workload-cell {
  padding-right: 14px;
  text-align: right;
}
.workload-number {
  color: var(--lf-text);
  white-space: nowrap;
  font-variant-numeric: tabular-nums;
}
.mobile-status,
.mobile-workload {
  display: none;
}
.expanded-row > td {
  padding: 0;
}
.resource-details {
  border-top: 1px solid var(--lf-border-soft);
  padding: 14px 20px 16px 44px;
  background: var(--lf-surface-muted);
}
.full-name {
  margin-bottom: 14px;
  color: var(--lf-text-muted);
  font-size: 12px;
  overflow-wrap: anywhere;
}
.round-heading,
.round-item {
  display: grid;
  grid-template-columns: minmax(0, 1fr) 132px 100px;
  column-gap: 16px;
}
.round-heading {
  margin-bottom: 3px;
  padding: 0 0 7px 20px;
  color: var(--lf-text-subtle);
  font-size: 11px;
}
.duration-heading {
  text-align: right;
}
.round-item {
  align-items: start;
  row-gap: 5px;
  padding: 8px 0;
}
.round-item + .round-item {
  border-top: 1px solid var(--lf-border-soft);
}
.round-stage {
  display: flex;
  grid-row: span 2;
  min-width: 0;
  align-items: flex-start;
  gap: 7px;
}
.round-icon {
  margin-top: 2px;
  font-size: 13px;
}
.round-mode {
  color: var(--lf-text);
  line-height: 1.5;
}
.round-state {
  margin-top: 3px;
  color: var(--lf-text-subtle);
  font-size: 11px;
}
.round-workload,
.round-duration {
  color: var(--lf-text);
  font-variant-numeric: tabular-nums;
  line-height: 1.5;
}
.round-workload {
  overflow-wrap: anywhere;
}
.running-workload {
  color: var(--lf-brand-500);
}
.round-duration {
  text-align: right;
}
.round-remaining {
  display: block;
  margin-top: 3px;
  color: var(--lf-text-subtle);
  font-size: 11px;
}
.round-times {
  grid-column: 2 / -1;
  text-align: right;
  color: var(--lf-text-subtle);
  font-size: 11px;
  font-variant-numeric: tabular-nums;
}
.issue-message {
  margin: 0 0 12px;
  padding-left: 10px;
  border-left: 2px solid currentColor;
  font-size: 12px;
}
.issue-message p {
  margin: 0;
  color: var(--lf-text-muted);
  line-height: 1.6;
  white-space: pre-wrap;
  overflow-wrap: anywhere;
}
.issue-message p.clamped {
  display: -webkit-box;
  -webkit-box-orient: vertical;
  -webkit-line-clamp: 2;
  overflow: hidden;
}
.text-button {
  margin-top: 3px;
  padding: 0;
  border: 0;
  background: none;
  color: var(--lf-text-subtle);
  font-size: 11px;
  cursor: pointer;
}
.text-button:hover {
  color: var(--lf-text-strong);
}
.round-error {
  grid-column: 1 / -1;
  margin: 3px 0 0 20px;
}
@media (max-width: 767px) {
  .expand-col {
    width: 32px;
  }
  .stage-col {
    width: 43%;
  }
  th,
  td {
    padding-left: 6px;
    padding-right: 6px;
  }
  .expand-cell {
    padding-left: 3px;
    padding-right: 0;
  }
  .expand-button {
    width: 26px;
  }
  .mobile-status {
    display: inline-flex;
    margin-top: 6px;
  }
  .name-line {
    gap: 4px;
  }
  .resource-name {
    font-size: 12px;
  }
  .stage-cell {
    padding-right: 10px;
  }
  .stage-headline {
    font-size: 11px;
  }
  .stage-summary {
    gap: 4px;
  }
  .stage-icon {
    font-size: 12px;
  }
  .resource-details {
    padding: 12px 12px 14px;
  }
  .full-name {
    margin-bottom: 10px;
  }
  .mobile-workload {
    display: flex;
    flex-wrap: wrap;
    gap: 4px 10px;
    margin-bottom: 14px;
    color: var(--lf-text-subtle);
    font-size: 11px;
  }
  .round-heading,
  .round-item {
    grid-template-columns: minmax(0, 1fr) minmax(0, 1fr);
    column-gap: 12px;
  }
  .round-heading {
    padding-left: 20px;
  }
  .round-heading > span:nth-child(2),
  .round-workload {
    text-align: right;
  }
  .duration-heading {
    display: none;
  }
  .round-stage {
    grid-row: span 2;
  }
  .round-duration {
    grid-column: 2;
    color: var(--lf-text-subtle);
    font-size: 11px;
  }
  .round-times {
    grid-column: 1 / -1;
    padding-left: 20px;
    text-align: left;
  }
}
</style>
