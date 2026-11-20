import { execFile } from 'node:child_process';
import { promisify } from 'node:util';

const exec = promisify(execFile);
const pause = milliseconds => new Promise(resolve => setTimeout(resolve, milliseconds));

function groupExists(pid) {
  try { process.kill(-pid, 0); return true; }
  catch (error) { if (error.code === 'ESRCH') return false; throw error; }
}

export async function terminateUnixGroup(pid, { graceMs = 1000, killWaitMs = 5000 } = {}) {
  const diagnostics = { platform: process.platform, group: pid, signals: [], confirmedGone: false };
  const waitGone = async milliseconds => {
    const deadline = Date.now() + milliseconds;
    while (groupExists(pid)) {
      if (Date.now() >= deadline) return false;
      await pause(25);
    }
    return true;
  };
  if (!groupExists(pid)) return { ...diagnostics, confirmedGone: true, alreadyGone: true };
  try { process.kill(-pid, 'SIGTERM'); diagnostics.signals.push('SIGTERM'); }
  catch (error) { if (error.code !== 'ESRCH') throw error; }
  if (await waitGone(graceMs)) return { ...diagnostics, confirmedGone: true };
  try { process.kill(-pid, 'SIGKILL'); diagnostics.signals.push('SIGKILL'); }
  catch (error) { if (error.code !== 'ESRCH') throw error; }
  if (await waitGone(killWaitMs)) return { ...diagnostics, confirmedGone: true };
  const error = new Error(`Owned process group ${pid} still exists after TERM/KILL; cleanup was not confirmed`);
  error.cleanup = diagnostics;
  throw error;
}

