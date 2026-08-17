param(
    [string]$Version = "latest",
    [string]$InstallDir = "",
    [switch]$DryRun,
    [switch]$Yes
)

$ErrorActionPreference = "Stop"
$Repo = "tidbcloud/ti-cli"
$DefaultInstallDir = Join-Path (Join-Path $HOME ".ti") "bin"

function Fail($Message) {
    Write-Error "ti install [ERROR]: $Message"
    exit 1
}

function Info($Message) {
    Write-Output "  $Message"
}

function Warn($Message) {
    Write-Warning $Message
}

function Resolve-InstallDir {
    if (-not [string]::IsNullOrWhiteSpace($env:TI_INSTALL_DIR) -and
        -not [string]::IsNullOrWhiteSpace($env:TDC_INSTALL_DIR) -and
        $env:TI_INSTALL_DIR -ne $env:TDC_INSTALL_DIR) {
        Fail "TI_INSTALL_DIR and deprecated TDC_INSTALL_DIR contain different values"
    }
    if (-not [string]::IsNullOrWhiteSpace($InstallDir)) {
        Info "Install dir: $InstallDir (from -InstallDir)"
        return $InstallDir
    }
    if (-not [string]::IsNullOrWhiteSpace($env:TI_INSTALL_DIR)) {
        Info "Install dir: $env:TI_INSTALL_DIR (from TI_INSTALL_DIR)"
        return $env:TI_INSTALL_DIR
    }
    if (-not [string]::IsNullOrWhiteSpace($env:TDC_INSTALL_DIR)) {
        Info "Install dir: $env:TDC_INSTALL_DIR (from deprecated TDC_INSTALL_DIR)"
        return $env:TDC_INSTALL_DIR
    }

    Info "Install dir: $DefaultInstallDir (default user install)"
    return $DefaultInstallDir
}

function Invoke-HomeMigration($MigrationBinary) {
    if ([string]::IsNullOrWhiteSpace($HOME)) {
        return
    }
    Info "Checking for state to migrate from $HOME\.tdc"
    & $MigrationBinary __migrate-home
    if ($LASTEXITCODE -ne 0) {
        Fail "state migration failed; the legacy directory was left unchanged"
    }
}

function Bootstrap-Config {
    if ([string]::IsNullOrWhiteSpace($HOME)) {
        return
    }
    $ConfigDir = Join-Path $HOME ".ti"
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
    $active = Get-Command ti -ErrorAction SilentlyContinue | Select-Object -First 1
    if (-not $active -or -not $active.Source) {
        Warn "ti is installed at $Target, but $InstallDir is not on your PATH"
    } elseif ($active.Source -ne $Target) {
        Warn "PATH shadowing detected: ti resolves to $($active.Source)"
        Warn "Installed binary: $Target"
    } else {
        return
    }
    Warn "Add ti to the current PowerShell PATH:"
    Warn ('$env:Path = "{0};$env:Path"' -f $InstallDir)
    Warn "Add $InstallDir to your user PATH to persist it"
}

function Print-Regions {
    Write-Output ""
    Write-Output "  Config regions:"
    Write-Output "    aws-us-east-1, aws-us-west-2, aws-eu-central-1, aws-ap-northeast-1, aws-ap-southeast-1"
    Write-Output "    alicloud-ap-southeast-1"
    Write-Output ""
    Write-Output "  ti fs regions:"
    Write-Output "    aws-us-east-1, aws-ap-southeast-1, aws-us-west-2, alicloud-ap-southeast-1"
}

function Print-NextSteps {
    Write-Output ""
    Write-Output "  Get started:"
    Write-Output ""
    Write-Output "    1. Add ti to PATH"
    Write-Output ('       $env:Path = "{0};$env:Path"' -f $InstallDir)
    Write-Output ""
    Write-Output "    2. Configure credentials"
    Write-Output "       ti configure"
    Write-Output ""
    Write-Output "    3. Create a Starter database"
    Write-Output "       ti db create-db-cluster --db-cluster-type starter --db-cluster-name my-database --wait"
    Write-Output ""
    Write-Output "    4. Create or check ti fs"
    Write-Output '       $env:TI_FS_FILE_SYSTEM_ID = ti fs create-file-system --query file_system_id --output text'
    Write-Output "       ti fs check-file-system --output text"
    Write-Output ""
    Write-Output "    5. Mount ti fs when FUSE is available"
    Write-Output '       ti fs mount-file-system --file-system-id $env:TI_FS_FILE_SYSTEM_ID --mount-path ./workspace'
    Write-Output ""
    Write-Output "  Docs: https://github.com/tidbcloud/ti-cli"
}

function Print-TelemetryNotice {
    Write-Output ""
    Write-Output "  Anonymous telemetry:"
    Write-Output ""
    Write-Output "  ti collects anonymous command usage and reliability telemetry in release builds."
    Write-Output "  It collects command and flag names (never values), exit and stable error codes,"
    Write-Output "  duration, region, ti version, OS, and architecture."
    Write-Output ""
    Write-Output "  It never collects credentials, tokens, SQL text, file paths or contents,"
    Write-Output "  command output, API response payloads, or cloud resource IDs."
    Write-Output ""
    Write-Output "  To disable telemetry, create or edit ~/.ti/.preferences:"
    Write-Output ""
    Write-Output "    [telemetry]"
    Write-Output "    enabled = false"
    Write-Output ""
    Write-Output "  For one process: TI_TELEMETRY=off ti ..."
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

$Artifact = "ti_windows_amd64.zip"
$ArchiveUrl = "$ReleaseBase/$Artifact"
$ChecksumsUrl = "$ReleaseBase/ti_checksums.txt"
$Target = Join-Path $InstallDir "ti.exe"
$CompanionArtifact = "drive9-windows-amd64.exe"
$CompanionUrl = "https://drive9.ai/releases/$CompanionArtifact"
$CompanionChecksumsUrl = "https://drive9.ai/releases/checksums.txt"
$CompanionTarget = Join-Path $InstallDir "ti-drive9.exe"

if ($DryRun) {
    Write-Output "ti install dry-run"
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
        Write-Error "ti install [ERROR]: cancelled"
        exit 130
    }
}

$TempDir = New-Item -ItemType Directory -Path (Join-Path ([System.IO.Path]::GetTempPath()) ("ti-install-" + [System.Guid]::NewGuid().ToString()))

try {
    $ArchivePath = Join-Path $TempDir.FullName $Artifact
    $ChecksumsPath = Join-Path $TempDir.FullName "ti_checksums.txt"
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
    $Extracted = Get-ChildItem -Path $TempDir.FullName -Recurse -Filter "ti.exe" | Select-Object -First 1
    if (-not $Extracted) {
        Fail "archive did not contain ti.exe"
    }

    Invoke-HomeMigration $Extracted.FullName
    New-Item -ItemType Directory -Force -Path $InstallDir | Out-Null
    Move-Item -Force -Path $Extracted.FullName -Destination $Target
    Move-Item -Force -Path $CompanionPath -Destination $CompanionTarget
    & $Target --version
    Write-Output "ti installed to $Target"
    Bootstrap-Config
    Report-PathStatus
    Print-Regions
    Print-TelemetryNotice
    Print-NextSteps
} finally {
    Remove-Item -Recurse -Force $TempDir.FullName -ErrorAction SilentlyContinue
}
