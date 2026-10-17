param([string]$GoExecutable)

$ErrorActionPreference = 'Stop'
$projectRoot = (Resolve-Path (Join-Path $PSScriptRoot '..')).Path
$workspaceRoot = (Resolve-Path (Join-Path $projectRoot '..\..')).Path

$go = $GoExecutable
if (-not $go) {
    $installedGo = Get-Command go -ErrorAction SilentlyContinue
    if ($installedGo) { $go = $installedGo.Source }
}
if (-not $go) {
    $bundledGo = Join-Path $workspaceRoot 'work\toolchain\go\bin\go.exe'
    if (Test-Path -LiteralPath $bundledGo) { $go = $bundledGo }
}
if (-not $go -or -not (Test-Path -LiteralPath $go)) {
    throw '找不到 Go 编译器，请安装 Go 或通过 -GoExecutable 指定编译器路径。'
}

$release = Join-Path $projectRoot 'release'
function Assert-ReleasePath([string]$path) {
    $releasePlain = [System.IO.Path]::GetFullPath($release).TrimEnd('\')
    $releasePrefix = $releasePlain + '\'
    $full = [System.IO.Path]::GetFullPath($path)
    if ($full -ne $releasePlain -and -not $full.StartsWith($releasePrefix, [System.StringComparison]::OrdinalIgnoreCase)) {
        throw "拒绝写入发布目录之外的路径：$full"
    }
    $cursor = $full
    while ($cursor -and ($cursor -eq $releasePlain -or $cursor.StartsWith($releasePrefix, [System.StringComparison]::OrdinalIgnoreCase))) {
        if (Test-Path -LiteralPath $cursor) {
            $attributes = [System.IO.File]::GetAttributes($cursor)
            if (($attributes -band [System.IO.FileAttributes]::ReparsePoint) -ne 0) {
                throw "拒绝通过链接或联接写入发布文件：$cursor"
            }
        }
        $cursor = Split-Path -Parent $cursor
    }
}

Assert-ReleasePath $release
New-Item -ItemType Directory -Force -Path $release | Out-Null
$windowsRelease = Join-Path $release 'windows'
Assert-ReleasePath $windowsRelease
New-Item -ItemType Directory -Force -Path $windowsRelease | Out-Null

function Copy-ReleaseFile([string]$source, [string]$destination, [bool]$preserveExisting = $false) {
    Assert-ReleasePath $destination
    if ($preserveExisting -and (Test-Path -LiteralPath $destination)) {
        if ((Get-Item -LiteralPath $destination).PSIsContainer) { throw "发布配置路径不是文件：$destination" }
        return
    }
    Copy-Item -LiteralPath $source -Destination $destination -Force
}

Push-Location $projectRoot
try {
    $binary = Join-Path $windowsRelease 'CodexRouter.exe'
    Assert-ReleasePath $binary
    $previousOS = $env:GOOS
    $previousArch = $env:GOARCH
    $previousCGO = $env:CGO_ENABLED
    try {
        $env:GOOS = 'windows'
        $env:GOARCH = 'amd64'
        $env:CGO_ENABLED = '0'
        & $go build -buildvcs=false -trimpath -ldflags '-H=windowsgui' -o $binary ./cmd
        if ($LASTEXITCODE -ne 0) { throw 'Windows 程序构建失败。' }
    } finally {
        $env:GOOS = $previousOS
        $env:GOARCH = $previousArch
        $env:CGO_ENABLED = $previousCGO
    }

    Copy-ReleaseFile (Join-Path $projectRoot 'config.yaml') (Join-Path $windowsRelease 'config.yaml') $true
    Copy-ReleaseFile (Join-Path $projectRoot 'README.md') (Join-Path $windowsRelease 'README.md')
    Copy-ReleaseFile (Join-Path $projectRoot '验证记录.md') (Join-Path $windowsRelease '验证记录.md')
    # 清理旧版本的批处理入口及程序备份，保留配置、日志和代理恢复数据。
    foreach ($oldEntry in (Get-ChildItem -LiteralPath $windowsRelease -File -Force | Where-Object { $_.Extension -eq '.cmd' -or $_.Name -eq 'CodexRouter.exe~' })) {
        Assert-ReleasePath $oldEntry.FullName
        Remove-Item -LiteralPath $oldEntry.FullName -Force
    }
    $thirdPartySource = Join-Path $projectRoot 'third_party'
    $thirdParty = Join-Path $windowsRelease 'third_party'
    Assert-ReleasePath $thirdParty
    New-Item -ItemType Directory -Force -Path $thirdParty | Out-Null
    foreach ($sourceDirectory in (Get-ChildItem -LiteralPath $thirdPartySource -Directory -Recurse)) {
        if (($sourceDirectory.Attributes -band [System.IO.FileAttributes]::ReparsePoint) -ne 0) { throw "第三方许可目录含链接：$($sourceDirectory.FullName)" }
        $relative = $sourceDirectory.FullName.Substring($thirdPartySource.Length).TrimStart('\')
        $destination = Join-Path $thirdParty $relative
        Assert-ReleasePath $destination
        New-Item -ItemType Directory -Force -Path $destination | Out-Null
    }
    foreach ($sourceFile in (Get-ChildItem -LiteralPath $thirdPartySource -File -Recurse)) {
        if (($sourceFile.Attributes -band [System.IO.FileAttributes]::ReparsePoint) -ne 0) { throw "第三方许可文件含链接：$($sourceFile.FullName)" }
        $relative = $sourceFile.FullName.Substring($thirdPartySource.Length).TrimStart('\')
        Copy-ReleaseFile $sourceFile.FullName (Join-Path $thirdParty $relative)
    }

    $zipPath = Join-Path $release 'CodexRouter-windows.zip'
    Assert-ReleasePath $zipPath
    $reparsePoints = Get-ChildItem -LiteralPath $windowsRelease -Force -Recurse | Where-Object { ($_.Attributes -band [System.IO.FileAttributes]::ReparsePoint) -ne 0 }
    if ($reparsePoints) { throw "Windows 发布目录含有链接或联接，拒绝打包：$($reparsePoints[0].FullName)" }
    $archiveItems = @(
        (Join-Path $windowsRelease 'CodexRouter.exe'),
        (Join-Path $windowsRelease 'config.yaml'),
        (Join-Path $windowsRelease 'README.md'),
        (Join-Path $windowsRelease '验证记录.md'),
        (Join-Path $windowsRelease 'third_party')
    )
    foreach ($item in $archiveItems) {
        Assert-ReleasePath $item
        if (-not (Test-Path -LiteralPath $item)) { throw "发布文件缺失：$item" }
    }
    $hashPath = $zipPath + '.sha256'
    Assert-ReleasePath $hashPath
    if (Test-Path -LiteralPath $hashPath) { Remove-Item -LiteralPath $hashPath -Force }
    Compress-Archive -Path $archiveItems -DestinationPath $zipPath -CompressionLevel Optimal -Force
    & (Join-Path $PSScriptRoot 'verify-package.ps1') -ArchivePath $zipPath -ReleaseDirectory $windowsRelease
    $archiveHash = (Get-FileHash -LiteralPath $zipPath -Algorithm SHA256).Hash.ToLowerInvariant()
    [System.IO.File]::WriteAllText($hashPath, $archiveHash + '  ' + [System.IO.Path]::GetFileName($zipPath) + [System.Environment]::NewLine, [System.Text.UTF8Encoding]::new($false))
} finally {
    Pop-Location
}

Write-Host 'Windows 发布包已生成到 release 目录。'
