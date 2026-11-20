import { spawn } from 'node:child_process';
import { createHash } from 'node:crypto';
import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { epubLeaves, epubTop, requiredTests, skipRules, workerPackage } from './ci-test-contract.mjs';
import { terminateOwnedProcess } from './process-cleanup.mjs';
import { performancePolicy, performanceRounds, sceneFailure, sceneSeconds } from './performance-policy.mjs';
import { ResourceScheduler } from './resource-scheduler.mjs';

const scriptPath = fileURLToPath(import.meta.url);
const defaultRoot = path.resolve(path.dirname(scriptPath), '../../..');
const signedCountMax = (1n << 63n) - 1n;
const defaultPackageParallelism = 'auto';
const key = (pkg, test) => `${pkg}::${test}`;
const sorted = values => [...values].sort();
const sha256 = value => createHash('sha256').update(value).digest('hex');
const writeJSON = (file, data) => fs.writeFileSync(file, `${JSON.stringify(data, null, 2)}\n`);

export function resolveTaskBinary(env = process.env, platform = process.platform) {
  const valid = candidate => {
    if (!candidate || !fs.existsSync(candidate) || !fs.statSync(candidate).isFile()) return false;
    const bytes = Buffer.alloc(4);
    const descriptor = fs.openSync(candidate, 'r');
    try { fs.readSync(descriptor, bytes, 0, 4, 0); } finally { fs.closeSync(descriptor); }
    if (platform === 'win32') {
      return path.extname(candidate).toLowerCase() === '.exe' && bytes[0] === 0x4d && bytes[1] === 0x5a;
    }
    // An executable pnpm shell shim is still not the installed native binary.
    const magic = bytes.readUInt32BE();
    if (platform === 'linux' && magic !== 0x7f454c46) return false;
    if (platform === 'darwin' && ![0xfeedface, 0xfeedfacf, 0xcefaedfe, 0xcffaedfe, 0xcafebabe, 0xbebafeca].includes(magic)) return false;
    try { fs.accessSync(candidate, fs.constants.X_OK); return true; } catch { return false; }
  };
  if (env.TASK_BINARY) {
    if (!path.isAbsolute(env.TASK_BINARY) || !valid(env.TASK_BINARY)) throw new Error('TASK_BINARY must identify an existing absolute native Task executable');
    return fs.realpathSync(env.TASK_BINARY);
  }
  const directories = (env.PATH ?? env.Path ?? '').split(platform === 'win32' ? ';' : ':').filter(Boolean);
  for (const directory of directories) {
    const candidate = path.join(directory, platform === 'win32' ? 'task.exe' : 'task');
    if (valid(candidate)) return fs.realpathSync(candidate);
  }
  if (platform === 'win32') {
    for (const directory of directories) {
      const shim = path.join(directory, 'task.ps1');
      if (!fs.existsSync(shim)) continue;
      // Read the installed shim only. Running its JS wrapper can download Task.
      const reference = fs.readFileSync(shim, 'utf8').match(/\$basedir\/([^'"\r\n]*@go-task[^'"\r\n]*\/run-task\.js)/);
      if (!reference) continue;
      const candidate = path.join(directory, path.dirname(reference[1]), 'bin/task.exe');
      if (valid(candidate)) return fs.realpathSync(candidate);
    }
  }
  throw new Error('Native Task executable unavailable; set TASK_BINARY to the installed executable (no automatic download)');
}

export async function runProcess(binary, args, { cwd, env, outputFile, signal, onSpawn } = {}) {
  const output = outputFile ? fs.createWriteStream(outputFile, { flags: 'wx' }) : null;
  const splitJSON = outputFile && path.basename(outputFile) === 'test-results.out';
  const stdoutOutput = splitJSON ? fs.createWriteStream(path.join(path.dirname(outputFile), 'test-events.jsonl'), { flags: 'wx' }) : null;
  const stderrOutput = splitJSON ? fs.createWriteStream(path.join(path.dirname(outputFile), 'task-stderr.out'), { flags: 'wx' }) : null;
  const streams = [output, stdoutOutput, stderrOutput].filter(Boolean);
  return new Promise((resolve, reject) => {
    let stdout = '';
    let stderr = '';
    let ioError;
    let finished = false;
    let exitedAt;
    let termination;
    let cancelled = false;
    let requestedSignal;
    const startedAt = Date.now();
    const child = spawn(binary, args, { cwd, env, shell: false, windowsHide: true, detached: process.platform !== 'win32' });
    child.once('spawn', () => { try { onSpawn?.({ pid: child.pid, startedAt }); } catch (error) { ioError = error; onSignal('ABORT'); } });
    child.stdin.end();
    child.stdout.setEncoding('utf8');
    child.stderr.setEncoding('utf8');
    const beginTermination = () => {
      if (!child.pid || termination) return;
      termination = terminateOwnedProcess(child.pid, startedAt, exitedAt);
      // A failed cleanup must be reported promptly, not hidden by waiting for
      // the direct child's stdio while an unconfirmed descendant remains.
      termination.catch(error => {
        child.stdout.destroy();
        child.stderr.destroy();
        child.unref();
        finish(() => reject(error));
      });
    };
    const onSignal = requested => {
      cancelled = true;
      requestedSignal = requested;
      beginTermination();
    };
    const onTerm = () => onSignal('SIGTERM');
    const onInt = () => onSignal('SIGINT');
    const onAbort = () => onSignal('ABORT');
    process.once('SIGTERM', onTerm);
    process.once('SIGINT', onInt);
    child.stdout.on('data', chunk => { stdout += chunk; output?.write(chunk); stdoutOutput?.write(chunk); });
    child.stderr.on('data', chunk => { stderr += chunk; output?.write(chunk); stderrOutput?.write(chunk); });
    const finish = callback => {
      if (finished) return;
      finished = true;
      process.removeListener('SIGTERM', onTerm);
      process.removeListener('SIGINT', onInt);
      signal?.removeEventListener('abort', onAbort);
      let remaining = streams.length;
      if (!remaining) callback();
      for (const stream of streams) {
        if (stream.destroyed) { if (--remaining === 0) callback(); }
        else stream.end(() => { if (--remaining === 0) callback(); });
      }
    };
    child.on('error', error => finish(() => reject(error)));
    child.on('exit', () => {
      exitedAt = Date.now();
      // Inherited descendant stdio can keep close pending after the parent
      // exits. Start cleanup at exit so those descendants cannot hide it.
      beginTermination();
    });
    child.on('close', async (code, childSignal) => {
      if (finished) return;
      try {
        // Stdio/direct-child close alone says nothing about grandchildren.
        termination ??= child.pid ? terminateOwnedProcess(child.pid, startedAt, exitedAt) : Promise.resolve({ confirmedGone: true, spawnFailed: true });
        const cleanup = await termination;
        const orphaned = !cancelled && (cleanup.platform === 'win32' ? cleanup.tracked?.length > 0 : !cleanup.alreadyGone);
        if (orphaned) stderr += '\nOwned descendants survived direct-child exit and required cleanup.\n';
        finish(() => ioError ? reject(Object.assign(ioError, { cleanup })) : resolve({ code: cancelled || orphaned ? 1 : code ?? 1, signal: childSignal ?? requestedSignal, stdout, stderr, cleanup }));
      } catch (error) { finish(() => reject(error)); }
    });
    for (const stream of streams) stream.on('error', error => { ioError = error; onSignal('SIGTERM'); });
    signal?.addEventListener('abort', onAbort, { once: true });
    if (signal?.aborted) onAbort();
  });
}

export function parseJSONObjects(text) {
  const objects = [];
  let depth = 0;
  let start = 0;
  let string = false;
  let escaped = false;
  for (let i = 0; i < text.length; i++) {
    const c = text[i];
    if (string) { if (escaped) escaped = false; else if (c === '\\') escaped = true; else if (c === '"') string = false; continue; }
    if (c === '"') string = true;
    else if (c === '{') { if (depth++ === 0) start = i; }
    else if (c === '}') { if (--depth < 0) throw new Error('Unexpected JSON closing brace'); if (depth === 0) objects.push(JSON.parse(text.slice(start, i + 1))); }
    else if (depth === 0 && !/\s/.test(c)) throw new Error('Unexpected non-JSON inventory output');
  }
  if (depth || string) throw new Error('Truncated JSON inventory');
  return objects;
}

export function parseTestEvents(text) {
  const tests = new Map();
  const packages = new Map();
  const issues = [];
  for (const line of text.split(/\r?\n/)) {
    if (!line.trim()) continue;
    if (!line.startsWith('{')) { issues.push('Non-JSON output on go test stdout'); continue; }
    let event;
    try { event = JSON.parse(line); } catch { issues.push('Malformed go test JSON'); continue; }
    if (event.Action?.startsWith('build-')) {
      if (event.Action === 'build-fail') issues.push(`Build failed: ${event.ImportPath ?? event.Package ?? 'unknown'}`);
      continue;
    }
    if (!event.Action || !event.Package) { issues.push('Missing Action/Package in go test JSON'); continue; }
    if (!packages.has(event.Package)) packages.set(event.Package, { name: event.Package, pass: 0, fail: 0, skip: 0, started: 0 });
    const pkg = packages.get(event.Package);
    if (event.Action === 'build-fail' || event.FailedBuild || event.BuildImportPath) issues.push(`Build failed: ${event.Package}`);
    if (!event.Test) {
      if (event.Action === 'start') pkg.started++;
      if (['pass', 'fail', 'skip'].includes(event.Action)) pkg[event.Action]++;
      continue;
    }
    const id = key(event.Package, event.Test);
    if (!tests.has(id)) tests.set(id, { package: event.Package, test: event.Test, run: 0, pass: 0, fail: 0, skip: 0, output: '' });
    const test = tests.get(id);
    if (['run', 'pass', 'fail', 'skip'].includes(event.Action)) test[event.Action]++;
    if (typeof event.Elapsed === 'number') test.elapsedSeconds = event.Elapsed;
    if (event.Action === 'output') test.output += event.Output ?? '';
  }
  const parents = new Set();
  for (const test of tests.values()) {
    const parts = test.test.split('/');
    while (parts.length > 1) { parts.pop(); parents.add(key(test.package, parts.join('/'))); }
  }
  for (const [id, test] of tests) test.terminal = !parents.has(id);
  return { tests, packages, issues };
}

export function parseInventory(listResult, packageRecords) {
  const packages = new Map();
  const terminalPackages = new Set();
  for (const record of packageRecords) {
    if (!record.ImportPath || record.Error || record.Incomplete) throw new Error(`Package discovery failed: ${record.ImportPath ?? 'unknown'}`);
    if (packages.has(record.ImportPath)) throw new Error(`Duplicate package discovery: ${record.ImportPath}`);
    packages.set(record.ImportPath, { name: record.ImportPath, tops: [], noTestFiles: !(record.TestGoFiles?.length || record.XTestGoFiles?.length) });
  }
  for (const line of listResult.split(/\r?\n/)) {
    if (!line.trim()) continue;
    const event = JSON.parse(line);
    if (event.Action === 'build-output') continue;
    if (event.Action === 'build-fail') throw new Error(`Test inventory build failed: ${event.ImportPath ?? event.Package}`);
    const pkg = packages.get(event.Package);
    if (!pkg) throw new Error(`Undiscovered package in test inventory: ${event.Package}`);
    if (event.Action === 'fail' || event.Action === 'build-fail') throw new Error(`Test inventory failed: ${event.Package}`);
    if (event.Action === 'pass' || event.Action === 'skip') {
      if (terminalPackages.has(event.Package)) throw new Error(`Repeated inventory package result: ${event.Package}`);
      terminalPackages.add(event.Package);
    }
    if (event.Output) for (const name of event.Output.trim().split(/\r?\n/)) {
      if (/^(Test\w+|Example\w*|Fuzz\w+)$/.test(name)) pkg.tops.push(name);
    }
  }
  for (const pkg of packages.values()) {
    if (!terminalPackages.has(pkg.name)) throw new Error(`Missing inventory package result: ${pkg.name}`);
    if (new Set(pkg.tops).size !== pkg.tops.length) throw new Error(`Duplicate top-level test in inventory: ${pkg.name}`);
    pkg.tops.sort();
  }
  if (!packages.size || ![...packages.values()].some(pkg => pkg.tops.length)) throw new Error('Test inventory has zero executable tests');
  return packages;
}

function permittedSkip(test, env, rules) {
  return rules.find(rule => rule.package === test.package && (rule.test === test.test || (rule.descendants && test.test.startsWith(`${rule.test}/`))) && test.output.includes(rule.reason) &&
    (rule.condition === 'always' || (rule.condition === 'postgres-off' && !env.LINGUAFLOW_TEST_POSTGRES_DSN) || (rule.condition === 'capacity-off' && env.LINGUAFLOW_CAPACITY_TESTS !== '1') || (rule.condition === 'storage-acceptance-off' && env.LINGUAFLOW_STORAGE_ACCEPTANCE !== '1') || (rule.condition === 'windows' && env.CI_GOOS === 'windows')));
}

export function validateShard(result, expectedPackages, expectedTops, { env = {}, rules = skipRules } = {}) {
  const parsed = parseTestEvents(result.stdout);
  const issues = [...parsed.issues];
  const skips = [];
  if (result.code !== 0) issues.push(`Task exited ${result.code}${result.signal ? ` (${result.signal})` : ''}`);
  if (/\[build failed\]|^FAIL\s+.*build failed/m.test(result.stderr)) issues.push('Build failure diagnostic');
  for (const [name, pkg] of expectedPackages) {
    const actual = parsed.packages.get(name);
    if (!actual) { issues.push(`Missing package result: ${name}`); continue; }
    if (actual.fail || (pkg.tops.length ? actual.pass !== 1 || actual.skip !== 0 : actual.pass + actual.skip !== 1)) issues.push(`Unsuccessful package result: ${name}`);
  }
  for (const name of parsed.packages.keys()) if (!expectedPackages.has(name)) issues.push(`Unexpected package: ${name}`);
  const actualTops = new Set([...parsed.tests.values()].filter(test => !test.test.includes('/')).map(test => key(test.package, test.test)));
  for (const top of expectedTops) if (!actualTops.has(top)) issues.push(`Missing top-level test: ${top}`);
  for (const top of actualTops) if (!expectedTops.has(top)) issues.push(`Unexpected top-level test: ${top}`);
  for (const test of parsed.tests.values()) {
    if (test.run !== 1 || test.pass + test.skip + test.fail !== 1 || test.fail) issues.push(`Unfinished/repeated/failed test: ${key(test.package, test.test)}`);
    if (test.skip) {
      const rule = permittedSkip(test, env, rules);
      if (!rule) issues.push(`Unexpected skip: ${key(test.package, test.test)}`);
      else skips.push({ name: key(test.package, test.test), reason: rule.reason, verification: 'unverified' });
    }
  }
  const terminal = [...parsed.tests.values()].filter(test => test.terminal);
  if ([...expectedPackages.values()].some(pkg => pkg.tops.length) && !terminal.some(test => test.pass === 1)) issues.push('Zero passing terminal leaves');
  return { issues, skips, tops: sorted(actualTops), leaves: terminal.map(test => ({ name: key(test.package, test.test), pass: test.pass, skip: test.skip, elapsedSeconds: test.elapsedSeconds })), packages: [...parsed.packages.values()] };
}

export function validateRound(shards, inventory, required = requiredTests) {
  const issues = [];
  const leaves = new Map();
  const tops = new Map();
  for (const shard of shards) {
    issues.push(...shard.validation.issues.map(issue => `${shard.id}: ${issue}`));
    for (const leaf of shard.validation.leaves) {
      if (leaves.has(leaf.name)) issues.push(`Terminal leaf executes in multiple shards: ${leaf.name}`);
      leaves.set(leaf.name, leaf);
    }
    for (const top of shard.validation.tops) tops.set(top, (tops.get(top) ?? 0) + 1);
  }
  for (const pkg of inventory.values()) for (const top of pkg.tops) {
    const id = key(pkg.name, top);
    const expected = pkg.name === workerPackage && top === epubTop ? 2 : 1;
    if (tops.get(id) !== expected) issues.push(`Top-level coverage mismatch: ${id} expected ${expected}`);
  }
  for (const [pkg, names] of Object.entries(required)) for (const name of names) {
    // A named parent matrix must pass, but is not mislabelled as a terminal leaf.
    const id = key(pkg, name);
    const leaf = leaves.get(id);
    const present = name.includes('/') ? leaf?.pass === 1 && !leaf.skip : shards.some(shard => shard.validation.tops.includes(id) && !shard.validation.issues.length && !shard.validation.skips.some(skip => skip.name === id || skip.name.startsWith(`${id}/`)));
    if (!present) issues.push(`Missing/failed/skipped required test: ${id}`);
  }
  return { issues, leaves: sorted(leaves.keys()), skips: shards.flatMap(shard => shard.validation.skips) };
}

export function validAtomicRange([sl, sc, el, ec], numStmt) {
  // Go emits zero-width counters for empty (zero-statement) source blocks.
  return sl < el || (sl === el && (sc < ec || (sc === ec && numStmt === 0n)));
}

export function mergeAtomicProfiles(profiles, outputFile) {
  if (!profiles.length) throw new Error('No atomic profiles to merge');
  const sums = new Map();
  const structures = new Map();
  const packageStructures = new Map();
  for (const profile of profiles) {
    if (!Array.isArray(profile.packages) || !profile.packages.length || new Set(profile.packages).size !== profile.packages.length) throw new Error(`Missing/invalid profile package identity: ${profile.id}`);
    if (!fs.existsSync(profile.path)) throw new Error(`Missing profile: ${profile.id}`);
    const lines = fs.readFileSync(profile.path, 'utf8').trim().split(/\r?\n/);
    if (lines.shift() !== 'mode: atomic') throw new Error(`Wrong coverage mode: ${profile.id}`);
    if (!lines.length) throw new Error(`Empty coverage profile: ${profile.id}`);
    const blocks = new Map();
    const sourceFiles = new Map();
    const packageFiles = new Map(profile.packages.map(pkg => [pkg, []]));
    for (const line of lines) {
      const match = line.match(/^(.*):(\d+)\.(\d+),(\d+)\.(\d+) (\d+) (\d+)$/);
      if (!match) throw new Error(`Malformed coverage block: ${profile.id}`);
      const [, file, startLine, startCol, endLine, endCol, numStmt, value] = match;
      const count = BigInt(value);
      const coordinates = [startLine, startCol, endLine, endCol].map(BigInt);
      if (count > signedCountMax || coordinates.some(number => number <= 0n || number > signedCountMax) || !validAtomicRange(coordinates, BigInt(numStmt)) || BigInt(numStmt) > signedCountMax) throw new Error(`Invalid/overflowing coverage block: ${profile.id}`);
      const location = `${file}:${startLine}.${startCol},${endLine}.${endCol}`;
      const block = `${location} ${numStmt}`;
      if (blocks.has(location)) throw new Error(`Duplicate coverage block: ${profile.id}`);
      blocks.set(location, numStmt);
      if (!sourceFiles.has(file)) sourceFiles.set(file, []);
      sourceFiles.get(file).push(block);
      const owners = profile.packages.filter(pkg => file.startsWith(`${pkg}/`)).sort((a, b) => b.length - a.length);
      const owner = owners[0] ?? (profile.packages.length === 1 ? profile.packages[0] : null);
      if (!owner) throw new Error(`Cannot attribute coverage source to declared packages: ${file}`);
      packageFiles.get(owner).push(block);
      const sum = (sums.get(block) ?? 0n) + count;
      if (sum > signedCountMax) throw new Error(`Coverage count overflow: ${block}`);
      sums.set(block, sum);
    }
    for (const [file, entries] of sourceFiles) {
      // Function literals can legitimately overlap their enclosing source range.
      // Compare the entire file's partition across profiles instead of rejecting
      // overlap inside a compiler-generated profile.
      const structure = entries.sort().join('\n');
      if (structures.has(file) && structures.get(file) !== structure) throw new Error(`Inconsistent source blocks/numStmt: ${file}`);
      structures.set(file, structure);
    }
    for (const [pkg, entries] of packageFiles) {
      const structure = entries.sort().join('\n');
      if (packageStructures.has(pkg) && packageStructures.get(pkg) !== structure) throw new Error(`Incomplete/inconsistent package file+block set: ${pkg}`);
      packageStructures.set(pkg, structure);
    }
  }
  fs.writeFileSync(outputFile, `mode: atomic\n${sorted(sums.keys()).map(block => `${block} ${sums.get(block)}`).join('\n')}\n`, { flag: 'wx' });
  return { profiles: profiles.map(profile => profile.id), blocks: sums.size, sha256: sha256(fs.readFileSync(outputFile)) };
}

export function resolvePackageParallelism(value = defaultPackageParallelism, availableParallelism = os.availableParallelism, factor = 2) {
  if (value === 'auto') {
    if (!Number.isFinite(factor) || factor <= 0) throw new Error('PACKAGE_PARALLELISM_FACTOR must be a positive finite number');
    const cpus = availableParallelism();
    if (!Number.isSafeInteger(cpus) || cpus < 1) throw new Error('Available CPU count must be a positive safe integer');
    value = Math.ceil(cpus * factor);
  }
  if (!Number.isSafeInteger(value) || value < 1) throw new Error('PACKAGE_PARALLELISM must be auto or a positive safe integer');
  return value;
}

export function loadCPUReserve(root, env = {}) {
  const explicit = env.LINGUAFLOW_CI_CPU_RESERVE;
  if (explicit != null && explicit !== '') return { value: explicit, source: 'environment' };
  const file = path.join(root, 'backend/.ci-resources.local.json');
  if (!fs.existsSync(file)) return { source: 'host-default' };
  const config = JSON.parse(fs.readFileSync(file, 'utf8'));
  if (!config || !Number.isFinite(config.cpuReserve) || config.cpuReserve < 0) throw new Error('Local CI cpuReserve must be a nonnegative number');
  return { value: String(config.cpuReserve), source: 'workspace-local-file', file };
}

export function planShards(mode, inventory, packageParallelism = defaultPackageParallelism, rounds = performancePolicy.defaultRounds) {
  if (!['plain', 'race', 'race-atomic'].includes(mode)) throw new Error(`Unsupported CI mode: ${mode}`);
  packageParallelism = resolvePackageParallelism(packageParallelism);
  const worker = inventory.get(workerPackage);
  if (!worker?.tops.includes(epubTop)) throw new Error('Worker EPUB test missing from inventory');
  const others = [...inventory.keys()].filter(name => name !== workerPackage);
  const shards = [];
  if (others.length) shards.push({ id: 'packages', round: 1, effectiveP: packageParallelism, packages: others, timeout: '20m', extra: [], tops: others.flatMap(name => inventory.get(name).tops.map(top => key(name, top))) });
  shards.push({ id: 'worker-remainder', round: 1, effectiveP: 1, packages: [workerPackage], timeout: '20m', extra: ['-skip', `^${epubTop}$`], tops: worker.tops.filter(top => top !== epubTop).map(top => key(workerPackage, top)) });
  rounds = performanceRounds(rounds);
  for (let round = 1; round <= (mode === 'plain' ? 1 : rounds); round++) {
    epubLeaves.forEach((leaf, i) => shards.push({ id: `${i === 0 ? 'dense' : 'synthetic'}-r${round}`, round, effectiveP: 1, packages: [workerPackage], timeout: '15m', extra: ['-run', `^${leaf.split('/')[0]}$/^${leaf.split('/')[1]}$`], tops: [key(workerPackage, epubTop)], requiredLeaf: key(workerPackage, leaf) }));
  }
  return shards;
}

export async function sourceIdentity(root, outputDir, execute = runProcess) {
  const git = async args => {
    const result = await execute('git', args, { cwd: root, env: process.env });
    if (result.code) throw new Error(`Cannot freeze source: git ${args[0]} failed`);
    return result.stdout;
  };
  const head = (await git(['rev-parse', 'HEAD'])).trim();
  const paths = ['backend', 'api', 'Taskfile.yml', '.github/workflows/ci-backend.yml', '.github/workflows/release.yml'];
  const files = new Set((await git(['ls-files', '-z', '--cached', '--others', '--exclude-standard', '--', ...paths])).split('\0').filter(Boolean));
  const hash = createHash('sha256');
  for (const relative of sorted(files)) {
    const absolute = path.resolve(root, relative);
    if (absolute === outputDir || absolute.startsWith(`${outputDir}${path.sep}`)) continue;
    hash.update(`${relative}\0`);
    if (fs.existsSync(absolute) && fs.statSync(absolute).isFile()) hash.update(fs.readFileSync(absolute)); else hash.update('[deleted]');
    hash.update('\0');
  }
  return { head, sourceSha256: hash.digest('hex') };
}

export async function runCI({ mode, outputDir, packageParallelism = defaultPackageParallelism, packageParallelismFactor = 2, availableParallelism = os.availableParallelism, root = defaultRoot, env = process.env, execute = runProcess, identity = sourceIdentity, contract = requiredTests, rules = skipRules, taskBinary, resourceOptions = {} } = {}) {
  const backend = path.join(root, 'backend');
  outputDir = path.resolve(backend, outputDir ?? `.ci-test/${Date.now()}-${mode}`);
  if (outputDir === backend || !outputDir.startsWith(`${backend}${path.sep}`)) throw new Error('OUTPUT_DIR must be a new directory inside backend');
  if (/["`$\r\n]/.test(outputDir)) throw new Error('OUTPUT_DIR contains unsupported shell substitution characters');
  fs.mkdirSync(path.dirname(outputDir), { recursive: true });
  fs.mkdirSync(outputDir);
  const summaryPath = path.join(outputDir, 'summary.json');
  const summary = { status: 'running', mode, startedAt: new Date().toISOString(), outputDir, issues: [], shards: [], budgetCalibration: 'candidate workflow step=90m job=120m; calibration pending final same-source full-mode worst wall / 0.75 and actual Ubuntu whole-job wall', performanceTargets: performancePolicy, integrityScope: 'complete package/top inventory; exhaustive split EPUB leaves; additional named critical matrix; no claim of a pre-enumerated global dynamic-leaf inventory' };
  writeJSON(summaryPath, summary);
  const requestedProcs = Number(env.GOMAXPROCS);
  const suppliedGoFlags = (env.GOFLAGS ?? '').trim();
  // Explicit -p on inventory/shard commands overrides GOFLAGS. Keep this
  // fallback at 1 for nested Go commands launched by test fixtures.
  const childGoFlags = /(?:^|\s)-p=1$/.test(suppliedGoFlags) ? suppliedGoFlags : `${suppliedGoFlags} -p=1`.trim();
  const runtimeEnv = { ...env, GOFLAGS: childGoFlags, GOTOOLCHAIN: 'local', GOMAXPROCS: String(Number.isInteger(requestedProcs) && requestedProcs > 0 ? Math.min(requestedProcs, 4) : 4) };
  if (mode !== 'plain') runtimeEnv.CGO_ENABLED = '1';
  let frozen;
  let scheduler;
  let cancelled = false;
  const cancellation = new AbortController();
  const cancel = () => { cancelled = true; cancellation.abort(); };
  process.once('SIGTERM', cancel);
  process.once('SIGINT', cancel);
  const task = async (name, cliArgs, outputFile, budget = {}) => execute(taskBinary, [name, ...(cliArgs ? [`CLI_ARGS=${cliArgs}`] : [])], { cwd: root, env: { ...runtimeEnv, ...(budget.gomaxprocs ? { GOMAXPROCS: String(budget.gomaxprocs) } : {}), ...(budget.persistenceReport ? { LINGUAFLOW_PERSISTENCE_REPORT: budget.persistenceReport } : {}) }, outputFile, signal: budget.signal ?? cancellation.signal, onSpawn: budget.onSpawn });
  try {
    if (!['plain', 'race', 'race-atomic'].includes(mode)) throw new Error(`Unsupported CI mode: ${mode}`);
    const requestedPackageParallelism = packageParallelism;
    const availableCPUCount = packageParallelism === 'auto' ? availableParallelism() : null;
    packageParallelism = resolvePackageParallelism(packageParallelism, () => availableCPUCount, packageParallelismFactor);
    const requestedRounds = performanceRounds(runtimeEnv.LINGUAFLOW_CI_PERFORMANCE_ROUNDS);
    const rounds = mode === 'plain' ? 1 : requestedRounds;
    taskBinary ??= resolveTaskBinary(runtimeEnv);
    const localCPU = loadCPUReserve(root, runtimeEnv);
    if (localCPU.value != null) runtimeEnv.LINGUAFLOW_CI_CPU_RESERVE = localCPU.value;
    const resourcesFile = path.join(outputDir, 'resources.jsonl');
    scheduler = new ResourceScheduler({ env: runtimeEnv, signal: cancellation.signal, emit: event => fs.appendFileSync(resourcesFile, `${JSON.stringify(event)}\n`), ...resourceOptions });
    scheduler.policy.cpuReserveSource = localCPU.source;
    await scheduler.initialize();
    summary.resources = { policy: scheduler.policy, cpuPool: scheduler.pool, samples: resourcesFile, requestedPackageParallelism: packageParallelism };
    const taskVersion = await execute(taskBinary, ['--version'], { cwd: root, env: runtimeEnv, outputFile: path.join(outputDir, 'task-version.out') });
    if (taskVersion.code) throw new Error('Native Task version query failed');
    frozen = await identity(root, outputDir, execute);
    summary.identity = frozen;
    summary.runnerSHA256 = sha256(fs.readFileSync(scriptPath));
    summary.contractSHA256 = sha256(fs.readFileSync(path.join(path.dirname(scriptPath), 'ci-test-contract.mjs')));
    const environment = await task('backend:test:ci:environment', '', path.join(outputDir, 'environment.out'));
    if (environment.code) throw new Error('Go environment discovery failed');
    summary.environment = { ...JSON.parse(environment.stdout), node: process.version, nodeBinary: process.execPath, actualHostOS: process.platform, actualHostArch: process.arch, hostRelease: os.release(), taskBinary, taskVersion: taskVersion.stdout.trim(), cpuCount: os.cpus().length, cpuModel: os.cpus()[0]?.model, totalMemoryBytes: os.totalmem(), gomaxprocs: runtimeEnv.GOMAXPROCS, runnerImage: runtimeEnv.ImageOS ?? null, runnerImageVersion: runtimeEnv.ImageVersion ?? null, postgresConfigured: Boolean(runtimeEnv.LINGUAFLOW_TEST_POSTGRES_DSN) };
    summary.environment.availableCPUCount = availableCPUCount;
    summary.environment.storageAcceptanceEnabled = runtimeEnv.LINGUAFLOW_STORAGE_ACCEPTANCE === '1';
    runtimeEnv.CI_GOOS = summary.environment.GOOS;
    if (runtimeEnv.CI && !runtimeEnv.LINGUAFLOW_TEST_POSTGRES_DSN) throw new Error('CI requires the PostgreSQL test service DSN');
    const raceFlags = mode === 'plain' ? '' : '-race';
    const maxGoProcs = Number(runtimeEnv.GOMAXPROCS);
    const inventoryBudget = await scheduler.bulkBudget(packageParallelism, maxGoProcs);
    const discovery = await scheduler.executeOne({ id: 'package-inventory' }, inventoryBudget, (_job, budget) => task('backend:test:ci:inventory', `${raceFlags} -p=${budget.effectiveP}`, path.join(outputDir, 'packages.out'), budget));
    if (discovery.code) throw new Error('Package inventory failed');
    const listBudget = await scheduler.bulkBudget(packageParallelism, maxGoProcs);
    const list = await scheduler.executeOne({ id: 'test-inventory' }, listBudget, (_job, budget) => task('backend:test', `-json -list . -p=${budget.effectiveP} -parallel=1 -count=1 -timeout=20m ${raceFlags} ${mode === 'race-atomic' ? '-covermode=atomic' : ''} ./...`, path.join(outputDir, 'test-inventory.out'), budget));
    if (list.code) throw new Error('Top-level test inventory failed');
    const inventory = parseInventory(list.stdout, parseJSONObjects(discovery.stdout));
    for (const [pkg, tests] of Object.entries(contract)) for (const name of tests) if (!inventory.get(pkg)?.tops.includes(name.split('/')[0])) throw new Error(`Required matrix absent from inventory: ${key(pkg, name)}`);
    const bulkBudget = await scheduler.bulkBudget(packageParallelism, maxGoProcs);
    const plan = planShards(mode, inventory, bulkBudget.effectiveP, rounds);
    summary.manifest = { ...frozen, runnerSHA256: summary.runnerSHA256, contractSHA256: summary.contractSHA256, environment: summary.environment, mode, count: 1, performanceRounds: rounds, performancePolicy, bulkPackageParallelismRequested: requestedPackageParallelism, bulkPackageParallelismTarget: packageParallelism, bulkPackageParallelismFactor: requestedPackageParallelism === 'auto' ? packageParallelismFactor : null, bulkPackageParallelism: bulkBudget.effectiveP, resources: summary.resources, inventoryBudget, listBudget, bulkBudget, parallel: 1, packages: [...inventory.values()], required: contract, skipRules: rules, plan };
    writeJSON(path.join(outputDir, 'manifest.json'), summary.manifest);
    const sameSource = async () => {
      if (cancelled) throw new Error('CI execution cancelled; later shards were not launched');
      const current = await identity(root, outputDir, execute);
      summary.lastObservedIdentity = current;
      if (current.head !== frozen.head || current.sourceSha256 !== frozen.sourceSha256) {
        summary.finalIdentity = current;
        throw new Error('Source changed during the frozen execution round');
      }
      return current;
    };
    const executeShard = async (shard, budget) => {
      const directory = path.join(outputDir, `round-${shard.round}`, mode, shard.id);
      fs.mkdirSync(directory, { recursive: true });
      const profilePath = path.join(directory, 'coverage.out');
      const cliArgs = ['-json', `-p=${shard.effectiveP}`, '-parallel=1', '-count=1', `-timeout=${shard.timeout}`, ...shard.extra.map(value => /^[^\s'"`$;&|<>]+$/.test(value) ? value : `'${value.replaceAll("'", "'\\''")}'`), ...(mode === 'race-atomic' ? ['-covermode=atomic', `-coverprofile="${profilePath.replaceAll('\\', '/')}"`] : []), ...shard.packages].join(' ');
      const started = performance.now();
      let result;
      try { result = await task(mode === 'plain' ? 'backend:test' : 'backend:test:race', cliArgs, path.join(directory, 'test-results.out'), { ...budget, persistenceReport: runtimeEnv.LINGUAFLOW_PERSISTENCE_REPORT ? path.join(directory, 'persistence-report.jsonl') : undefined }); }
      catch (error) {
        result = { code: 1, stdout: '', stderr: String(error), cleanup: error.cleanup };
        if (error.cleanup && !error.cleanup.confirmedGone) cancelled = true;
      }
      const expected = new Map(shard.packages.map(pkg => [pkg, inventory.get(pkg)]));
      const validation = validateShard(result, expected, new Set(shard.tops), { env: runtimeEnv, rules });
      if (shard.requiredLeaf && !validation.leaves.some(leaf => leaf.name === shard.requiredLeaf && leaf.pass === 1 && !leaf.skip)) validation.issues.push(`Missing required EPUB terminal leaf: ${shard.requiredLeaf}`);
      if (shard.requiredLeaf && validation.leaves.some(leaf => leaf.name !== shard.requiredLeaf)) validation.issues.push('EPUB selector executed an unexpected terminal leaf');
      const scene = validation.leaves.find(leaf => leaf.name === shard.requiredLeaf);
      const shape = shard.id.startsWith('synthetic-') ? 'synthetic' : 'dense';
      if (shard.requiredLeaf && mode !== 'plain' && (!Number.isFinite(scene?.elapsedSeconds) || scene.elapsedSeconds > sceneSeconds(mode, shape))) validation.issues.push(sceneFailure(mode, shape));
      const record = { ...shard, cliArgs, gomaxprocs: budget.gomaxprocs, resourceBudget: { memoryReservationBytes: budget.memoryReservationBytes, cpuPool: budget.pool }, wallSeconds: (performance.now() - started) / 1000, exitCode: result.code, cleanup: result.cleanup, profilePath: mode === 'race-atomic' ? profilePath : null, validation };
      summary.shards.push(record);
      writeJSON(path.join(directory, 'result.json'), record);
      writeJSON(summaryPath, summary);
      console.log(`${shard.id}: ${validation.issues.length ? 'FAIL' : 'PASS'} (${record.wallSeconds.toFixed(2)}s)`);
      if (result.cleanup && !result.cleanup.confirmedGone) throw Object.assign(new Error('Owned shard cleanup could not be confirmed'), { cleanup: result.cleanup });
      return record;
    };
    const packagesShard = plan.find(shard => shard.id === 'packages');
    if (packagesShard) await scheduler.executeOne(packagesShard, bulkBudget, executeShard, sameSource);
    for (let roundNumber = 1; roundNumber <= (mode === 'plain' ? 1 : rounds); roundNumber++) {
      if (roundNumber > 1 && summary.shards.filter(done => done.round === 1).some(done => done.validation.issues.length)) {
        summary.issues.push('Performance repeat round not executed because the first complete round failed'); break;
      }
      const heavy = plan.filter(shard => shard.id !== 'packages' && shard.round === roundNumber).map(shard => ({ ...shard, trustKey: shard.id.replace(/-r\d+$/, '') }));
      await scheduler.run(heavy, executeShard, { beforeLaunch: sameSource, maxGoProcs });
    }
    summary.shards.sort((a, b) => plan.findIndex(shard => shard.id === a.id) - plan.findIndex(shard => shard.id === b.id));
    summary.finalIdentity = await sameSource();
    const round = validateRound(summary.shards.filter(shard => shard.round === 1), inventory, contract);
    summary.round = round;
    summary.performanceMeasurements = summary.shards.filter(shard => shard.requiredLeaf).map(shard => ({ round: shard.round, leaf: shard.requiredLeaf, wallSeconds: shard.wallSeconds, testSeconds: shard.validation.leaves.find(leaf => leaf.name === shard.requiredLeaf)?.elapsedSeconds }));
    summary.responseMarginEvidence = 'The CI runner preserves the production 120s response deadline; the 90s response target requires PF-04 per-response measurements.';
    summary.issues.push(...round.issues);
    for (const shard of summary.shards.filter(done => done.round > 1)) summary.issues.push(...shard.validation.issues.map(issue => `${shard.id}: ${issue}`));
    if (summary.shards.length !== plan.length) summary.issues.push('Planned execution not complete');
    await scheduler.close();
    summary.resources.cpuAffinityRestored = true;
    if (mode === 'race-atomic' && !summary.issues.length) summary.coverage = mergeAtomicProfiles(summary.shards.filter(shard => shard.round === 1).map(shard => ({ id: shard.id, path: shard.profilePath, packages: shard.packages })), path.join(outputDir, 'coverage.out'));
    summary.finalIdentity = await sameSource();
    summary.status = summary.issues.length ? 'failed' : 'passed';
  } catch (error) {
    summary.status = 'failed';
    if (error.cleanup) summary.cleanupFailure = error.cleanup;
    summary.issues.push(error.stack ?? String(error));
  } finally {
    process.removeListener('SIGTERM', cancel);
    process.removeListener('SIGINT', cancel);
    try { await scheduler?.close(); }
    catch (error) { summary.status = 'failed'; summary.issues.push(`Controller CPU affinity restoration failed: ${error.message}`); }
    if (summary.resources) summary.resources.cpuAffinityRestored = Boolean(scheduler?.closed);
    if (summary.manifest?.plan) summary.shards.sort((a, b) => summary.manifest.plan.findIndex(shard => shard.id === a.id) - summary.manifest.plan.findIndex(shard => shard.id === b.id));
    summary.finishedAt = new Date().toISOString();
    summary.wallSeconds = (Date.parse(summary.finishedAt) - Date.parse(summary.startedAt)) / 1000;
    writeJSON(summaryPath, summary);
    fs.writeFileSync(path.join(outputDir, 'summary.md'), `CI test mode: ${mode}\n\nResult: ${summary.status}\n\n${summary.issues.map(issue => `- ${issue}`).join('\n')}\n\nBudget calibration: ${summary.budgetCalibration}\n\nActual source/environment/commands, named skips and raw evidence: manifest.json, summary.json and round directories. Coverage is merged only after all required execution succeeds.\n`);
    if (env.GITHUB_STEP_SUMMARY) fs.appendFileSync(env.GITHUB_STEP_SUMMARY, fs.readFileSync(path.join(outputDir, 'summary.md')));
  }
  return summary;
}

if (process.argv[1] && path.resolve(process.argv[1]) === scriptPath) {
  const args = new Map(process.argv.slice(2).map(argument => { const split = argument.indexOf('='); return [argument.slice(0, split), argument.slice(split + 1)]; }));
  const parallelism = args.get('PACKAGE_PARALLELISM') ?? defaultPackageParallelism;
  runCI({ mode: args.get('MODE') ?? 'plain', outputDir: args.get('OUTPUT_DIR') || undefined, packageParallelism: parallelism === 'auto' ? parallelism : Number(parallelism), packageParallelismFactor: Number(args.get('PACKAGE_PARALLELISM_FACTOR') ?? 2) }).then(summary => { process.exitCode = summary.status === 'passed' ? 0 : 1; }).catch(error => { console.error(error); process.exitCode = 1; });
}
