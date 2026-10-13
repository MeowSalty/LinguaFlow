import assert from 'node:assert/strict';
import { createHash, randomBytes, timingSafeEqual } from 'node:crypto';
import { createServer } from 'node:http';

const random = () => randomBytes(32).toString('base64url');
const digest = value => createHash('sha256').update(value).digest('base64url');
const equal = (a, b) => typeof a === 'string' && typeof b === 'string' && Buffer.byteLength(a) === Buffer.byteLength(b) && timingSafeEqual(Buffer.from(a), Buffer.from(b));
const escape = value => String(value).replaceAll('&', '&amp;').replaceAll('"', '&quot;').replaceAll('<', '&lt;');

// A real, controlled authorization-code server for browser acceptance. Profiles
// use decimal JSON literals deliberately beyond JavaScript's safe integer range.
export async function controlledOAuth({ callbackBases, secrets }) {
  const clientSecret = random(), password = random(); secrets.push(clientSecret, password);
  const clients = new Map([['b3-basic', 'client_secret_basic'], ['b3-post', 'client_secret_post']]);
  const subjects = { provideradmin: '900719925474099312341', alice: '900719925474099312342', neighbor: '900719925474099312343', young: '900719925474099312344', missing: '900719925474099312345' };
  const codes = new Map(), tokens = new Map(), authorizations = new Map(), events = [], errors = [];
  let base;
  function json(res, status, body) { res.writeHead(status, { 'Content-Type': 'application/json', 'Cache-Control': 'no-store' }); res.end(JSON.stringify(body)); }
  async function form(req) {
    assert.match(req.headers['content-type'] || '', /^application\/x-www-form-urlencoded(?:;|$)/);
    let body = ''; for await (const chunk of req) { body += chunk; assert.ok(body.length <= 16384); }
    return new URLSearchParams(body);
  }
  const server = createServer(async (req, res) => {
    try {
      const url = new URL(req.url, base);
      if (url.pathname === '/authorize' && req.method === 'GET') {
        const params = url.searchParams, clientID = params.get('client_id'), redirect = params.get('redirect_uri');
        assert.ok(clients.has(clientID)); assert.equal(params.get('response_type'), 'code');
        assert.equal(params.get('code_challenge_method'), 'S256'); assert.match(params.get('code_challenge') || '', /^[A-Za-z0-9_-]{43}$/);
        assert.ok(params.get('state')); assert.ok(callbackBases.some(origin => redirect?.startsWith(origin + '/api/v1/auth/external/') && redirect.endsWith('/callback')));
        const authorization = random();
        authorizations.set(authorization, { clientID, redirect, state: params.get('state'), challenge: params.get('code_challenge') });
        res.writeHead(200, { 'Content-Type': 'text/html; charset=utf-8', 'Cache-Control': 'no-store', 'Referrer-Policy': 'no-referrer' });
        res.end(`<!doctype html><html lang="en"><title>Controlled OAuth acceptance</title><h1>Authorize test identity</h1><form action="/approve" method="post"><input type="hidden" name="authorization" value="${escape(authorization)}"><label>Username <input id="username" name="username" required></label><label>Password <input id="password" name="password" type="password" required></label><button id="oauth-approve" name="decision" value="allow">Authorize</button><button id="oauth-deny" name="decision" value="deny" formnovalidate>Deny</button></form></html>`);
        return;
      }
      if (url.pathname === '/approve' && req.method === 'POST') {
        const params = await form(req), key = params.get('authorization'), transaction = authorizations.get(key);
        assert.ok(transaction); authorizations.delete(key);
        const redirect = new URL(transaction.redirect); redirect.searchParams.set('state', transaction.state);
        if (params.get('decision') === 'deny') redirect.searchParams.set('error', 'access_denied');
        else {
          assert.ok(Object.hasOwn(subjects, params.get('username'))); assert.ok(equal(params.get('password'), password));
          const code = random(); codes.set(code, { ...transaction, username: params.get('username') }); secrets.push(code);
          redirect.searchParams.set('code', code);
        }
        res.writeHead(303, { Location: redirect.href, 'Cache-Control': 'no-store', 'Referrer-Policy': 'no-referrer' }); res.end(); return;
      }
      if (url.pathname === '/token' && req.method === 'POST') {
        const params = await form(req), code = params.get('code'), transaction = codes.get(code);
        assert.ok(transaction); codes.delete(code);
        assert.equal(params.get('grant_type'), 'authorization_code'); assert.equal(params.get('redirect_uri'), transaction.redirect);
        assert.match(params.get('code_verifier') || '', /^[A-Za-z0-9._~-]{43,128}$/); assert.equal(digest(params.get('code_verifier')), transaction.challenge);
        const method = clients.get(transaction.clientID);
        if (method === 'client_secret_basic') {
          const credentials = Buffer.from((req.headers.authorization || '').replace(/^Basic /, ''), 'base64').toString();
          assert.ok(equal(credentials, `${encodeURIComponent(transaction.clientID)}:${encodeURIComponent(clientSecret)}`));
          assert.ok(!params.has('client_secret')); assert.ok(!params.has('client_id'));
        } else {
          assert.ok(!req.headers.authorization); assert.equal(params.get('client_id'), transaction.clientID); assert.ok(equal(params.get('client_secret'), clientSecret));
        }
        const token = random(); tokens.set(token, transaction); secrets.push(token);
        events.push({ phase: 'token', method, username: transaction.username, pkce: 'S256' });
        json(res, 200, { access_token: token, token_type: 'Bearer', expires_in: 60 }); return;
      }
      if (url.pathname === '/user' && req.method === 'GET') {
        assert.equal(url.search, '');
        const token = (req.headers.authorization || '').replace(/^Bearer /, ''), transaction = tokens.get(token); assert.ok(transaction); tokens.delete(token);
        const { username } = transaction;
        const created = username === 'young' ? new Date().toISOString() : '2010-01-02T03:04:05Z';
        const profile = { login: username, name: username, email: username + '@example.invalid', email_verified: true, auth_time: Math.floor(Date.now() / 1000), ...(username === 'missing' ? {} : { created_at: created }) };
        events.push({ phase: 'profile', method: clients.get(transaction.clientID), username, subject: subjects[username] });
        res.writeHead(200, { 'Content-Type': 'application/json', 'Cache-Control': 'no-store' });
        res.end(`{"id":${subjects[username]},${JSON.stringify(profile).slice(1)}`); return;
      }
      json(res, 404, { error: 'not_found' });
    } catch {
      // Do not include assertions or request values: they may contain secrets.
      errors.push({ method: req.method, path: new URL(req.url, base).pathname });
      json(res, 400, { error: 'invalid_request' });
    }
  });
  await new Promise((res, rej) => { server.once('error', rej); server.listen(0, '127.0.0.1', res); });
  base = `http://127.0.0.1:${server.address().port}`;
  return { base, clientSecret, password, server, events, errors, subjects };
}
