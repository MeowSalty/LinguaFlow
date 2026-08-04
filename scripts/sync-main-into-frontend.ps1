# 将 origin/main 合并进当前 frontend 分支。
# 全树分支模型：frontend 与 main 共享完整树（含 backend/、docs/，均为只读），
# 同步即普通合并，无需清理或恢复任何目录。
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
    Write-Host '合并存在冲突，请解决后提交。'
    exit $LASTEXITCODE
}

Write-Host '同步完成。'
