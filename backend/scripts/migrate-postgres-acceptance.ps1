param(
    [switch]$PrepareOnly,
    [switch]$ProvisionOnly
)

Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'

$script:LogPath = $null
$script:SecretValues = @()
$script:RunToken = $null
$script:RunDirectory = $null
$script:AcceptanceRoot = $null
$script:DataDirectory = $null
$script:ServerStarted = $false
$script:Failure = $null

$backendRoot = [System.IO.Path]::GetFullPath((Join-Path $PSScriptRoot '..'))
$binRoot = [System.IO.Path]::GetFullPath((Join-Path $backendRoot 'bin'))
$cacheRoot = Join-Path $binRoot '.migration-postgres-18.6-cache'
$packageName = 'postgresql-18.6-5-windows-x64-binaries.zip'
$packageUrl = "https://get.enterprisedb.com/postgresql/$packageName"
$packageBytes = [int64]384620317
$packageSha256 = 'e2246ba91d22345bc3d017586c09ede52d9df180b1eeb480f050445f1cad84e2'
$archivePath = Join-Path $cacheRoot $packageName
$checksumPath = "$archivePath.sha256"
$toolsRoot = Join-Path $cacheRoot 'tools'
$testPattern = $env:LINGUAFLOW_MIGRATION_POSTGRES_TEST_PATTERN
$defaultTestPattern = '^TestPostgres(ServeReleaseDumpRestore|InplaceExternalRestore|AutoInplaceDumpOnly|StorageProtection)Acceptance$'

function Assert-NoReparsePoint {
    param([Parameter(Mandatory)][string]$Path)

    $current = [System.IO.Path]::GetFullPath($Path)
    while ($current -and (Test-Path -LiteralPath $current)) {
        $item = Get-Item -LiteralPath $current -Force
        if (($item.Attributes -band [System.IO.FileAttributes]::ReparsePoint) -ne 0) {
            throw 'A generated path contains a reparse point; refusing to use or clean it.'
        }
        $parent = [System.IO.Directory]::GetParent($current)
        if ($null -eq $parent) {
            break
        }
        $current = $parent.FullName
    }
}

function Assert-CacheExtractionDirectory {
    param([Parameter(Mandatory)][string]$Path, [Parameter(Mandatory)][string]$Token)

    $fullCache = [System.IO.Path]::GetFullPath($cacheRoot)
    $fullPath = [System.IO.Path]::GetFullPath($Path)
    $expectedPath = Join-Path $fullCache ('.extract-' + $Token)
    if ($Token -notmatch '^[a-f0-9]{32}$' -or -not [string]::Equals($fullPath, $expectedPath, [System.StringComparison]::OrdinalIgnoreCase)) {
        throw 'The extraction directory no longer matches its absolute private cache path.'
    }
    Assert-NoReparsePoint -Path $fullCache
    Assert-NoReparsePoint -Path $fullPath
    if ([System.IO.File]::ReadAllText((Join-Path $fullPath '.linguaflow-extraction-token')).Trim() -ne $Token) {
        throw 'The extraction ownership token changed; refusing move or cleanup.'
    }
    foreach ($item in Get-ChildItem -LiteralPath $fullPath -Force -Recurse) {
        if (($item.Attributes -band [System.IO.FileAttributes]::ReparsePoint) -ne 0) {
            throw 'The extraction directory contains a reparse point; refusing move or cleanup.'
        }
    }
    return $fullPath
}

function Protect-LogText {
    param([Parameter(Mandatory)][string]$Text)

    $safeText = $Text
    foreach ($secretValue in $script:SecretValues) {
        if ($secretValue) {
            $safeText = $safeText.Replace([string]$secretValue, '<redacted>')
        }
    }
    $safeText = [System.Text.RegularExpressions.Regex]::Replace(
        $safeText,
        '(?i)postgres(?:ql)?://[^\s"''<>]+',
        '<redacted PostgreSQL DSN>'
    )
    $safeText = [System.Text.RegularExpressions.Regex]::Replace(
        $safeText,
        '(?i)(password\s*=\s*)(?:''[^'']*''|"[^"]*"|\S+)',
        '$1<redacted>'
    )
    return $safeText
}

function Write-RunLog {
    param([Parameter(Mandatory)][string]$Text)

    if ($script:LogPath) {
        [System.IO.File]::AppendAllText($script:LogPath, (Protect-LogText $Text) + [Environment]::NewLine, [System.Text.UTF8Encoding]::new($false))
    }
}

