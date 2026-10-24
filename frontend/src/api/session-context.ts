import { shallowRef } from 'vue'
import { onSessionGenerationChange } from './session-core'

export * from './session-core'

export const sessionGeneration = shallowRef(0)

onSessionGenerationChange((generation) => {
  sessionGeneration.value = generation
})
