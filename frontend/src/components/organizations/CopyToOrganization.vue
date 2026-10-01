<script setup lang="ts">
import { computed, ref } from 'vue'
import { NAlert, NButton, NModal, NSelect } from 'naive-ui'
import { useI18n } from 'vue-i18n'
import { useOrganizationsStore } from '@/stores/organizations'
const emit = defineEmits<{ copy: [orgId: number] }>()
const { t } = useI18n()
const store = useOrganizationsStore()
const show = ref(false)
const target = ref<number | null>(null)
const options = computed(() =>
  store.writable.map((org) => ({ label: org.display_name || org.name, value: org.id })),
)
const open = () => {
  target.value = null
  show.value = true
  void store.refresh()
}
const confirm = () => {
  if (target.value && store.canWrite(target.value)) {
    emit('copy', target.value)
    show.value = false
  }
}
</script>
<template>
  <NButton text @click.stop="open">{{ t('team.copy') }}</NButton>
  <NModal v-model:show="show" preset="card" class="max-w-lg" :title="t('team.copyTitle')">
    <p class="mb-4 text-lf-text-muted">{{ t('team.copyHint') }}</p>
    <NSelect
      v-model:value="target"
      :options="options"
      :loading="store.loading"
      :placeholder="t('team.copyTarget')"
    />
    <NAlert v-if="!store.loading && options.length === 0" class="mt-4" type="info">{{
      t('team.noWritable')
    }}</NAlert>
    <template #footer
      ><div class="flex justify-end gap-3">
        <NButton @click="show = false">{{ t('team.cancel') }}</NButton
        ><NButton type="primary" :disabled="!target || !store.canWrite(target)" @click="confirm">{{
          t('team.continue')
        }}</NButton>
      </div></template
    >
  </NModal>
</template>
