import assert from 'node:assert/strict';
import { randomBytes } from 'node:crypto';
import { writeFile } from 'node:fs/promises';
import { join, resolve } from 'node:path';
import { createInterface } from 'node:readline/promises';
import { chromium } from '@playwright/test';
import { createRuntime, backendDir } from './runtime.mjs';
import { run } from './prepare.mjs';

// Deliberately separate from automated deployment acceptance. No GitHub password,
// session cookie or personal token is read by the harness. The operator completes
// GitHub login/consent in the visible browser after configuring an exact callback.
const clientID = process.env.LINGUAFLOW_GITHUB_CLIENT_ID;
const clientSecret = process.env.LINGUAFLOW_GITHUB_CLIENT_SECRET;
const minimumAge = Number(process.env.LINGUAFLOW_GITHUB_MIN_ACCOUNT_AGE_DAYS || '365');
if (!clientID || !clientSecret || !Number.isSafeInteger(minimumAge) || minimumAge < 0 || !process.stdin.isTTY) {
  console.error('GitHub NOT ACCEPTED: run interactively with LINGUAFLOW_GITHUB_CLIENT_ID and LINGUAFLOW_GITHUB_CLIENT_SECRET. Optional LINGUAFLOW_GITHUB_MIN_ACCOUNT_AGE_DAYS defaults to 365. See github.env.example.');
  process.exit(1);
}
const runtime = await createRuntime();
runtime.secrets.push(clientSecret);
let browser, terminal;
const evidence = [];
async function response(promise, expected = 200) {
  const result = await promise;
  assert.equal(result.status, expected, `GitHub acceptance API status ${result.status} (${result.body?.error_code || result.body?.title || 'unexpected response'})`);
  return result.body;
}
const request = (page, path, data, method = 'POST', expected = 200, actor) => response(page.evaluate(args => auth.request(args.path, args.data, args.method, args.actor), { path, data, method, actor }), expected);
async function authorize(page, path, input, anonymous = false) {
  const start = await response(page.evaluate(args => auth.begin(args.path, args.input, args.anonymous), { path, input, anonymous }));
  const url = new URL(start.authorization_url);
  assert.equal(url.origin, 'https://github.com'); assert.equal(url.pathname, '/login/oauth/authorize');
  assert.equal(url.searchParams.get('code_challenge_method'), 'S256');
  assert.match(url.searchParams.get('code_challenge') || '', /^[A-Za-z0-9_-]{43}$/);
  console.log('Complete GitHub login and authorization in the browser. No secrets should be pasted into the terminal.');
  await page.goto(start.authorization_url);
  await page.waitForURL('**/complete', { timeout: 300000 });
  await page.waitForFunction(() => window.result?.status, undefined, { timeout: 20000 });
  assert.equal(new URL(page.url()).search, '');
  const result = await page.evaluate(() => window.result);
  assert.equal(result.cache, 'no-store');
  return response(Promise.resolve(result));
}
try {
  await run('task', ['backend:build'], { cwd: resolve(backendDir, '..') });
  const app = await runtime.application('same-origin', '', { publicOAuth: true });
  browser = await chromium.launch({ headless: false, ...(process.env.LINGUAFLOW_AUTH_BROWSER ? { executablePath: process.env.LINGUAFLOW_AUTH_BROWSER } : {}) });
  const context = await browser.newContext(), admin = await context.newPage(), page = await context.newPage();
  await admin.goto(app.completionBase); await page.goto(app.completionBase);
  const session = await request(admin, '/auth/login', { identifier: 'admin', password: app.adminPassword }, 'POST', 200, null);
  await admin.evaluate(value => auth.adopt(value), session);
  let connection = await request(admin, '/admin/auth-connections', {
    kind: 'generic_oauth2', display_name: 'GitHub interactive acceptance', client_secret: clientSecret,
    oauth2: { authorization_endpoint: 'https://github.com/login/oauth/authorize', token_endpoint: 'https://github.com/login/oauth/access_token', profile_endpoint: 'https://api.github.com/user', client_id: clientID, client_auth_method: 'client_secret_post', scopes: [], subject_mapping: { pointer: '/id', type: 'integer' } }
  }, 'POST', 201);
  const path = '/admin/auth-connections/' + connection.id;
  console.log('Set this exact Authorization callback URL in the dedicated GitHub OAuth App settings:');
  console.log(`${app.publicBase}/api/v1/auth/external/${connection.id}/callback`);
  console.log('Documentation: https://docs.github.com/en/apps/oauth-apps/building-oauth-apps/authorizing-oauth-apps#redirect-urls');
  terminal = createInterface({ input: process.stdin, output: process.stdout });
  await terminal.question('Press Enter after saving the exact callback URL. ');
  terminal.close(); terminal = undefined;
  connection = await request(admin, path, { expected_revision: connection.aggregate_revision,
    attribute_mappings: [{ field_id: 'external.account_created_at', label: 'Account created', type: 'datetime', source: 'oauth2_profile', pointer: '/created_at' }],
    policy: { enabled: true, login_enabled: true, signup_enabled: true, link_enabled: true, login_rules: { mode: 'unrestricted' }, link_rules: { mode: 'unrestricted' }, signup_rules: { mode: 'conditions', match: 'all', conditions: [{ field_id: 'external.account_age_days', operator: 'gte', value: minimumAge }] } }
  }, 'PATCH');
  const checked = await request(admin, path + '/check', { expected_revision: connection.aggregate_revision });
  assert.ok(checked.valid && !checked.fresh_supported);
  const tested = await authorize(admin, path + '/test', { expected_revision: connection.aggregate_revision });
  assert.equal(tested.kind, 'test_result'); assert.equal(tested.fresh_supported, false);
  assert.equal(tested.attributes['external.account_created_at'].status, 'pass');
  assert.equal(tested.rules.signup.status, 'pass', 'Choose an account meeting the configured age threshold or adjust the environment setting and restart');
  connection = await request(admin, path, undefined, 'GET');
  await request(admin, path + '/publish', { expected_revision: connection.aggregate_revision });
  evidence.push('github_authorization_code_pkce_s256', 'github_current_user_integer_subject', 'github_created_at_derived_age_rule', 'real_test_fresh_false_publish');
  const signup = await authorize(page, `/auth/external/${connection.id}/start`, {}, true);
  assert.equal(signup.kind, 'signup_confirmation'); assert.deepEqual(signup.signup_requirements, ['local_password']);
  await response(page.evaluate(() => auth.complete({ username: 'github_acceptance' })), 400);
  assert.equal((await request(admin, '/admin/users?search=github_acceptance', undefined, 'GET')).total, 0);
  const password = randomBytes(32).toString('base64url'); runtime.secrets.push(password);
  const completed = await response(page.evaluate(password => auth.complete({ username: 'github_acceptance', initial_password: password }), password));
  assert.equal(completed.kind, 'session');
  const proof = await request(page, '/auth/reauthenticate', { method: 'password', password, action: 'password_delete', target_id: 'password' });
  assert.ok(proof.reauthentication_proof);
  await request(page, '/users/me/password', { reauthentication_proof: proof.reauthentication_proof }, 'DELETE', 409);
  const login = await authorize(page, `/auth/external/${connection.id}/start`, {}, true);
  assert.equal(login.kind, 'session'); assert.equal(login.session.user.id, completed.session.user.id);
  evidence.push('atomic_signup_initial_password', 'password_reauthentication', 'maintenance_password_protected', 'same_subject_subsequent_login');
  await writeFile(join(runtime.root, 'github-report.json'), JSON.stringify({ tested_at: new Date().toISOString(), github: { status: 'passed', minimum_account_age_days: minimumAge }, flows: evidence }, null, 2));
  console.log('GitHub acceptance passed. Sanitized report: ' + join(runtime.root, 'github-report.json'));
} catch (error) {
  const safeMessage = runtime.secrets.reduce((message, secret) => message.replaceAll(secret, '[redacted]'), String(error.message));
  await writeFile(join(runtime.root, 'github-report.json'), JSON.stringify({ tested_at: new Date().toISOString(), github: { status: 'failed' }, completed_flows: evidence, error: safeMessage }, null, 2));
  console.error('GitHub NOT ACCEPTED: ' + safeMessage); process.exitCode = 1;
} finally { terminal?.close(); await browser?.close(); await runtime.close(); }
