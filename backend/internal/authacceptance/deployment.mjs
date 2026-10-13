import assert from 'node:assert/strict';
import { readFile, readdir, writeFile } from 'node:fs/promises';
import { join, resolve } from 'node:path';
import { chromium } from '@playwright/test';
import { createRuntime, backendDir } from './runtime.mjs';
import { run, keycloakVersion, keycloakSHA256 } from './prepare.mjs';

const runtime = await createRuntime();
let browser;
const results = [];
const must = (condition, message) => assert.ok(condition, message);
function rememberSecrets(value) {
  if (!value || typeof value !== 'object') return;
  for (const [key, item] of Object.entries(value)) {
    if (typeof item === 'string' && ['access_token', 'refresh_token', 'confirmation_token', 'reauthentication_proof'].includes(key)) runtime.secrets.push(item);
    else if (item && typeof item === 'object') rememberSecrets(item);
  }
}
async function expectResponse(promise, expected, operation) {
  const value = await promise;
  assert.equal(value.status, expected, `${operation}: unexpected status (${value.body?.code || value.body?.title || 'no public error'})`);
  if (value.status >= 400) {
    for (const marker of runtime.secrets) must(!JSON.stringify(value.body).includes(marker), 'Problem response leaked a marker secret');
  }
  rememberSecrets(value.body);
  return value.body;
}
async function request(page, path, data, method = 'POST', expected = 200, actor) {
  return expectResponse(page.evaluate(({ path, data, method, actor }) => auth.request(path, data, method, actor), { path, data, method, actor }), expected, path);
}
async function adopt(page, session) { await page.evaluate(s => auth.adopt(s), session); }
async function loginPassword(page, username, password) { const session = await request(page, '/auth/login', { identifier: username, password }, 'POST', 200, null); await adopt(page, session); return session; }
async function driveProvider(page, authorizationURL, provider, username, requirePrompt = false, expected = 200) {
  const authURL = new URL(authorizationURL);
  must(authURL.searchParams.get('code_challenge_method') === 'S256', 'Upstream PKCE S256 missing');
  if (!provider.base) must(authURL.searchParams.has('nonce'), 'OIDC nonce missing');
  if (requirePrompt) must(authURL.searchParams.get('max_age') === '0', 'Fresh authentication must request max_age=0');
  // auth_time is expressed in whole seconds. Age any existing cookie beyond
  // that precision before asserting max_age=0 causes an interactive challenge.
  if (requirePrompt) await new Promise(resolve => setTimeout(resolve, 1100));
  await page.goto(authorizationURL);
  await Promise.race([page.locator('#password').waitFor({ state: 'visible' }), page.waitForURL('**/complete')]);
  if (await page.locator('#password').isVisible()) {
    if (await page.locator('#username').isVisible()) await page.locator('#username').fill(username);
    await page.locator('#password').fill(provider.password);
    await page.locator(provider.base ? '#oauth-approve' : '#kc-login').click();
  } else must(!requirePrompt, 'Existing IdP cookie bypassed required fresh authentication');
  await page.waitForURL('**/complete');
  await page.waitForFunction(() => window.result !== undefined && window.result !== null);
  must(!new URL(page.url()).search, 'Completion URL retained a ticket');
  const response = await page.evaluate(() => window.result);
  assert.equal(response.status, expected, `Provider exchange failed (${response.body?.code || response.body?.title || response.status})`);
  must(response.cache === 'no-store', 'Exchange must be no-store');
  rememberSecrets(response.body);
  return response.body;
}
async function flow(page, path, input, provider, username, { anonymous = false, fresh = false, expected = 200 } = {}) {
  const start = await expectResponse(page.evaluate(({ path, input, anonymous }) => auth.begin(path, input, anonymous), { path, input, anonymous }), 200, 'start');
  return driveProvider(page, start.authorization_url, provider, username, fresh, expected);
}
async function reauth(page, bindingID, action, targetID, provider) {
  const result = await flow(page, '/auth/reauthenticate', { method: 'oidc', binding_id: bindingID, action, target_id: targetID }, provider, 'alice', { fresh: true });
  must(result.kind === 'reauthentication_proof', 'Expected an action-bound reauthentication proof');
  return result.reauthentication_proof;
}
async function createConnection(page, provider, clientID) {
  console.log(`Checking and testing connection ${clientID}`);
  let record = await request(page, '/admin/auth-connections', { kind: 'oidc', display_name: clientID, oidc: { issuer: provider.issuer, client_id: clientID }, client_secret: provider.clientSecret }, 'POST', 201);
  const path = '/admin/auth-connections/' + record.id;
  must(Object.values(record.draft.policy).filter(v => typeof v === 'boolean').every(v => !v), 'New connection switches must be off');
  const checked = await request(page, path + '/check', { expected_revision: record.aggregate_revision });
  must(checked.valid && !checked.fresh_supported, 'Discovery must not claim tested fresh authentication');
  const tested = await flow(page, path + '/test', { expected_revision: record.aggregate_revision }, provider, 'provideradmin', { fresh: true });
  must(tested.kind === 'test_result' && tested.fresh_supported, 'Real connection test failed');
  record = await request(page, path, undefined, 'GET');
  record = await request(page, path + '/publish', { expected_revision: record.aggregate_revision });
  must(!record.published.policy.enabled && !record.published.policy.login_enabled, 'First publication must keep switches off');
  record = await request(page, path, { expected_revision: record.aggregate_revision, policy: { enabled: true, login_enabled: true, signup_enabled: true, link_enabled: true, login_rules: { mode: 'unrestricted' }, signup_rules: { mode: 'unrestricted' }, link_rules: { mode: 'unrestricted' } } }, 'PATCH');
  record = await request(page, path + '/publish', { expected_revision: record.aggregate_revision });
  return record;
}

