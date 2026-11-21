package server

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"stata-remote/internal/api"
)

const (
	batchDriver = "driver.do"
	batchLog    = "driver.log"
	batchResult = "stata-rc.txt"
)

func (s *Server) run(j *job, script, output string) {
	// The process belongs to the server, independently of every HTTP request.
	// Windows Stata takes the log location from the launch directory and the
	// driver itself sets the Stata working directory for relative .do paths.
	driver := filepath.Join(j.dir, batchDriver)
	resultPath := filepath.Join(j.dir, batchResult)
	cmd := exec.Command(s.executable, "/e", "do", driver)
	cmd.Dir = j.dir
	if output != "" {
		output = filepath.Join(s.workspace, output)
	}
	cmd.Env = append(cmd.Environ(),
		"STATA_REMOTE_OUTPUT_DIR="+output,
		"STATATMP="+j.dir,
		"STATA_REMOTE_TOKEN=",
	)
	source := driverSource(filepath.ToSlash(s.workspace), filepath.ToSlash(script), filepath.ToSlash(resultPath), filepath.ToSlash(output), j.request.Args)
	if err := os.WriteFile(driver, []byte(source), 0600); err != nil {
		s.finish(j, nil, nil, fmt.Sprintf("prepare Stata batch: %v", err))
		return
	}
	if err := cmd.Start(); err != nil {
		s.finish(j, nil, nil, fmt.Sprintf("start Stata: %v", err))
		return
	}
	s.mu.Lock()
	j.status.State = api.Running
	s.mu.Unlock()

	waitErr := cmd.Wait()
	exitCode := cmd.ProcessState.ExitCode()
	content, err := os.ReadFile(resultPath)
	if err != nil {
		message := "Stata exited before reporting a return code"
		if waitErr != nil {
			message += ": " + waitErr.Error()
		}
		s.finish(j, nil, &exitCode, message)
		return
	}
	rc, err := strconv.Atoi(strings.TrimSpace(string(content)))
	if err != nil || rc < 0 {
		s.finish(j, nil, &exitCode, "Stata reported an invalid return code")
		return
	}
	var message string
	if rc != 0 {
		message = fmt.Sprintf("Stata returned r(%d)", rc)
	} else if waitErr != nil {
		message = fmt.Sprintf("Stata process failed: %v", waitErr)
	}
	s.finish(j, &rc, &exitCode, message)
}

func (s *Server) finish(j *job, stataRC, processExit *int, message string) {
	now := time.Now().UTC()
	s.mu.Lock()
	defer s.mu.Unlock()
	j.status.FinishedAt = &now
	j.status.StataRC = stataRC
	j.status.ProcessExit = processExit
	j.status.Error = message
	j.status.State = api.Succeeded
	if message != "" {
		j.status.State = api.Failed
	}
}

func driverSource(workspace, script, resultPath, output string, args []string) string {
	var driver strings.Builder
	fmt.Fprintf(&driver, "local srWorkspace = %s\n", stataString(workspace))
	fmt.Fprintf(&driver, "local srScript = %s\n", stataString(script))
	fmt.Fprintf(&driver, "local srResult = %s\n", stataString(resultPath))
	fmt.Fprintf(&driver, "global STATA_REMOTE_OUTPUT_DIR = %s\n", stataString(output))
	for i, arg := range args {
		fmt.Fprintf(&driver, "local srArg%d = %s\n", i+1, stataString(arg))
	}
	driver.WriteString("capture noisily cd `\"`macval(srWorkspace)'\"'\n" +
		"local srRC = _rc\n" +
		"if `srRC' == 0 {\n" +
		"    capture noisily do `\"`macval(srScript)'\"'")
	for i := range args {
		fmt.Fprintf(&driver, " `\"`macval(srArg%d)'\"'", i+1)
	}
	driver.WriteString("\n    local srRC = _rc\n" +
		"}\n" +
		"tempname srHandle\n" +
		"file open `srHandle' using `\"`macval(srResult)'\"', write text replace\n" +
		"file write `srHandle' \"`srRC'\" _n\n" +
		"file close `srHandle'\n")
	return driver.String()
}

// Stata 17 on Windows can read environment variables in the local Windows
// code page. Reconstruct Unicode and Stata metacharacters inside the engine.
func stataString(value string) string {
	var expression strings.Builder
	expression.WriteString(`ustrunescape("`)
	for _, ch := range value {
		switch {
		case ch > 0xffff:
			fmt.Fprintf(&expression, `\U%08x`, ch)
		case ch < ' ' || ch > '~' || strings.ContainsRune("\\\"$`'", ch):
			fmt.Fprintf(&expression, `\u%04x`, ch)
		default:
			expression.WriteRune(ch)
		}
	}
	expression.WriteString(`")`)
	return expression.String()
}
