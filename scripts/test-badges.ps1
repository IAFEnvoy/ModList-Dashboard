# 徽章接口自测：批量检查状态码与关键内容
#
# 用法：
#   pwsh -File scripts/test-badges.ps1
#   pwsh -File scripts/test-badges.ps1 -Base http://localhost:3456 -Slug iceandfire
#
# 退出码 0 表示全部通过，1 表示有失败项。

param(
    [string]$Base = "http://localhost:3456",
    [string]$Slug = "iceandfire"
)

$ErrorActionPreference = "Stop"
$ProgressPreference = "SilentlyContinue"
$script:pass = 0
$script:fail = 0

# 探测一个端点：校验状态码，并可选校验响应体包含/不包含某些字符串。
function Test-Badge {
    param(
        [string]$Path,
        [int]$Expect,          # 期望状态码
        [string]$Contains,     # 期望响应体包含（可选）
        [string]$NotContains,  # 期望响应体不包含（可选）
        [string]$Note = ""
    )
    try {
        $resp = Invoke-WebRequest -Uri "$Base$Path" -UseBasicParsing -TimeoutSec 20
        $code = [int]$resp.StatusCode
        $body = [string]$resp.Content
    } catch {
        $code = if ($_.Exception.Response) { [int]$_.Exception.Response.StatusCode } else { 0 }
        $body = ""
    }

    $ok = $code -eq $Expect
    if ($ok -and $Contains) { $ok = $body.Contains($Contains) }
    if ($ok -and $NotContains) { $ok = -not $body.Contains($NotContains) }

    if ($ok) {
        $script:pass++
        Write-Host ("  PASS  {0,-46} {1}" -f $Path, $code) -ForegroundColor Green
    } else {
        $script:fail++
        $detail = "want=$Expect got=$code"
        if ($Contains -and -not $body.Contains($Contains)) { $detail += " missing='$Contains'" }
        Write-Host ("  FAIL  {0,-46} {1}" -f $Path, $detail) -ForegroundColor Red
    }
    if ($Note) { Write-Host ("        $Note") -ForegroundColor DarkGray }
}

Write-Host ""
Write-Host "Badge API self-test" -ForegroundColor Cyan
Write-Host "  base = $Base"
Write-Host "  slug = $Slug"
Write-Host ""

# ---------- 服务可达性 ----------
Write-Host "[0] 服务可达性" -ForegroundColor Yellow
Test-Badge "/api/config" 200 -Contains "loaders" -Note "看板本身可用（含加载器列表）"

# ---------- 静态徽章 ----------
Write-Host ""
Write-Host "[1] 静态徽章（无 slug，固定文案）" -ForegroundColor Yellow
Test-Badge "/api/badge/loader/fabric"   200 -Contains "<svg"
Test-Badge "/api/badge/loader/forge"    200 -Contains "<svg"
Test-Badge "/api/badge/loader/neoforge" 200 -Contains "<svg"
Test-Badge "/api/badge/loader/FABRIC"   200 -Contains "<svg" -Note "加载器名大小写不敏感"
Test-Badge "/api/badge/loader/quilt"    404 -Note "未收录的加载器应 404"

# 动态返回的是模板渲染的 SVG，静态资源是上游成品矢量，两者都不该出现模板占位符残留
Test-Badge "/api/badge/loader/fabric" 200 -NotContains "__W__" -Note "确认没有未替换的模板占位符"

# ---------- 数据徽章 ----------
Write-Host ""
Write-Host "[2] 数据徽章（内容随模组变化）" -ForegroundColor Yellow
Test-Badge "/api/badge/curseforge/downloads/$Slug" 200 -Contains "CurseForge Downloads"
Test-Badge "/api/badge/github/downloads/$Slug"     200 -Contains "GitHub Downloads"
Test-Badge "/api/badge/github/issues/$Slug"        200 -Contains "GitHub Issues"
Test-Badge "/api/badge/github/prs/$Slug"           200 -Contains "GitHub Pull Requests"
Test-Badge "/api/badge/license/$Slug"              200 -Contains ">License</tspan>"
Test-Badge "/api/badge/powered-by/$Slug"           200 -Contains ">Powered by</tspan>"
Test-Badge "/api/badge/link/powered-by/$Slug"      200 -Contains "http"