function Invoke-LoggedProcess {
    param(
        [Parameter(Mandatory)][string]$FilePath,
        [Parameter(Mandatory)][string[]]$ArgumentList,
        [Parameter(Mandatory)][string]$Label,
        [string]$StandardInput = '',
        [hashtable]$Environment = @{},
        [switch]$ClearPostgresEnvironment,
        [switch]$NoCaptureOutput
    )

    $startInfo = [System.Diagnostics.ProcessStartInfo]::new()
    $startInfo.FileName = $FilePath
    $startInfo.WorkingDirectory = $backendRoot
    $startInfo.UseShellExecute = $false
    $startInfo.CreateNoWindow = $true
    $startInfo.WindowStyle = [System.Diagnostics.ProcessWindowStyle]::Hidden
    $startInfo.RedirectStandardOutput = -not $NoCaptureOutput
    $startInfo.RedirectStandardError = -not $NoCaptureOutput
    $startInfo.RedirectStandardInput = $true
    foreach ($argument in $ArgumentList) {
        $startInfo.ArgumentList.Add([string]$argument)
    }
    if ($ClearPostgresEnvironment) {
        foreach ($key in @($startInfo.Environment.Keys)) {
            if ([string]$key -match '(?i)^PG') {
                [void]$startInfo.Environment.Remove([string]$key)
            }
        }
    }
    foreach ($key in $Environment.Keys) {
        $startInfo.Environment[[string]$key] = [string]$Environment[$key]
    }

    $process = [System.Diagnostics.Process]::new()
    $process.StartInfo = $startInfo
    $processStarted = $false
    try {
        if (-not $process.Start()) {
            throw 'process did not start'
        }
        $processStarted = $true
        if (-not $NoCaptureOutput) {
            $stdoutTask = $process.StandardOutput.ReadToEndAsync()
            $stderrTask = $process.StandardError.ReadToEndAsync()
            if ($StandardInput) {
                $process.StandardInput.Write($StandardInput)
            }
        }
        $process.StandardInput.Close()
        $process.WaitForExit()
        $stdout = ''
        $stderr = ''
        if (-not $NoCaptureOutput) {
            $stdout = $stdoutTask.GetAwaiter().GetResult()
            $stderr = $stderrTask.GetAwaiter().GetResult()
        }
        Write-RunLog ("[$Label] exit code: {0}" -f $process.ExitCode)
        if ($stdout.Trim()) {
            Write-RunLog $stdout.TrimEnd()
        }
        if ($stderr.Trim()) {
            Write-RunLog $stderr.TrimEnd()
        }
        if ($process.ExitCode -ne 0) {
            throw ("process failed: {0} (exit {1})" -f $Label, $process.ExitCode)
        }
        return $stdout
    }
    catch {
        if ($processStarted -and $process.HasExited -eq $false) {
            try { $process.Kill($true) } catch { }
        }
        if ($_.Exception.Message -notlike 'process failed:*') {
            Write-RunLog ("[$Label] process launch or I/O failed: {0}" -f $_.Exception.Message)
        }
        throw ("process failed: {0}" -f $Label)
    }
    finally {
        $process.Dispose()
    }
}

function Get-PostgresToolBin {
    param([Parameter(Mandatory)][string]$Root)

    $matches = @(Get-ChildItem -LiteralPath $Root -Filter 'postgres.exe' -File -Recurse -ErrorAction Stop)
    if ($matches.Count -ne 1 -or $matches[0].Directory.Name -ne 'bin') {
        throw 'The cached PostgreSQL package does not contain one expected pgsql/bin/postgres.exe.'
    }
    $toolBin = $matches[0].Directory.FullName
    foreach ($tool in @('postgres.exe', 'pg_ctl.exe', 'initdb.exe', 'psql.exe', 'pg_dump.exe', 'pg_restore.exe')) {
        if (-not (Test-Path -LiteralPath (Join-Path $toolBin $tool) -PathType Leaf)) {
            throw ("The cached PostgreSQL package is missing {0}." -f $tool)
        }
    }
    return $toolBin
}

