# Stata Remote Phase 1 design

Stata Remote lets agents in WSL run a specific Stata installation on the Windows host over the network. In Phase 1, a single Windows service runs `.do` files with the installed Stata executable and serves project files through WebDAV on the same port. Large datasets stay in the Windows project directory, where Stata reads and writes them directly. Agents upload scripts and retrieve outputs as needed.

## Requirements and scope

- Reuse the configured Windows Stata installation and preserve normal use of Stata commands, `.do` files, and `.ado` files. The WSL client's `stata-remote do` command follows the local batch workflow.
- Use WSL 2 in NAT mode. Deployment disables Windows program interop and DrvFs mounts. The agent accesses execution and file operations over the network. Stata itself runs on the Windows host. Disabling interop prevents WSL from launching Windows programs directly. Submitted Stata scripts run with the Windows service account's permissions. [WSL interop settings](https://learn.microsoft.com/windows/wsl/wsl-config#interop-settings) · [WSL NAT networking](https://learn.microsoft.com/windows/wsl/networking#default-networking-mode-nat) · [Stata system commands](https://www.stata.com/manuals/dshell.pdf)
- Configure one Windows project directory as both the Stata working directory and the WebDAV root. The agent chooses the directory layout and script names and uses WebDAV to manage scripts, dependencies, data, and outputs. Existing `.dta` files stay on Windows for later jobs to reuse. Each invocation starts an independent Stata process and runs the submitted script path.
- Agents can list active jobs or use a known job ID to retrieve status and logs. The WSL terminal shows the original Stata log, the job result, and any available Stata return code so the agent can revise the script and resubmit it. For larger directories, rclone can filter recursively by filename.
- Start the service directly on Windows and let it own the Stata processes it launches. The Windows service and accepted jobs keep running if the agent exits, the connection drops, or `wsl --shutdown` stops WSL. After reconnecting, the agent can look up status, read missed logs, and retrieve outputs using the job ID.

