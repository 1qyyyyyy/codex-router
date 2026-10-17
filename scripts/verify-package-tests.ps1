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
$validator = Join-Path $PSScriptRoot 'verify-package.ps1'
$temporaryRoot = Join-Path $projectRoot 'tmp\verify-package-tests'
[void][System.IO.Directory]::CreateDirectory($temporaryRoot)
$temporaryRoot = (Resolve-Path -LiteralPath $temporaryRoot).Path

Add-Type -AssemblyName System.IO.Compression.FileSystem

function Add-ZipEntryText {
    param(
        [System.IO.Compression.ZipArchive]$Archive,
        [string]$Name,
        [string]$Text
    )

    $entry = $Archive.CreateEntry($Name)
    $stream = $entry.Open()
    $writer = [System.IO.StreamWriter]::new($stream, [System.Text.UTF8Encoding]::new($false))
    try {
        $writer.Write($Text)
    } finally {
        $writer.Dispose()
    }
}

function Read-ZipEntryBytes {
    param([System.IO.Compression.ZipArchiveEntry]$Entry)

    $stream = $Entry.Open()
    $memory = [System.IO.MemoryStream]::new()
    try {
        $stream.CopyTo($memory)
        return ,$memory.ToArray()
    } finally {
        $memory.Dispose()
        $stream.Dispose()
    }
}

function Add-ZipEntryBytes {
    param(
        [System.IO.Compression.ZipArchive]$Archive,
        [string]$Name,
        [byte[]]$Bytes
    )

    $entry = $Archive.CreateEntry($Name)
    $stream = $entry.Open()
    try {
        $stream.Write($Bytes, 0, $Bytes.Length)
    } finally {
        $stream.Dispose()
    }
}

function Invoke-ZipMutation {
    param(
        [string]$Path,
        [scriptblock]$Mutation
    )

    $archive = [System.IO.Compression.ZipFile]::Open($Path, [System.IO.Compression.ZipArchiveMode]::Update)
    try {
        & $Mutation $archive
    } finally {
        $archive.Dispose()
    }
}

function Invoke-ExpectedRejection {
    param(
        [string]$CaseName,
        [string]$Path,
        [string]$ExpectedReason
    )

    $failure = $null
    try {
        & $validator -ArchivePath $Path -ReleaseDirectory $ReleaseDirectory
    } catch {
        $failure = $_.Exception.Message
    }

    if (-not $failure) {
        throw "篡改用例未被拒绝：$CaseName"
    }
    if (-not $failure.Contains($ExpectedReason)) {
        throw "篡改用例拒绝原因不符：$CaseName；预期包含[$ExpectedReason]，实际为[$failure]"
    }
    Write-Host "篡改用例通过：$CaseName（$ExpectedReason）"
}

function New-MutatedArchivePath {
    param([string]$CaseName)

    $caseDirectory = Join-Path $temporaryRoot ($CaseName + '-' + [guid]::NewGuid().ToString('N'))
    [void][System.IO.Directory]::CreateDirectory($caseDirectory)
    $caseDirectory = (Resolve-Path -LiteralPath $caseDirectory).Path
    if (-not $caseDirectory.StartsWith($temporaryRoot + [System.IO.Path]::DirectorySeparatorChar, [System.StringComparison]::OrdinalIgnoreCase)) {
        throw '篡改用例临时目录超出仓库临时目录。'
    }
    $mutatedPath = Join-Path $caseDirectory '测试发布包.zip'
    Copy-Item -LiteralPath $ArchivePath -Destination $mutatedPath
    return $mutatedPath
}

# 先确认原始发布包与构建目录本身有效，避免把基线错误误报为回归结果。
& $validator -ArchivePath $ArchivePath -ReleaseDirectory $ReleaseDirectory

$mutatedPath = New-MutatedArchivePath '额外日志文件'
Invoke-ZipMutation $mutatedPath {
    param($archive)
    Add-ZipEntryText $archive 'logs/泄漏.log' '敏感日志内容'
}
Invoke-ExpectedRejection '额外日志文件' $mutatedPath '发布包包含非预期文件：logs/泄漏.log'

$mutatedPath = New-MutatedArchivePath '删除必需说明'
Invoke-ZipMutation $mutatedPath {
    param($archive)
    $entry = $archive.GetEntry('README.md')
    if ($null -eq $entry) { throw '基线发布包缺少 README.md。' }
    $entry.Delete()
}
Invoke-ExpectedRejection '删除必需说明' $mutatedPath '发布包缺少文件：README.md'

$mutatedPath = New-MutatedArchivePath '条目哈希不匹配'
Invoke-ZipMutation $mutatedPath {
    param($archive)
    $entry = $archive.GetEntry('config.yaml')
    if ($null -eq $entry) { throw '基线发布包缺少 config.yaml。' }
    $bytes = Read-ZipEntryBytes $entry
    $entry.Delete()
    $changed = [byte[]]::new($bytes.Length + 1)
    [System.Array]::Copy($bytes, $changed, $bytes.Length)
    $changed[$changed.Length - 1] = 0x23
    Add-ZipEntryBytes $archive 'config.yaml' $changed
}
Invoke-ExpectedRejection '条目哈希不匹配' $mutatedPath '发布包文件与构建产物不一致：config.yaml'

$mutatedPath = New-MutatedArchivePath '路径穿越条目'
Invoke-ZipMutation $mutatedPath {
    param($archive)
    Add-ZipEntryText $archive '../越界.txt' '越界内容'
}
Invoke-ExpectedRejection '路径穿越条目' $mutatedPath '发布包包含无效路径：../越界.txt'

$mutatedPath = New-MutatedArchivePath '重复条目'
Invoke-ZipMutation $mutatedPath {
    param($archive)
    Add-ZipEntryText $archive 'README.md' '重复内容'
}
Invoke-ExpectedRejection '重复条目' $mutatedPath '发布包包含重复条目：README.md'

Write-Host '发布包篡改拒绝回归全部通过。'
