package server

import (
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/net/webdav"

	"stata-remote/internal/api"
)

type job struct {
	request api.Submission
	status  api.Job
	dir     string
}

type Server struct {
	executable string
	workspace  string
	root       *os.Root
	stateDir   string
	handler    http.Handler

	mu      sync.RWMutex
	jobs    map[string]*job
	workers sync.WaitGroup
}

func New(executable, workspace, token string) (*Server, error) {
	if strings.TrimSpace(token) == "" || strings.ContainsAny(token, " \t\r\n") {
		return nil, errors.New("set STATA_REMOTE_TOKEN to a nonempty bearer token without whitespace")
	}
	if executable == "" || workspace == "" {
		return nil, errors.New("both --stata and --workspace are required")
	}
	executable, err := filepath.Abs(executable)
	if err != nil {
		return nil, err
	}
	info, err := os.Stat(executable)
	if err != nil {
		return nil, fmt.Errorf("Stata executable: %w", err)
	}
	if !info.Mode().IsRegular() {
		return nil, errors.New("--stata must point to the Stata executable")
	}
	workspace, err = filepath.Abs(workspace)
	if err != nil {
		return nil, err
	}
	workspace, err = filepath.EvalSymlinks(workspace)
	if err != nil {
		return nil, fmt.Errorf("workspace: %w", err)
	}
	root, err := os.OpenRoot(workspace)
	if err != nil {
		return nil, fmt.Errorf("workspace: %w", err)
	}
	stateDir, err := privateStateDir(workspace)
	if err != nil {
		root.Close()
		return nil, err
	}
	s := &Server{executable: executable, workspace: workspace, root: root, stateDir: stateDir, jobs: make(map[string]*job)}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/jobs", s.submit)
	mux.HandleFunc("GET /api/jobs", s.list)
	mux.HandleFunc("GET /api/jobs/{id}", s.status)
	mux.HandleFunc("GET /api/jobs/{id}/log", s.logs)
	mux.Handle("/dav/", &webdav.Handler{Prefix: "/dav", FileSystem: davFS{root}, LockSystem: webdav.NewMemLS()})
	expected := []byte("Bearer " + token)
	s.handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if subtle.ConstantTimeCompare([]byte(r.Header.Get("Authorization")), expected) != 1 {
			w.Header().Set("WWW-Authenticate", "Bearer")
			respond(w, http.StatusUnauthorized, api.Error{Error: "invalid bearer token"})
			return
		}
		mux.ServeHTTP(w, r)
	})
	return s, nil
}

func privateStateDir(workspace string) (string, error) {
	cache, _ := os.UserCacheDir()
	for _, parent := range []string{os.TempDir(), cache} {
		if parent == "" {
			continue
		}
		parent, err := filepath.EvalSymlinks(parent)
		if err != nil {
			continue
		}
		if relative, err := filepath.Rel(workspace, parent); err == nil && filepath.IsLocal(relative) {
			continue
		}
		if dir, err := os.MkdirTemp(parent, "stata-remote-"); err == nil {
			return dir, nil
		}
	}
	return "", errors.New("could not create a private job directory outside the workspace")
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) { s.handler.ServeHTTP(w, r) }

// Close is called only once the HTTP server has stopped accepting submissions.
func (s *Server) Close() error {
	s.workers.Wait()
	return errors.Join(s.root.Close(), os.RemoveAll(s.stateDir))
}

func respond(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}

func failure(w http.ResponseWriter, status int, message string) {
	respond(w, status, api.Error{Error: message})
}

func (s *Server) submit(w http.ResponseWriter, r *http.Request) {
	var request api.Submission
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64*1024))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil || decoder.Decode(new(any)) != io.EOF {
		failure(w, http.StatusBadRequest, "expected a single job submission JSON object")
		return
	}
	if request.ID == "" {
		var err error
		request.ID, err = api.NewID()
		if err != nil {
			failure(w, http.StatusInternalServerError, "could not generate a job ID")
			return
		}
	}
	if !api.ValidID(request.ID) {
		failure(w, http.StatusBadRequest, "job ID must be 32 lowercase hexadecimal characters")
		return
	}
	status, code, err := s.accept(request)
	if err != nil {
		failure(w, code, err.Error())
		return
	}
	respond(w, code, status)
}

