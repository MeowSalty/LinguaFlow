export type RuntimeTranslator = (key: string, named?: Record<string, unknown>) => string

let translate: RuntimeTranslator = (key) => key

/** Transport code can translate errors without importing the application or its dictionaries. */
export function setRuntimeTranslator(translator: RuntimeTranslator): void {
  translate = translator
}

export const t: RuntimeTranslator = (key, named) => translate(key, named)
