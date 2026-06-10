# gdcopy

A Go command-line interface tool to navigate through Google Drive folders (including files and folders shared with you), recursively copy folders concurrently, perform bulk file renaming, and manage files on Google Drive.

## Features

- **Google OAuth2 Authentication**: Dynamic local loopback server captures the authentication callback. Uses a custom token source that automatically refreshes expired credentials and saves them to `token.json` for persistent access.
- **Concurrent & High-Performance Copying**: Copies files in parallel (up to 10 concurrent requests using a semaphore limiter) on each page (up to 100 files at a time).
- **Collision Resolution Strategies**: When copying files to a destination containing duplicate file names:
  - **Skip**: Leaves the existing file in the destination untouched and skips the copy.
  - **Overwrite**: Deletes the conflicting destination file and uploads the new copy.
  - **Add Suffix**: Renames the new copy to `filename (1).ext`, `filename (2).ext`, etc., using thread-safe name reservation.
- **Folder Merging**: Automatically merges directory structures when copying a folder into a destination parent that already has a folder of the same name.
- **Subfolder Creation**: Create new directories directly in the destination parent from the CLI interface.
- **Bulk File Renaming**: Choose from suffixing, prefixing, find-and-replace, and sequential numbering strategies with dry-run previews before applying changes.
- **File Inspector**: Browse files and view their human-readable sizes.

---

## Google Cloud Console Setup Instructions

To run this application, you must provide a `credentials.json` file. Follow these steps to obtain one:

