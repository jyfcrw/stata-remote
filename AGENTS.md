# Stata Remote project guidelines

## Repository layout

- Build, deployment, and usage instructions are in [README.md](README.md). The architecture, interfaces, and acceptance checks are in [docs/design.md](docs/design.md).
- `cmd/stata-remote-server` is the Windows service entry point. `cmd/stata-remote` is the WSL client entry point.
- `internal/server` manages jobs, Stata batch execution, and the WebDAV filesystem. `internal/api` defines the protocol types shared by the client and server.

## Implementation boundaries

- Use Go. Run the service directly on the Windows host and launch the configured Stata executable there. WSL connects through the HTTP API and WebDAV. Deployment assumes WSL 2 in NAT mode.
- Launch an independent Stata process for each job. The Windows service owns the job lifecycle independently of the HTTP request, client, and WSL. Preserve the semantics of retrying a submission with the same job ID.
- The agent chooses script names, contents, and output paths and manages project files through rclone. Keep the service's bootstrap scripts, temporary files, and logs outside the WebDAV root. Stata reads existing large datasets in place.
- `--output-dir` is a project-relative path chosen by the agent. Pass it to `.do` files through the Stata global macro `STATA_REMOTE_OUTPUT_DIR`, preserving Chinese characters and spaces. Record the Stata return code and the Windows process exit code separately.
- The job API and WebDAV share bearer authentication. Keep file operations within the configured project root. Use a temporary project directory when testing on the development machine. The user makes any required changes to the host firewall or WSL settings.

## Verification

Use the Go version declared in `go.mod` and run these commands from the repository root.

```bash
go test ./...
go vet ./...
```

With a C compiler installed, you can also run `go test -race ./...`. Follow the [build instructions](README.md#build-and-install) to verify that the Windows amd64 service and Linux amd64 client build separately. Put build artifacts in `dist/`.

Use a real Windows Stata installation to verify changes involving Stata processes, Windows paths, or file permissions. When reporting results, specify whether testing used temporary forwarding or a direct WSL NAT connection and whether it covered `wsl --shutdown`. Update the README whenever commands, flags, or deployment steps change.
