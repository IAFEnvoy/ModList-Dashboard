# 同步 devins-badges 徽章资源 + 重新生成图标常量
#
# 本脚本做两件事：
#
#   1. 下载 Discord 徽章到 badge/assets/discord.svg —— 这是**运行时内嵌**的资源，
#      直接由 /api/badge/discord 输出（上游成品矢量，文字已烘焙成路径）。
#
#   2. 下载 加载器 / 文档 徽章的源矢量到**临时目录**，从中提取图标形状与品牌配色，
#      生成 badge_icons_gen.go。这两个徽章需要两行文字，无法用上游成品矢量，
#      因此只复用它的图形、文字由本项目模板渲染（见 badge_text.go）。
#      源矢量下载完即删除，不进仓库。
#
# 用法：
#   pwsh -File scripts/sync-devins-badges.ps1              # 全部
#   pwsh -File scripts/sync-devins-badges.ps1 -SkipRuntime # 只重新生成图标常量
#   pwsh -File scripts/sync-devins-badges.ps1 -Version 3.2.0

param(
    [string]$Version = "3.3.1",
    [switch]$SkipRuntime
)

$ErrorActionPreference = "Stop"
$ProgressPreference = "SilentlyContinue"

$root = Resolve-Path (Join-Path $PSScriptRoot "..")
$base = "https://cdn.jsdelivr.net/npm/@intergrav/devins-badges@$Version/assets/compact"
$web = New-Object Net.WebClient
$web.Headers.Add("User-Agent", "modlist-dashboard/asset-sync")

