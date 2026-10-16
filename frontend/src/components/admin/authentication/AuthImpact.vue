<script setup lang="ts">
import { useI18n } from 'vue-i18n'
import { NAlert } from 'naive-ui'
import type { AuthConnectionImpact } from '@/api/auth-connections'
defineProps<{ impact: AuthConnectionImpact }>()
const { t } = useI18n()
const groups = [
  'affected_users',
  'blocked_users',
  'pass_users',
  'fail_users',
  'unknown_users',
  'administrator_users',
] as const
const labels = {
  affected_users: 'affectedUsers',
  blocked_users: 'blockedUsers',
  pass_users: 'passUsers',
  fail_users: 'failUsers',
  unknown_users: 'unknownUsers',
  administrator_users: 'administrators',
} as const
</script>
<template>
  <div class="space-y-3">
    <n-alert :type="impact.can_publish ? 'success' : 'warning'">{{
      t(impact.can_publish ? 'authAdmin.canPublish' : 'authAdmin.cannotPublish')
    }}</n-alert>
    <dl class="grid grid-cols-1 gap-3 sm:grid-cols-2">
      <div
        v-for="group in groups"
        :key="group"
        class="min-w-0 rounded-lg border border-lf-border p-3"
      >
        <dt class="text-sm text-lf-text-muted">{{ t(`authAdmin.${labels[group]}`) }}</dt>
        <dd class="mt-1 break-words">{{ impact[group]?.join(', ') || t('authAdmin.none') }}</dd>
      </div>
      <div>
        <dt class="text-sm text-lf-text-muted">{{ t('authAdmin.affectedSessions') }}</dt>
        <dd>{{ impact.affected_sessions }}</dd>
      </div>
    </dl>
    <n-alert v-if="impact.remediation" type="warning" :title="t('authAdmin.remediation')">{{
      impact.remediation
    }}</n-alert>
  </div>
</template>