# 平台字标徽章已按需求删除，确认不再对外暴露
Test-Badge "/api/badge/curseforge/powered-by/$Slug" 404 -Note "平台字标已移除"
Test-Badge "/api/badge/github/powered-by/$Slug"     404 -Note "平台字标已移除"

Write-Host ""
Write-Host "[3] 数据内容抽查（人工核对）" -ForegroundColor Yellow
foreach ($p in @("curseforge/downloads/$Slug", "github/downloads/$Slug", "github/issues/$Slug",
                 "github/prs/$Slug", "license/$Slug")) {
    try {
        $body = (Invoke-WebRequest -Uri "$Base/api/badge/$p" -UseBasicParsing -TimeoutSec 20).Content
        $t = [regex]::Match($body, 'y="15\.3182">([^<]*)<').Groups[1].Value
        $v = [regex]::Match($body, 'y="15\.1818">([^<]*)<').Groups[1].Value
        Write-Host ("  {0,-38} {1} = {2}" -f $p, $t, $v) -ForegroundColor Gray
    } catch {
        Write-Host ("  {0,-38} <请求失败>" -f $p) -ForegroundColor Red
    }
}
Write-Host "  提示：N/A = 该缓存条目尚未建立；0 = 缓存存在且值为 0" -ForegroundColor DarkGray

# ---------- 文档 / Discord ----------
Write-Host ""
Write-Host "[4] 文档与 Discord（依赖模组/全局配置）" -ForegroundColor Yellow
Test-Badge "/api/badge/link/docs/$Slug" 200 -Contains "http" -Note "若该模组没有 wiki 链接，这里会是 404"
Test-Badge "/api/badge/docs/$Slug"      200 -Contains "<svg"

$discordBadge = 0
try { $discordBadge = (Invoke-WebRequest -Uri "$Base/api/badge/discord" -UseBasicParsing -TimeoutSec 20).StatusCode } catch { $discordBadge = 404 }
if ($discordBadge -eq 200) {
    Test-Badge "/api/badge/discord"      200 -Contains "<svg"
    Test-Badge "/api/badge/link/discord" 200 -Contains "http"
} else {
    Write-Host "  SKIP  /api/badge/discord                           未配置 badges.discord" -ForegroundColor DarkYellow
    Test-Badge "/api/badge/discord"      404 -Note "config/config.yml 的 badges.discord 为空时按预期 404"
}

# ---------- 错误处理 ----------
Write-Host ""
Write-Host "[5] 错误处理" -ForegroundColor Yellow
Test-Badge "/api/badge/curseforge/downloads/does-not-exist" 404 -Note "未知 slug"
Test-Badge "/api/badge/license/does-not-exist"              404
Test-Badge "/api/badge/bogus/downloads/$Slug"               404 -Note "未知平台"
Test-Badge "/api/badge/curseforge/bogus/$Slug"              404 -Note "未知 kind"
Test-Badge "/api/badge/loader/bogus"                        404
Test-Badge "/api/badge/curseforge/downloads"                404 -Note "段数不足"
Test-Badge "/api/badge"                                     404
Test-Badge "/api/badge/nope"                                404

# POST 必须是 405
try {
    Invoke-WebRequest -Uri "$Base/api/badge/loader/fabric" -Method POST -UseBasicParsing -TimeoutSec 20 | Out-Null
    $script:fail++
    Write-Host "  FAIL  POST /api/badge/loader/fabric              期望 405" -ForegroundColor Red
} catch {
    $code = if ($_.Exception.Response) { [int]$_.Exception.Response.StatusCode } else { 0 }
    if ($code -eq 405) {
        $script:pass++
        Write-Host "  PASS  POST /api/badge/loader/fabric               405" -ForegroundColor Green
    } else {
        $script:fail++
        Write-Host "  FAIL  POST /api/badge/loader/fabric               got=$code want=405" -ForegroundColor Red
    }
}

