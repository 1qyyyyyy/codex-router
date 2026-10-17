param(
    [string]$ArchivePath,
    [string]$ReleaseDirectory
)

$ErrorActionPreference = 'Stop'
$projectRoot = (Resolve-Path (Join-Path $PSScriptRoot '..')).Path
if (-not $ArchivePath) { $ArchivePath = Join-Path $projectRoot 'release\CodexRouter-windows.zip' }
if (-not $ReleaseDirectory) { $ReleaseDirectory = Join-Path $projectRoot 'release\windows' }
$ArchivePath = (Resolve-Path -LiteralPath $ArchivePath).Path
$ReleaseDirectory = (Resolve-Path -LiteralPath $ReleaseDirectory).Path

Add-Type -AssemblyName System.IO.Compression.FileSystem
$expectedFiles = [System.Collections.Generic.HashSet[string]]::new([System.StringComparer]::Ordinal)
foreach ($name in @('CodexRouter.exe', 'config.yaml', 'README.md', '验证记录.md')) {
    [void]$expectedFiles.Add($name)
}
$licenseRoot = Join-Path $projectRoot 'third_party'
foreach ($file in Get-ChildItem -LiteralPath $licenseRoot -File -Recurse) {
    $relative = $file.FullName.Substring($licenseRoot.Length).TrimStart('\').Replace('\', '/')
    [void]$expectedFiles.Add('third_party/' + $relative)
}
$expectedDirectories = [System.Collections.Generic.HashSet[string]]::new([System.StringComparer]::Ordinal)
foreach ($name in $expectedFiles) {
    $directory = $name
    while ($directory.Contains('/')) {
        $directory = $directory.Substring(0, $directory.LastIndexOf('/'))
        [void]$expectedDirectories.Add($directory + '/')
    }
}

$seen = [System.Collections.Generic.HashSet[string]]::new([System.StringComparer]::Ordinal)
$archive = [System.IO.Compression.ZipFile]::OpenRead($ArchivePath)
try {
    foreach ($entry in $archive.Entries) {
        $name = $entry.FullName.Replace('\', '/')
        if ($name.StartsWith('/') -or $name.Contains(':') -or $name -match '(^|/)\.\.?(/|$)') {
            throw "发布包包含无效路径：$name"
        }
        if (-not $seen.Add($name)) { throw "发布包包含重复条目：$name" }
        if ($name.EndsWith('/')) {
            if (-not $expectedDirectories.Contains($name)) { throw "发布包包含非预期目录：$name" }
            continue
        }
        if (-not $expectedFiles.Contains($name)) { throw "发布包包含非预期文件：$name" }
        $stagedPath = Join-Path $ReleaseDirectory $name.Replace('/', '\')
        $expectedHash = (Get-FileHash -LiteralPath $stagedPath -Algorithm SHA256).Hash
        $stream = $entry.Open()
        $algorithm = [System.Security.Cryptography.SHA256]::Create()
        try {
            $actualHash = [System.BitConverter]::ToString($algorithm.ComputeHash($stream)).Replace('-', '')
        } finally {
            $algorithm.Dispose()
            $stream.Dispose()
        }
        if ($actualHash -ne $expectedHash) { throw "发布包文件与构建产物不一致：$name" }
    }
    foreach ($name in $expectedFiles) {
        if (-not $seen.Contains($name)) { throw "发布包缺少文件：$name" }
    }
} finally {
    $archive.Dispose()
}

# 核对视窗子系统与处理器架构，避免发布会弹出命令窗口的构建。
$binaryPath = Join-Path $ReleaseDirectory 'CodexRouter.exe'
$binary = [System.IO.File]::ReadAllBytes($binaryPath)
if ($binary.Length -lt 256 -or [System.BitConverter]::ToUInt16($binary, 0) -ne 0x5A4D) {
    throw '发布程序不是有效的可执行文件。'
}
$peOffset = [System.BitConverter]::ToInt32($binary, 0x3C)
if ($peOffset -lt 0 -or $peOffset -gt $binary.Length - 96) { throw '发布程序头部无效。' }
if ([System.BitConverter]::ToUInt32($binary, $peOffset) -ne 0x4550 -or
    [System.BitConverter]::ToUInt16($binary, $peOffset + 4) -ne 0x8664 -or
    [System.BitConverter]::ToUInt16($binary, $peOffset + 24) -ne 0x20B -or
    [System.BitConverter]::ToUInt16($binary, $peOffset + 92) -ne 2) {
    throw '发布程序必须使用六十四位视窗图形子系统。'
}
$versionSource = [System.IO.File]::ReadAllText((Join-Path $projectRoot 'cmd\main.go'))
$versionMatch = [regex]::Match($versionSource, 'const version = "([^"]+)"')
if (-not $versionMatch.Success) { throw '未能读取源码版本。' }
$expectedVersion = $versionMatch.Groups[1].Value
$readme = [System.IO.File]::ReadAllText((Join-Path $ReleaseDirectory 'README.md'))
if (-not $readme.Contains("当前发布版本：$expectedVersion。")) { throw '发布说明版本与源码版本不一致。' }

$startInfo = [System.Diagnostics.ProcessStartInfo]::new()
$startInfo.FileName = $binaryPath
$startInfo.Arguments = 'version'
$startInfo.WorkingDirectory = $ReleaseDirectory
$startInfo.UseShellExecute = $false
$startInfo.CreateNoWindow = $true
$startInfo.RedirectStandardOutput = $true
$startInfo.RedirectStandardError = $true
$startInfo.StandardOutputEncoding = [System.Text.UTF8Encoding]::new($false)
$startInfo.StandardErrorEncoding = [System.Text.UTF8Encoding]::new($false)
$process = [System.Diagnostics.Process]::new()
$process.StartInfo = $startInfo
try {
    [void]$process.Start()
    $stdout = $process.StandardOutput.ReadToEndAsync()
    $stderr = $process.StandardError.ReadToEndAsync()
    if (-not $process.WaitForExit(10000)) {
        $process.Kill()
        throw '发布程序版本检查超时。'
    }
    $versionOutput = $stdout.GetAwaiter().GetResult().Trim()
    $errorOutput = $stderr.GetAwaiter().GetResult()
    if ($process.ExitCode -ne 0 -or $versionOutput -ne "版本：$expectedVersion" -or $errorOutput) {
        throw '发布程序版本检查失败。'
    }
} finally {
    $process.Dispose()
}
Write-Host "发布包校验通过：版本 $expectedVersion，文件清单、文件哈希和图形子系统均正确。"
