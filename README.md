# Stata Remote

Stata Remote lets agents in WSL run the Stata installation on the Windows host. The Windows service runs each job in a separate Stata batch process and exposes the job API and WebDAV on the same port. Agents use `stata-remote` to run and inspect jobs and `rclone` to manage project files. Large datasets stay on Windows, where Stata reads and writes them directly.

## Build and install

Download the binaries from [GitHub Releases](https://github.com/jyfcrw/stata-remote/releases). Extract `stata-remote-server.exe` from `stata-remote-server-windows-amd64.zip` on x86-64 Windows 10 or 11. Extract `stata-remote` from `stata-remote-linux-amd64.tar.gz` in x86-64 Ubuntu on WSL and place it on your `PATH`.

- Windows requires an activated Stata installation and a project directory the service can read and write. 
- WSL requires WSL 2 in NAT mode, the Linux `stata-remote` client, and `rclone`. On Ubuntu, install rclone with `sudo apt install rclone`.
- Building from source requires Go 1.26 or later. Run these commands from the repository root to build the x86-64 Windows service and Linux client.

```bash
mkdir -p dist
CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build -buildvcs=false -trimpath -o dist/stata-remote-server.exe ./cmd/stata-remote-server
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -buildvcs=false -trimpath -o dist/stata-remote ./cmd/stata-remote

install -Dm755 dist/stata-remote "$HOME/.local/bin/stata-remote"
export PATH="$HOME/.local/bin:$PATH"
stata-remote --help
```

Copy `dist/stata-remote-server.exe` to Windows, for example to `D:\stata-remote\stata-remote-server.exe`. You can open `\\wsl.localhost` in Windows File Explorer to find the build artifacts in WSL. If you already have both binaries, skip the build and install them directly. [Microsoft WSL file access](https://learn.microsoft.com/windows/wsl/filesystems)

## Deploy on Windows

### Configure WSL networking and the firewall

This setup uses the default WSL 2 NAT mode. If mirrored mode is enabled, set `networkingMode` to `nat` under `[wsl2]` in `%UserProfile%\.wslconfig` on Windows. Merge the settings below into `/etc/wsl.conf` in WSL to disable Windows program interop and automatic mounting of fixed drives. Remove any DrvFs entries from `/etc/fstab`. To apply the changes, run `wsl.exe --shutdown` from Windows PowerShell and reopen WSL. [WSL configuration](https://learn.microsoft.com/windows/wsl/wsl-config)

```ini
[interop]
enabled=false
appendWindowsPath=false

[automount]
enabled=false
```

In an **elevated Windows PowerShell** session, allow inbound TCP `8765` on the WSL virtual network adapter. The development machine uses `vEthernet (WSL)`. On another machine, use `Get-NetAdapter -IncludeHidden` to confirm the interface name. Create the rule once. If you choose another port, use it in the firewall rule, the server's listen address, and the WSL connection URL. [Windows Firewall interface rules](https://learn.microsoft.com/powershell/module/netsecurity/new-netfirewallrule#-interfacealias)

```powershell
New-NetFirewallRule -Name 'StataRemoteWSL' -DisplayName 'Stata Remote from WSL' `
  -Direction Inbound -Action Allow -Protocol TCP -LocalPort 8765 `
  -InterfaceAlias 'vEthernet (WSL)'
```

### Start Stata Remote

Open the directory containing the server executable in a regular **Windows PowerShell** session. The commands below generate a random bearer token for the server and WSL to share. Copy it to WSL during initial setup. You can reuse the same token when restarting the service. [rclone WebDAV authentication](https://rclone.org/webdav/#webdav-bearer-token)

```powershell
$bytes = New-Object byte[] 32
$rng = [System.Security.Cryptography.RandomNumberGenerator]::Create()
$rng.GetBytes($bytes)
$rng.Dispose()
$env:STATA_REMOTE_TOKEN = [BitConverter]::ToString($bytes).Replace('-', '').ToLowerInvariant()
$env:STATA_REMOTE_TOKEN
```

Create or select a Windows project directory and replace `--stata` and `--workspace` with your actual paths. The server runs in the foreground under the current Windows user. Listening on `0.0.0.0:8765` accepts connections from the WSL NAT interface, with inbound access governed by the firewall rule above. For HTTPS, supply both `--tls-cert` and `--tls-key` and configure WSL to use an HTTPS address that matches the certificate. [WSL NAT networking](https://learn.microsoft.com/windows/wsl/networking#default-networking-mode-nat)

```powershell
Set-Location 'D:\stata-remote'
.\stata-remote-server.exe `
  --stata 'D:\Stata17\StataMP-64.exe' `
  --workspace 'D:\stata\study' `
  --listen '0.0.0.0:8765'
```

## Configure the WSL connection

Get the Windows NAT address from the WSL default route. Set the connection URL and token in the environment where the agent runs. On initial setup, `rclone config create` maps `stata-files:` to the server's WebDAV root, which serves the Windows project directory specified by `--workspace`. [WSL NAT networking](https://learn.microsoft.com/windows/wsl/networking#default-networking-mode-nat) · [rclone WebDAV configuration](https://rclone.org/webdav/)

```bash
windows_host_ip=$(ip -4 route show default | awk 'NR == 1 {print $3}')
export STATA_REMOTE_URL="http://${windows_host_ip}:8765"
read -rsp 'Stata Remote token: ' STATA_REMOTE_TOKEN
printf '\n'
export STATA_REMOTE_TOKEN
export RCLONE_WEBDAV_BEARER_TOKEN="$STATA_REMOTE_TOKEN"

rclone config create stata-files webdav url "${STATA_REMOTE_URL}/dav/" vendor other
stata-remote jobs
rclone lsf stata-files:
```

If `jobs` returns `[]`, the connection works and there are no active jobs. New terminals or agent sessions need the environment variables above. If the Windows NAT address changes after WSL restarts, look it up again, update `STATA_REMOTE_URL`, and use the command below to update the saved rclone remote. The Windows service can keep running. [Update an rclone remote](https://rclone.org/commands/rclone_config_update/)

```bash
rclone config update stata-files url "${STATA_REMOTE_URL}/dav/"
```

## Run and inspect jobs

Agents choose the names of their `.do` files, `.ado` files, and other dependencies and upload them to the project. For a first deployment check, save the following as `baseline.do`. `sysuse auto` loads a dataset bundled with Stata. Real jobs can read existing `.dta` files in the Windows project using relative paths.

```stata
local outdir : copy global STATA_REMOTE_OUTPUT_DIR
sysuse auto, clear
summarize price mpg
export delimited price mpg using "`outdir'/auto.csv", replace
```

Upload the script, create an output directory, and run the job. Both `--output-dir` and the `.do` path are relative to the Windows project root. Stata Remote passes the output directory in the Stata global macro `STATA_REMOTE_OUTPUT_DIR`. Use different output directories for concurrent jobs and for reruns whose earlier results you want to keep.

```bash
rclone copyto baseline.do stata-files:models/baseline.do
rclone mkdir stata-files:results/baseline-v1
stata-remote do --output-dir results/baseline-v1 models/baseline.do
rclone lsf stata-files:results/baseline-v1/
rclone copyto stata-files:results/baseline-v1/auto.csv ./auto.csv
```

`do` prints the job ID to stderr and writes the Stata log to stdout. It exits with `0` on success or a nonzero status with an explanation on failure. You can inspect jobs from another WSL terminal or reconnect after the client disconnects. Replace the placeholder below with the job ID from `do` or `jobs`.

```bash
stata-remote jobs
job_id='YOUR_JOB_ID'
stata-remote status "$job_id"
stata-remote logs "$job_id"
```

`jobs` lists jobs that are starting or running. `status` returns the requested job's state and result as JSON, including `stata_rc` and `process_exit_code` when available. `logs` reads the log written so far. Completed jobs remain available by ID while the Windows service is running. If the submission response is lost, retry with `do --job-id "$job_id"` and the original script, arguments, and output directory. The service reuses the existing job.

## Agent skill

The [Stata Remote skill](skills/stata-remote/SKILL.md) covers running `.do` files, working with project files through rclone, and recovering jobs by ID. Set up the WSL connection before using the skill.

With Node.js 22.20 or later on WSL, install the skill for Codex across all your projects using the Skills CLI. [Skills CLI reference](https://github.com/vercel-labs/skills/blob/main/docs/commands.md#skills-add)

```bash
npx skills add jyfcrw/stata-remote --skill stata-remote --agent codex --global --yes
```

## Troubleshooting

| Symptom | What to check |
| --- | --- |
| Connection timeout | Confirm that the WSL URL uses the current Windows NAT address, that the server listens on `0.0.0.0:8765`, and that Windows Firewall allows TCP `8765` on the WSL interface. |
| HTTP `401` | The Windows server and WSL client need the same `STATA_REMOTE_TOKEN`. rclone reads the same token from `RCLONE_WEBDAV_BEARER_TOKEN`. |
| Script or data file missing | Run `rclone lsf stata-files:` to inspect the remote project layout. Relative script and data paths resolve from the Windows project root. |
| Client disconnected | The Windows service manages Stata processes on Windows. Reconnect and use the original job ID to read its status and logs. |

The development machine passed integration testing with the WSL client, Windows service, Stata 17, and rclone through a temporary TCP forwarder. Direct NAT access and recovery after a full `wsl --shutdown` remain to be verified. See the architecture, interfaces, and test coverage in the [Phase 1 design](docs/design.md).

## CI and releases

The [GitHub Actions workflow](.github/workflows/ci.yml) runs the Go tests and `go vet` on Ubuntu and Windows for every push to `main`. The Ubuntu tests run with race detection.

To release, push a `vMAJOR.MINOR.PATCH` tag whose commit is already on `main`. Once the tests pass, Actions builds and publishes both amd64 archives to the same GitHub Release. Push the workflow to `main` before creating the first release tag.

```bash
git switch main
git pull --ff-only
git tag -a v0.1.0 -m 'Release v0.1.0'
git push origin v0.1.0
```
