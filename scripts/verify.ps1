param(
    [string]$GoExecutable,
    [switch]$RunLifecycle
)

$ErrorActionPreference = 'Stop'
$projectRoot = (Resolve-Path (Join-Path $PSScriptRoot '..')).Path
$go = $GoExecutable
if (-not $go) {
    $installedGo = Get-Command go -ErrorAction SilentlyContinue
    if ($installedGo) { $go = $installedGo.Source }
}
if (-not $go -or -not (Test-Path -LiteralPath $go)) {
    throw '请安装编译器或通过 -GoExecutable 指定其完整路径。'
}
$go = (Resolve-Path -LiteralPath $go).Path
$outputRoot = Join-Path $projectRoot 'tmp\verification'
New-Item -ItemType Directory -Force -Path $outputRoot | Out-Null
$previousValues = @{}
foreach ($name in @('GOOS', 'GOARCH', 'CGO_ENABLED', 'ROUTER_TEST_BINARY')) {
    $previousValues[$name] = [System.Environment]::GetEnvironmentVariable($name, 'Process')
}
Push-Location $projectRoot
try {
    $env:GOOS = 'windows'
    $env:GOARCH = 'amd64'
    $env:CGO_ENABLED = '0'
    Remove-Item Env:ROUTER_TEST_BINARY -ErrorAction SilentlyContinue
    & $go mod verify
    if ($LASTEXITCODE -ne 0) { throw '依赖校验失败。' }
    & $go test ./... -count=1
    if ($LASTEXITCODE -ne 0) { throw '普通回归测试失败。' }
    & $go vet ./...
    if ($LASTEXITCODE -ne 0) { throw '静态检查失败。' }

    $env:GOOS = 'linux'
    & $go build -trimpath -o (Join-Path $outputRoot 'codex-router-linux') ./cmd
    if ($LASTEXITCODE -ne 0) { throw '非视窗命令交叉构建失败。' }
    $env:GOOS = 'windows'

    if ($RunLifecycle) {
        $testBinary = Join-Path $outputRoot 'CodexRouter.exe'
        & $go build -trimpath -ldflags '-H=windowsgui' -o $testBinary ./cmd
        if ($LASTEXITCODE -ne 0) { throw '生命周期测试程序构建失败。' }
        $env:ROUTER_TEST_BINARY = $testBinary
        & $go test ./cmd -run '^TestBackgroundLifecycleRealWindows$' -count=1 -v
        if ($LASTEXITCODE -ne 0) { throw '后台生命周期及代理恢复验证失败。' }
        Remove-Item Env:ROUTER_TEST_BINARY -ErrorAction SilentlyContinue
    }

    & (Join-Path $PSScriptRoot 'build.ps1') -GoExecutable $go
    & (Join-Path $PSScriptRoot 'verify-package-tests.ps1')
} finally {
    foreach ($name in $previousValues.Keys) {
        [System.Environment]::SetEnvironmentVariable($name, $previousValues[$name], 'Process')
    }
    Pop-Location
}
Write-Host '验证与发布打包完成。'
