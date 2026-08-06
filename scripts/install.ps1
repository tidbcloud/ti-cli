param(
    [string]$Version = "latest",
    [string]$InstallDir = "",
    [switch]$DryRun,
    [switch]$Yes
)

$ErrorActionPreference = "Stop"
$Repo = "tidbcloud/tdc"
$DefaultInstallDir = Join-Path (Join-Path $HOME ".tdc") "bin"

function Fail($Message) {
    Write-Error "tdc install [ERROR]: $Message"
    exit 1
}

function Info($Message) {
    Write-Output "  $Message"
}

function Warn($Message) {
    Write-Warning $Message
}

function Resolve-InstallDir {
    if (-not [string]::IsNullOrWhiteSpace($InstallDir)) {
        Info "Install dir: $InstallDir (from -InstallDir)"
        return $InstallDir
    }
    if (-not [string]::IsNullOrWhiteSpace($env:TDC_INSTALL_DIR)) {
        Info "Install dir: $env:TDC_INSTALL_DIR (from TDC_INSTALL_DIR)"
        return $env:TDC_INSTALL_DIR
    }

    Info "Install dir: $DefaultInstallDir (default user install)"
    return $DefaultInstallDir
}

function Bootstrap-Config {
    if ([string]::IsNullOrWhiteSpace($HOME)) {
        return
    }
    $ConfigDir = Join-Path $HOME ".tdc"
    $ConfigFile = Join-Path $ConfigDir "config"
    New-Item -ItemType Directory -Force -Path $ConfigDir | Out-Null
    if (-not (Test-Path $ConfigFile)) {
        @"
[default]
region_code = 'aws-us-east-1'
"@ | Set-Content -Path $ConfigFile -NoNewline
        Info "Bootstrapped $ConfigFile with default aws/us-east-1 placement"
    }
}

function Report-PathStatus {
    $active = Get-Command tdc -ErrorAction SilentlyContinue | Select-Object -First 1
    if (-not $active -or -not $active.Source) {
        Warn "tdc is installed at $Target, but $InstallDir is not on your PATH"
    } elseif ($active.Source -ne $Target) {
        Warn "PATH shadowing detected: tdc resolves to $($active.Source)"
        Warn "Installed binary: $Target"
    } else {
        return
    }
    Warn "Add tdc to the current PowerShell PATH:"
    Warn ('$env:Path = "{0};$env:Path"' -f $InstallDir)
    Warn "Add $InstallDir to your user PATH to persist it"
}

function Print-Regions {
    Write-Output ""
    Write-Output "  Config regions:"
    Write-Output "    aws-us-east-1, aws-us-west-2, aws-eu-central-1, aws-ap-northeast-1, aws-ap-southeast-1"
    Write-Output "    ali-ap-southeast-1"
    Write-Output ""
    Write-Output "  tdc fs regions:"
    try {
        $manifest = Invoke-RestMethod -Uri "https://drive9.ai/manifest/regions/drive9-regions.json"
        $regions = @($manifest.regions | Where-Object { $_.mode -eq "tidb_cloud_native" } | ForEach-Object {
            $prefix = $_.cloud_provider
            if ($prefix -eq "alicloud" -or $prefix -eq "alibaba_cloud") {
                $prefix = "ali"
            }
            "    $prefix-$($_.tidb_region)"
        } | Sort-Object -Unique)
        if ($regions.Count -gt 0) {
            $regions | ForEach-Object { Write-Output $_ }
            return
        }
    } catch {
    }
    Write-Output "    aws-us-east-1, aws-ap-southeast-1"
    Warn "Could not fetch the latest tdc fs region manifest; run tdc fs check-file-system after configure"
}

function Print-NextSteps {
    Write-Output ""
    Write-Output "  Get started:"
    Write-Output ""
    Write-Output "    1. Add tdc to PATH"
    Write-Output ('       $env:Path = "{0};$env:Path"' -f $InstallDir)
    Write-Output ""
    Write-Output "    2. Configure credentials"
    Write-Output "       tdc configure"
    Write-Output ""
    Write-Output "    3. List projects"
    Write-Output "       tdc organization list-projects --output text"
    Write-Output ""
    Write-Output "    4. Create or check tdc fs"
    Write-Output '       $env:TDC_FS_FILE_SYSTEM_ID = tdc fs create-file-system --query file_system_id --output text'
    Write-Output "       tdc fs check-file-system --output text"
    Write-Output ""
    Write-Output "    5. Mount tdc fs when FUSE is available"
    Write-Output '       tdc fs mount-file-system --file-system-id $env:TDC_FS_FILE_SYSTEM_ID --mount-path ./workspace'
    Write-Output ""
    Write-Output "  Docs: https://github.com/tidbcloud/tdc"
}

function Print-TelemetryNotice {
    Write-Output ""
    Write-Output "  Anonymous telemetry:"
    Write-Output ""
    Write-Output "  tdc collects anonymous command usage and reliability telemetry in release builds."
    Write-Output "  It collects command and flag names (never values), exit and stable error codes,"
    Write-Output "  duration, region, tdc version, OS, and architecture."
    Write-Output ""
    Write-Output "  It never collects credentials, tokens, SQL text, file paths or contents,"
    Write-Output "  command output, API response payloads, or cloud resource IDs."
    Write-Output ""
    Write-Output "  To disable telemetry, create or edit ~/.tdc/.preferences:"
    Write-Output ""
    Write-Output "    [telemetry]"
    Write-Output "    enabled = false"
    Write-Output ""
    Write-Output "  For one process: TDC_TELEMETRY=off tdc ..."
}

