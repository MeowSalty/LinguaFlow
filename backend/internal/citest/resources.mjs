import { execFile } from 'node:child_process';
import fs from 'node:fs/promises';
import os from 'node:os';
import path from 'node:path';
import { promisify } from 'node:util';

const exec = promisify(execFile);
export const GiB = 1024 ** 3;
const read = async file => { try { return await fs.readFile(file, 'utf8'); } catch (error) { if (error.code === 'ENOENT') return null; throw error; } };
const sum = values => values.reduce((a, b) => a + b, 0);
const heavy = /^(go|compile|link|gcc|cc1|g\+\+|task|vmmem(?:wsl)?)(?:\.exe)?$|\.test(?:\.exe)?$/i;

export function cpuIds(text) {
  const ids = new Set();
  for (const field of text.trim().split(',')) {
    if (!/^\d+(?:-\d+)?$/.test(field)) throw new Error('Invalid allowed CPU list');
    const [start, end = start] = field.split('-').map(Number);
    if (!Number.isSafeInteger(start) || !Number.isSafeInteger(end) || end < start || end - start > 65536) throw new Error('Invalid allowed CPU range');
    for (let i = start; i <= end; i++) ids.add(i);
  }
  return [...ids].sort((a, b) => a - b);
}

// Mount roots matter in containers: /sys/fs/cgroup may already be mounted at
// the process's cgroup, so simply appending /proc/self/cgroup is incorrect.
export async function linuxLimits(readFile = read, { cpuDiagnosticsOnly = false } = {}) {
  const membership = (await readFile('/proc/self/cgroup') ?? '').trim().split('\n').map(line => line.split(':'));
  const mounts = (await readFile('/proc/self/mountinfo') ?? '').trim().split('\n');
  let cpuQuota = Infinity;
  let memoryHeadroom = Infinity;
  let usageSeconds = null;
  let quotaSource = null;
  let cpuError;
  const unescape = value => value.replace(/\\([0-7]{3})/g, (_, octal) => String.fromCharCode(parseInt(octal, 8)));
  for (const line of mounts) {
    const [left, right] = line.split(' - ');
    if (!right) continue;
    const fields = left.split(' '), extra = right.split(' ');
    if (!['cgroup', 'cgroup2'].includes(extra[0])) continue;
    const unified = extra[0] === 'cgroup2';
    const controllers = new Set((extra[2] ?? '').split(','));
    const entry = membership.find(parts => unified ? parts[0] === '0' : parts[1]?.split(',').some(c => controllers.has(c)));
    if (!entry) continue;
    const mountRoot = unescape(fields[3]), mount = unescape(fields[4]);
    const relative = path.posix.relative(mountRoot, entry[2]);
    if (relative === '..' || relative.startsWith('../')) continue;
    let directory = path.posix.join(mount, relative);
    while (directory === mount || directory.startsWith(`${mount}/`)) {
      try {
        const cpu = unified ? await readFile(`${directory}/cpu.max`) : await readFile(`${directory}/cpu.cfs_quota_us`);
        const period = unified ? cpu?.trim().split(/\s+/)[1] : await readFile(`${directory}/cpu.cfs_period_us`);
        const amount = unified ? cpu?.trim().split(/\s+/)[0] : cpu?.trim();
        if (amount && amount !== 'max' && Number(amount) > 0) {
          if (!(Number(period) > 0)) throw new Error('Invalid cgroup CPU period');
          const quota = Number(amount) / Number(period);
          if (quota < cpuQuota) {
            cpuQuota = quota; quotaSource = directory;
            const usage = unified ? await readFile(`${directory}/cpu.stat`) : await readFile(`${directory}/cpuacct.usage`);
            usageSeconds = unified ? Number(usage?.match(/^usage_usec\s+(\d+)/m)?.[1]) / 1e6 : Number(usage) / 1e9;
            if (!Number.isFinite(usageSeconds)) usageSeconds = null;
          }
        }
      } catch (error) {
        if (!cpuDiagnosticsOnly) throw error;
        cpuError ??= error.message;
      }
      const max = await readFile(`${directory}/${unified ? 'memory.max' : 'memory.limit_in_bytes'}`);
      if (max?.trim() && max.trim() !== 'max') {
        const current = await readFile(`${directory}/${unified ? 'memory.current' : 'memory.usage_in_bytes'}`);
        if (current == null) throw new Error('Missing cgroup memory usage');
        const allowance = BigInt(max.trim()) - BigInt(current.trim());
        memoryHeadroom = Math.min(memoryHeadroom, allowance < 0n ? 0 : Number(allowance));
      }
      if (directory === mount) break;
      directory = path.posix.dirname(directory);
    }
  }
  return { cpuQuota: cpuError ? null : cpuQuota, memoryHeadroom, usageSeconds: cpuError ? null : usageSeconds, quotaSource, ...(cpuError ? { cpuError } : {}) };
}