// Windows has no POSIX process group. Query only PID/parent/creation metadata,
// follow the owned ancestry, join taskkill, then revalidate each remaining
// process's creation time before using a pinned Process handle to kill it.
// The script never emits command lines or touches unrelated process trees.
export function windowsCleanupScript(pid, ownerPid, startedAt, exitedAt) {
  if (![pid, ownerPid, startedAt].every(Number.isSafeInteger) || (exitedAt != null && !Number.isSafeInteger(exitedAt))) throw new Error('Invalid owned Windows process identity');
  return `
$ErrorActionPreference = 'Stop'
$rootId = ${pid}
$ownerId = ${ownerPid}
$startedAt = [DateTimeOffset]::FromUnixTimeMilliseconds(${startedAt}).UtcDateTime
$exitedAt = ${exitedAt == null ? '$null' : `[DateTimeOffset]::FromUnixTimeMilliseconds(${exitedAt}).UtcDateTime`}
$tracked = @{}
$diagnostics = [Collections.Generic.List[string]]::new()
$taskkillExit = $null
$rootBirth = $null
$rootWasOwned = $false
function Snapshot {
  $map = @{}
  Get-CimInstance Win32_Process -Property ProcessId,ParentProcessId,CreationDate | ForEach-Object {
    $map[[int]$_.ProcessId] = $_
  }
  return $map
}
function Birth($item) { return $item.CreationDate.ToUniversalTime() }
function Discover($map) {
  $currentRoot = $map[$rootId]
  if ($currentRoot -and !$script:rootBirth -and !$exitedAt) {
    if ([int]$currentRoot.ParentProcessId -ne $ownerId -or (Birth $currentRoot) -lt $startedAt.AddSeconds(-1)) { throw 'Root PID ownership could not be confirmed' }
    $script:rootBirth = Birth $currentRoot
    $script:rootWasOwned = $true
    $tracked[$rootId] = $currentRoot
  }
  $changed = $true
  while ($changed) {
    $changed = $false
    foreach ($item in $map.Values) {
      $itemId = [int]$item.ProcessId
      if ($tracked.ContainsKey($itemId) -and (Birth $item) -eq (Birth $tracked[$itemId])) { continue }
      $parentId = [int]$item.ParentProcessId
      $born = Birth $item
      $owned = $false
      if ($parentId -eq $rootId) {
        $lower = if ($script:rootBirth) { $script:rootBirth } else { $startedAt.AddSeconds(-1) }
        $owned = $born -ge $lower
        # A root PID already reused by a later process is never killed. Its new
        # children are distinguished from the previous tree by creation time.
        if ($currentRoot -and ($exitedAt -or ($script:rootBirth -and (Birth $currentRoot) -ne $script:rootBirth))) { $owned = $owned -and $born -lt (Birth $currentRoot) }
        elseif ($exitedAt) { $owned = $owned -and $born -le $exitedAt }
      } elseif ($tracked.ContainsKey($parentId)) {
        $parent = $tracked[$parentId]
        $liveParent = $map[$parentId]
        $owned = $born -ge (Birth $parent)
        if ($liveParent -and (Birth $liveParent) -ne (Birth $parent)) { $owned = $owned -and $born -lt (Birth $liveParent) }
      }
      if ($owned -and $itemId -ne $ownerId) {
        if ($tracked.ContainsKey($itemId)) { throw 'Owned PID was reused before tree cleanup could be confirmed' }
        $tracked[$itemId] = $item; $changed = $true
      }
    }
  }
}
function Remaining($map) {
  $result = @()
  foreach ($itemId in @($tracked.Keys)) {
    $current = $map[$itemId]
    if ($current -and (Birth $current) -eq (Birth $tracked[$itemId])) { $result += $current }
  }
  return $result
}
$map = Snapshot
Discover $map
if ($rootWasOwned -and $map[$rootId] -and (Birth $map[$rootId]) -eq $rootBirth) {
  try {
    $killOutput = (& taskkill.exe /PID $rootId /T /F 2>&1 | Out-String)
    $taskkillExit = $LASTEXITCODE
  } catch { $taskkillExit = -1; $diagnostics.Add('taskkill unavailable/failed: ' + $_.Exception.Message) }
  if ($taskkillExit -ne 0) { $diagnostics.Add('taskkill returned ' + $taskkillExit + '; revalidating owned descendants for fallback') }
}
$deadline = [DateTime]::UtcNow.AddSeconds(5)
do {
  $map = Snapshot
  Discover $map
  $remaining = @(Remaining $map)
  if (!$remaining.Count) { break }
  foreach ($item in $remaining) {
    $handle = $null
    try {
      $handle = [Diagnostics.Process]::GetProcessById([int]$item.ProcessId)
      $null = $handle.Handle
      # CIM timestamps are microsecond precision; StartTime pins the same
      # process handle and prevents a fresh lookup when Kill is called.
      if ([Math]::Abs(($handle.StartTime.ToUniversalTime() - (Birth $item)).TotalMilliseconds) -gt 1) { throw 'Owned PID creation time changed' }
      if (!$handle.HasExited) { $handle.Kill() }
      $null = $handle.WaitForExit(1000)
    } catch {
      $now = Snapshot
      if ($now[[int]$item.ProcessId] -and (Birth $now[[int]$item.ProcessId]) -eq (Birth $item)) { $diagnostics.Add('Could not terminate owned PID ' + $item.ProcessId + ': ' + $_.Exception.Message) }
    } finally { if ($handle) { $handle.Dispose() } }
  }
  Start-Sleep -Milliseconds 50
} while ([DateTime]::UtcNow -lt $deadline)
$map = Snapshot
Discover $map
$remaining = @(Remaining $map)
[pscustomobject]@{platform='win32'; root=$rootId; tracked=@($tracked.Keys); taskkillExit=$taskkillExit; confirmedGone=($remaining.Count -eq 0); remaining=@($remaining | ForEach-Object { [int]$_.ProcessId }); diagnostics=$diagnostics.ToArray()} | ConvertTo-Json -Depth 4 -Compress
if ($remaining.Count) { exit 2 }
`;
}

export async function terminateWindowsTree(pid, ownerPid, startedAt, exitedAt) {
  const script = windowsCleanupScript(pid, ownerPid, startedAt, exitedAt);
  try {
    const result = await exec('pwsh.exe', ['-NoProfile', '-NonInteractive', '-Command', script], { windowsHide: true, timeout: 15000, maxBuffer: 1024 * 1024 });
    const diagnostics = JSON.parse(result.stdout.trim());
    if (!diagnostics.confirmedGone) { const error = new Error('Owned Windows process tree remains alive'); error.cleanup = diagnostics; throw error; }
    return diagnostics;
  } catch (error) {
    if (!error.cleanup) {
      try { error.cleanup = JSON.parse(error.stdout?.trim() ?? ''); }
      catch { error.cleanup = { platform: 'win32', root: pid, confirmedGone: false, diagnostic: error.stderr?.trim() || error.message }; }
    }
    throw error;
  }
}

export async function terminateOwnedProcess(pid, startedAt, exitedAt, options) {
  try { return await (process.platform === 'win32' ? terminateWindowsTree(pid, process.pid, startedAt, exitedAt) : terminateUnixGroup(pid, options)); }
  catch (error) {
    error.cleanup ??= { platform: process.platform, root: pid, confirmedGone: false, diagnostic: error.message };
    throw error;
  }
}
