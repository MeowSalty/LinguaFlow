import { watch, type WatchSource } from 'vue'

import { useMessage } from 'naive-ui'

/**
 * 监听 store 的错误字段，非空时弹出需要手动关闭的错误提示并清空该字段。
 * 适用于错误字段名不为 error 的 store（如 admin），可通过 getter/setter 自定义。
 */
export const useStoreErrorToast = (
  getError: WatchSource<string | null | undefined>,
  clearError: () => void,
): void => {
  const message = useMessage()

  watch(getError, (err) => {
    if (err) {
      message.error(err, { duration: 0, closable: true })
      clearError()
    }
  })
}