export async function linuxProcesses() {
  const result = [];
  for (const entry of await fs.readdir('/proc')) {
    if (!/^\d+$/.test(entry)) continue;
    try {
      const stat = await read(`/proc/${entry}/stat`);
      if (!stat) continue;
      const end = stat.lastIndexOf(')'), fields = stat.slice(end + 2).split(' ');
      const status = await read(`/proc/${entry}/status`);
      result.push({ pid: Number(entry), parent: Number(fields[1]), birth: fields[19], name: stat.slice(stat.indexOf('(') + 1, end), cpuSeconds: (Number(fields[11]) + Number(fields[12])) / 100, rssBytes: status?.match(/^VmRSS:\s+(\d+)\s+kB/m) ? Number(status.match(/^VmRSS:\s+(\d+)/m)[1]) * 1024 : null });
    } catch (error) { if (!['ENOENT', 'EACCES', 'EPERM'].includes(error.code)) throw error; }
  }
  return result;
}

export const windowsSnapshotScript = `
$ErrorActionPreference='Stop'
[Console]::OutputEncoding=[Text.UTF8Encoding]::new($false)
$cpuDiagnosticsOnly=$env:LINGUAFLOW_RESOURCE_CPU_DIAGNOSTICS_ONLY -eq '1'
$cpuError=$null
$memory=Get-CimInstance Win32_OperatingSystem -Property FreePhysicalMemory,TotalVisibleMemorySize
$processes=@(Get-CimInstance Win32_Process -Property ProcessId,ParentProcessId,CreationDate,WorkingSetSize,KernelModeTime,UserModeTime,Name | ForEach-Object {
  [pscustomobject]@{pid=[int]$_.ProcessId;parent=[int]$_.ParentProcessId;birth=if($_.CreationDate){([DateTimeOffset]$_.CreationDate).ToUnixTimeMilliseconds()}else{0};name=$_.Name;rssBytes=[double]$_.WorkingSetSize;cpuSeconds=([double]$_.KernelModeTime+[double]$_.UserModeTime)/10000000}
})
$owner=$null
$mask=$null
$hostCpu=$null
$hostCores=0
try {
  if($env:LINGUAFLOW_RESOURCE_OWNER){
    $owner=Get-Process -Id ([int]$env:LINGUAFLOW_RESOURCE_OWNER)
    $mask=$owner.ProcessorAffinity.ToInt64().ToString('X')
  }else{
    $hostCpu=Get-CimInstance Win32_PerfFormattedData_PerfOS_Processor -Filter "Name='_Total'"
    $hostCores=(Get-CimInstance Win32_ComputerSystem -Property NumberOfLogicalProcessors).NumberOfLogicalProcessors
  }
}catch{
  if(!$cpuDiagnosticsOnly){throw}
  $cpuError=$_.Exception.Message
  $mask=$null
  $hostCores=$null
  $hostCpu=$null
}
[pscustomobject]@{availableBytes=[double]$memory.FreePhysicalMemory*1024;totalBytes=[double]$memory.TotalVisibleMemorySize*1024;processes=$processes;mask=$mask;hostCpuCores=if($cpuError){$null}else{[double]$hostCores};hostBusyCores=if($cpuError){$null}elseif($hostCpu){[double]$hostCores*[double]$hostCpu.PercentProcessorTime/100}else{0};cpuError=$cpuError} | ConvertTo-Json -Depth 4 -Compress
`;

export async function windowsSnapshot({ interop = false, cpuDiagnosticsOnly = false } = {}) {
  const result = await exec('pwsh.exe', ['-NoProfile', '-NonInteractive', '-Command', windowsSnapshotScript], { windowsHide: true, timeout: 4000, maxBuffer: 8 * 1024 * 1024, env: { ...process.env, LINGUAFLOW_RESOURCE_OWNER: interop ? '' : String(process.pid), LINGUAFLOW_RESOURCE_CPU_DIAGNOSTICS_ONLY: cpuDiagnosticsOnly ? '1' : '' } });
  return JSON.parse(result.stdout.trim());
}

