# 检查 frontend 分支与远端的分叉情况，不动工作树。
# 同时报告两个方向：
#   - 落后 origin/main：开干前应先 sync（保证基于最新 main）
#   - 落后 origin/frontend：远端有新提交（网页端合并、其他设备推送），应先 pull 本分支
# 用法: pwsh -File scripts/check-divergence.ps1

$ErrorActionPreference = 'Stop'

function Assert-FrontendBranch {
    $branch = (git rev-parse --abbrev-ref HEAD).Trim()
    $ok = ($branch -eq 'frontend') -or ($branch -like 'frontend/*') -or ($branch -like 'temp/frontend*')
    if (-not $ok) {
        Write-Error "当前分支为 '$branch'。此脚本仅用于 frontend 相关分支。"
    }
}

Assert-FrontendBranch

Write-Host '>> git fetch origin'
git fetch origin --quiet
if ($LASTEXITCODE -ne 0) { exit $LASTEXITCODE }

$behindMain = [int](git rev-list --count HEAD..origin/main 2>$null)
$aheadMain = [int](git rev-list --count origin/main..HEAD 2>$null)
$behindFrontend = [int](git rev-list --count HEAD..origin/frontend 2>$null)
$aheadFrontend = [int](git rev-list --count origin/frontend..HEAD 2>$null)

$warnings = @()

if ($behindFrontend -gt 0) {
    Write-Host "WARNING: 本地落后 origin/frontend $behindFrontend 个提交（远端有新提交，如网页端合并或其他设备推送）。"
    Write-Host '  建议先拉取本分支远端更新：git pull --ff-only origin frontend'
    $warnings += 'pull-frontend'
} else {
    Write-Host "OK: 本地与 origin/frontend 同步（领先 $aheadFrontend 个提交）。"
}

if ($behindMain -gt 0) {
    Write-Host "WARNING: frontend 落后 origin/main $behindMain 个提交。"
    Write-Host '  建议开干前先同步：pwsh -File scripts/sync-main-into-frontend.ps1'
    $warnings += 'sync-main'
} else {
    Write-Host "OK: frontend 与 origin/main 同步（领先 $aheadMain 个提交）。"
}

if ($warnings.Count -gt 0) { exit 0 } else { exit 0 }
