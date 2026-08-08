import { ref, computed } from 'vue'
import { defineStore } from 'pinia'

export const useUiStore = defineStore('ui', () => {
  const selectionBarCount = ref(0)
  const selectionBarActive = computed(() => selectionBarCount.value > 0)

  function registerSelectionBar(active: boolean): void {
    if (active) {
      selectionBarCount.value++
    } else if (selectionBarCount.value > 0) {
      selectionBarCount.value--
    }
  }

  return { selectionBarActive, registerSelectionBar }
})