function tree(rows, root, known) {
  const map = new Map(rows.map(row => [row.pid, row]));
  const node = map.get(root.pid);
  if (node && !known.has(node.pid)) known.set(node.pid, node.birth);
  const owned = new Set([...known].filter(([pid, birth]) => map.get(pid)?.birth === birth).map(([pid]) => pid));
  let changed = true;
  while (changed) {
    changed = false;
    for (const row of rows) if (!owned.has(row.pid) && owned.has(row.parent)) {
      owned.add(row.pid); known.set(row.pid, row.birth); changed = true;
    }
  }
  return rows.filter(row => owned.has(row.pid));
}

export function createResourceMonitor({ platform = process.platform, now = Date.now, readFile = read, processes = platform === 'linux' ? linuxProcesses : null, windows = windowsSnapshot, wsl = Boolean(process.env.WSL_DISTRO_NAME || /microsoft/i.test(os.release())), cpuDiagnosticsOnly = false } = {}) {
  let previousTimes = os.cpus().map(cpu => cpu.times);
  let previousAt = now();
  let previousUsage = null;
  let previousProcesses = new Map();
  const known = new Map();
  const sharedPeaks = new Map();
  const monitor = { pool: null };
  monitor.sample = async (groups = []) => {
    const timestamp = now(), elapsed = (timestamp - previousAt) / 1000;
    const allTimes = os.cpus().map(cpu => cpu.times);
    const perCPU = allTimes.map((times, i) => {
      const old = previousTimes[i];
      if (!old) return null;
      const delta = sum(Object.keys(times).map(k => times[k] - old[k]));
      return delta > 0 ? Math.max(0, 1 - (times.idle - old.idle) / delta) : null;
    });
    let availableBytes, totalBytes, allowedIds, cpuQuota, limits = null, rows, windowsHost = null, mask = null, cpuError;
    if (platform === 'win32') {
      const data = await windows({ cpuDiagnosticsOnly });
      ({ availableBytes, totalBytes, mask } = data); rows = data.processes;
      cpuError = data.cpuError;
      if (!/^[0-9a-f]+$/i.test(mask)) {
        if (!cpuDiagnosticsOnly) throw new Error('Windows processor affinity unavailable');
        allowedIds = []; cpuQuota = null;
      } else {
        const bits = BigInt(`0x${mask}`);
        allowedIds = Array.from({ length: 64 }, (_, i) => i).filter(i => bits & (1n << BigInt(i)));
        cpuQuota = Math.min(os.availableParallelism(), allowedIds.length);
      }
    } else if (platform === 'linux') {
      const memory = await readFile('/proc/meminfo');
      availableBytes = Number(memory?.match(/^MemAvailable:\s+(\d+)\s+kB/m)?.[1]) * 1024;
      totalBytes = Number(memory?.match(/^MemTotal:\s+(\d+)\s+kB/m)?.[1]) * 1024;
      try {
        const status = await readFile('/proc/self/status');
        allowedIds = cpuIds(status?.match(/^Cpus_allowed_list:\s*(.+)$/m)?.[1] ?? '');
      } catch (error) {
        if (!cpuDiagnosticsOnly) throw error;
        allowedIds = []; cpuError = error.message;
      }
      limits = await linuxLimits(readFile, { cpuDiagnosticsOnly });
      cpuError ??= limits.cpuError;
      availableBytes = Math.min(availableBytes, limits.memoryHeadroom);
      cpuQuota = cpuError ? null : Math.min(os.availableParallelism(), allowedIds.length, limits.cpuQuota);
      rows = await processes();
      if (wsl) {
        windowsHost = await windows({ interop: true, cpuDiagnosticsOnly });
        if (!Number.isFinite(windowsHost.availableBytes) || !(windowsHost.availableBytes > 0) || (!cpuDiagnosticsOnly && (!(windowsHost.hostCpuCores > 0) || !Number.isFinite(windowsHost.hostBusyCores) || windowsHost.hostBusyCores < 0))) throw new Error('WSL Windows host physical/CPU budget unavailable');
        availableBytes = Math.min(availableBytes, windowsHost.availableBytes);
        if (windowsHost.hostCpuCores > 0 && cpuQuota != null) cpuQuota = Math.min(cpuQuota, windowsHost.hostCpuCores);
      }
    } else throw new Error(`Physical resource sampling unsupported on ${platform}`);
    if (!Number.isFinite(availableBytes) || availableBytes <= 0 || !Number.isFinite(totalBytes) || totalBytes <= 0) throw Object.assign(new Error('Physical memory sample invalid or exhausted'), { physicalMemoryInvalid: true });
    if (!cpuDiagnosticsOnly && (!Number.isFinite(cpuQuota) || cpuQuota < 0)) throw new Error('CPU sample invalid');
    const selected = monitor.pool?.ids ?? allowedIds;
    let busyCores = sum(selected.map(i => perCPU[i] ?? 1));
    if (limits?.usageSeconds != null && previousUsage != null && elapsed > 0) busyCores = Math.max(busyCores, Math.min(cpuQuota, Math.max(0, (limits.usageSeconds - previousUsage) / elapsed)));
    const owned = groups.map(group => {
      const identities = known.get(group.id) ?? new Map(); known.set(group.id, identities);
      const members = group.pid ? tree(rows, group, identities) : [];
      const cpuCores = elapsed > 0 ? sum(members.map(row => Math.max(0, row.cpuSeconds - (previousProcesses.get(`${row.pid}:${row.birth}`) ?? row.cpuSeconds)))) / elapsed : 0;
      return { id: group.id, pid: group.pid ?? null, processCount: members.length, rssBytes: sum(members.map(row => row.rssBytes ?? 0)), cpuCores, complete: members.length > 0 && members.every(row => Number.isFinite(row.rssBytes)) };
    });
    const controllerKnown = known.get('controller') ?? new Map(); known.set('controller', controllerKnown);
    const ourRows = tree(rows, { pid: process.pid }, controllerKnown);
    const ourIds = new Set(ourRows.map(row => row.pid));
    const byId = new Map(rows.map(row => [row.pid, row]));
    const ancestors = new Set();
    let parent = byId.get(byId.get(process.pid)?.parent);
    while (parent && !ancestors.has(parent.pid)) { ancestors.add(parent.pid); parent = byId.get(parent.parent); }
    const candidates = rows.filter(row => heavy.test(row.name) && !ourIds.has(row.pid) && !ancestors.has(row.pid));
    const candidateIds = new Set(candidates.map(row => row.pid));
    const shared = [];
    for (const row of candidates) {
      let ancestor = byId.get(row.parent), nested = false;
      const visited = new Set();
      while (ancestor && !visited.has(ancestor.pid)) { visited.add(ancestor.pid); if (candidateIds.has(ancestor.pid)) { nested = true; break; } ancestor = byId.get(ancestor.parent); }
      if (nested) continue;
      const members = tree(rows, row, new Map()).filter(item => !ourIds.has(item.pid)), rssBytes = sum(members.map(item => item.rssBytes ?? 0));
      const identity = `${row.pid}:${row.birth}`, peak = Math.max(sharedPeaks.get(identity) ?? 0, rssBytes);
      sharedPeaks.set(identity, peak); shared.push({ pid: row.pid, name: row.name, rssBytes, peakRSSBytes: peak });
    }
    previousTimes = allTimes; previousAt = timestamp; previousUsage = limits?.usageSeconds ?? null;
    previousProcesses = new Map(rows.map(row => [`${row.pid}:${row.birth}`, row.cpuSeconds]));
    return { atMs: timestamp, platform, availableBytes, totalBytes, cpuQuota, allowedIds, cpuLoads: perCPU, mask, busyCores, sharedBusyCores: Math.max(0, busyCores - sum(owned.map(item => item.cpuCores))), owned, shared, cpuLoadKnown: !cpuError && (!cpuDiagnosticsOnly || selected.length > 0) && selected.every(i => perCPU[i] != null), ...(cpuError ? { cpuError } : {}), cgroup: limits ? { cpuQuota: Number.isFinite(limits.cpuQuota) ? limits.cpuQuota : null, memoryHeadroom: Number.isFinite(limits.memoryHeadroom) ? limits.memoryHeadroom : null, quotaSource: limits.quotaSource } : null, wslHost: windowsHost ? { availableBytes: windowsHost.availableBytes, cpuCores: windowsHost.hostCpuCores, busyCores: windowsHost.hostBusyCores, ...(windowsHost.cpuError ? { cpuError: windowsHost.cpuError } : {}) } : null, owner: rows.find(row => row.pid === process.pid) ?? null };
  };
  return monitor;
}
