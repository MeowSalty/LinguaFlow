import type {
  ExecutionPlanFormRound,
  ExecutionPlanFormRubyRetry,
} from '@/utils/execution-plan-config'

interface PlanDraft {
  profile_id: number | null
  ruby_retry: ExecutionPlanFormRubyRetry
  rounds: ExecutionPlanFormRound[]
}
/** Copying is explicit: retain eligible IDs, clear private/other-organization dependencies. */
export function clearUnavailablePlanDependencies<T extends PlanDraft>(
  draft: T,
  allowed: {
    profiles: readonly { id: number }[]
    backends: readonly { id: number }[]
    prompts: readonly { id: number }[]
    bootstrap: readonly { id: number }[]
  },
): T {
  const copy = JSON.parse(JSON.stringify(draft)) as T
  if (!allowed.profiles.some((item) => item.id === copy.profile_id)) copy.profile_id = null
  if (!allowed.backends.some((item) => item.id === copy.ruby_retry.backend_id))
    copy.ruby_retry.backend_id = null
  for (const round of copy.rounds) {
    if (!allowed.backends.some((item) => item.id === round.backend_id)) round.backend_id = null
    if (
      round.translate &&
      !allowed.prompts.some((item) => item.id === round.translate?.prompt_template_id)
    )
      round.translate.prompt_template_id = null
    if (round.extract && !allowed.bootstrap.some((item) => item.id === round.extract?.template_id))
      round.extract.template_id = null
  }
  return copy
}
