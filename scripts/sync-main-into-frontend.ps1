# 将 origin/main 合并进当前 frontend 分支，并确保不保留 backend/。
# 用法: pwsh -File scripts/sync-main-into-frontend.ps1

$ErrorActionPreference = 'Stop'

function Assert-FrontendBranch {
    $branch = (git rev-parse --abbrev-ref HEAD).Trim()
    $ok = ($branch -eq 'frontend') -or ($branch -like 'frontend/*') -or ($branch -like 'temp/frontend*')
    if (-not $ok) {
        Write-Error "当前分支为 '$branch'。此脚本仅用于 frontend 相关分支。"
    }
}

Assert-FrontendBranch

$dirty = git status --porcelain
if ($dirty) {
    Write-Error "工作区不干净，请先提交或暂存后再同步。"
}

Write-Host '>> git fetch origin'
git fetch origin
if ($LASTEXITCODE -ne 0) { exit $LASTEXITCODE }

Write-Host '>> git merge origin/main'
git merge origin/main --no-edit
if ($LASTEXITCODE -ne 0) {
    Write-Host "合并存在冲突。解决后若仍有 backend/，执行: git rm -r backend"
    exit $LASTEXITCODE
}

$tracked = git ls-files backend
if ((Test-Path -LiteralPath 'backend') -or $tracked) {
    Write-Host '>> 移除合并带回的 backend/'
    git rm -r backend
    if ($LASTEXITCODE -ne 0) { exit $LASTEXITCODE }
    git commit -m 'chore: 同步 main 后保持前端分支无 backend'
    if ($LASTEXITCODE -ne 0) { exit $LASTEXITCODE }
    Write-Host '已提交 backend/ 清理。'
} else {
    Write-Host 'OK: 树中无 backend/，无需清理。'
}

Write-Host '同步完成。'
