package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"stata-remote/internal/api"
)

const usage = `Usage:
  stata-remote do [--output-dir DIR] [--job-id ID] SCRIPT.do [ARG ...]
  stata-remote jobs
  stata-remote status JOB_ID
  stata-remote logs JOB_ID

Set STATA_REMOTE_URL and STATA_REMOTE_TOKEN to connect to the Windows server.
Script and output paths are relative to the Windows workspace.
`

type client struct {
	base, token string
	http        *http.Client
}

type remoteError struct {
	code    int
	message string
}

func (e *remoteError) Error() string { return fmt.Sprintf("server HTTP %d: %s", e.code, e.message) }

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) == 0 || args[0] == "-h" || args[0] == "--help" || args[0] == "help" {
		fmt.Print(usage)
		return nil
	}
	command := args[0]
	var submission api.Submission
	var id string
	switch command {
	case "do":
		flags := flag.NewFlagSet("do", flag.ContinueOnError)
		flags.StringVar(&submission.OutputDir, "output-dir", "", "Agent-selected output directory in the Windows workspace")
		flags.StringVar(&submission.ID, "job-id", "", "reuse the previously printed ID to retry a submission")
		flags.Usage = func() { fmt.Fprint(flags.Output(), usage) }
		if err := flags.Parse(args[1:]); err != nil {
			if errors.Is(err, flag.ErrHelp) {
				return nil
			}
			return err
		}
		if flags.NArg() == 0 {
			return errors.New("do requires a project-relative .do script")
		}
		submission.Script, submission.Args = flags.Arg(0), flags.Args()[1:]
	case "jobs":
		if len(args) != 1 {
			return errors.New("jobs takes no arguments")
		}
	case "status", "logs":
		if len(args) != 2 || !api.ValidID(args[1]) {
			return fmt.Errorf("%s requires a job ID printed by do or jobs", command)
		}
		id = args[1]
	default:
		return fmt.Errorf("unknown command %q, see stata-remote --help", command)
	}
	c, err := newClient(os.Getenv("STATA_REMOTE_URL"), os.Getenv("STATA_REMOTE_TOKEN"))
	if err != nil {
		return err
	}
	switch command {
	case "do":
		return c.runJob(submission)
	case "jobs":
		var jobs []api.Job
		return c.printJSON("/api/jobs", &jobs)
	case "status":
		var status api.Job
		return c.printJSON("/api/jobs/"+id, &status)
	}
	return c.readLogs(id, false)
}

func newClient(base, token string) (*client, error) {
	u, err := url.Parse(base)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return nil, errors.New("set STATA_REMOTE_URL to the Windows service HTTP or HTTPS URL")
	}
	if strings.TrimSpace(token) == "" || strings.ContainsAny(token, " \t\r\n") {
		return nil, errors.New("set STATA_REMOTE_TOKEN to the server's bearer token")
	}
	return &client{
		base:  strings.TrimRight(base, "/"),
		token: token,
		http: &http.Client{
			Timeout:       30 * time.Second,
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		},
	}, nil
}

func (c *client) request(method, path string, data, into any) error {
	var body io.Reader
	if data != nil {
		encoded, err := json.Marshal(data)
		if err != nil {
			return err
		}
		body = bytes.NewReader(encoded)
	}
	req, err := http.NewRequest(method, c.base+path, body)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	if data != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		var payload api.Error
		_ = json.NewDecoder(io.LimitReader(resp.Body, 4096)).Decode(&payload)
		if payload.Error == "" {
			payload.Error = http.StatusText(resp.StatusCode)
		}
		return &remoteError{code: resp.StatusCode, message: payload.Error}
	}
	return json.NewDecoder(resp.Body).Decode(into)
}

func (c *client) printJSON(path string, target any) error {
	if err := c.request(http.MethodGet, path, nil, target); err != nil {
		return err
	}
	encoder := json.NewEncoder(os.Stdout)
	encoder.SetIndent("", "  ")
	return encoder.Encode(target)
}

func (c *client) runJob(submission api.Submission) error {
	if submission.ID == "" {
		var err error
		submission.ID, err = api.NewID()
		if err != nil {
			return err
		}
	} else if !api.ValidID(submission.ID) {
		return errors.New("--job-id must be an ID printed by do or jobs")
	}
	// Print before sending so the caller can recover even if the response is lost.
	fmt.Fprintln(os.Stderr, "Job ID:", submission.ID)
	var status api.Job
	err := c.request(http.MethodPost, "/api/jobs", submission, &status)
	var replyError *remoteError
	if err != nil && !errors.As(err, &replyError) {
		err = c.request(http.MethodPost, "/api/jobs", submission, &status)
	}
	if err != nil {
		return fmt.Errorf("submit job %s: %w", submission.ID, err)
	}
	if status.OutputDir != "" {
		fmt.Fprintln(os.Stderr, "Output directory:", status.OutputDir)
	}
	if err := c.readLogs(status.ID, true); err != nil {
		return fmt.Errorf("read logs for job %s: %w", status.ID, err)
	}
	if err := c.request(http.MethodGet, "/api/jobs/"+status.ID, nil, &status); err != nil {
		return fmt.Errorf("read status for job %s: %w", status.ID, err)
	}
	if status.State != api.Succeeded {
		return fmt.Errorf("job %s %s: %s", status.ID, status.State, status.Error)
	}
	return nil
}

func (c *client) readLogs(id string, wait bool) error {
	var offset int64
	for {
		var chunk api.LogChunk
		path := "/api/jobs/" + id + "/log?offset=" + strconv.FormatInt(offset, 10)
		if err := c.request(http.MethodGet, path, nil, &chunk); err != nil {
			return err
		}
		if chunk.NextOffset != offset+int64(len(chunk.Data)) {
			return errors.New("server returned an invalid log offset")
		}
		if len(chunk.Data) > 0 {
			if _, err := os.Stdout.Write(chunk.Data); err != nil {
				return err
			}
		}
		if chunk.Complete || (!wait && len(chunk.Data) == 0) {
			return nil
		}
		offset = chunk.NextOffset
		if wait && len(chunk.Data) == 0 {
			time.Sleep(250 * time.Millisecond)
		}
	}
}