async function rulesScenario(admin, page, app, provider, first, second, bindingID) {
  const path = `/admin/auth-connections/${second.id}`;
  let record = await request(admin, path, undefined, 'GET');
  const onlineRevision = record.published_revision;
  const mappings = [
    { field_id: 'custom.department', label: 'Department', type: 'string', source: 'oidc_userinfo', pointer: '/department' },
    { field_id: 'custom.groups', label: 'Groups', type: 'string_list', source: 'oidc_userinfo', pointer: '/groups' }
  ];
  const loginRules = { mode: 'conditions', match: 'all', conditions: [
    { field_id: 'custom.department', operator: 'eq', value: 'engineering' },
    { field_id: 'custom.groups', operator: 'contains', value: 'ops' }
  ] };
  record = await request(admin, path, { expected_revision: record.aggregate_revision, attribute_mappings: mappings, evidence_max_age_seconds: 10, policy: { ...record.published.policy, login_rules: loginRules } }, 'PATCH');
  const catalog = await request(admin, path + '/attributes?expected_revision=' + record.aggregate_revision, undefined, 'GET');
  must(catalog.fields.some(f => f.field_id === 'custom.department') && catalog.operators.string_list.includes('contains'), 'Catalog missing typed mappings');
  const sample = await request(admin, path + '/preview', { expected_revision: record.aggregate_revision, sources: { oidc_userinfo: { department: 'engineering', groups: ['ops'] } } });
  must(sample.simulated && sample.rules.login.status === 'pass', 'Simulated rule preview failed');
  await request(page, path + '/preview', { expected_revision: record.aggregate_revision, sources: {} }, 'POST', 403);
  const tested = await flow(admin, path + '/test', { expected_revision: record.aggregate_revision }, provider, 'provideradmin', { fresh: true });
  must(tested.kind === 'test_result' && tested.rules.login.status === 'fail' && !tested.session, 'Administrator failing test did not return isolated diagnostics');
  let impact = await request(admin, path + '/impact', { expected_revision: record.aggregate_revision });
  must(!impact.can_publish && impact.unknown_users.length === 1 && impact.blocked_users.length === 1, 'Old unrestricted login or simulation became candidate evidence');
  const blocked = await request(admin, path + '/publish', { expected_revision: record.aggregate_revision }, 'POST', 409);
  must(blocked.impact?.unknown_users.length === 1 && blocked.impact.remediation, 'Missing publication remediation');
  must((await request(admin, path, undefined, 'GET')).published_revision === onlineRevision, 'Blocked publication replaced online version');
  const setProof = await reauth(page, bindingID, 'password_set', 'password', provider);
  const password = 'B2-verified-fallback-password!'; runtime.secrets.push(password);
  await request(page, '/users/me/password', { reauthentication_proof: setProof, new_password: password }, 'POST', 204);
  await request(admin, path + '/publish', { expected_revision: record.aggregate_revision }, 'POST', 409);
  await loginPassword(page, 'alice_local', password);
  must((await request(page, '/users/me/login-methods', undefined, 'GET')).password.verified, 'Password login did not establish verified fallback status');
  const oldLogin = await flow(page, `/auth/external/${second.id}/start`, {}, provider, 'alice', { anonymous: true });
  const oldProof = await reauth(page, bindingID, 'link', first.id, provider);
  record = await request(admin, path + '/publish', { expected_revision: record.aggregate_revision });
  must(record.published.login_policy_revision > second.published.login_policy_revision, 'Login rule version did not advance');
  await request(page, '/auth/refresh', { refresh_token: oldLogin.session.refresh_token }, 'POST', 401, null);
  await request(page, `/users/me/login-bindings/${first.id}/start`, { reauthentication_proof: oldProof, browser_challenge: 'a'.repeat(43), completion_url: app.completionBase + '/complete' }, 'POST', 403);
  const current = await flow(page, `/auth/external/${second.id}/start`, {}, provider, 'alice', { anonymous: true });
  must(current.kind === 'session' && !current.attributes && !current.rules, 'Formal login leaked administrator diagnostics');
  let methods = await request(page, '/users/me/login-methods', undefined, 'GET');
  const evidenceDeadline = methods.bindings.find(b => b.binding_id === bindingID).evidence_expires_at;
  must(methods.bindings.find(b => b.binding_id === bindingID).login_rule_status === 'pass', 'Fresh verified login not marked pass');
  const rotated = await request(page, '/auth/refresh', { refresh_token: current.session.refresh_token }, 'POST', 200, null);
  methods = await request(page, '/users/me/login-methods', undefined, 'GET');
  must(methods.bindings.find(b => b.binding_id === bindingID).evidence_expires_at === evidenceDeadline, 'Refresh extended evidence deadline');
  await new Promise(resolve => setTimeout(resolve, Math.max(0, Date.parse(evidenceDeadline) - Date.now()) + 100));
  await request(page, '/auth/refresh', { refresh_token: rotated.refresh_token }, 'POST', 401, null);
  methods = await request(page, '/users/me/login-methods', undefined, 'GET');
  must(methods.bindings.find(b => b.binding_id === bindingID).login_rule_status === 'unknown', 'Expired evidence remained pass');
  await flow(page, `/auth/external/${second.id}/start`, {}, provider, 'alice', { anonymous: true });
  for (const username of ['bob', 'missing']) {
    const deniedContext = await browser.newContext(), deniedPage = await deniedContext.newPage();
    await deniedPage.goto(app.completionBase);
    const denied = await flow(deniedPage, `/auth/external/${second.id}/start`, {}, provider, username, { anonymous: true, expected: 403 });
    must(!denied.attributes && !denied.rules && denied.type === 'urn:linguaflow:authentication-rule-denied', 'Rule denial leaked attributes or used wrong status');
    await deniedContext.close();
  }
  console.log('PASS B2: unknown blocks publication, verified fallback permits it, formal attributes and expiry enforced');
}

