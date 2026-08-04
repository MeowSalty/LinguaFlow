# 将 frontend 分支安全合并进 main，并强制保留 main 的 backend/ 与 docs/。
# 必须在 main 分支的 worktree 中运行。
# 用法: pwsh -File scripts/merge-frontend-into-main.ps1 [-FrontendRef frontend]

param(
    [string]$FrontendRef = 'frontend'
)

$ErrorActionPreference = 'Stop'

# frontend 分支不含 backend/ 与 docs/（分支路径所有权约定），
# 合入 main 时这两个目录会被识别为"frontend 单方删除"，必须强制恢复。
$preserveDirs = @('backend', 'docs')

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

# 无论合并是否冲突，一律恢复 main 合并前各受保护目录的内容
foreach ($dir in $preserveDirs) {
    if (git rev-parse --verify "HEAD:$dir" 2>$null) {
        Write-Host ">> 保留 main 的 $dir/（checkout HEAD -- $dir）"
        git checkout HEAD -- $dir
        if ($LASTEXITCODE -ne 0) { exit $LASTEXITCODE }
    }
}

# 清理可能残留的删除暂存状态后重新保证各受保护目录完整
foreach ($dir in $preserveDirs) {
    git add -A $dir 2>$null
}

if ($mergeExit -ne 0) {
    $conflicts = git diff --name-only --diff-filter=U
    $preservePrefixes = $preserveDirs | ForEach-Object { "$_/*" }
    $nonPreserved = $conflicts | Where-Object {
        $f = $_
        -not ($preservePrefixes | Where-Object { $f -like $_ } ) -and ($preserveDirs -notcontains $f)
    }
    if ($nonPreserved) {
        Write-Host "$($preserveDirs -join ' 与 ')/ 已恢复，但仍有其他冲突，请手动解决后提交："
        $nonPreserved | ForEach-Object { Write-Host "  $_" }
        exit 1
    }
    # 仅受保护目录冲突时已用 HEAD 版本解决
    foreach ($dir in $preserveDirs) {
        git add -A $dir 2>$null
    }
}

$status = git status --porcelain
if (-not $status) {
    Write-Host '没有需要提交的变更（可能已合并过）。'
    exit 0
}

$kept = ($preserveDirs | ForEach-Object { "$_/" }) -join ' 与 '
git commit -m "Merge branch '$FrontendRef' into main (keep $kept)"
if ($LASTEXITCODE -ne 0) { exit $LASTEXITCODE }

Write-Host "合并完成：frontend 已合入 main，$kept 保持 main 版本。"
