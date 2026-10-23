import { randomBytes } from 'node:crypto';
import { createServer, request as httpRequest } from 'node:http';
import { createServer as netServer } from 'node:net';
import { createWriteStream } from 'node:fs';
import { access, cp, mkdir, mkdtemp, readFile, writeFile } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import { dirname, join, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';
import { spawn } from 'node:child_process';
import { prepareKeycloak, keycloakHome, run } from './prepare.mjs';
import { controlledOAuth } from './oauth2.mjs';

export const sourceDir = dirname(fileURLToPath(import.meta.url));
export const backendDir = resolve(sourceDir, '../..');
const secret = () => randomBytes(32).toString('base64url');
export async function freePort() {
  const server = netServer();
  await new Promise((res, rej) => { server.once('error', rej); server.listen(0, '127.0.0.1', res); });
  const port = server.address().port;
  await new Promise(res => server.close(res));
  return port;
}
const cleanEnvironment = () => Object.fromEntries(Object.entries(process.env).filter(([key]) => !key.startsWith('LINGUAFLOW_') && !key.startsWith('KC_')));
export async function ready(url, child, timeout = 120000) {
  const end = Date.now() + timeout;
  while (Date.now() < end) {
    if (child?.exitCode !== null && child?.exitCode !== undefined) throw new Error('Owned acceptance process exited before readiness');
    try { if ((await fetch(url, { signal: AbortSignal.timeout(1500) })).ok) return; } catch {}
    await new Promise(res => setTimeout(res, 300));
  }
  throw new Error('Acceptance readiness timed out: ' + new URL(url).pathname);
}
export async function createRuntime() {
  const root = await mkdtemp(join(tmpdir(), 'linguaflow-b3-acceptance-'));
  const children = [], servers = [], secrets = [];
  let closed = false;
  const applicationPorts = {};
  for (const mode of ['same-origin', 'cross-origin']) {
    applicationPorts[mode] = { apiPort: await freePort(), proxyPort: await freePort(), crossPort: await freePort() };
  }
  function launch(program, args, name, env = {}) {
    if (closed) throw new Error("Acceptance runtime closed before launch");
    const log = createWriteStream(join(root, name + '.log'), { flags: 'a', mode: 0o600 });
    const child = spawn(program, args, { cwd: root, env: { ...cleanEnvironment(), ...env }, windowsHide: true, stdio: ['ignore', 'pipe', 'pipe'] });
    child.stdout.pipe(log); child.stderr.pipe(log);
    child.once('error', error => log.write(String(error)));
    children.push(child);
    return child;
  }
  async function close() {
    if (closed) return;
    closed = true;
    for (const child of children.reverse()) {
      if (child.exitCode !== null || !child.pid) continue;
      if (process.platform === 'win32') {
        await run('taskkill', ['/PID', String(child.pid), '/T', '/F'], { stdio: 'ignore' }).catch(() => {});
      } else child.kill('SIGTERM');
    }
    for (const server of servers) { server.closeAllConnections(); await new Promise(res => server.close(res)); }
  }
  async function postgres() {
    const bin = process.env.LINGUAFLOW_AUTH_POSTGRES_BIN;
    if (!bin) throw new Error('Real PostgreSQL unverified: set LINGUAFLOW_AUTH_POSTGRES_BIN to portable PostgreSQL bin directory');
    const exe = name => join(bin, name + (process.platform === 'win32' ? '.exe' : ''));
    await access(exe('initdb'));
    await run(exe('postgres'), ['--version']);
    const pgRoot = join(root, 'postgres'), password = secret(), pwfile = join(root, 'pg-password');
    secrets.push(password);
    await writeFile(pwfile, password, { mode: 0o600 });
    await run(exe('initdb'), ['-D', pgRoot, '-U', 'lf_acceptance', '--pwfile', pwfile, '--auth=scram-sha-256', '--encoding=UTF8', '--locale=C'], { stdio: 'ignore', windowsHide: true });
    const port = await freePort();
    const child = launch(exe('postgres'), ['-D', pgRoot, '-h', '127.0.0.1', '-p', String(port)], 'postgres');
    for (let i = 0; i < 120; i++) {
      try { await run(exe('pg_isready'), ['-h', '127.0.0.1', '-p', String(port)], { stdio: 'ignore' }); break; }
      catch { if (i === 119 || child.exitCode !== null) throw new Error('PostgreSQL readiness failed'); await new Promise(res => setTimeout(res, 250)); }
    }
    // Some existing integration tests write fixtures in the default schema.
    // The browser application must initialize a separate, empty database.
    await run(exe('createdb'), ['-h', '127.0.0.1', '-p', String(port), '-U', 'lf_acceptance', 'linguaflow_browser'], { stdio: 'ignore', env: { ...cleanEnvironment(), PGPASSWORD: password } });
    const base = `postgres://lf_acceptance:${password}@127.0.0.1:${port}`;
    return { dsn: `${base}/postgres?sslmode=disable`, applicationDSN: `${base}/linguaflow_browser?sslmode=disable`, exe, port, password };
  }
  async function keycloak() {
    await prepareKeycloak();
    const kcRoot = join(root, 'keycloak');
    await cp(keycloakHome, kcRoot, { recursive: true, filter: path => !path.startsWith(join(keycloakHome, 'data')) });
    const port = await freePort(), issuer = `http://127.0.0.1:${port}/realms/linguaflow-b1`;
    const clientSecret = secret(), password = secret();
    secrets.push(clientSecret, password);
    const realm = {
      realm: 'linguaflow-b1', enabled: true, sslRequired: 'none', registrationAllowed: false,
      clients: ['b1-first', 'b1-second'].map(clientId => ({ clientId, enabled: true, protocol: 'openid-connect', publicClient: false,
        secret: clientSecret, standardFlowEnabled: true, directAccessGrantsEnabled: false,
        redirectUris: Object.entries(applicationPorts).map(([mode, { proxyPort }]) => `http://127.0.0.1:${proxyPort}${mode === 'cross-origin' ? '/gateway' : ''}/api/v1/auth/external/*`),
        attributes: { 'pkce.code.challenge.method': 'S256' }, defaultClientScopes: ['basic', 'profile', 'email'],
        protocolMappers: [
          { name: 'department', protocol: 'openid-connect', protocolMapper: 'oidc-usermodel-attribute-mapper', config: { 'user.attribute': 'department', 'claim.name': 'department', 'jsonType.label': 'String', 'id.token.claim': 'true', 'access.token.claim': 'true', 'userinfo.token.claim': 'true' } },
          { name: 'groups', protocol: 'openid-connect', protocolMapper: 'oidc-group-membership-mapper', config: { 'claim.name': 'groups', 'full.path': 'false', 'id.token.claim': 'true', 'access.token.claim': 'true', 'userinfo.token.claim': 'true' } }
        ] })),
      groups: [{ name: 'engineering' }, { name: 'ops' }],
      users: ['provideradmin', 'alice', 'bob', 'missing'].map(username => ({ username, enabled: true, email: `${username}@example.invalid`, emailVerified: true,
        ...(username === 'missing' ? {} : { attributes: { department: [username === 'alice' ? 'engineering' : 'sales'] } }),
        groups: username === 'alice' ? ['/engineering', '/ops'] : [],
        firstName: username, lastName: 'Acceptance', credentials: [{ type: 'password', value: password, temporary: false }] }))
    };
    await mkdir(join(kcRoot, 'data/import'), { recursive: true });
    await writeFile(join(kcRoot, 'data/import/realm.json'), JSON.stringify(realm), { mode: 0o600 });
    let child;
    if (process.platform === 'win32') {
      child = launch('powershell', ['-NoProfile', '-NonInteractive', '-Command', '& $env:LF_AUTH_KC start-dev --http-host=127.0.0.1 --http-port=$env:LF_AUTH_KC_PORT --import-realm --hostname-strict=false --http-management-port=$env:LF_AUTH_KC_MANAGEMENT_PORT'], 'keycloak', {
        LF_AUTH_KC: join(kcRoot, 'bin/kc.bat'), LF_AUTH_KC_PORT: String(port), LF_AUTH_KC_MANAGEMENT_PORT: String(await freePort())
      });
    } else child = launch(join(kcRoot, 'bin/kc.sh'), ['start-dev', '--http-host=127.0.0.1', `--http-port=${port}`, '--import-realm', '--hostname-strict=false', `--http-management-port=${await freePort()}`], 'keycloak');
    await ready(issuer + '/.well-known/openid-configuration', child, 180000);
    return { issuer, clientSecret, password };
  }
  async function oauth2() {
    const provider = await controlledOAuth({ callbackBases: Object.entries(applicationPorts).map(([mode, { proxyPort }]) => `http://127.0.0.1:${proxyPort}${mode === 'cross-origin' ? '/gateway' : ''}`), secrets });
    servers.push(provider.server);
    return provider;
  }
  async function application(mode, dsn = '', options = {}) {
    const { apiPort, proxyPort, crossPort } = applicationPorts[mode];
    const apiPrefix = mode === 'cross-origin' ? '/gateway' : '';
    const publicBase = `http://127.0.0.1:${proxyPort}${apiPrefix}`, crossBase = `http://127.0.0.1:${crossPort}`;
    const completionBase = mode === 'same-origin' ? publicBase : crossBase;
    const directory = join(root, mode); await mkdir(directory);
    const adminPassword = secret(); secrets.push(adminPassword);
    const serverConfig = { kind: 'server', version: 1, server: {
      host: '127.0.0.1', port: apiPort, data_dir: directory, serve_ui: false, auto_migrate: true,
      jwt_secret: secret(), credentials: { master_key: randomBytes(32).toString('base64') },
      storage: { initialization: { capacity_bytes: 1073741824, logical_limit_bytes: 1073741824 } },
      ...(dsn ? { database: { driver: 'postgres', dsn } } : {}),
      authentication: { public_base_url: publicBase, completion_urls: [completionBase + '/complete'], allowed_cidrs: options.publicOAuth ? [] : ['127.0.0.1/32'], allow_http_loopback: true, rate_limit_per_minute: 1000 },
      cors: { allowed_origins: mode === 'same-origin' ? [] : [crossBase] }
    }, bootstrap: { registration_enabled: true, admin: { username: 'admin', password: adminPassword } } };
    const configPath = join(directory, 'server.json');
    await writeFile(configPath, JSON.stringify(serverConfig), { mode: 0o600 });
    const binary = resolve(process.env.LINGUAFLOW_AUTH_BINARY || join(backendDir, 'bin', 'linguaflow' + (process.platform === 'win32' ? '.exe' : '')));
    const child = launch(binary, ['serve', '--config', configPath], mode);
    const fixture = (await readFile(join(sourceDir, 'fixture.html'), 'utf8')).replace('__API_BASE__', JSON.stringify(publicBase));
    function handler(proxy) { return (req, res) => {
      if (proxy && req.url.startsWith(apiPrefix + '/api/')) {
        const upstream = httpRequest({ host: '127.0.0.1', port: apiPort, method: req.method, path: req.url.slice(apiPrefix.length), headers: req.headers }, answer => { res.writeHead(answer.statusCode, answer.headers); answer.pipe(res); });
        upstream.on('error', () => { res.writeHead(502); res.end(); }); req.pipe(upstream); return;
      }
      res.writeHead(200, { 'Content-Type': 'text/html; charset=utf-8', 'Cache-Control': 'no-store', 'Referrer-Policy': 'no-referrer' }); res.end(fixture);
    }; }
    for (const [port, proxy] of [[proxyPort, true], [crossPort, false]]) {
      const server = createServer(handler(proxy)); servers.push(server);
      await new Promise((res, rej) => { server.once('error', rej); server.listen(port, '127.0.0.1', res); });
    }
    await ready(`http://127.0.0.1:${apiPort}/health/ready`, child);
    return { publicBase, completionBase, adminPassword, directory };
  }
  return { root, secrets, postgres, keycloak, oauth2, application, close };
}