1. **Go to the Google Cloud Console**: [https://console.cloud.google.com/](https://console.cloud.google.com/)
2. **Create a Project**: Click the project dropdown, select **New Project**, and give it a name (e.g., `gdcopy`).
3. **Enable Google Drive API**:
   - Go to **APIs & Services** > **Library**.
   - Search for **Google Drive API** and click **Enable**.
4. **Configure OAuth Consent Screen**:
   - Go to **APIs & Services** > **OAuth Consent Screen**.
   - Choose User Type: **External** and click **Create**.
   - Fill in the required **App Information** and **Developer Contact Information**.
   - Click **Save and Continue** until you reach **Test Users**.
   - Under **Test Users**, click **Add Users** and enter your Google account email address.
   - Click **Save and Continue**.
5. **Create OAuth Client ID**:
   - Go to **APIs & Services** > **Credentials**.
   - Click **+ Create Credentials** > **OAuth client ID**.
   - Set the Application type to **Desktop App** and name it.
   - Click **Create**.
   - Download the client configuration JSON file.
6. **Deploy Credentials**:
   - Rename the downloaded file to `credentials.json`.
   - Place it in the root directory of this project.

---

## Installation and Running

### 1. Quick Installation (Recommended)

You can download and install the tool automatically using a single command. The installation scripts automatically detect your platform (OS) and CPU architecture.

#### Install Latest Release
* **macOS / Linux**:
  ```bash
  curl -fsSL https://raw.githubusercontent.com/rutvik24/gdcopy-cli/main/install.sh | bash
  ```
* **Windows (PowerShell)**:
  ```powershell
  iwr -useb https://raw.githubusercontent.com/rutvik24/gdcopy-cli/main/install.ps1 | iex
  ```

#### Install a Specific Version
You can set a `VERSION` environment variable to download a specific tag or version (e.g. `v1.0.0`):
* **macOS / Linux**:
  ```bash
  curl -fsSL https://raw.githubusercontent.com/rutvik24/gdcopy-cli/main/install.sh | VERSION=v1.0.0 bash
  ```
* **Windows (PowerShell)**:
  ```powershell
  $env:VERSION="v1.0.0"; iwr -useb https://raw.githubusercontent.com/rutvik24/gdcopy-cli/main/install.ps1 | iex
  ```

---

### 2. Manual Setup & Run (For Development)

#### Prerequisites
- Go (v1.18 or higher recommended, compiles out-of-the-box on v1.26+)
- Internet access (for Google Drive API calls)

#### Setup & Run
1. Clone or download this project.
2. Build and run the project:

   ```bash
   # Run directly
   go run .

   # Or compile a binary
   go build -o gdcopy
   ./gdcopy
   ```

---

## Uninstallation

If you installed `gdcopy` using the quick installation scripts, you can completely uninstall it by following the instructions below for your platform.

### macOS / Linux

To remove the binary:
```bash
sudo rm /usr/local/bin/gdcopy
```

### Windows (PowerShell)

Run the following commands in PowerShell to remove the installation directory and clean up your `PATH` environment variable:

```powershell
# 1. Remove the binary and directory
Remove-Item -Recurse -Force "$HOME\.gdcopy"

# 2. Remove from User PATH environment variable
$installDir = "$HOME\.gdcopy"
$userPath = [System.Environment]::GetEnvironmentVariable("PATH", "User")
if ($userPath -like "*$installDir*") {
    $newPath = ($userPath -split ";" | Where-Object { $_ -ne $installDir }) -join ";"
    [System.Environment]::SetEnvironmentVariable("PATH", $newPath, "User")
    Write-Host "Successfully removed $installDir from User PATH."
}
```
*Note: Restart any open terminals/shells for the PATH changes to take effect.*

### Cleaning Up Authentication Cache (All Platforms)

`gdcopy` saves the encrypted OAuth credentials to `token.json` in the directory where the command is executed. If you wish to clear all cached credentials, locate and delete these `token.json` files:
```bash
rm token.json
```

---

### Command Line Options

The tool supports the following command line flags:

```bash
# Print the version of the tool and exit
gdcopy -v
# or
gdcopy -version

# Run the tool specifying a custom path to credentials.json
gdcopy -c /path/to/my/credentials.json
# or
gdcopy -credentials /path/to/my/credentials.json
```

---

### Interactive Credentials Setup

If the `credentials.json` file is not found at the default path (`credentials.json` in the current folder) or the path specified via `-c`/`-credentials` flag, the tool will start an interactive helper in the terminal:
1. **Provide a custom path**: Allows you to enter the path to the credentials file dynamically.
2. **Show step-by-step setup instructions**: Displays comprehensive guides on how to create a Google Cloud Project, enable the Google Drive API, configure the OAuth Consent Screen (including scopes/permissions and adding test users), and download the credentials file.

### Running Tests

To run the unit tests (which cover the exponential backoff retry and rate limit handling):

```bash
go test -v
```

### Windows-Specific Guidelines

If you are running or building this tool on Windows, keep the following in mind:

#### 1. Compiling & Executing
When building the binary, Go compiles it into a `.exe` executable:
```cmd
# Compile the binary
go build -o gdcopy.exe

# Run the binary in PowerShell
.\gdcopy.exe

# Run the binary in Command Prompt (CMD)
gdcopy.exe
```

#### 2. Terminal Environment
The interactive terminal UI is powered by `promptui`, which uses ANSI escape codes for coloring and styling as well as Unicode emojis (like 📂 and 👉).
- **Recommended**: Use **Windows Terminal** (standard on Windows 11, downloadable for Windows 10) or **Git Bash**. They render colors and emojis natively.
- **Legacy Console (CMD/PowerShell)**: If you use the older console host, colors or emojis might appear as broken characters (like `` or `?`). 

#### 3. Windows Firewall Prompt
When signing in for the first time, a Windows Defender Firewall warning may pop up.
- This alert is triggered because the tool boots up a temporary, local loopback web server (`127.0.0.1:<random-port>`) to capture the OAuth code from your browser.
- The server only binds locally, so you can safely allow access or dismiss the prompt.

---


## File Structure

- `main.go`: Application initialization.
- `oauth.go`: Manages credentials, tokens, dynamic loopback server, and auto-refresh persistence.
- `drive.go`: Handles core Google Drive API calls, concurrent copy routines, collision resolution, and renaming.
- `ui.go`: Handles terminal prompts, subfolder creation dialogs, and navigation states.
- `drive_test.go`: Unit tests for helper utilities.
- `.gitignore`: Ensures secrets (`credentials.json`, `token.json`) and compiled binaries are not committed.