# ---------- 1) 运行时资源：Discord 徽章（直接内嵌输出） ----------
if (-not $SkipRuntime) {
    $dest = Join-Path $root "badge\assets"
    New-Item -ItemType Directory -Force -Path $dest | Out-Null
    $out = Join-Path $dest "discord.svg"
    Write-Host "下载运行时资源 discord.svg"
    $text = $web.DownloadString("$base/social/discord-plural_vector.svg")
    # 上游所有文件统一用 a/b/c/d/e/f 作 id，多个 SVG 同页会互相覆盖，加文件名前缀隔离
    $text = [regex]::Replace($text, 'id="([a-z])"', { param($m) "id=`"discord-$($m.Groups[1].Value)`"" })
    $text = [regex]::Replace($text, 'url\(#([a-z])\)', { param($m) "url(#discord-$($m.Groups[1].Value))" })
    $text = [regex]::Replace($text, 'xlink:href="#([a-z])"', { param($m) "xlink:href=`"#discord-$($m.Groups[1].Value)`"" })
    [IO.File]::WriteAllText($out, $text, [Text.UTF8Encoding]::new($false))
    Write-Host "  -> badge/assets/discord.svg ($($text.Length) 字符)"
}

# ---------- 2) 生成图标常量：源矢量放临时目录 ----------
$targets = [ordered]@{
    "badgeIconFabric"   = "supported/fabric_vector.svg"
    "badgeIconForge"    = "supported/forge_vector.svg"
    "badgeIconNeoForge" = "supported/neoforge_vector.svg"
    "badgeIconDocs"     = "documentation/ghpages_vector.svg"
}

$tmp = Join-Path ([IO.Path]::GetTempPath()) ("badge-icons-" + [Guid]::NewGuid().ToString("N").Substring(0, 8))
New-Item -ItemType Directory -Force -Path $tmp | Out-Null
Write-Host ""
Write-Host "下载源矢量到临时目录 $tmp"

$sources = @{}
foreach ($name in $targets.Keys) {
    $file = Split-Path $targets[$name] -Leaf
    $local = Join-Path $tmp $file
    $web.DownloadString($base + "/" + $targets[$name]) | Out-Null   # 触发下载并校验可达
    [IO.File]::WriteAllText($local, $web.DownloadString($base + "/" + $targets[$name]), [Text.UTF8Encoding]::new($false))
    # 这里同样给 id 加前缀，保证与运行时资源的处理方式一致
    $t = Get-Content $local -Raw
    $prefix = ($file -replace '_vector\.svg$', '') -replace '[^a-z0-9]', ''
    $t = [regex]::Replace($t, 'id="([a-z])"', { param($m) "id=`"$prefix-$($m.Groups[1].Value)`"" })
    $t = [regex]::Replace($t, 'url\(#([a-z])\)', { param($m) "url(#$prefix-$($m.Groups[1].Value))" })
    $t = [regex]::Replace($t, 'xlink:href="#([a-z])"', { param($m) "xlink:href=`"#$prefix-$($m.Groups[1].Value)`"" })
    [IO.File]::WriteAllText($local, $t, [Text.UTF8Encoding]::new($false))
    $sources[$name] = $local
    Write-Host "  $file"
}

# 取一个 <g ...> 的完整内容。图标块内部还嵌着 <g>（如 <defs> 里的渐变），
# 因此不能用非贪婪 .*?</g>，必须按标签配对扫描。
function Get-Group {
    param([string]$svg, [string]$id)
    $m = [regex]::Match($svg, "<g\s+filter=`"url\(#$([regex]::Escape($id))\)`"[^>]*>")
    if (-not $m.Success) { return $null }

    $depth = 1
    $i = $m.Index + $m.Length
    while ($i -lt $svg.Length -and $depth -gt 0) {
        $open = $svg.IndexOf('<g', $i)
        $close = $svg.IndexOf('</g>', $i)
        if ($close -lt 0) { return $null }
        if ($open -ge 0 -and $open -lt $close) {
            $depth++
            $i = $open + 2
        } else {
            $depth--
            if ($depth -eq 0) { return $svg.Substring($m.Index + $m.Length, $close - ($m.Index + $m.Length)) }
            $i = $close + 4
        }
    }
    return $null
}

# 图标块内部引用的 <defs>（如 NeoForge 的 <pattern>+内嵌 PNG）就地内联，
# 并把 id 换到本项目命名空间，避免与模板的 badge_bg / badge_icon_clip 冲突。
function Resolve-IconRefs {
    param([string]$svg, [string]$block, [string]$ns, [string]$srcPrefix)

    $extra = [Text.StringBuilder]::new()
    $seen = @{}
    $map = @{}

    # 去掉上游同步时加的 id 前缀，再换成本项目的短命名空间，
    # 否则会出现 bineoforge-neoforge-d 这种叠词。
    function Short-Id([string]$id) {
        if ($srcPrefix -and $id -like "$srcPrefix-*") { return $id.Substring($srcPrefix.Length + 1) }
        return $id
    }

    # 去掉已重写过的 id 引用（否则会解析出 bineoforge-bineoforge-x 之类）
    function Drop-Known([string]$text, [string]$ns) {
        return [regex]::Replace($text, "#$ns-([\w.-]+)", { param($m) "#" + $m.Groups[1].Value })
    }

    $block = Drop-Known $block $ns

    for ($pass = 0; $pass -lt 6; $pass++) {
        # 每轮基于「当前块 + 已内联内容」重新收集，才能发现被内联定义里还引用了别的 id
        $haystack = $extra.ToString() + $block
        $refs = @()
        # 同样用 @() 保证是数组：单个匹配时管道会塌缩成字符串
        $refs += @([regex]::Matches($haystack, 'url\(#([^)]+)\)') | ForEach-Object { $_.Groups[1].Value })
        $refs += @([regex]::Matches($haystack, '(?:xlink:)?href="#([^"]+)"') | ForEach-Object { $_.Groups[1].Value })
        $refs = $refs | Sort-Object -Unique
        $added = $false
        foreach ($id in $refs) {
            if ($seen.ContainsKey($id)) { continue }
            $seen[$id] = $true

            # id 不一定是首个属性（<image> 的 id 在最后），所以用 \b[^>]*\bid=
            $def = [regex]::Match($svg,
                "<(\w+)\b[^>]*\bid=`"$([regex]::Escape($id))`"[^>]*(?:/>|>.*?</\1>)",
                'Singleline')
            if (-not $def.Success) {
                # 已经被上一轮改名并内联好的引用（如 <pattern> 已用新 id），无需告警
                if ($extra.ToString() -notmatch "id=`"$([regex]::Escape($id))`"") {
                    Write-Warning "  引用 #$id 找不到定义，已跳过"
                }
                continue
            }
            $newId = "$ns-$(Short-Id $id)"
            $map[$id] = $newId

            $defText = $def.Value -replace "id=`"$([regex]::Escape($id))`"", "id=`"$newId`""
            [void]$extra.Append($defText)

            # 立刻改名引用：否则下一轮扫描到的还是旧名，会重复解析
            $block = $block -replace "#$([regex]::Escape($id))\b", "#$newId"
            $added = $true
        }
        if (-not $added) { break }
    }

    $resolvedExtra = $extra.ToString()
    foreach ($old in $map.Keys) {
        $resolvedExtra = $resolvedExtra -replace "#$([regex]::Escape($old))\b", "#$($map[$old])"
    }
    return @{ Block = $block; Extra = $resolvedExtra }
}

# 统一成 LF：AppendLine 在 Windows 上产生 CRLF，而 gofmt 只认 LF，
# 否则生成的文件每次都会被 gofmt 报成"未格式化"（整个文件重写）。
function To-LF([string]$text) {
    return $text.Replace("`r`n", "`n").Replace("`r", "`n")
}

# 把「用图案填充一个矩形」的写法换成直接拉伸图片。
#
# NeoForge 的图标是一张 128x128 的 8x8 精灵图：上游用
#   <pattern patternContentUnits="objectBoundingBox">
#     <use xlink:href="#img" transform="scale(.00781)"/>
#   </pattern>
#   <path fill="url(#pattern)" d="M7.993 6.497h28v28h-28z"/>
# 来铺满 28x28 的矩形。但 objectBoundingBox 的内容单位是「相对单位」，
# 内层图片带 width/height="128"（由 use 引入），在浏览器里只会占 1 个单位的宽度，
# 于是实际画出来是一个约 1px 的碎点，再被 40x40 的 clipPath 切掉大半
# ——看起来就是一个灰色小方块。直接改成把 PNG 拉伸到矩形区域即可。
function Convert-PatternImageToImage {
    param([string]$markup, [string]$ns)

    # 分步匹配而不是用反向引用：PowerShell/.NET 组合下 \3 这类反向引用在这里不可靠，
    # 改成每个数字各占一个捕获组，含义更直白。
    # 1) 找被图案填充的矩形：NeoForge 是 d="M7.993 6.497h28v28h-28z"
    #    坐标约定：x=组1 y=组2 宽=组3 高=组4
    $pathRe = @'
<path\b[^>]*fill="url\(#([\w.-]+)\)"[^>]*d="M([\d.]+) ([\d.]+)h([\d.]+)v([\d.]+)h-[\d.]+z"\s*/>
'@
    $pm = [regex]::Match($markup, $pathRe, 'Singleline')
    if (-not $pm.Success) { return $markup }

    $patId = $pm.Groups[1].Value
    # 2) 图案必须存在，且里面引用了一张内嵌图片，否则不是我们要处理的结构
    $patternRe = '<pattern\b[^>]*id="' + [regex]::Escape($patId) + '"[^>]*(?:/>|>.*?</pattern>)'
    $pat = [regex]::Match($markup, $patternRe, 'Singleline')
    if (-not $pat.Success) { return $markup }

    # <image> 可能落在图案内部，也可能被放在图案旁边（生成的块是后者），
    # 所以在整段标记里找，别只在图案里找。
    $img = [regex]::Match($markup, '<image\b[^>]*xlink:href="data:image/[^"]+"[^>]*/>', 'Singleline')
    if (-not $img.Success) { return $markup }

    # 3) 图案 + 矩形一起换成「直接把图片拉伸到矩形区域」的 <image>。
    #    只取 data:image/... 这个属性值——<image> 的其余属性（id、width=128 等）都不能带过来，
    #    否则会拼出两个 <image> 标签。
    $x = $pm.Groups[2].Value
    $y = $pm.Groups[3].Value
    $w = $pm.Groups[4].Value
    $h = $pm.Groups[5].Value
    $href = [regex]::Match($img.Value, 'xlink:href="(data:image/[^"]+)"').Groups[1].Value
    $replacement = '<image x="' + $x + '" y="' + $y + '" width="' + $w + '" height="' + $h +
        '" preserveAspectRatio="none" xlink:href="' + $href + '"/>'

    $span = $markup.Substring($pat.Index, $pm.Index + $pm.Length - $pat.Index)
    $new = $markup.Remove($pat.Index, $span.Length).Insert($pat.Index, $replacement)
    Write-Host "  $ns：图案填充（$patId）改为直接拉伸的 <image>（$x,$y ${w}x$h）"
    return $new
}

# 把一段 SVG 标记切成多个 Go 字符串字面量的行，用于生成 "a" + "b" 形式的拼接。
# 只在标签边界（>< ）切分：绝不能在路径 d 属性中间断开，否则预览或解析会错乱。
# 每行不超过 approx 字符。indent 是行首缩进，prefix 是第一行的前缀（含 = 或字段名）。
function New-MarkupLines {
    param([string]$markup, [string]$first, [string]$cont, [int]$approx = 110)

    # 按标签边界切开，并在每段后补回被吃掉的 '>'
    $tokens = @()
    $last = 0
    foreach ($m in [regex]::Matches($markup, '><')) {
        $tokens += $markup.Substring($last, $m.Index - $last + 1)   # 含结尾的 '>'
        $last = $m.Index + 1                                        # 下一段从 '<' 开始
    }
    $tokens += $markup.Substring($last)

    # 贪心装箱
    $packed = @()
    $buf = ""
    foreach ($t in $tokens) {
        if ($buf -ne "" -and ($buf.Length + $t.Length) -gt $approx) {
            $packed += $buf
            $buf = $t
        } else {
            $buf += $t
        }
    }
    if ($buf -ne "") { $packed += $buf }

    $lines = @()
    for ($i = 0; $i -lt $packed.Count; $i++) {
        $lead = if ($i -eq 0) { $first } else { $cont }
        $suffix = if ($i -lt $packed.Count - 1) { " +" } else { "" }
        $escaped = ([string]$packed[$i]).Replace('"', '\"')
        $lines += $lead + '"' + $escaped + '"' + $suffix
    }
    return $lines
}

$sb = [Text.StringBuilder]::new()
[void]$sb.AppendLine("package badge")
[void]$sb.AppendLine()
[void]$sb.AppendLine("// 本文件由 scripts/sync-devins-badges.ps1 生成，请勿手改。")
[void]$sb.AppendLine("//")
[void]$sb.AppendLine("// 图标形状与品牌配色取自 @intergrav/devins-badges (CC0-1.0) 的 compact 成品矢量。")
[void]$sb.AppendLine("// 上游把文字烘焙成了路径，无法只改排版，因此这里只复用图形，")
[void]$sb.AppendLine("// 文字改由本项目模板渲染（这样才能两行显示、也能本地化）。")
[void]$sb.AppendLine("//")
[void]$sb.AppendLine("// 图标在上游 40px 设计与本项目 56px 画布中的像素尺寸一致，")
[void]$sb.AppendLine("// 因此路径坐标原样保留，仅用 transform 平移进 40x40 图标区。")
[void]$sb.AppendLine("//")
[void]$sb.AppendLine("// 内嵌位图的图标（NeoForge 是一张 128x128 PNG）由 Convert-PatternImageToImage")
[void]$sb.AppendLine("// 改写成直接拉伸的 <image>：上游那种「pattern 填矩形」写法在")
[void]$sb.AppendLine("// patternContentUnits=objectBoundingBox 下只会画出约 1px，实测会被裁成一个灰点。")

$registry = [ordered]@{}

foreach ($name in $targets.Keys) {
    $path = $sources[$name]
    $svg = Get-Content $path -Raw
    $file = Split-Path $path -Leaf

    # 图标块 = filter 区域在左上角 40x40 内的那个 <g>；文字块的 filter 在 x>36
    $iconMarkup = $null
    foreach ($gm in [regex]::Matches($svg, 'filter id="([^"]+)"[^>]*x="([\d.]+)"')) {
        $filterId = $gm.Groups[1].Value
        $fx = [double]::Parse($gm.Groups[2].Value, [Globalization.CultureInfo]::InvariantCulture)
        if ($fx -ge 10) { continue }
        $iconMarkup = Get-Group -svg $svg -id $filterId
        if ($iconMarkup) { $iconMarkup = $iconMarkup.Trim(); break }
    }
    if (-not $iconMarkup) {
        Write-Warning "跳过 $name：没找到图标块"
        continue
    }

    # 强调色取自文字块：优先它的渐变末端（上游用渐变做渐变文字），否则取十六进制填充
    $accent = "#FFFFFF"
    foreach ($gm in [regex]::Matches($svg, 'filter id="([^"]+)"[^>]*x="([\d.]+)"')) {
        $filterId = $gm.Groups[1].Value
        $fx = [double]::Parse($gm.Groups[2].Value, [Globalization.CultureInfo]::InvariantCulture)
        if ($fx -lt 10) { continue }
        $block = Get-Group -svg $svg -id $filterId
        if (-not $block) { continue }

        $gradRef = [regex]::Match($block, 'fill="url\(#([^)]+)\)"')
        if ($gradRef.Success) {
            $gm2 = [regex]::Match($svg,
                "<linearGradient id=`"$([regex]::Escape($gradRef.Groups[1].Value))`"[^>]*>(.*?)</linearGradient>",
                'Singleline')
            if ($gm2.Success) {
                $s = [regex]::Matches($gm2.Groups[1].Value, 'stop-color="(#[0-9A-Fa-f]{6})"')
                if ($s.Count -ge 1) { $accent = $s[$s.Count - 1].Groups[1].Value }
            }
        } else {
            foreach ($c in [regex]::Matches($block, 'fill="(#[0-9A-Fa-f]{6})"')) {
                if ($c.Groups[1].Value -ne "#e8e8e8") { $accent = $c.Groups[1].Value }
            }
        }
    }

    # 背景渐变
    $bgStart, $bgEnd = "#202020", "#000000"
    $gm3 = [regex]::Match($svg, '<linearGradient[^>]*>(.*?)</linearGradient>', 'Singleline')
    if ($gm3.Success) {
        $stops = [regex]::Matches($gm3.Groups[1].Value, 'stop-color="(#[0-9A-Fa-f]{6})"')
        if ($stops.Count -ge 2) { $bgStart = $stops[0].Groups[1].Value; $bgEnd = $stops[1].Groups[1].Value }
    }

    $ns = "bi" + ($name -replace '^badgeIcon', '').ToLower()
    $srcPrefix = (Split-Path $path -Leaf) -replace '_vector\.svg$', '' -replace '[^a-z0-9]', ''
    $resolved = Resolve-IconRefs -svg $svg -block $iconMarkup -ns $ns -srcPrefix $srcPrefix

    $body = $resolved.Block
    if ($resolved.Extra) { $body = $resolved.Extra + $body }

    # 上游用「图案填充矩形」来表达内嵌图片（NeoForge 的精灵图），改写成直接拉伸的 <image>
    $body = Convert-PatternImageToImage -markup $body -ns $ns

    $markup = '<g clip-path="url(#badge_icon_clip)"><g transform="translate(12 8)">' + $body + '</g></g>'

    $lines = New-MarkupLines -markup $markup -first "const $name = " -cont "`t"

    [void]$sb.AppendLine()
    [void]$sb.AppendLine("// $name 来自 $file")
    [void]$sb.AppendLine(($lines -join "`n"))

    $registry[$name] = @{ accent = $accent; bgStart = $bgStart; bgEnd = $bgEnd }
    Write-Host "  提取 $name  accent=$accent bg=$bgStart/$bgEnd"
}

[void]$sb.AppendLine()
[void]$sb.AppendLine("// badgeAssetStyle 上游资源的品牌配色：强调色与背景渐变。")
[void]$sb.AppendLine("var badgeAssetStyle = map[string]struct{ Accent, BgStart, BgEnd string }{")
foreach ($name in $registry.Keys) {
    $r = $registry[$name]
    [void]$sb.AppendLine("`t`"$name`": {Accent: `"$($r.accent)`", BgStart: `"$($r.bgStart)`", BgEnd: `"$($r.bgEnd)`"},")
}
[void]$sb.AppendLine("}")

$outFile = Join-Path $root "badge\badge_icons_gen.go"
[IO.File]::WriteAllText($outFile, (To-LF $sb.ToString()), [Text.UTF8Encoding]::new($false))
Write-Host ""
Write-Host "已生成 $outFile" -ForegroundColor Green

# ---------- 3) GitHub 状态图标：从 static/img 提取 path + 颜色 ----------
# 这些图标是仓库自有的（前端 core.js 的 getGhStateIcon 在用），
# 徽章的 issue/PR 计数行复用同一套图形与配色，保证看板与徽章语义一致。
$stateIcons = [ordered]@{
    "ghStateIssueOpen"      = "issue_open.svg"
    "ghStateIssueCompleted" = "issue_completed.svg"
    "ghStateIssueClosed"    = "issue_closed.svg"
    "ghStatePROpen"         = "pr-open.svg"
    "ghStatePRMerged"       = "pr-merged.svg"
    "ghStatePRDraft"        = "pr-draft.svg"
    "ghStatePRClosed"       = "pr-closed.svg"
}

$sb2 = [Text.StringBuilder]::new()
[void]$sb2.AppendLine("package badge")
[void]$sb2.AppendLine()
[void]$sb2.AppendLine("// 本文件由 scripts/sync-devins-badges.ps1 生成，请勿手改。")
[void]$sb2.AppendLine("//")
[void]$sb2.AppendLine("// issue / PR 各状态的图标，取自 static/img/*.svg —— 与前端 core.js 的")
[void]$sb2.AppendLine("// getGhStateIcon 使用同一套图形与配色，保证看板与徽章语义一致。")
[void]$sb2.AppendLine("// 这些图标原为 16x16 Octicon，path 坐标按 16x16 坐标系保留。")
[void]$sb2.AppendLine()
[void]$sb2.AppendLine("// ghStateIcon 一个状态图标的图形与颜色。")
[void]$sb2.AppendLine("type ghStateIcon struct {")
[void]$sb2.AppendLine("`tPath string // path 的 d 属性，坐标基于 16x16")
[void]$sb2.AppendLine("`tFill string // 前景色")
[void]$sb2.AppendLine("}")
[void]$sb2.AppendLine()

$stateRegistry = [ordered]@{}
foreach ($name in $stateIcons.Keys) {
    $p = Join-Path $root ("static\img\" + $stateIcons[$name])
    if (-not (Test-Path $p)) {
        Write-Warning "跳过 $name：找不到 $($stateIcons[$name])"
        continue
    }
    $svg = Get-Content $p -Raw
    $fill = [regex]::Match($svg, 'fill="(#[0-9A-Fa-f]{6})"').Groups[1].Value
    if (-not $fill) { $fill = "#FFFFFF" }
    # 收集所有 path 的 d，按顺序拼起来
    # 收集所有 path 的 d，按顺序拼起来。
    # 必须用 @() 强制成数组：只有 1 个匹配时管道会塌缩成单个字符串，
    # 而索引字符串得到的是「字符」（$ds[0] → 'M'），会把路径数据静默截断。
    # 同时显式转 [string]：单字符内容会退化成 [char]，没有 .Replace 方法。
    $ds = @([regex]::Matches($svg, '<path[^>]*\sd="([^"]+)"') | ForEach-Object { [string]$_.Groups[1].Value })
    if ($ds.Count -eq 0) {
        Write-Warning "跳过 $name：没找到 path"
        continue
    }
    # 每个 path 一行，避免单行过长。注意显式转成 [string]：
    # 单字符内容在 PowerShell 里会退化成 [char]，没有 .Replace 方法。
    # 续接行缩进两个 Tab：gofmt 会把多行表达式按此对齐。
    $lines = @()
    for ($i = 0; $i -lt $ds.Count; $i++) {
        # 续接符号必须放在闭合引号之后，否则 gofmt 会把 " 和 + 之间的空白重排
        $suffix = if ($i -lt $ds.Count - 1) { " +" } else { "" }
        $escaped = ([string]$ds[$i]).Replace('"', '\"')
        $lead = if ($i -eq 0) { "`t" } else { "`t`t" }
        $lines += $lead + '"' + $escaped + '"' + $suffix
    }
    [void]$sb2.AppendLine("// $name 来自 $($stateIcons[$name])")
    [void]$sb2.AppendLine("var $name = ghStateIcon{")
    [void]$sb2.AppendLine("`tFill: `"$fill`",")
    [void]$sb2.AppendLine("`tPath: " + ($lines -join "`n") + ",")
    [void]$sb2.AppendLine("}")
    [void]$sb2.AppendLine()
    $stateRegistry[$name] = $fill
    Write-Host "  状态图标 $name  fill=$fill  path数=$($ds.Count)"
}

$outFile2 = Join-Path $root "badge\github_state_icons_gen.go"
[IO.File]::WriteAllText($outFile2, (To-LF $sb2.ToString()), [Text.UTF8Encoding]::new($false))
Write-Host "已生成 $outFile2" -ForegroundColor Green

# ---------- 4) 交给 gofmt 定稿 ----------
# 生成器只负责内容，缩进/换行这类排版交给 gofmt，避免手写的缩进规则和它打架。
# 这样生成的文件天然是 gofmt 稳定的，CI 里跑 gofmt -l 不会报未格式化。
if (Get-Command gofmt -ErrorAction SilentlyContinue) {
    foreach ($f in @($outFile, $outFile2)) {
        & gofmt -w $f
        if ($LASTEXITCODE -ne 0) { Write-Warning "gofmt 失败：$f" }
    }
    Write-Host "已用 gofmt 定稿" -ForegroundColor Green
} else {
    Write-Warning "未找到 gofmt，生成的文件可能需要手动 gofmt -w"
}

Remove-Item -Recurse -Force $tmp -ErrorAction SilentlyContinue
Write-Host "源矢量临时目录已清理：$tmp"