func (s *Server) accept(request api.Submission) (api.Job, int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if existing := s.jobs[request.ID]; existing != nil {
		previous := existing.request
		if previous.Script != request.Script || previous.OutputDir != request.OutputDir || !slices.Equal(previous.Args, request.Args) {
			return api.Job{}, http.StatusConflict, errors.New("job ID is already used by a different submission")
		}
		return existing.status, http.StatusOK, nil
	}
	script, err := projectPath(request.Script)
	if err != nil || !strings.EqualFold(filepath.Ext(script), ".do") {
		return api.Job{}, http.StatusBadRequest, errors.New("script must be a project-relative .do path")
	}
	info, err := s.root.Stat(script)
	if err != nil || !info.Mode().IsRegular() {
		return api.Job{}, http.StatusBadRequest, fmt.Errorf("script is not a regular file in the workspace: %s", request.Script)
	}
	for _, arg := range request.Args {
		if strings.ContainsAny(arg, "\x00\r\n") {
			return api.Job{}, http.StatusBadRequest, errors.New("script arguments cannot contain line breaks or NUL")
		}
	}
	var output string
	if request.OutputDir != "" {
		output, err = projectPath(request.OutputDir)
		if err != nil || !s.outputPathAllowed(output) {
			return api.Job{}, http.StatusBadRequest, errors.New("output directory must be a project-relative directory")
		}
	}
	dir, err := os.MkdirTemp(s.stateDir, request.ID+"-")
	if err != nil {
		return api.Job{}, http.StatusInternalServerError, fmt.Errorf("create job directory: %w", err)
	}
	status := api.Job{
		ID: request.ID, Script: request.Script, OutputDir: request.OutputDir, State: api.Starting, SubmittedAt: time.Now().UTC(),
	}
	j := &job{request: request, status: status, dir: dir}
	s.jobs[request.ID] = j
	s.workers.Go(func() { s.run(j, script, output) })
	return status, http.StatusAccepted, nil
}

// An Agent may create its directory inside the do-file. Check the nearest
// existing ancestor through os.Root before passing the absolute path to Stata.
func (s *Server) outputPathAllowed(name string) bool {
	for {
		info, err := s.root.Stat(name)
		if err == nil {
			return info.IsDir()
		}
		if !errors.Is(err, fs.ErrNotExist) || name == "." {
			return false
		}
		name = filepath.Dir(name)
	}
}

func (s *Server) list(w http.ResponseWriter, _ *http.Request) {
	s.mu.RLock()
	active := make([]api.Job, 0)
	for _, j := range s.jobs {
		if j.status.Active() {
			active = append(active, j.status)
		}
	}
	s.mu.RUnlock()
	sort.Slice(active, func(i, j int) bool { return active[i].SubmittedAt.Before(active[j].SubmittedAt) })
	respond(w, http.StatusOK, active)
}

func (s *Server) lookup(id string) (api.Job, string, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	j := s.jobs[id]
	if j == nil {
		return api.Job{}, "", false
	}
	return j.status, j.dir, true
}

func (s *Server) status(w http.ResponseWriter, r *http.Request) {
	status, _, ok := s.lookup(r.PathValue("id"))
	if !ok {
		failure(w, http.StatusNotFound, "job not found")
		return
	}
	respond(w, http.StatusOK, status)
}

func (s *Server) logs(w http.ResponseWriter, r *http.Request) {
	status, dir, ok := s.lookup(r.PathValue("id"))
	if !ok {
		failure(w, http.StatusNotFound, "job not found")
		return
	}
	var offset int64
	if value := r.URL.Query().Get("offset"); value != "" {
		var err error
		offset, err = strconv.ParseInt(value, 10, 64)
		if err != nil || offset < 0 {
			failure(w, http.StatusBadRequest, "offset must be a nonnegative integer")
			return
		}
	}
	chunk := api.LogChunk{NextOffset: offset, Complete: !status.Active()}
	file, err := os.Open(filepath.Join(dir, batchLog))
	if errors.Is(err, fs.ErrNotExist) {
		respond(w, http.StatusOK, chunk)
		return
	}
	if err != nil {
		failure(w, http.StatusInternalServerError, fmt.Sprintf("open job log: %v", err))
		return
	}
	defer file.Close()
	const chunkSize = 64 * 1024
	data := make([]byte, chunkSize+1)
	n, err := file.ReadAt(data, offset)
	if err != nil && !errors.Is(err, io.EOF) {
		failure(w, http.StatusInternalServerError, fmt.Sprintf("read job log: %v", err))
		return
	}
	chunk.Data = data[:min(n, chunkSize)]
	chunk.NextOffset += int64(len(chunk.Data))
	chunk.Complete = chunk.Complete && n <= chunkSize
	respond(w, http.StatusOK, chunk)
}
