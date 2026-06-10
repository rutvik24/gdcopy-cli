$githubRepo = "rutvik24/gdcopy-cli"
$binaryName = "gdcopy"

# Detect Architecture
$arch = "amd64"
if ([System.Environment]::Is64BitOperatingSystem -and ($env:PROCESSOR_ARCHITECTURE -eq "ARM64" -or $env:PROCESSOR_ARCHITEW6432 -eq "ARM64")) {
    $arch = "arm64"
}

# Allow specifying a custom version via environment variable
$version = $env:VERSION
if (-not $version) {
    $version = "latest"
}

if ($version -eq "latest") {
    $downloadUrl = "https://github.com/$githubRepo/releases/latest/download/gdcopy-windows-$arch.zip"
} else {
    # Ensure tag format starts with 'v' if omitted
    if ($version -notlike "v*") {
        $version = "v$version"
    }
    $downloadUrl = "https://github.com/$githubRepo/releases/download/$version/gdcopy-windows-$arch.zip"
}

Write-Host "Installing $binaryName ($version) for Windows ($arch)..."
Write-Host "Downloading from $downloadUrl"

# Create temp workspace
$tempDir = Join-Path $env:TEMP "gdrive-cli-install"
if (Test-Path $tempDir) { Remove-Item -Recurse -Force $tempDir }
New-Item -ItemType Directory -Path $tempDir | Out-Null

$zipPath = Join-Path $tempDir "archive.zip"

# Download the zip file
try {
    Invoke-WebRequest -Uri $downloadUrl -OutFile $zipPath -ErrorAction Stop
} catch {
    Write-Error "Failed to download binary. The release might not exist yet or URL is incorrect: $_"
    exit 1
}

# Extract binary
Write-Host "Extracting files..."
Expand-Archive -Path $zipPath -DestinationPath $tempDir -Force

# Create installation directory in user profile
$installDir = Join-Path $env:USERPROFILE ".gdcopy"
if (!(Test-Path $installDir)) {
    New-Item -ItemType Directory -Path $installDir | Out-Null
}

$exeSource = Join-Path $tempDir "$binaryName.exe"
$exeDest = Join-Path $installDir "$binaryName.exe"

Move-Item -Path $exeSource -Destination $exeDest -Force

Write-Host "Successfully installed $binaryName.exe to $installDir"

# Add to user PATH if not present
$userPath = [System.Environment]::GetEnvironmentVariable("PATH", "User")
if ($userPath -notlike "*$installDir*") {
    Write-Host "Adding $installDir to User PATH..."
    $newPath = $userPath + ";" + $installDir
    [System.Environment]::SetEnvironmentVariable("PATH", $newPath, "User")
    $env:PATH = $env:PATH + ";" + $installDir
    Write-Host "Please restart your terminal/shell to refresh environment variables."
} else {
    Write-Host "Path is already configured in environment variables."
}

Write-Host "Installation complete! Try running '$binaryName -version' in a new terminal."
