param(
    [switch]$ChecksumSelfTest
)

Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'

$backendRoot = [System.IO.Path]::GetFullPath((Join-Path $PSScriptRoot '..'))
$binRoot = [System.IO.Path]::GetFullPath((Join-Path $backendRoot 'bin'))
$releaseRoot = Join-Path $binRoot 'release'

function Write-ArtifactChecksum {
    param([Parameter(Mandatory)][string]$ArtifactPath)

    if (-not (Test-Path -LiteralPath $ArtifactPath -PathType Leaf)) {
        throw ("Migration release artifact is missing: {0}" -f $ArtifactPath)
    }
    $resolved = (Resolve-Path -LiteralPath $ArtifactPath).Path
    $digest = (Get-FileHash -LiteralPath $resolved -Algorithm SHA256).Hash.ToLowerInvariant()
    $sidecarPath = "$resolved.sha256"
    $sidecar = "{0}  {1}{2}" -f $digest, [System.IO.Path]::GetFileName($resolved), [Environment]::NewLine
    [System.IO.File]::WriteAllText($sidecarPath, $sidecar, [System.Text.Encoding]::ASCII)
    return [pscustomobject]@{ Path = $resolved; Hash = $digest; Sidecar = $sidecarPath }
}

function Invoke-ChecksumSelfTest {
    $testRoot = Join-Path $binRoot ('.migration-checksum-selftest-' + [guid]::NewGuid().ToString('N'))
    New-Item -ItemType Directory -Path $testRoot | Out-Null
    $payloadPath = Join-Path $testRoot 'vector.txt'
    try {
        [System.IO.File]::WriteAllText($payloadPath, 'abc', [System.Text.Encoding]::ASCII)
        $result = Write-ArtifactChecksum -ArtifactPath $payloadPath
        if ($result.Hash -ne 'ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad') {
            throw 'The SHA-256 known-vector digest did not match.'
        }
        $expected = "{0}  vector.txt" -f $result.Hash
        if ([System.IO.File]::ReadAllText($result.Sidecar).Trim() -ne $expected) {
            throw 'The generated SHA-256 sidecar did not match the release format.'
        }
        Write-Host 'Migration release checksum writer passed the SHA-256 known-vector check.'
    }
    finally {
        $fullBin = [System.IO.Path]::GetFullPath($binRoot).TrimEnd('\', '/') + [System.IO.Path]::DirectorySeparatorChar
        $fullTest = [System.IO.Path]::GetFullPath($testRoot)
        if (-not $fullTest.StartsWith($fullBin, [System.StringComparison]::OrdinalIgnoreCase) -or
            [System.IO.Path]::GetFileName($fullTest) -notmatch '^\.migration-checksum-selftest-[0-9a-f]{32}$') {
            throw 'The checksum self-test path failed its cleanup guard.'
        }
        Remove-Item -LiteralPath $fullTest -Recurse -Force
    }
}

if ($ChecksumSelfTest) {
    if (-not (Test-Path -LiteralPath $binRoot -PathType Container)) {
        New-Item -ItemType Directory -Path $binRoot -Force | Out-Null
    }
    Invoke-ChecksumSelfTest
    exit 0
}

if (-not (Test-Path -LiteralPath $releaseRoot -PathType Container)) {
    New-Item -ItemType Directory -Path $releaseRoot -Force | Out-Null
}

$targets = @(
    @{ GOOS = 'linux'; GOARCH = 'amd64' },
    @{ GOOS = 'linux'; GOARCH = 'arm64' },
    @{ GOOS = 'darwin'; GOARCH = 'amd64' },
    @{ GOOS = 'darwin'; GOARCH = 'arm64' },
    @{ GOOS = 'windows'; GOARCH = 'amd64' },
    @{ GOOS = 'windows'; GOARCH = 'arm64' }
)
$taskCommand = Get-Command task -ErrorAction Stop
$artifacts = [System.Collections.Generic.List[object]]::new()

foreach ($target in $targets) {
    $artifactBase = Join-Path $releaseRoot ("linguaflow-migrate-{0}-{1}" -f $target.GOOS, $target.GOARCH)
    & $taskCommand.Source --dir $backendRoot migrate:build "GOOS=$($target.GOOS)" "GOARCH=$($target.GOARCH)" "MIGRATE_BINARY=$artifactBase"
    if ($LASTEXITCODE -ne 0) {
        throw ("Task migrate:build failed for {0}/{1}." -f $target.GOOS, $target.GOARCH)
    }
    $artifactPath = $artifactBase
    if ($target.GOOS -eq 'windows') {
        $artifactPath += '.exe'
    }
    $result = Write-ArtifactChecksum -ArtifactPath $artifactPath
    $artifacts.Add($result)
    Write-Host ("{0}  {1}" -f $result.Hash, [System.IO.Path]::GetFileName($result.Path))
}

Write-Host ("Built {0} migration binaries and SHA-256 sidecars under {1}. These are cross-compilation artifacts; no non-Windows runtime acceptance is implied." -f $artifacts.Count, $releaseRoot)
