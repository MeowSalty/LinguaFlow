# 检查 frontend 分支落后 origin/main 多少个提交，不动工作树。
# 建议每次开干前运行，落后较多时先 sync 再开工。
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

$behind = [int](git rev-list --count HEAD..origin/main 2>$null)
$ahead = [int](git rev-list --count origin/main..HEAD 2>$null)

if ($behind -eq 0) {
    Write-Host "OK: frontend 与 origin/main 同步（领先 $ahead 个提交）。"
    exit 0
}

Write-Host "WARNING: frontend 落后 origin/main $behind 个提交（领先 $ahead 个）。"
Write-Host '建议开干前先同步：pwsh -File scripts/sync-main-into-frontend.ps1'
exit 0
