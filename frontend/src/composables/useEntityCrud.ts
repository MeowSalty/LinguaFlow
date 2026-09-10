import { ref } from 'vue'
import { useI18n } from 'vue-i18n'

import { useMessage } from 'naive-ui'

interface ScopeEntity {
  id: number
  scope: string
}

interface UseEntityCrudOptions {
  /** i18n key 前缀，如 'promptTemplates'（复用各页已有的 filters/scopes/messages/delete key） */
  i18nPrefix: string
  /** 执行删除请求，由各页注入自己的 store 方法 */
  deleteItem: (id: number) => Promise<unknown>
  /** 判断某 id 是否删除中（用于删除弹窗 loading 态） */
  isDeleting: (id: number) => boolean
}

/**
 * 模板/配置类管理页的同构样板：作用域标签配色、删除确认弹窗。
 * 各页保留自己的 store、表单与编辑器；错误 toast 走 useStoreErrorToast，不在此处理。
 */
export const useEntityCrud = <T extends ScopeEntity>(options: UseEntityCrudOptions) => {
  const { t } = useI18n()
  const message = useMessage()

  const deleteModalVisible = ref(false)
  const deletingItem = ref<T | null>(null)

  const getScopeTagType = (scope: string): 'default' | 'info' | 'success' => {
    switch (scope) {
      case 'system':
        return 'default'
      case 'user':
        return 'info'
      default:
        return 'default'
    }
  }

  const confirmDelete = (item: T, event?: MouseEvent): void => {
    event?.stopPropagation()
    if (item.scope === 'system') {
      message.warning(t(`${options.i18nPrefix}.messages.systemDeleteForbidden`))
      return
    }
    deletingItem.value = item
    deleteModalVisible.value = true
  }

  const executeDelete = async (): Promise<void> => {
    if (!deletingItem.value) return

    try {
      await options.deleteItem(deletingItem.value.id)
      message.success(t(`${options.i18nPrefix}.messages.deleteSuccess`))
      deleteModalVisible.value = false
      deletingItem.value = null
    } catch {
      // Error is handled by the store
    }
  }

  return {
    getScopeTagType,
    deleteModalVisible,
    deletingItem,
    confirmDelete,
    executeDelete,
  }
}
