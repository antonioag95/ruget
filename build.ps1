# Builds ruget for every target platform into .\dist.
#
# Usage:
#   .\build.ps1                     # version read from version.go
#   .\build.ps1 -Version 1.2.3      # override the stamped version
#   .\build.ps1 -Clean              # wipe dist first
#   .\build.ps1 -Only linux/amd64   # build a single target
#
# If script execution is blocked, run:
#   powershell -ExecutionPolicy Bypass -File .\build.ps1

[CmdletBinding()]
param(
    [string]$Version = "",
    [switch]$Clean,
    [string[]]$Only = @()
)

$ErrorActionPreference = "Stop"
$root = $PSScriptRoot
$dist = Join-Path $root "dist"

# Single source of truth: read the version from version.go unless overridden.
if (-not $Version) {
    $versionSrc = Get-Content (Join-Path $root "version.go") -Raw
    if ($versionSrc -match 'var version = "([^"]+)"') {
        $Version = $Matches[1]
    } else {
        throw "Could not read version from version.go; pass -Version explicitly."
    }
}

$targets = @(
    [pscustomobject]@{ OS = "windows"; Arch = "amd64"; Ext = ".exe" }
    [pscustomobject]@{ OS = "linux";   Arch = "amd64"; Ext = "" }
    [pscustomobject]@{ OS = "linux";   Arch = "arm64"; Ext = "" }
    [pscustomobject]@{ OS = "darwin";  Arch = "amd64"; Ext = "" }
    [pscustomobject]@{ OS = "darwin";  Arch = "arm64"; Ext = "" }
)

if ($Only.Count -gt 0) {
    $filter = $Only
    $targets = $targets | Where-Object { $filter -contains "$($_.OS)/$($_.Arch)" }
    if ($targets.Count -eq 0) {
        throw "No matching targets for -Only $($Only -join ', ')"
    }
}

if ($Clean -and (Test-Path $dist)) {
    Remove-Item -Recurse -Force $dist
}
New-Item -ItemType Directory -Path $dist -Force | Out-Null

# Save/restore the Go environment so the caller's session is untouched.
$prevGoOS = $env:GOOS
$prevGoArch = $env:GOARCH
$prevCgo = $env:CGO_ENABLED

Push-Location $root
try {
    $env:CGO_ENABLED = "0"
    $ldflags = "-s -w -X main.version=$Version"

    $results = @()
    foreach ($t in $targets) {
        $name = "ruget-$($t.OS)-$($t.Arch)$($t.Ext)"
        $out = Join-Path $dist $name

        Write-Host ("building {0} (v{1})..." -f $name, $Version) -ForegroundColor Cyan
        $env:GOOS = $t.OS
        $env:GOARCH = $t.Arch
        & go build -trimpath -ldflags $ldflags -o $out .
        if ($LASTEXITCODE -ne 0) {
            throw "go build failed for $name"
        }

        $results += [pscustomobject]@{
            Binary = $name
            Size   = "{0:N1} MB" -f ((Get-Item $out).Length / 1MB)
        }
    }

    Write-Host ""
    $results | Format-Table -AutoSize
    Write-Host ("Done - {0} artifact(s) in {1}" -f $results.Count, $dist) -ForegroundColor Green
}
finally {
    $env:GOOS = $prevGoOS
    $env:GOARCH = $prevGoArch
    $env:CGO_ENABLED = $prevCgo
    Pop-Location
}