function Get-PostgresVersion {
    param([Parameter(Mandatory)][string]$ToolPath)

    $output = Invoke-LoggedProcess -FilePath $ToolPath -ArgumentList @('--version') -Label 'PostgreSQL tool version' -ClearPostgresEnvironment
    $match = [System.Text.RegularExpressions.Regex]::Match($output, 'PostgreSQL\)\s+(\d+\.\d+)')
    if (-not $match.Success -or $match.Groups[1].Value -ne '18.6') {
        throw 'The cached PostgreSQL tool version is not 18.6.'
    }
    return $match.Groups[1].Value
}

function Prepare-PostgresTools {
    if (-not (Test-Path -LiteralPath $binRoot -PathType Container)) {
        New-Item -ItemType Directory -Path $binRoot -Force | Out-Null
    }
    Assert-NoReparsePoint -Path $binRoot
    if (-not (Test-Path -LiteralPath $cacheRoot -PathType Container)) {
        New-Item -ItemType Directory -Path $cacheRoot | Out-Null
    }
    Assert-NoReparsePoint -Path $cacheRoot

    if (-not (Test-Path -LiteralPath $archivePath -PathType Leaf)) {
        $partialPath = Join-Path $cacheRoot ("{0}.{1}.partial" -f $packageName, [guid]::NewGuid().ToString('N'))
        $client = [System.Net.Http.HttpClient]::new()
        $cancellation = [System.Threading.CancellationTokenSource]::new([TimeSpan]::FromMinutes(25))
        try {
            Write-RunLog 'Downloading the pinned PostgreSQL 18.6 Windows binary package over HTTPS.'
            $response = $client.GetAsync($packageUrl, [System.Net.Http.HttpCompletionOption]::ResponseHeadersRead, $cancellation.Token).GetAwaiter().GetResult()
            try {
                $response.EnsureSuccessStatusCode()
                if ($response.Content.Headers.ContentLength -ne $packageBytes) {
                    throw 'The PostgreSQL package response has an unexpected content length.'
                }
                $source = $response.Content.ReadAsStream($cancellation.Token)
                $destination = [System.IO.File]::Open($partialPath, [System.IO.FileMode]::CreateNew, [System.IO.FileAccess]::Write, [System.IO.FileShare]::None)
                try {
                    $source.CopyTo($destination, 1048576)
                    $destination.Flush($true)
                }
                finally {
                    $destination.Dispose()
                    $source.Dispose()
                }
            }
            finally {
                $response.Dispose()
            }
            if ((Get-Item -LiteralPath $partialPath).Length -ne $packageBytes) {
                throw 'The downloaded PostgreSQL package has an unexpected size.'
            }
            Move-Item -LiteralPath $partialPath -Destination $archivePath
        }
        finally {
            $cancellation.Dispose()
            $client.Dispose()
            if (Test-Path -LiteralPath $partialPath) {
                Remove-Item -LiteralPath $partialPath -Force
            }
        }
    }

    $archiveItem = Get-Item -LiteralPath $archivePath
    if ($archiveItem.Length -ne $packageBytes) {
        throw 'The cached PostgreSQL package size does not match the verified EDB package size.'
    }
    $actualHash = (Get-FileHash -LiteralPath $archivePath -Algorithm SHA256).Hash.ToLowerInvariant()
    if ($actualHash -ne $packageSha256) {
        throw 'The PostgreSQL package SHA-256 does not match the pinned EDB 18.6 package.'
    }
    if (Test-Path -LiteralPath $checksumPath -PathType Leaf) {
        $recorded = [System.IO.File]::ReadAllText($checksumPath).Trim().Split(' ')[0].ToLowerInvariant()
        if ($recorded -notmatch '^[0-9a-f]{64}$' -or $recorded -ne $actualHash) {
            throw 'The cached PostgreSQL package checksum does not match its recorded SHA-256.'
        }
    }
    else {
        $checksumLine = "{0}  {1}{2}" -f $actualHash, $packageName, [Environment]::NewLine
        [System.IO.File]::WriteAllText($checksumPath, $checksumLine, [System.Text.Encoding]::ASCII)
    }

    if (-not (Test-Path -LiteralPath $toolsRoot -PathType Container)) {
        $extractToken = [guid]::NewGuid().ToString('N')
        $extractRoot = Join-Path $cacheRoot ('.extract-' + $extractToken)
        New-Item -ItemType Directory -Path $extractRoot | Out-Null
        [System.IO.File]::WriteAllText((Join-Path $extractRoot '.linguaflow-extraction-token'), $extractToken, [System.Text.Encoding]::ASCII)
        try {
            Expand-Archive -LiteralPath $archivePath -DestinationPath $extractRoot
            [void](Get-PostgresToolBin -Root $extractRoot)
            [System.IO.File]::WriteAllText((Join-Path $extractRoot '.linguaflow-package-sha256'), $actualHash, [System.Text.Encoding]::ASCII)
            $validatedExtraction = Assert-CacheExtractionDirectory -Path $extractRoot -Token $extractToken
            $fullTools = [System.IO.Path]::GetFullPath($toolsRoot)
            if (-not [string]::Equals([System.IO.Path]::GetDirectoryName($fullTools), [System.IO.Path]::GetFullPath($cacheRoot), [System.StringComparison]::OrdinalIgnoreCase) -or (Test-Path -LiteralPath $fullTools)) {
                throw 'The extraction destination is not the unused tools directory in this cache.'
            }
            Move-Item -LiteralPath $validatedExtraction -Destination $fullTools
            $extractRoot = $null
        }
        finally {
            if ($extractRoot -and (Test-Path -LiteralPath $extractRoot)) {
                $validatedExtraction = Assert-CacheExtractionDirectory -Path $extractRoot -Token $extractToken
                Remove-Item -LiteralPath $validatedExtraction -Recurse -Force
            }
        }
    }
    $packageMarker = Join-Path $toolsRoot '.linguaflow-package-sha256'
    if (-not (Test-Path -LiteralPath $packageMarker -PathType Leaf) -or [System.IO.File]::ReadAllText($packageMarker).Trim() -ne $actualHash) {
        throw 'The extracted PostgreSQL tools are not bound to the cached package checksum.'
    }
    $toolBin = Get-PostgresToolBin -Root $toolsRoot
    foreach ($tool in @('postgres.exe', 'pg_ctl.exe', 'initdb.exe', 'psql.exe', 'pg_dump.exe', 'pg_restore.exe')) {
        [void](Get-PostgresVersion -ToolPath (Join-Path $toolBin $tool))
    }
    Write-RunLog ("PostgreSQL tools verified: version 18.6; package SHA-256 {0}." -f $actualHash)
    return [pscustomobject]@{ Bin = $toolBin; Hash = $actualHash }
}

