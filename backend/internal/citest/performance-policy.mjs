// Engineering targets are separate from the unchanged native 600s/120s budgets.
export const performancePolicy = Object.freeze({
  version: 2,
  defaultRounds: 1,
  sceneSeconds: 450,
  raceSyntheticSceneSeconds: 540,
  atomicSyntheticSceneSeconds: 540,
  responseSeconds: 90,
  originalSceneSeconds: 600,
  originalResponseSeconds: 120,
});

export function sceneSeconds(mode, shape) {
  if (!['plain', 'race', 'race-atomic'].includes(mode) || !['dense', 'synthetic'].includes(shape)) throw new Error('Unknown EPUB mode/shape');
  if (shape === 'synthetic' && mode === 'race') return performancePolicy.raceSyntheticSceneSeconds;
  if (shape === 'synthetic' && mode === 'race-atomic') return performancePolicy.atomicSyntheticSceneSeconds;
  return performancePolicy.sceneSeconds;
}

export function sceneFailure(mode, shape) {
  const target = sceneSeconds(mode, shape);
  const margin = 100 * (performancePolicy.originalSceneSeconds - target) / performancePolicy.originalSceneSeconds;
  return `Instrumented ${mode}/${shape} EPUB scene exceeds ${target}s engineering target (${margin}% margin within original ${performancePolicy.originalSceneSeconds}s budget)`;
}

export function performanceRounds(value = performancePolicy.defaultRounds) {
  const rounds = Number(value);
  if (!Number.isSafeInteger(rounds) || rounds < 1) throw new Error('LINGUAFLOW_CI_PERFORMANCE_ROUNDS must be a positive safe integer');
  return rounds;
}