async function scenario(app, provider, mode) {
  console.log(`Running ${mode} browser lifecycle`);
  const adminContext = await browser.newContext(), userContext = await browser.newContext();
  const admin = await adminContext.newPage(), page = await userContext.newPage();
  page.setDefaultTimeout(20000); admin.setDefaultTimeout(20000);
  const callbacks = [];
  page.on('response', async response => {
    if (new URL(response.url()).pathname.endsWith('/callback')) {
      callbacks.push({ status: response.status(), headers: await response.allHeaders(), url: response.url() });
    }
  });
  await admin.goto(app.completionBase); await page.goto(app.completionBase);
  await loginPassword(admin, 'admin', app.adminPassword);
  const first = await createConnection(admin, provider, 'b1-first');
  const second = await createConnection(admin, provider, 'b1-second');
  const capabilities = await request(page, '/auth/capabilities', undefined, 'GET', 200, null);
  must(capabilities.connections.length === 2 && capabilities.connections.every(c => c.can_login && c.can_signup && c.signup_requirements.length === 0), 'Capabilities mismatch');
  await request(page, '/auth/register', { username: 'taken_name', password: app.adminPassword }, 'POST', 201, null);
  const signup = await flow(page, `/auth/external/${first.id}/start`, {}, provider, 'alice', { anonymous: true });
  must(signup.kind === 'signup_confirmation', 'Callback/exchange should require signup confirmation');
  const signupVerifier = await page.evaluate(() => JSON.parse(sessionStorage.getItem(flowKey)).verifier);
  await expectResponse(page.evaluate(() => auth.complete({ username: 'taken_name' })), 409, 'duplicate username');
  const signedUp = await expectResponse(page.evaluate(() => auth.complete({ username: 'alice_local', display_name: 'Alice' })), 200, 'complete signup');
  must(signedUp.kind === 'session' && signedUp.session.user.email === null, 'Provider email was elevated into platform account email');
  const firstSession = signedUp.session;
  // Replaying an accepted confirmation cannot resend session secrets.
  await request(page, '/auth/external/complete', { confirmation_token: signup.confirmation_token, verifier: signupVerifier, username: 'alice_local' }, 'POST', 400, null);
  let methods = await request(page, '/users/me/login-methods', undefined, 'GET');
  const firstBinding = methods.bindings.find(b => b.connection_id === first.id).binding_id;
  const initialProof = await reauth(page, firstBinding, 'password_set', 'password', provider);
  const password = 'Acceptance-local-password-1!'; runtime.secrets.push(password);
  await request(page, '/users/me/password', { reauthentication_proof: initialProof, new_password: password }, 'POST', 204);
  await request(page, '/users/me/login-methods', undefined, 'GET', 401);
  await request(page, '/auth/refresh', { refresh_token: firstSession.refresh_token }, 'POST', 401, null);
  await loginPassword(page, 'alice_local', password);
  const changedPassword = 'Acceptance-local-password-2!'; runtime.secrets.push(changedPassword);
  await request(page, '/users/me/password', { current_password: password, new_password: changedPassword }, 'PUT', 204);
  await request(page, '/users/me/login-methods', undefined, 'GET', 401);
  await loginPassword(page, 'alice_local', changedPassword);
  const linkProof = await request(page, '/auth/reauthenticate', { method: 'password', password: changedPassword, action: 'link', target_id: second.id });
  const linked = await flow(page, `/users/me/login-bindings/${second.id}/start`, { reauthentication_proof: linkProof.reauthentication_proof }, provider, 'alice');
  must(linked.kind === 'link_confirmation', 'Expected explicit binding confirmation');
  const completion = await expectResponse(page.evaluate(() => auth.complete()), 200, 'complete link');
  must(completion.kind === 'linked' && !completion.session, 'Binding must not issue the target-channel session');
  methods = await request(page, '/users/me/login-methods', undefined, 'GET');
  const secondBinding = methods.bindings.find(b => b.connection_id === second.id).binding_id;
  must(!methods.bindings.find(b => b.binding_id === secondBinding).verified, 'Binding alone must not count as a verified fallback');
  const secondLogin = await flow(page, `/auth/external/${second.id}/start`, {}, provider, 'alice', { anonymous: true });
  must(secondLogin.kind === 'session', 'Second connection explicit login failed');
  methods = await request(page, '/users/me/login-methods', undefined, 'GET');
  must(methods.bindings.find(b => b.binding_id === secondBinding).verified, 'Actual login must verify the binding generation');
  const ownPasswordProof = await request(page, '/auth/reauthenticate', { method: 'password', password: changedPassword, action: 'password_delete', target_id: 'password' });
  await request(page, '/users/me/password', { reauthentication_proof: ownPasswordProof.reauthentication_proof }, 'DELETE', 403);
  const deleteProof = await reauth(page, secondBinding, 'password_delete', 'password', provider);
  await request(page, '/users/me/password', { reauthentication_proof: deleteProof }, 'DELETE', 204);
  await request(page, '/users/me/login-methods', undefined, 'GET', 401);
  await flow(page, `/auth/external/${second.id}/start`, {}, provider, 'alice', { anonymous: true });
  const ownBindingProof = await reauth(page, firstBinding, 'unlink', String(firstBinding), provider);
  await request(page, `/users/me/login-bindings/${firstBinding}`, { reauthentication_proof: ownBindingProof }, 'DELETE', 403);
  const unlinkProof = await reauth(page, secondBinding, 'unlink', String(firstBinding), provider);
  await request(page, `/users/me/login-bindings/${firstBinding}`, { reauthentication_proof: unlinkProof }, 'DELETE', 204);
  await request(page, '/users/me/login-methods', undefined, 'GET', 401);
  await flow(page, `/auth/external/${second.id}/start`, {}, provider, 'alice', { anonymous: true });
  const lastProof = await reauth(page, secondBinding, 'unlink', String(secondBinding), provider);
  await request(page, `/users/me/login-bindings/${secondBinding}`, { reauthentication_proof: lastProof }, 'DELETE', 409);
  let record = await request(admin, `/admin/auth-connections/${second.id}`, undefined, 'GET');
  await request(admin, `/admin/auth-connections/${second.id}/archive`, { expected_revision: record.aggregate_revision }, 'POST', 409);
  // A display-only publication must preserve current sessions.
  const previousRevision = record.aggregate_revision;
  record = await request(admin, `/admin/auth-connections/${second.id}`, { expected_revision: previousRevision, description: 'Updated display only' }, 'PATCH');
  await request(admin, `/admin/auth-connections/${second.id}/publish`, { expected_revision: previousRevision }, 'POST', 409);
  await request(admin, `/admin/auth-connections/${second.id}/publish`, { expected_revision: record.aggregate_revision });
  await request(page, '/users/me/login-methods', undefined, 'GET');
  const adminProof = await request(admin, '/auth/reauthenticate', { method: 'password', password: app.adminPassword, action: 'password_delete', target_id: 'password' });
  await request(admin, '/users/me/password', { reauthentication_proof: adminProof.reauthentication_proof }, 'DELETE', 409);
  // Cancellation is durable even if the browser subsequently visits the old authorization URL.
  const cancelStart = await expectResponse(page.evaluate(id => auth.begin(`/auth/external/${id}/start`, {}, true), second.id), 200, 'cancel start');
  await expectResponse(page.evaluate(() => auth.cancel()), 204, 'cancel');
  const state = new URL(cancelStart.authorization_url).searchParams.get('state');
  const cancelled = await page.request.get(`${app.publicBase}/api/v1/auth/external/${second.id}/callback?state=${encodeURIComponent(state)}&error=access_denied`, { maxRedirects: 0 });
  must(cancelled.status() === 400 && !cancelled.headers().location, 'Cancelled callback redirected');
  const failureStart = await expectResponse(page.evaluate(id => auth.begin(`/auth/external/${id}/start`, {}, true), second.id), 200, 'failure start');
  const failureState = new URL(failureStart.authorization_url).searchParams.get('state');
  const failureURL = `${app.publicBase}/api/v1/auth/external/${second.id}/callback?state=${encodeURIComponent(failureState)}&error=access_denied`;
  await page.goto(failureURL); await page.waitForURL('**/complete'); await page.waitForFunction(() => window.result?.status);
  must((await page.evaluate(() => window.result.status)) === 400 && !new URL(page.url()).search, 'Failed flow ticket contract mismatch');
  const replay = await page.request.get(failureURL, { maxRedirects: 0 });
  must(replay.status() === 400 && !replay.headers().location, 'Replayed state redirected');
  await flow(page, `/auth/external/${second.id}/start`, {}, provider, 'alice', { anonymous: true });
  const badCompletion = await page.evaluate(id => auth.request(`/auth/external/${id}/start`, { browser_challenge: 'a'.repeat(43), completion_url: 'https://untrusted.invalid/complete' }), second.id);
  must(badCompletion.status === 400, 'Unapproved completion accepted');
  const cors = await page.request.fetch(`${app.publicBase}/api/v1/auth/external/exchange`, { method: 'OPTIONS', headers: { Origin: 'https://untrusted.invalid', 'Access-Control-Request-Method': 'POST', 'Access-Control-Request-Headers': 'content-type' } });
  must(!cors.headers()['access-control-allow-origin'], 'Unapproved origin allowed');
  must(callbacks.filter(c => c.status === 303).every(c => c.headers['cache-control'] === 'no-store' && c.headers['referrer-policy'] === 'no-referrer'), 'Callback caching/referrer headers missing');
  // No authentication secrets may appear in administrator views.
  await rulesScenario(admin, page, app, provider, first, second, secondBinding);
  const allConnections = await request(admin, '/admin/auth-connections', undefined, 'GET');
  for (const marker of runtime.secrets) must(!JSON.stringify(allConnections).includes(marker), 'Administrator response leaked a marker secret');
  const audit = await request(admin, '/admin/audit-logs', undefined, 'GET');
  for (const marker of runtime.secrets) must(!JSON.stringify(audit).includes(marker), 'Audit response leaked a marker secret');
  await adminContext.close(); await userContext.close();
  results.push({ mode, status: 'passed', flows: ['connection_check_test_publish', 'signup_username_retry', 'login', 'fresh_oidc_reauthentication_with_cookie', 'set_change_delete_password', 'explicit_second_binding', 'verified_fallback', 'safe_unlink', 'last_method_and_admin_protection', 'display_publication_session', 'cancel', 'denial_retry_replay', 'completion_allowlist', 'cors', 'url_ticket_removal', 'no_store_no_referrer', 'typed_attribute_catalog_preview', 'admin_test_rule_failure', 'unknown_blocks_publish', 'password_control_required_for_fallback', 'verified_fallback_allows_publish', 'old_rule_refresh_and_proof_rejected', 'department_and_groups_rules', 'refresh_retains_evidence_deadline', 'expired_evidence_reauthentication', 'denied_and_missing_attributes_private'] });
  console.log(`PASS ${mode}: real Keycloak account lifecycle and browser contract`);
}