# ---------- issue/PR 状态项结构 ----------
Write-Host ""
Write-Host "[7] issue / PR 状态项结构" -ForegroundColor Yellow
$stateIconRe = '<path fill-rule="evenodd" clip-rule="evenodd" fill="(#[0-9A-Fa-f]{6})"'
$stateCountRe = '<text x="[\d.]+" y="(4[0-9.]+)" font-size="15" font-weight="800" fill="(#[0-9A-Fa-f]{6})">'
foreach ($spec in @(@{ Path = "github/issues/$Slug"; Name = "issues"; Want = 3 },
                    @{ Path = "github/prs/$Slug";    Name = "prs";    Want = 4 })) {
    try {
        $body = (Invoke-WebRequest -Uri "$Base/api/badge/$($spec.Path)" -UseBasicParsing -TimeoutSec 20).Content
    } catch {
        $script:fail++
        Write-Host ("  FAIL  {0,-38} <请求失败>" -f $spec.Path) -ForegroundColor Red
        continue
    }
    $icons = @([regex]::Matches($body, $stateIconRe))
    $counts = @([regex]::Matches($body, $stateCountRe))
    if ($icons.Count -eq $spec.Want -and $counts.Count -eq $spec.Want) {
        $script:pass++
        Write-Host ("  PASS  {0,-38} {1} 个状态项" -f $spec.Path, $spec.Want) -ForegroundColor Green
    } else {
        $script:fail++
        Write-Host ("  FAIL  {0,-38} 图标 {1} / 数字 {2}，期望各 {3}" -f $spec.Path, $icons.Count, $counts.Count, $spec.Want) -ForegroundColor Red
    }

    # issue 的 not planned 与 closed 共用灰色叉号，必须合并为一项（灰色只出现一次图标）
    if ($spec.Name -eq "issues") {
        $gray = @($icons | Where-Object { $_.Groups[1].Value -eq "#777777" })
        if ($gray.Count -eq 1) {
            $script:pass++
            Write-Host "  PASS  issues 灰色（not planned + closed）合并为 1 项" -ForegroundColor Green
        } else {
            $script:fail++
            Write-Host ("  FAIL  issues 灰色状态项 {0} 个，期望 1（not planned 必须并入 closed）" -f $gray.Count) -ForegroundColor Red
        }
    }
}

# ---------- 缓存隔离 ----------
Write-Host ""
Write-Host "[6] 缓存隔离（访问一条不应刷新其他条）" -ForegroundColor Yellow
$cacheDir = Join-Path $PSScriptRoot "..\cache"
if (Test-Path $cacheDir) {
    $before = (Get-ChildItem $cacheDir -Recurse -File -ErrorAction SilentlyContinue |
               Measure-Object -Property LastWriteTimeUtc -Maximum).Maximum
    Invoke-WebRequest -Uri "$Base/api/badge/curseforge/downloads/$Slug" -UseBasicParsing -TimeoutSec 20 | Out-Null
    Start-Sleep -Seconds 2
    $touched = Get-ChildItem $cacheDir -Recurse -File -ErrorAction SilentlyContinue |
               Where-Object { $_.LastWriteTimeUtc -gt $before }
    if ($touched.Count -le 1) {
        $script:pass++
        Write-Host ("  PASS  访问 1 个徽章最多只改动 1 个缓存文件（实际 {0}）" -f $touched.Count) -ForegroundColor Green
    } else {
        $script:fail++
        Write-Host ("  FAIL  访问 1 个徽章改动了 {0} 个缓存文件" -f $touched.Count) -ForegroundColor Red
        $touched | Select-Object -First 5 | ForEach-Object { Write-Host "        $($_.FullName)" -ForegroundColor DarkGray }
    }
    if ($touched.Count -ge 1) {
        $touched | Select-Object -First 3 | ForEach-Object { Write-Host "        改动: $($_.Name)" -ForegroundColor DarkGray }
    }
} else {
    Write-Host "  SKIP  cache/ 目录不存在（服务可能从未启动过）" -ForegroundColor DarkYellow
}

# ---------- 汇总 ----------
Write-Host ""
Write-Host ("结果：{0} 通过，{1} 失败" -f $script:pass, $script:fail) -ForegroundColor $(if ($script:fail -eq 0) { "Green" } else { "Red" })
Write-Host ""
if ($script:fail -gt 0) { exit 1 }
exit 0