function New-PrivateRunDirectory {
    $script:AcceptanceRoot = Join-Path $binRoot 'migration-postgres-acceptance'
    if (-not (Test-Path -LiteralPath $script:AcceptanceRoot -PathType Container)) {
        New-Item -ItemType Directory -Path $script:AcceptanceRoot | Out-Null
    }
    Assert-NoReparsePoint -Path $script:AcceptanceRoot
    $script:RunToken = [guid]::NewGuid().ToString('N')
    $script:RunDirectory = Join-Path $script:AcceptanceRoot $script:RunToken
    New-Item -ItemType Directory -Path $script:RunDirectory | Out-Null

    $identity = [System.Security.Principal.WindowsIdentity]::GetCurrent()
    $acl = Get-Acl -LiteralPath $script:RunDirectory
    $acl.SetAccessRuleProtection($true, $false)
    $acl.SetOwner($identity.User)
    $rule = [System.Security.AccessControl.FileSystemAccessRule]::new(
        $identity.User,
        [System.Security.AccessControl.FileSystemRights]::FullControl,
        [System.Security.AccessControl.InheritanceFlags]'ContainerInherit, ObjectInherit',
        [System.Security.AccessControl.PropagationFlags]::None,
        [System.Security.AccessControl.AccessControlType]::Allow
    )
    $acl.SetAccessRule($rule)
    Set-Acl -LiteralPath $script:RunDirectory -AclObject $acl
    $privateAcl = Get-Acl -LiteralPath $script:RunDirectory
    if (-not $privateAcl.AreAccessRulesProtected) {
        throw 'The private acceptance directory unexpectedly inherits filesystem permissions.'
    }
    $script:LogPath = Join-Path $script:RunDirectory 'acceptance.log'
    [System.IO.File]::WriteAllText((Join-Path $script:RunDirectory '.owner-token'), $script:RunToken, [System.Text.Encoding]::ASCII)
    Write-RunLog 'Created a private PostgreSQL acceptance directory owned by the current Windows user.'
}