async function instanceIsolation(source, other, provider) {
  const context = await browser.newContext();
  const page = await context.newPage();
  await page.goto(source.completionBase);
  const capabilities = await request(page, '/auth/capabilities', undefined, 'GET', 200, null);
  const connection = capabilities.connections.find(c => c.display_name === 'b1-second');
  must(connection, 'Missing isolation test connection');
  let captured;
  const exchangePath = '**/api/v1/auth/external/exchange';
  await page.route(exchangePath, async route => {
    captured = route.request().postDataJSON();
    await route.fulfill({ status: 200, contentType: 'application/json', headers: { 'Cache-Control': 'no-store' }, body: JSON.stringify({ kind: 'held_for_isolation' }) });
  });
  const held = await flow(page, `/auth/external/${connection.connection_id}/start`, {}, provider, 'alice', { anonymous: true });
  must(held.kind === 'held_for_isolation' && captured?.ticket, 'Could not hold an unconsumed exchange ticket');
  const otherPage = await context.newPage();
  await otherPage.goto(`${other.completionBase}/complete?ticket=${encodeURIComponent(captured.ticket)}`);
  await otherPage.waitForFunction(() => window.result?.status);
  must((await otherPage.evaluate(() => window.result.body.code)) === 'missing_instance_flow' && !new URL(otherPage.url()).search, 'Other instance completion used foreign flow state');
  const wrongInstance = await page.request.post(`${other.publicBase}/api/v1/auth/external/exchange`, { data: captured });
  must(wrongInstance.status() === 400, 'Foreign instance accepted a ticket');
  await page.unroute(exchangePath);
  const correct = await request(page, '/auth/external/exchange', captured, 'POST', 200, null);
  must(correct.kind === 'session', 'Foreign rejection consumed the legitimate instance ticket');
  await request(page, '/auth/external/exchange', captured, 'POST', 400, null);
  await context.close();
  results.push({ mode: 'instance-isolation', status: 'passed', flows: ['foreign_completion_rejected', 'foreign_ticket_rejected', 'original_ticket_usable_once'] });
  console.log('PASS instance isolation and exchange replay');
}

