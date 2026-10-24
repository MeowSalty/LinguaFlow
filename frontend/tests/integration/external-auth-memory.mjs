import assert from 'node:assert/strict'

/** Assertions report policy violations without printing authentication material. */
export async function assertExternalAuthMemory(page, secrets = []) {
  const snapshot = await page.evaluate(() => ({
    url: location.href,
    keys: Object.keys(sessionStorage),
    values: [localStorage, sessionStorage].flatMap((storage) =>
      Object.keys(storage).map((key) => storage.getItem(key) ?? ''),
    ),
  }))
  assert.ok(!new URL(snapshot.url).searchParams.has('ticket'), 'Callback ticket remains in the URL')
  for (const key of [
    'linguaflow.external-flow.v1',
    'linguaflow.external-flow.v2',
    'linguaflow.external-flow.v2.probe',
    'linguaflow.external-result.v1',
  ])
    assert.ok(!snapshot.keys.includes(key), 'An external authentication record remains in storage')
  const text = snapshot.values.join('\n')
  assert.ok(
    !/"(?:verifier|confirmation_token|reauthentication_proof|proof)"\s*:/u.test(text),
    'External authentication secrets were serialized into browser storage',
  )
  for (const secret of secrets.filter(Boolean)) {
    assert.ok(
      !text.includes(secret) && !snapshot.url.includes(secret),
      'External authentication material was exposed in browser storage or the URL',
    )
  }
  return { callback_ticket_removed: true, external_records_removed: true, secrets_not_stored: true }
}