function New-Secret {
    $bytes = [byte[]]::new(32)
    [System.Security.Cryptography.RandomNumberGenerator]::Fill($bytes)
    return [Convert]::ToBase64String($bytes).TrimEnd('=').Replace('+', 'A').Replace('/', 'B')
}

function Invoke-Psql {
    param(
        [Parameter(Mandatory)][string]$ToolBin,
        [Parameter(Mandatory)][int]$Port,
        [Parameter(Mandatory)][string]$Database,
        [Parameter(Mandatory)][string]$Sql,
        [Parameter(Mandatory)][string]$Password,
        [string[]]$ExtraArguments = @()
    )

    $environment = @{ PGPASSWORD = $Password }
    $arguments = @('-X', '-v', 'ON_ERROR_STOP=1', '-h', '127.0.0.1', '-p', [string]$Port, '-U', 'postgres', '-d', $Database) + $ExtraArguments
    return Invoke-LoggedProcess -FilePath (Join-Path $ToolBin 'psql.exe') -ArgumentList $arguments -Label 'private-cluster SQL setup' -StandardInput $Sql -Environment $environment -ClearPostgresEnvironment
}

function Start-PrivatePostgres {
    param([Parameter(Mandatory)][string]$ToolBin)

    $script:DataDirectory = Join-Path $script:RunDirectory 'data'
    $password = New-Secret
    $script:SecretValues += $password
    $passwordFile = Join-Path $script:RunDirectory '.bootstrap-password'
    [System.IO.File]::WriteAllText($passwordFile, $password + [Environment]::NewLine, [System.Text.Encoding]::ASCII)
    try {
        [void](Invoke-LoggedProcess -FilePath (Join-Path $ToolBin 'initdb.exe') -ArgumentList @('-D', $script:DataDirectory, '-U', 'postgres', '--auth-local=trust', '--auth-host=scram-sha-256', '--encoding=UTF8', '--no-instructions', "--pwfile=$passwordFile") -Label 'private PostgreSQL initdb' -ClearPostgresEnvironment)
    }
    finally {
        if (Test-Path -LiteralPath $passwordFile) {
            Remove-Item -LiteralPath $passwordFile -Force
        }
    }

    $listener = [System.Net.Sockets.TcpListener]::new([System.Net.IPAddress]::Loopback, 0)
    $listener.Start()
    $port = ([System.Net.IPEndPoint]$listener.LocalEndpoint).Port
    $listener.Stop()
    $serverLog = Join-Path $script:RunDirectory 'postgres.log'
    $script:ServerStarted = $true
    $options = "-h 127.0.0.1 -p $port -c listen_addresses=127.0.0.1 -c password_encryption=scram-sha-256"
    [void](Invoke-LoggedProcess -FilePath (Join-Path $ToolBin 'pg_ctl.exe') -ArgumentList @('-D', $script:DataDirectory, '-l', $serverLog, '-o', $options, '-w', '-t', '90', 'start') -Label 'private PostgreSQL start' -ClearPostgresEnvironment -NoCaptureOutput)

    $reportedDirectory = (Invoke-Psql -ToolBin $ToolBin -Port $port -Database 'postgres' -Sql "SELECT current_setting('data_directory');`n" -Password $password -ExtraArguments @('-q', '-t', '-A')).Trim()
    $expectedDirectory = [System.IO.Path]::GetFullPath($script:DataDirectory).TrimEnd('\', '/')
    $reportedFullPath = [System.IO.Path]::GetFullPath($reportedDirectory).TrimEnd('\', '/')
    if (-not [string]::Equals($expectedDirectory, $reportedFullPath, [System.StringComparison]::OrdinalIgnoreCase)) {
        throw 'The loopback PostgreSQL connection did not report the private cluster data directory.'
    }

    $rolePassword = New-Secret
    $script:SecretValues += $rolePassword
    $databaseNames = @(
        'source_inplace', 'target_inplace', 'rehearsal_inplace',
        'source_auto', 'target_auto', 'rehearsal_auto',
        'source_clone', 'target_clone', 'rehearsal_clone'
    )
    $sql = "CREATE ROLE migration_acceptance LOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOINHERIT PASSWORD '$rolePassword';`n"
    foreach ($databaseName in $databaseNames) {
        $sql += "CREATE DATABASE migration_$databaseName OWNER migration_acceptance;`n"
    }
    [void](Invoke-Psql -ToolBin $ToolBin -Port $port -Database 'postgres' -Sql $sql -Password $password)
    $roleCheck = (Invoke-Psql -ToolBin $ToolBin -Port $port -Database 'postgres' -Sql "SELECT rolsuper::text || ':' || rolcreatedb::text FROM pg_roles WHERE rolname='migration_acceptance';`n" -Password $password -ExtraArguments @('-q', '-t', '-A')).Trim()
    if ($roleCheck -ne 'false:false') {
        throw 'The acceptance database owner is unexpectedly privileged.'
    }

    $databaseCount = (Invoke-Psql -ToolBin $ToolBin -Port $port -Database 'postgres' -Sql "SELECT count(*) FROM pg_database d JOIN pg_roles r ON r.oid=d.datdba WHERE r.rolname='migration_acceptance' AND d.datname IN ('migration_source_inplace','migration_target_inplace','migration_rehearsal_inplace','migration_source_auto','migration_target_auto','migration_rehearsal_auto','migration_source_clone','migration_target_clone','migration_rehearsal_clone');`n" -Password $password -ExtraArguments @('-q', '-t', '-A')).Trim()
    if ($databaseCount -ne '9') {
        throw 'The private cluster does not contain all nine databases owned by the migration role.'
    }
    Write-RunLog 'Provision verified: nine owned databases; role flags false:false; server data directory matches the unique owned path.'

    $script:SecretValues += $rolePassword
    foreach ($caseName in @('inplace', 'auto', 'clone')) {
        foreach ($databaseKind in @('source', 'target', 'rehearsal')) {
            $variableName = "LINGUAFLOW_MIGRATION_TEST_POSTGRES_{0}_{1}" -f $databaseKind.ToUpperInvariant(), $caseName.ToUpperInvariant()
            $databaseName = "migration_{0}_{1}" -f $databaseKind, $caseName
            Set-Item -Path "Env:$variableName" -Value "postgres://migration_acceptance:$rolePassword@127.0.0.1:$port/$databaseName`?sslmode=disable&connect_timeout=5"
        }
    }
    $script:SourceDsn = $env:LINGUAFLOW_MIGRATION_TEST_POSTGRES_SOURCE_INPLACE
    $script:TargetDsn = $env:LINGUAFLOW_MIGRATION_TEST_POSTGRES_TARGET_INPLACE
    $script:RehearsalDsn = $env:LINGUAFLOW_MIGRATION_TEST_POSTGRES_REHEARSAL_INPLACE
    $env:LINGUAFLOW_MIGRATION_TEST_POSTGRES_SOURCE = $script:SourceDsn
    $env:LINGUAFLOW_MIGRATION_TEST_POSTGRES_TARGET = $script:TargetDsn
    $env:LINGUAFLOW_MIGRATION_TEST_POSTGRES_REHEARSAL = $script:RehearsalDsn
    $script:PostgresPort = $port
    Write-RunLog ("Private cluster listening only on 127.0.0.1:{0}; owner is NOSUPERUSER NOCREATEDB." -f $port)
}

function Stop-PrivatePostgres {
    param([Parameter(Mandatory)][string]$ToolBin)

    if (-not $script:ServerStarted -or -not $script:DataDirectory -or -not (Test-Path -LiteralPath $script:DataDirectory -PathType Container)) {
        return
    }
    $ownedRun = Assert-OwnedRunDirectory
    if (-not [string]::Equals([System.IO.Path]::GetFullPath($script:DataDirectory), (Join-Path $ownedRun 'data'), [System.StringComparison]::OrdinalIgnoreCase)) {
        throw 'The PostgreSQL stop target is not the data directory of this owned run.'
    }
    Assert-NoReparsePoint -Path $script:DataDirectory
    [void](Invoke-LoggedProcess -FilePath (Join-Path $ToolBin 'pg_ctl.exe') -ArgumentList @('-D', $script:DataDirectory, '-m', 'fast', '-w', '-t', '180', 'stop') -Label 'private PostgreSQL stop' -ClearPostgresEnvironment -NoCaptureOutput)
    $script:ServerStarted = $false
    Write-RunLog 'The private PostgreSQL server stopped in the finally path.'
}

function Assert-OwnedRunDirectory {
    $fullRoot = [System.IO.Path]::GetFullPath($script:AcceptanceRoot).TrimEnd('\', '/') + [System.IO.Path]::DirectorySeparatorChar
    $fullRun = [System.IO.Path]::GetFullPath($script:RunDirectory)
    if (-not $fullRun.StartsWith($fullRoot, [System.StringComparison]::OrdinalIgnoreCase) -or
        [System.IO.Path]::GetFileName($fullRun) -ne $script:RunToken) {
        throw 'The generated PostgreSQL directory no longer matches its recorded private path.'
    }
    Assert-NoReparsePoint -Path $script:AcceptanceRoot
    Assert-NoReparsePoint -Path $fullRun
    $identity = [System.Security.Principal.WindowsIdentity]::GetCurrent()
    $acl = Get-Acl -LiteralPath $fullRun
    if (-not $acl.AreAccessRulesProtected -or ($acl.Owner -ne $identity.Name -and $acl.Owner -ne $identity.User.Value)) {
        throw 'The generated PostgreSQL directory owner changed; refusing cleanup.'
    }
    $tokenPath = Join-Path $fullRun '.owner-token'
    Assert-NoReparsePoint -Path $tokenPath
    if ([System.IO.File]::ReadAllText($tokenPath).Trim() -ne $script:RunToken) {
        throw 'The generated PostgreSQL directory ownership token changed; refusing cleanup.'
    }
    return $fullRun
}

function Remove-VerifiedRunDirectory {
    $fullRun = Assert-OwnedRunDirectory
    foreach ($item in Get-ChildItem -LiteralPath $fullRun -Force -Recurse) {
        if (($item.Attributes -band [System.IO.FileAttributes]::ReparsePoint) -ne 0) {
            throw 'The generated PostgreSQL directory contains a reparse point; refusing cleanup.'
        }
    }
    Remove-Item -LiteralPath $fullRun -Recurse -Force
}

function Save-RunEvidence {
    $fullRun = Assert-OwnedRunDirectory
    $evidenceDirectory = Join-Path $script:AcceptanceRoot ($script:RunToken + '-evidence')
    Assert-NoReparsePoint -Path $script:AcceptanceRoot
    if (Test-Path -LiteralPath $evidenceDirectory) {
        throw 'The evidence destination already exists.'
    }
    New-Item -ItemType Directory -Path $evidenceDirectory | Out-Null
    Set-Acl -LiteralPath $evidenceDirectory -AclObject (Get-Acl -LiteralPath $fullRun)
    foreach ($name in @('acceptance.log', 'postgres.log')) {
        $sourcePath = Join-Path $fullRun $name
        if (Test-Path -LiteralPath $sourcePath -PathType Leaf) {
            $contents = [System.IO.File]::ReadAllText($sourcePath)
            if ($contents) {
                [System.IO.File]::WriteAllText((Join-Path $evidenceDirectory $name), (Protect-LogText $contents), [System.Text.UTF8Encoding]::new($false))
            }
        }
    }
    return $evidenceDirectory
}

if (-not $IsWindows) {
    throw 'The temporary PostgreSQL acceptance task requires Windows.'
}
if (-not $testPattern) {
    $testPattern = $defaultTestPattern
}

$toolBin = $null
New-PrivateRunDirectory
try {
    $prepared = Prepare-PostgresTools
    $toolBin = $prepared.Bin
    if ($PrepareOnly) {
        Write-Host ("PostgreSQL 18.6 tools ready; package SHA-256: {0}" -f $prepared.Hash)
    }
    else {
        Start-PrivatePostgres -ToolBin $toolBin
        if ($ProvisionOnly) {
            Write-RunLog 'ProvisionOnly passed with PostgreSQL 18.6 on Windows; no Go compilation was required.'
            Write-Host 'Private PostgreSQL 18.6 cluster provisioned: nine isolated databases are owned by a NOSUPERUSER NOCREATEDB role.'
        }
        else {
        $env:LINGUAFLOW_MIGRATION_TEST_POSTGRES_SOURCE = $script:SourceDsn
        $env:LINGUAFLOW_MIGRATION_TEST_POSTGRES_TARGET = $script:TargetDsn
        $env:LINGUAFLOW_MIGRATION_TEST_POSTGRES_REHEARSAL = $script:RehearsalDsn
        $env:LINGUAFLOW_MIGRATION_POSTGRES_BIN = $toolBin
        foreach ($name in @('PGHOST', 'PGHOSTADDR', 'PGPORT', 'PGUSER', 'PGPASSWORD', 'PGDATABASE', 'PGSERVICE', 'PGSERVICEFILE')) {
            Remove-Item -Path "Env:$name" -ErrorAction SilentlyContinue
        }
        Write-RunLog ("Running the migration PostgreSQL test pattern: {0}" -f $testPattern)
        $taskPath = (Get-Command task -ErrorAction Stop).Source
        $taskArguments = @('--taskfile', (Join-Path $backendRoot 'Taskfile.yml'), 'migrate:test:postgres:run', "CLI_ARGS=$testPattern")
        if ([System.IO.Path]::GetExtension($taskPath) -eq '.ps1') {
            $taskArguments = @('-NoProfile', '-File', $taskPath) + $taskArguments
            $taskPath = Join-Path $PSHOME 'pwsh.exe'
        }
        $testOutput = Invoke-LoggedProcess -FilePath $taskPath -ArgumentList $taskArguments -Label 'Task PostgreSQL migration acceptance' -ClearPostgresEnvironment
        if ($testOutput -match '(?m)^\s*--- SKIP:') {
            throw 'A PostgreSQL acceptance test skipped; this run cannot establish runtime acceptance.'
        }
        if ($testPattern -eq $defaultTestPattern) {
            foreach ($requiredTest in @('TestPostgresServeReleaseDumpRestoreAcceptance', 'TestPostgresInplaceExternalRestoreAcceptance', 'TestPostgresAutoInplaceDumpOnlyAcceptance', 'TestPostgresStorageProtectionAcceptance')) {
                if ($testOutput -notmatch ('(?m)^--- PASS: ' + [regex]::Escape($requiredTest) + ' \(')) {
                    throw ("The required PostgreSQL acceptance test did not pass: {0}." -f $requiredTest)
                }
            }
        }
        Write-RunLog 'Actual PostgreSQL 18.6 Windows strategy acceptance passed.'
        Write-Host ("PostgreSQL migration acceptance passed (18.6, test pattern: {0})." -f $testPattern)
        }
    }
}
catch {
    $script:Failure = $_
    Write-RunLog ("Acceptance failed: {0}. Credentials and PostgreSQL DSNs are redacted from this log." -f $_.Exception.Message)
}
finally {
    if ($script:ServerStarted) {
        try {
            Stop-PrivatePostgres -ToolBin $toolBin
        }
        catch {
            $script:Failure = $_
            Write-RunLog 'The private PostgreSQL server could not be confirmed stopped; its private directory is retained.'
        }
    }
    if (-not $script:Failure -and -not $script:ServerStarted) {
        try {
            $evidenceDirectory = Save-RunEvidence
            Remove-VerifiedRunDirectory
            [System.IO.File]::AppendAllText((Join-Path $evidenceDirectory 'acceptance.log'), 'Owned cluster directory removed after absolute path, ACL owner, token and reparse validation.' + [Environment]::NewLine, [System.Text.UTF8Encoding]::new($false))
            Write-Host ("Sanitized PostgreSQL acceptance evidence: {0}" -f $evidenceDirectory)
        }
        catch {
            $script:Failure = $_
            Write-RunLog 'The private run directory failed ownership/path validation and was retained.'
        }
    }
    if ($script:Failure -and $script:RunDirectory -and (Test-Path -LiteralPath (Join-Path $script:RunDirectory 'postgres.log') -PathType Leaf)) {
        try {
            [void](Assert-OwnedRunDirectory)
            $serverLogPath = Join-Path $script:RunDirectory 'postgres.log'
            $serverLogContents = [System.IO.File]::ReadAllText($serverLogPath)
            if ($serverLogContents) {
                [System.IO.File]::WriteAllText($serverLogPath, (Protect-LogText $serverLogContents), [System.Text.UTF8Encoding]::new($false))
            }
        }
        catch {
            Write-RunLog ("Could not sanitize the retained server log; its private directory is retained: {0}" -f $_.Exception.Message)
        }
    }
}

if ($script:Failure) {
    if ($script:LogPath -and (Test-Path -LiteralPath $script:LogPath)) {
        Write-Error ("PostgreSQL acceptance failed; private data and sanitized log retained at {0}." -f $script:RunDirectory)
    }
    else {
        Write-Error 'PostgreSQL acceptance failed before the private run log could be created.'
    }
    exit 1
}
