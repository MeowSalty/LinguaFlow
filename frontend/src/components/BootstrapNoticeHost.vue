<script setup lang="ts">
import { h } from 'vue'
import { useI18n } from 'vue-i18n'
import { useMessage, useNotification } from 'naive-ui'

import { consumeBootstrapNotices } from '@/bootstrap'
import { blockedCapabilityLabelKeys, currentOrigin } from '@/utils/secureContext'

const message = useMessage()
const notification = useNotification()
const { t } = useI18n()

const detectBrowser = (): 'chrome' | 'edge' | 'other' => {
  if (typeof navigator === 'undefined') return 'other'
  const ua = navigator.userAgent
  if (/Edg\//i.test(ua)) return 'edge'
  if (/Chrome\//i.test(ua) && !/Chromium/i.test(ua)) return 'chrome'
  return 'other'
}

/**
 * 用 VNode 渲染结构化的非安全上下文提醒：
 * - 正文段落
 * - 浏览器操作指引（含可选中的 origin）
 */
const buildInsecureContextContent = () => {
  const origin = currentOrigin()
  const features = blockedCapabilityLabelKeys()
    .map((key) => t(key))
    .join('、')

  const children = [h('p', { class: 'my-0 leading-6' }, t('secureContext.bootBody', { features }))]

  const browser = detectBrowser()
  let guide: string
  if (browser === 'chrome') {
    guide = t('secureContext.chromeGuide', { origin })
  } else if (browser === 'edge') {
    guide = t('secureContext.edgeGuide', { origin })
  } else {
    guide = t('secureContext.httpsHint')
  }
  children.push(h('p', { class: 'mt-2 mb-0 leading-6 text-xs opacity-80 break-all' }, guide))

  return () => h('div', { class: 'text-sm' }, children)
}

onMounted(() => {
  for (const notice of consumeBootstrapNotices()) {
    if (notice === 'localUserFailed') {
      message.warning(t('appBootstrap.errors.localUserFailed'))
    } else if (notice === 'modeUnreachable') {
      message.warning(t('appBootstrap.errors.modeUnreachable'))
    } else if (notice === 'insecureContext') {
      notification.warning({
        title: t('secureContext.bootTitle'),
        content: buildInsecureContextContent(),
        duration: 12000,
      })
    }
  }
})
</script>