$arch = [System.Runtime.InteropServices.RuntimeInformation]::OSArchitecture
if ($arch -ne [System.Runtime.InteropServices.Architecture]::X64) {
    Fail "unsupported Windows architecture: $arch"
}

$InstallDir = Resolve-InstallDir

if ($Version -eq "latest") {
    $ReleaseBase = "https://github.com/$Repo/releases/latest/download"
} else {
    if ($Version.StartsWith("v")) {
        $Tag = $Version
    } else {
        $Tag = "v$Version"
    }
    $ReleaseBase = "https://github.com/$Repo/releases/download/$Tag"
}

$Artifact = "tdc_windows_amd64.zip"
$ArchiveUrl = "$ReleaseBase/$Artifact"
$ChecksumsUrl = "$ReleaseBase/tdc_checksums.txt"
$Target = Join-Path $InstallDir "tdc.exe"
$CompanionArtifact = "drive9-windows-amd64.exe"
$CompanionUrl = "https://drive9.ai/releases/$CompanionArtifact"
$CompanionChecksumsUrl = "https://drive9.ai/releases/checksums.txt"
$CompanionTarget = Join-Path $InstallDir "tdc-drive9.exe"

if ($DryRun) {
    Write-Output "tdc install dry-run"
    Write-Output "version: $Version"
    Write-Output "artifact: $Artifact"
    Write-Output "archive_url: $ArchiveUrl"
    Write-Output "checksums_url: $ChecksumsUrl"
    Write-Output "target: $Target"
    Write-Output "companion_artifact: $CompanionArtifact"
    Write-Output "companion_url: $CompanionUrl"
    Write-Output "companion_target: $CompanionTarget"
    Write-Output ('path_command: $env:Path = "{0};$env:Path"' -f $InstallDir)
    exit 0
}

if ((Test-Path $Target) -and -not $Yes) {
    $answer = Read-Host "Replace existing $Target? [y/N]"
    if ($answer -notin @("y", "Y", "yes", "YES")) {
        Write-Error "tdc install [ERROR]: cancelled"
        exit 130
    }
}

New-Item -ItemType Directory -Force -Path $InstallDir | Out-Null
$TempDir = New-Item -ItemType Directory -Path (Join-Path ([System.IO.Path]::GetTempPath()) ("tdc-install-" + [System.Guid]::NewGuid().ToString()))

try {
    $ArchivePath = Join-Path $TempDir.FullName $Artifact
    $ChecksumsPath = Join-Path $TempDir.FullName "tdc_checksums.txt"
    $CompanionPath = Join-Path $TempDir.FullName $CompanionArtifact
    $CompanionChecksumsPath = Join-Path $TempDir.FullName "drive9_checksums.txt"
    Invoke-WebRequest -Uri $ArchiveUrl -OutFile $ArchivePath
    Invoke-WebRequest -Uri $ChecksumsUrl -OutFile $ChecksumsPath
    Invoke-WebRequest -Uri $CompanionUrl -OutFile $CompanionPath
    Invoke-WebRequest -Uri $CompanionChecksumsUrl -OutFile $CompanionChecksumsPath

    $checksumLine = Get-Content $ChecksumsPath | Where-Object { $_ -match "\s+$([regex]::Escape($Artifact))$" } | Select-Object -First 1
    if (-not $checksumLine) {
        Fail "checksum for $Artifact not found"
    }
    $Expected = ($checksumLine -split "\s+")[0].ToLowerInvariant()
    $Actual = (Get-FileHash -Algorithm SHA256 -Path $ArchivePath).Hash.ToLowerInvariant()
    if ($Expected -ne $Actual) {
        Fail "checksum mismatch for $Artifact"
    }

    $companionChecksumLine = Get-Content $CompanionChecksumsPath | Where-Object { $_ -match "\s+$([regex]::Escape($CompanionArtifact))$" } | Select-Object -First 1
    if (-not $companionChecksumLine) {
        Fail "checksum for $CompanionArtifact not found"
    }
    $CompanionExpected = ($companionChecksumLine -split "\s+")[0].ToLowerInvariant()
    $CompanionActual = (Get-FileHash -Algorithm SHA256 -Path $CompanionPath).Hash.ToLowerInvariant()
    if ($CompanionExpected -ne $CompanionActual) {
        Fail "checksum mismatch for $CompanionArtifact"
    }

    Expand-Archive -Path $ArchivePath -DestinationPath $TempDir.FullName -Force
    $Extracted = Get-ChildItem -Path $TempDir.FullName -Recurse -Filter "tdc.exe" | Select-Object -First 1
    if (-not $Extracted) {
        Fail "archive did not contain tdc.exe"
    }

    Move-Item -Force -Path $Extracted.FullName -Destination $Target
    Move-Item -Force -Path $CompanionPath -Destination $CompanionTarget
    & $Target --version
    Write-Output "tdc installed to $Target"
    Write-Output "tdc fs companion installed to $CompanionTarget"
    Bootstrap-Config
    Report-PathStatus
    Print-Regions
    Print-TelemetryNotice
    Print-NextSteps
} finally {
    Remove-Item -Recurse -Force $TempDir.FullName -ErrorAction SilentlyContinue
}