Phase 1 covers batch jobs with a `.do` entry point. A persistent Windows PyStata session is an option if independent jobs later need to share in-memory data and estimation results. GUI and interactive terminal support can be evaluated when either workflow is needed. [Official PyStata API](https://www.stata.com/python/pystata19/stata.html)

## Technology choices and distribution

| Component | Phase 1 choice | Rationale |
| --- | --- | --- |
| Windows execution | Installed Stata executable with `/e do` | Uses the official Windows batch entry point. Stata writes a text log and exits automatically when the script finishes. [Stata batch mode on Windows](https://www.stata.com/support/faqs/windows/batch-mode/) |
| Service and file access | Go with `net/http` and `golang.org/x/net/webdav` | Mounts the WebDAV handler at `/dav/` and shares the listener and authentication with the job API. The library handles WebDAV and the service confines file operations to the Windows project directory. [Go WebDAV](https://pkg.go.dev/golang.org/x/net/webdav) |
| WSL file client | rclone WebDAV backend | `copyto` uploads and downloads, `lsf` lists directories, `lsjson` provides structured listings with recursive filtering, and `cat` displays text outputs. [rclone WebDAV](https://rclone.org/webdav/) · [rclone lsjson](https://rclone.org/commands/rclone_lsjson/) |
| WSL job client | Go-built Linux executable `stata-remote` | `do` submits a `.do` file, `jobs` lists active jobs, and `status` and `logs` retrieve information by job ID. |

WebDAV is a standard HTTP extension that works with existing file clients. FTP uses separate control and data connections even when embedded in the same process with `pyftpdlib`. A standalone SFTP service also supports the required file operations and requires its own listening port and authentication. [WebDAV specification](https://www.rfc-editor.org/rfc/rfc4918) · [pyftpdlib API](https://pyftpdlib.readthedocs.io/en/latest/api.html) · [rclone SFTP service](https://rclone.org/commands/rclone_serve_sftp/)

The Go build produces `stata-remote-server.exe` for Windows and the Linux executable `stata-remote` for WSL. WSL can build both binaries because the code uses pure Go. The binaries run without the Go toolchain. Windows uses the existing Stata installation, and WSL uses rclone. [Go cross-compilation](https://go.dev/wiki/WindowsCrossCompiling)

Build from the repository root with Go 1.26 or later. These commands target the x86-64 Windows and WSL development environments.

```bash
mkdir -p dist
CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build -buildvcs=false -trimpath -o dist/stata-remote-server.exe ./cmd/stata-remote-server
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -buildvcs=false -trimpath -o dist/stata-remote ./cmd/stata-remote
```

A local prototype verified FastAPI, `a2wsgi`, and WsgiDAV in Python. PyInstaller can package this stack as a single `.exe` on Windows. The executable extracts its bundled dependencies to a temporary directory at startup. Rust can also produce a Windows `.exe`. Official cross-compilation support for the MSVC target is limited to Windows hosts. Go meets the service's HTTP, CLI, and process management needs while supporting embedded WebDAV and cross-compilation from WSL. [PyInstaller build requirements](https://pyinstaller.org/en/stable/index.html) · [PyInstaller one-file mode](https://pyinstaller.org/en/stable/operating-mode.html#how-the-one-file-program-works) · [Rust Windows MSVC documentation](https://doc.rust-lang.org/rustc/platform-support/windows-msvc.html)

WebDAV and the job API share an HTTP or HTTPS listener. Stata reads and writes data on Windows. Job requests, logs, directory listings, and files explicitly transferred by the agent cross the NAT connection. When HTTPS is enabled, only these network operations incur TLS overhead.

## Windows service and WSL configuration

Configure the service with startup flags. Consider a configuration file if the number of settings grows. `--stata`, `--workspace`, and `--listen` specify the executable, the single project directory, and the listen address. The example uses a fixed port on the IPv4 wildcard address so the listener can accept connections after the WSL NAT address changes. Restrict inbound access on that port to the WSL virtual network interface before starting the service. [WSL NAT networking](https://learn.microsoft.com/windows/wsl/networking#default-networking-mode-nat) · [Windows Firewall interface rules](https://learn.microsoft.com/powershell/module/netsecurity/new-netfirewallrule#-interfacealias)

```powershell
.\stata-remote-server.exe `
  --stata 'D:\Stata17\StataMP-64.exe' `
  --workspace 'D:\stata\study' `
  --listen '0.0.0.0:8765'
```

The development machine uses `vEthernet (WSL)` as its NAT interface. If you need to allow inbound connections, create an interface-scoped rule from an elevated Windows PowerShell session. Use the actual interface name on each machine. [Windows Firewall interface rules](https://learn.microsoft.com/powershell/module/netsecurity/new-netfirewallrule#-interfacealias)

```powershell
New-NetFirewallRule -DisplayName 'Stata Remote from WSL' -Direction Inbound `
  -Action Allow -Protocol TCP -LocalPort 8765 -InterfaceAlias 'vEthernet (WSL)'
```

The Windows service and WSL agent read the same bearer token from `STATA_REMOTE_TOKEN`. Authentication applies to both the job API and `/dav/`. The service account needs permission to run the configured Stata executable, read inputs, and write results. The WebDAV adapter uses Go's `os.Root` for project-relative operations. Verify symbolic link and junction handling on the target Windows machine. The HTTP example assumes inbound access is restricted to WSL. Set `--tls-cert` and `--tls-key` to enable HTTPS on the same listener. [Go WebDAV directory semantics](https://pkg.go.dev/golang.org/x/net/webdav#Dir) · [Go os.Root](https://pkg.go.dev/os#Root)

In WSL, `stata-files` is the rclone remote name for the Windows service's `/dav/` endpoint. With `D:\stata\study` as the Windows project directory, `stata-files:results/baseline-v1/result.dta` maps to `D:\stata\study\results\baseline-v1\result.dta`. This example configures the remote and checks the directory listing over HTTP on port `8765`. [Create an rclone remote](https://rclone.org/commands/rclone_config_create/) · [rclone WebDAV authentication](https://rclone.org/webdav/#webdav-bearer-token)

```bash
windows_host_ip=$(ip -4 route show default | awk 'NR == 1 {print $3}')
export STATA_REMOTE_URL="http://${windows_host_ip}:8765"
export RCLONE_WEBDAV_BEARER_TOKEN="$STATA_REMOTE_TOKEN"

rclone config create stata-files webdav url "${STATA_REMOTE_URL}/dav/" vendor other
rclone lsf stata-files:
```

If the host address changes after WSL restarts, look it up again in WSL and set `STATA_REMOTE_URL`. Update the saved rclone remote with `rclone config update stata-files url "${STATA_REMOTE_URL}/dav/"`. The Windows service keeps listening on the same port and retains running jobs. [WSL NAT networking](https://learn.microsoft.com/windows/wsl/networking#default-networking-mode-nat) · [Update an rclone remote](https://rclone.org/commands/rclone_config_update/)

## Job execution and isolation

Each invocation gets a job ID and starts an independent Stata process. Independent jobs can run concurrently, and the caller controls their order. To share in-memory data or estimation results, use Stata's native `do` command to run child scripts in sequence from one entry-point `.do` file. [Stata 17 Windows manual, section B.6](https://www.stata.com/manuals17/gsw.pdf)

Once the Windows service accepts a job, it owns the Stata process and its logs. HTTP requests submit jobs and retrieve information. Closing or disconnecting the client ends the client's wait. Status and logs remain available by job ID after reconnecting. [Go subprocess cancellation semantics](https://pkg.go.dev/os/exec#CommandContext)

The client submits a script path relative to `--workspace`, such as `models/baseline.do`, along with its arguments. The agent names, writes, uploads, and edits project scripts. The service stores its own batch bootstrap files, temporary files, and logs outside the project directory and tracks them by job ID. It starts the configured Stata executable with the absolute bootstrap path. The bootstrap explicitly runs `cd` to `--workspace` inside Stata before executing the requested script. On the development machine, Stata 17 could miss a relative entry-point script when only the Windows process working directory was set. Jobs read the same input data in place within the project directory.

For jobs that produce files, the agent chooses an output directory within the project, creates it with rclone or in the script, and can submit its project-relative path with `--output-dir`. The bootstrap puts the absolute Windows path in the Stata global macro `STATA_REMOTE_OUTPUT_DIR` so Stata 17 can read Unicode paths correctly. The script reads the macro and writes its outputs there. Use distinct output paths for concurrent jobs and for reruns whose prior results should be kept. Jobs that only need logs can omit `--output-dir`. [Stata macros](https://www.stata.com/manuals/pmacro.pdf) · [Stata 17 Unicode string functions](https://www.stata.com/manuals17/fn.pdf)

```stata
local outdir : copy global STATA_REMOTE_OUTPUT_DIR
use "data/panel.dta", clear
save "`outdir'/result.dta", replace
```

| Endpoint | Behavior |
| --- | --- |
| `POST /api/jobs` | Accepts a project-relative `.do` path, arguments, an optional output directory, and a client-generated ID. Retrying the same submission with the same ID returns the original job. |
| `GET /api/jobs` | Lists starting or running jobs with their IDs, script paths, and current states. |
| `GET /api/jobs/{id}` | Returns the job state by ID, including completed results, any available Stata return code, and the reason for failure. It also returns the output directory if supplied on submission. |
| `GET /api/jobs/{id}/log?offset=0` | Reads a chunk of the original log starting at the specified byte offset. The response contains Base64 data, the next offset, and a completion flag. |
| `/dav/` | Provides WebDAV uploads, downloads, and directory listings. |

The WSL client displays the job ID, output directory, and Stata log. It exits with `0` on success or a nonzero code with an explanation if the script, process launch, or connection fails. The service records Stata's `r(...)` code separately from the Windows process exit code. The batch adapter uses `capture noisily do` to preserve command output and saves the script's `_rc` immediately. It reports a failure reason if the process is interrupted or the Stata return code is unavailable. Log visibility during execution depends on when the target Stata version flushes logs to disk. [Stata batch logs](https://www.stata.com/support/faqs/windows/batch-mode/) · [Stata capture and return codes](https://www.stata.com/manuals/pcapture.pdf)

## Agent workflow

The agent chooses script and output directory names and uses rclone to upload, edit, and manage project files. The client provides a job ID for tracking the submission. When the job completes, the agent can list the directory it chose and fetch outputs as needed. Large `.dta` files can stay on Windows for the next job. The script and output paths below are example choices.

```bash
rclone copyto baseline.do stata-files:models/baseline.do
rclone mkdir stata-files:results/baseline-v1
stata-remote do --output-dir results/baseline-v1 models/baseline.do
rclone lsf stata-files:results/baseline-v1/
```

The agent can run `stata-remote jobs` from another terminal or after reconnecting to see starting and running jobs. With a known job ID, use `stata-remote status <job-id>` to check the state or `stata-remote logs <job-id>` to recover missed logs. These queries also work for completed jobs. `do` prints the job ID before submitting the request. If the submission response is lost, retry with `do --job-id <job-id>` and the original parameters.

To find `.dta` files under a directory, use `rclone lsjson stata-files:results -R --files-only --include '*.dta'`. The client handles directory traversal and filtering using standard WebDAV listings. Use `rclone cat` to view a text output or `rclone copyto` to download a file for a local tool. [rclone lsjson](https://rclone.org/commands/rclone_lsjson/) · [rclone copyto](https://rclone.org/commands/rclone_copyto/)

## Verification

The Go Windows service and Linux client built successfully and passed `go test -race ./...` and `go vet ./...`. Integration testing with Windows Stata 17, the WSL client, and rclone used a temporary TCP forwarder. It covered paths with Chinese characters and spaces, script arguments, concurrent execution, independent output directories, retrieving results by job ID after the client exited, and Stata reporting `r(601)` with a Windows process exit code of `0`. Direct access to TCP `8765` at the Windows NAT address still timed out on the development machine. Direct NAT access with interop and DrvFs disabled, and recovery after `wsl --shutdown`, remain to be verified after the port is opened.

1. Build the Windows service and Linux client from WSL and run them on their target systems. With interop and DrvFs disabled, use rclone to upload, list, and download files with Chinese characters or spaces and submit a job through the same service. Verify that WebDAV operations stay within the configured project directory.
2. Run an existing `.do` file using the configured Windows Stata installation. Verify relative path resolution, `.ado` lookup, and script arguments. Check that WSL receives the log and a successful exit status.
3. Run a script that deliberately fails and a job whose Stata process fails to start. Verify that the logs and status distinguish the failures. Use a long-running job to observe when Windows Stata flushes its batch log.
4. Run two scripts with agent-chosen names concurrently against the same `.dta` file. Have both write identically named outputs to distinct directories chosen for each job. Verify that the logs and outputs stay separate and that the source data is read in place on Windows.
5. Start a long-running job and confirm that `stata-remote jobs` lists it. Run `wsl --shutdown` and confirm that the Windows service and Stata process keep running. Restart WSL and retrieve the final status, full log, and outputs using the original job ID.