async function oauthScenario(app, provider, mode) {
  console.log(`Running ${mode} generic OAuth 2.0 lifecycle`);
  const eventStart = provider.events.length;
  const adminContext = await browser.newContext(), userContext = await browser.newContext();
  const admin = await adminContext.newPage(), page = await userContext.newPage();
  await admin.goto(app.completionBase); await page.goto(app.completionBase);
  await loginPassword(admin, 'admin', app.adminPassword);
  const password = 'B3-first-maintenance-password!'; runtime.secrets.push(password);
  const connections = [];
  for (const [clientID, method] of [['b3-basic', 'client_secret_basic'], ['b3-post', 'client_secret_post']]) {
    let record = await request(admin, '/admin/auth-connections', {
      kind: 'generic_oauth2', display_name: clientID, client_secret: provider.clientSecret,
      oauth2: { authorization_endpoint: provider.base + '/authorize', token_endpoint: provider.base + '/token', profile_endpoint: provider.base + '/user', client_id: clientID, client_auth_method: method, scopes: [], subject_mapping: { pointer: '/id', type: 'integer' } }
    }, 'POST', 201);
    const path = '/admin/auth-connections/' + record.id;
    must(!record.draft.policy.enabled, 'New OAuth connection must start disabled');
    record = await request(admin, path, { expected_revision: record.aggregate_revision,
      attribute_mappings: [{ field_id: 'external.account_created_at', label: 'Account created', type: 'datetime', source: 'oauth2_profile', pointer: '/created_at' }],
      policy: { enabled: true, login_enabled: true, signup_enabled: true, link_enabled: true, login_rules: { mode: 'unrestricted' }, link_rules: { mode: 'unrestricted' }, signup_rules: { mode: 'conditions', match: 'all', conditions: [{ field_id: 'external.account_age_days', operator: 'gte', value: 365 }] } }
    }, 'PATCH');
    const checked = await request(admin, path + '/check', { expected_revision: record.aggregate_revision });
    must(checked.valid && !checked.fresh_supported, 'OAuth configuration check must not invent fresh authentication');
    await request(admin, path + '/publish', { expected_revision: record.aggregate_revision }, 'POST', 409);
    const tested = await flow(admin, path + '/test', { expected_revision: record.aggregate_revision }, provider, 'provideradmin');
    must(tested.kind === 'test_result' && !tested.fresh_supported && tested.rules.signup.status === 'pass', 'OAuth real test or account age rule failed');
    must(tested.attributes['external.account_created_at'].status === 'pass' && tested.attributes['external.account_age_days'].value >= 365, 'Profile date did not feed typed derived rule');
    record = await request(admin, path, undefined, 'GET');
    record = await request(admin, path + '/publish', { expected_revision: record.aggregate_revision });
    connections.push(record);
  }
  const [first, second] = connections;
  const capabilities = await request(page, '/auth/capabilities', undefined, 'GET', 200, null);
  for (const connection of connections) {
    const capability = capabilities.connections.find(c => c.connection_id === connection.id);
    must(capability?.can_signup, 'OAuth signup missing despite available password initialization');
    assert.deepEqual(capability.signup_requirements, ['local_password']);
  }
  const signup = await flow(page, `/auth/external/${first.id}/start`, {}, provider, 'alice', { anonymous: true });
  must(signup.kind === 'signup_confirmation' && !signup.session, 'OAuth signup issued a session before initialization');
  assert.deepEqual(signup.signup_requirements, ['local_password']);
  await expectResponse(page.evaluate(() => auth.complete({ username: 'oauth_alice' })), 400, 'missing initial password');
  must((await request(admin, '/admin/users?search=oauth_alice', undefined, 'GET')).total === 0, 'Missing initial password left a user');
  const registered = await expectResponse(page.evaluate(password => auth.complete({ username: 'oauth_alice', initial_password: password }), password), 200, 'OAuth first signup');
  must(registered.kind === 'session' && registered.session.user.email === null, 'Untrusted OAuth email became platform account email');
  const accountID = registered.session.user.id;
  let methods = await request(page, '/users/me/login-methods', undefined, 'GET');
  must(methods.password.is_set && !methods.email.verified && !methods.email.can_recover && methods.bindings.every(b => !b.can_reauthenticate), 'OAuth fabricated account maintenance or email capabilities');
  const firstBinding = methods.bindings.find(b => b.connection_id === first.id).binding_id;
  await expectResponse(page.evaluate(bindingID => auth.begin('/auth/reauthenticate', { method: 'oidc', binding_id: bindingID, action: 'link', target_id: 'password' }), firstBinding), 403, 'OAuth cannot provide fresh reauthentication');
  const linkProof = await request(page, '/auth/reauthenticate', { method: 'password', password, action: 'link', target_id: second.id });
  const linked = await flow(page, `/users/me/login-bindings/${second.id}/start`, { reauthentication_proof: linkProof.reauthentication_proof }, provider, 'alice');
  must(linked.kind === 'link_confirmation', 'OAuth binding skipped explicit confirmation');
  assert.deepEqual(linked.signup_requirements, []);
  const linkResult = await expectResponse(page.evaluate(() => auth.complete()), 200, 'OAuth second binding');
  must(linkResult.kind === 'linked' && !linkResult.session, 'OAuth binding issued an unrelated session');
  const login = await flow(page, `/auth/external/${second.id}/start`, {}, provider, 'alice', { anonymous: true });
  must(login.kind === 'session' && login.session.user.id === accountID, 'OAuth login changed account ownership');
  methods = await request(page, '/users/me/login-methods', undefined, 'GET');
  must(methods.bindings.length === 2 && methods.bindings.every(b => b.verified && !b.can_reauthenticate) && !methods.password.can_delete, 'OAuth capability/maintenance status mismatch');
  const deleteProof = await request(page, '/auth/reauthenticate', { method: 'password', password, action: 'password_delete', target_id: 'password' });
  await request(page, '/users/me/password', { reauthentication_proof: deleteProof.reauthentication_proof }, 'DELETE', 409);
  const unlinkProof = await request(page, '/auth/reauthenticate', { method: 'password', password, action: 'unlink', target_id: String(firstBinding) });
  await request(page, `/users/me/login-bindings/${firstBinding}`, { reauthentication_proof: unlinkProof.reauthentication_proof }, 'DELETE', 204);
  await request(page, '/users/me/login-methods', undefined, 'GET', 401);
  const passwordLogin = await loginPassword(page, 'oauth_alice', password);
  must(passwordLogin.user.id === accountID, 'Initial password cannot log back into the same account');
  must((await request(page, '/users/me/login-methods', undefined, 'GET')).bindings.length === 1, 'OAuth unlink left the binding');
  // Adjacent large decimal IDs would collide if decoded through float64/Number.
  const neighbor = await flow(page, `/auth/external/${second.id}/start`, {}, provider, 'neighbor', { anonymous: true });
  must(neighbor.kind === 'signup_confirmation', 'Adjacent large integer subject was rounded into another account');
  const neighborSession = await expectResponse(page.evaluate(password => auth.complete({ username: 'oauth_neighbor', initial_password: password }), password), 200, 'neighbor integer signup');
  must(neighborSession.session.user.id !== accountID, 'Adjacent large integer subjects share a user');
  for (const username of ['young', 'missing']) {
    const denied = await flow(page, `/auth/external/${first.id}/start`, {}, provider, username, { anonymous: true, expected: 403 });
    must(denied.type === 'urn:linguaflow:authentication-rule-denied' && !denied.attributes && !denied.rules && !denied.session, 'OAuth date rule denial leaked diagnostics or permitted signup');
  }
  must(provider.errors.length === 0, 'Controlled OAuth server rejected protocol requests');
  const events = provider.events.slice(eventStart);
  for (const method of ['client_secret_basic', 'client_secret_post']) {
    must(events.some(e => e.phase === 'token' && e.method === method && e.pkce === 'S256') && events.some(e => e.phase === 'profile' && e.method === method), 'OAuth client authentication or profile retrieval was not exercised');
  }
  for (const endpoint of ['/admin/auth-connections', '/admin/audit-logs']) {
    const response = await request(admin, endpoint, undefined, 'GET');
    for (const secret of runtime.secrets) must(!JSON.stringify(response).includes(secret), 'OAuth secrets leaked in administrator response');
  }
  await adminContext.close(); await userContext.close();
  results.push({ mode, database: mode === 'same-origin' ? 'SQLite' : 'PostgreSQL', protocol: 'generic_oauth2', status: 'passed', flows: ['client_secret_basic', 'client_secret_post', 'authorization_code_pkce_s256', 'bearer_current_user_profile', 'real_test_required_before_publish', 'fresh_supported_false_can_publish', 'created_at_derived_signup_rule', 'missing_and_young_date_denied', 'required_initial_password_atomic_signup', 'initial_password_immediate_reauthentication', 'generic_fresh_reauthentication_rejected', 'second_binding_and_login', 'distinct_adjacent_large_integer_subjects', 'unverified_email_not_promoted', 'last_maintenance_password_preserved', 'password_reauthenticated_unlink', 'password_login_after_unlink'] });
  console.log(`PASS ${mode}: generic OAuth, initial password, maintenance, profile date rules and integer identity isolation`);
}

