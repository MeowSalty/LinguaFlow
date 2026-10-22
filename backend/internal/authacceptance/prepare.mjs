import { createHash } from 'node:crypto';
import { createReadStream, createWriteStream } from 'node:fs';
import { access, mkdir, rename, unlink } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import { dirname, join, resolve } from 'node:path';
import { pipeline } from 'node:stream/promises';
import { Readable } from 'node:stream';
import { spawn } from 'node:child_process';

export const keycloakVersion = '26.8.0';
export const keycloakSHA256 = '7ed1de3fda2598369262613bf682aab7e233d80a38c405e91588f7a7454370a1';
export const runtimeRoot = resolve(process.env.LINGUAFLOW_AUTH_RUNTIME_DIR || join(tmpdir(), 'linguaflow-auth-b1-runtime'));
export const keycloakHome = join(runtimeRoot, `keycloak-${keycloakVersion}`);

async function exists(path) { try { await access(path); return true; } catch { return false; } }
async function digest(path) { const hash = createHash('sha256'); for await (const part of createReadStream(path)) hash.update(part); return hash.digest('hex'); }
export async function run(program, args, options = {}) {
  if (program === 'task' && process.platform === 'win32') {
    options = { ...options, env: { ...(options.env || process.env), LF_AUTH_TASK_ARGUMENTS: JSON.stringify(args) } };
    program = 'powershell';
    args = ['-NoProfile', '-NonInteractive', '-Command', '$taskArguments = ConvertFrom-Json $env:LF_AUTH_TASK_ARGUMENTS; & task @taskArguments; exit $LASTEXITCODE'];
  }
  return new Promise((resolvePromise, reject) => {
    const child = spawn(program, args, { stdio: 'inherit', windowsHide: true, ...options });
    child.once('error', reject);
    child.once('exit', code => code === 0 ? resolvePromise() : reject(new Error(`${program} exited with ${code}`)));
  });
}

export async function prepareKeycloak() {
  await mkdir(runtimeRoot, { recursive: true });
  const archive = join(runtimeRoot, `keycloak-${keycloakVersion}.zip`);
  if (!await exists(archive)) {
    const pending = `${archive}.${process.pid}.partial`;
    try {
      const response = await fetch(`https://github.com/keycloak/keycloak/releases/download/${keycloakVersion}/keycloak-${keycloakVersion}.zip`);
      if (!response.ok || !response.body) throw new Error('Keycloak distribution download failed');
      await pipeline(Readable.fromWeb(response.body), createWriteStream(pending, { flags: 'wx' }));
      if (await digest(pending) !== keycloakSHA256) throw new Error('Keycloak SHA-256 verification failed');
      await rename(pending, archive);
    } catch (error) { await unlink(pending).catch(() => {}); throw error; }
  }
  if (await digest(archive) !== keycloakSHA256) throw new Error('Cached Keycloak SHA-256 verification failed');
  const launcher = join(keycloakHome, 'bin', process.platform === 'win32' ? 'kc.bat' : 'kc.sh');
  if (!await exists(launcher)) {
    if (process.platform === 'win32') {
      await run('powershell', ['-NoProfile', '-NonInteractive', '-Command', 'Expand-Archive -LiteralPath $env:LF_AUTH_ARCHIVE -DestinationPath $env:LF_AUTH_RUNTIME'], {
        env: { ...process.env, LF_AUTH_ARCHIVE: archive, LF_AUTH_RUNTIME: runtimeRoot },
      });
    } else { await run('unzip', ['-q', archive, '-d', runtimeRoot]); }
  }
  return launcher;
}

if (process.argv[1] && resolve(process.argv[1]) === resolve(new URL(import.meta.url).pathname.replace(/^\/([A-Za-z]:)/, '$1'))) {
  await prepareKeycloak();
  console.log(`Verified Keycloak ${keycloakVersion}: ${keycloakHome}`);
}
