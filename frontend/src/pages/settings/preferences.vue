<script setup lang="ts">
import { useI18n } from 'vue-i18n'
import { usePreferencesStore } from '@/stores/preferences'
import { useThemeStore, type ThemeMode } from '@/stores/theme'
import { useLocaleStore } from '@/stores/locale'
import { useMessage } from 'naive-ui'

const preferences = usePreferencesStore()
const theme = useThemeStore()
const locale = useLocaleStore()
const { t } = useI18n()
const message = useMessage()
const themes = computed(() =>
  (['system', 'light', 'dark'] as ThemeMode[]).map((value) => ({
    value,
    label: t(`theme.${value}`),
  })),
)
const languages = computed(() =>
  locale.availableLocales.map((item) => ({ value: item.code, label: item.nativeName })),
)
const ranges = computed(() => [
  { value: 'active', label: t('workbench.settings.activeTasks') },
  { value: 'terminal', label: t('workbench.settings.terminalTasks') },
  { value: 'all', label: t('workbench.settings.allTasks') },
])
const restoreHidden = (): void => {
  preferences.clearHiddenTerminals()
  message.success(t('workbench.settings.resetHiddenSuccess'))
}
</script>
<template>
  <section class="lf-panel p-5 sm:p-6">
    <h2 class="mb-2 text-lg font-semibold text-lf-text-strong">
      {{ t('workbench.settings.preferences') }}
    </h2>
    <p class="mb-6 text-sm text-lf-text-muted">{{ t('workbench.settings.preferenceHint') }}</p>
    <NForm label-placement="top">
      <div class="grid gap-x-6 sm:grid-cols-2">
        <NFormItem :label="t('workbench.settings.theme')"
          ><NSelect :value="theme.mode" :options="themes" @update:value="theme.setMode"
        /></NFormItem>
        <NFormItem :label="t('workbench.settings.language')"
          ><NSelect
            :value="locale.currentLocale"
            :options="languages"
            @update:value="locale.setLocale"
        /></NFormItem>
      </div>
      <NFormItem :label="t('workbench.settings.defaultRange')"
        ><NSelect v-model:value="preferences.defaultTaskState" :options="ranges"
      /></NFormItem>
      <div class="flex items-start justify-between gap-4 border-t border-lf-border-soft py-5">
        <div>
          <label id="retain-terminal-label" class="text-sm font-medium">{{
            t('workbench.settings.keepTerminal')
          }}</label>
          <p class="mt-1 text-xs leading-5 text-lf-text-muted">
            {{ t('workbench.settings.keepTerminalHint') }}
          </p>
        </div>
        <NSwitch
          v-model:value="preferences.retainTerminal"
          aria-labelledby="retain-terminal-label"
        />
      </div>
      <NButton :disabled="!preferences.hiddenTerminalKeys.length" @click="restoreHidden">{{
        t('workbench.settings.resetHidden')
      }}</NButton>
    </NForm>
  </section>
</template>
