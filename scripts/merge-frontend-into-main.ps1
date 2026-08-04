# 将 frontend 分支合并进本地 main，用于合并预演与完整性检查。
# 必须在 main 分支的 worktree 中运行。
# 全树分支模型：frontend 与 main 共享完整树，合并不再清空 backend/ 或 docs/。
#
# 注意：main 受分支保护（禁止直接推送），本脚本只做本地合并与验证，
# 不执行 push。合入 main 请通过 Pull Request（网页 Merge 现已安全）。
# 用法: pwsh -File scripts/merge-frontend-into-main.ps1 [-FrontendRef frontend]

param(
    [string]$FrontendRef = 'frontend'
)

$ErrorActionPreference = 'Stop'

$branch = (git rev-parse --abbrev-ref HEAD).Trim()
if ($branch -ne 'main') {
    Write-Error "当前分支为 '$branch'。请在 main worktree 中运行此脚本。"
}

$dirty = git status --porcelain
if ($dirty) {
    Write-Error '工作区不干净，请先提交或暂存后再合并。'
}

Write-Host '>> git fetch origin'
git fetch origin
if ($LASTEXITCODE -ne 0) { exit $LASTEXITCODE }

$ref = $FrontendRef
if ($ref -notmatch '^(origin/|refs/)') {
    # 优先本地，其次 origin
    $local = git rev-parse --verify $ref 2>$null
    if (-not $local) {
        $ref = "origin/$FrontendRef"
    }
}

Write-Host ">> git merge --no-commit $ref"
git merge --no-commit --no-ff $ref
$mergeExit = $LASTEXITCODE

if ($mergeExit -ne 0) {
    $conflicts = git diff --name-only --diff-filter=U
    if ($conflicts) {
        Write-Host '合并存在冲突，请手动解决后提交：'
        $conflicts | ForEach-Object { Write-Host "  $_" }
        exit 1
    }
}

$status = git status --porcelain
if (-not $status) {
    Write-Host '没有需要提交的变更（可能已合并过）。'
    exit 0
}

# 完整性检查：确认合并后关键目录仍在
foreach ($dir in @('backend', 'docs', 'frontend', 'api')) {
    $count = (git ls-files $dir | Measure-Object).Count
    if ($count -eq 0) {
        Write-Error "完整性检查失败：合并后 $dir/ 为空，请中止合并并排查。"
    }
    Write-Host "OK: $dir/ ($count 个文件)"
}

git commit -m "Merge branch '$FrontendRef' into main"
if ($LASTEXITCODE -ne 0) { exit $LASTEXITCODE }

Write-Host '本地合并完成。请通过 Pull Request 合入 main（main 受分支保护，禁止直接推送）。'