try {
  await run('task', ['backend:build'], { cwd: resolve(backendDir, '..') });
  console.log('Starting isolated PostgreSQL and verified Keycloak 26.8.0');
  const [postgres, provider, oauth] = await Promise.all([runtime.postgres(), runtime.keycloak(), runtime.oauth2()]);
  if (process.argv.includes('--postgres')) {
    await run('task', ['backend:test:auth:postgres'], { cwd: resolve(backendDir, '..'), env: { ...process.env, LINGUAFLOW_TEST_POSTGRES_DSN: postgres.dsn } });
    results.push({ database: 'PostgreSQL', status: 'passed' });
  }
  browser = await chromium.launch({ headless: true, ...(process.env.LINGUAFLOW_AUTH_BROWSER ? { executablePath: process.env.LINGUAFLOW_AUTH_BROWSER } : {}) });
  const sameOrigin = await runtime.application('same-origin');
  await scenario(sameOrigin, provider, 'same-origin');
  await oauthScenario(sameOrigin, oauth, 'same-origin');
  const crossOrigin = await runtime.application('cross-origin', postgres.applicationDSN);
  await scenario(crossOrigin, provider, 'cross-origin');
  await oauthScenario(crossOrigin, oauth, 'cross-origin');
  await instanceIsolation(sameOrigin, crossOrigin, provider);
  for (const file of await readdir(runtime.root)) {
    if (!file.endsWith('.log') || file === 'postgres.log' || file === 'keycloak.log') continue;
    const data = await readFile(join(runtime.root, file), 'utf8');
    for (const marker of runtime.secrets) must(!data.includes(marker), 'Server log leaked a marker secret');
  }
  const report = { tested_at: new Date().toISOString(), keycloak: { version: keycloakVersion, sha256: keycloakSHA256 }, github: { status: 'pending', reason: 'Real GitHub OAuth application and interactive account authorization are deferred; controlled OAuth acceptance is not GitHub acceptance.' }, results };
  await writeFile(join(runtime.root, 'report.json'), JSON.stringify(report, null, 2));
  console.log('Acceptance report: ' + join(runtime.root, 'report.json'));
} catch (error) {
  console.error('Acceptance failed: ' + error.message);
  console.error('Isolated diagnostic directory: ' + runtime.root);
  process.exitCode = 1;
} finally { await browser?.close(); await runtime.close(); }
