package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"stata-remote/internal/api"
)

func TestMain(m *testing.M) {
	if len(os.Args) == 4 && os.Args[1] == "/e" && os.Args[2] == "do" {
		os.Exit(fakeStata())
	}
	os.Exit(m.Run())
}

func fakeStata() int {
	driver, err := os.ReadFile(os.Args[3])
	if err != nil {
		return 2
	}
	value := func(macro string) string {
		_, suffix, _ := strings.Cut(string(driver), "local "+macro+` = ustrunescape("`)
		value, _, _ := strings.Cut(suffix, `")`)
		return value
	}
	workspace := filepath.FromSlash(value("srWorkspace"))
	script, err := os.ReadFile(filepath.Join(workspace, filepath.FromSlash(value("srScript"))))
	if err != nil {
		return 2
	}
	if err := os.WriteFile(batchLog, []byte("output from Stata\n"), 0600); err != nil {
		return 2
	}
	for deadline := time.Now().Add(8 * time.Second); ; {
		if _, err := os.Stat(filepath.Join(workspace, "release")); err == nil {
			break
		}
		if time.Now().After(deadline) {
			return 2
		}
		time.Sleep(20 * time.Millisecond)
	}
	if err := os.WriteFile(batchResult, script, 0600); err != nil {
		return 2
	}
	return 0
}

func newTestServer(t *testing.T) (*Server, string) {
	t.Helper()
	workspace := t.TempDir()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	s, err := New(executable, workspace, "test-token")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s, workspace
}

func send(s *Server, method, path, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer test-token")
	response := httptest.NewRecorder()
	s.ServeHTTP(response, req)
	return response
}

func decode[T any](t *testing.T, response *httptest.ResponseRecorder, status int) T {
	t.Helper()
	if response.Code != status {
		t.Fatalf("HTTP %d, want %d: %s", response.Code, status, response.Body)
	}
	var result T
	if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	return result
}

func TestIndependentJobsAndIdempotentSubmission(t *testing.T) {
	s, workspace := newTestServer(t)
	for name, rc := range map[string]string{"ok.do": "0\n", "error.do": "601\n"} {
		if err := os.WriteFile(filepath.Join(workspace, name), []byte(rc), 0600); err != nil {
			t.Fatal(err)
		}
	}
	firstID, secondID := strings.Repeat("a", 32), strings.Repeat("b", 32)
	submit := func(id, script string, expected int) api.Job {
		body := fmt.Sprintf(`{"id":%q,"script":%q,"output_dir":"results/%s"}`, id, script, id)
		return decode[api.Job](t, send(s, "POST", "/api/jobs", body), expected)
	}
	submit(firstID, "ok.do", http.StatusAccepted)
	submit(secondID, "error.do", http.StatusAccepted)
	submit(firstID, "ok.do", http.StatusOK)
	active := decode[[]api.Job](t, send(s, "GET", "/api/jobs", ""), http.StatusOK)
	if len(active) != 2 || active[0].ID != firstID || active[1].ID != secondID {
		t.Fatalf("active jobs = %+v", active)
	}
	decode[api.Error](t, send(s, "POST", "/api/jobs", fmt.Sprintf(`{"id":%q,"script":"error.do"}`, firstID)), http.StatusConflict)
	if err := os.WriteFile(filepath.Join(workspace, "release"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	s.workers.Wait()
	for id, wanted := range map[string]int{firstID: 0, secondID: 601} {
		state := decode[api.Job](t, send(s, "GET", "/api/jobs/"+id, ""), http.StatusOK)
		if state.StataRC == nil || *state.StataRC != wanted || state.ProcessExit == nil || *state.ProcessExit != 0 {
			t.Fatalf("job %s returned %+v", id, state)
		}
		if (state.State == api.Succeeded) != (wanted == 0) {
			t.Fatalf("job %s returned state %s", id, state.State)
		}
		log := decode[api.LogChunk](t, send(s, "GET", "/api/jobs/"+id+"/log?offset=7", ""), http.StatusOK)
		if !log.Complete || string(log.Data) != "from Stata\n" || log.NextOffset != 18 {
			t.Fatalf("unexpected job log: %+v", log)
		}
	}
	if active := decode[[]api.Job](t, send(s, "GET", "/api/jobs", ""), http.StatusOK); len(active) != 0 {
		t.Fatalf("expected no active jobs, got %+v", active)
	}
}

func TestWebDAVWorkspaceAndAuth(t *testing.T) {
	s, workspace := newTestServer(t)
	unauthorized := httptest.NewRecorder()
	s.ServeHTTP(unauthorized, httptest.NewRequest("PROPFIND", "/dav/", nil))
	if unauthorized.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated WebDAV returned %d", unauthorized.Code)
	}
	if result := send(s, "PUT", "/dav/analysis.do", "display 42\n"); result.Code != http.StatusCreated {
		t.Fatalf("WebDAV PUT returned %d: %s", result.Code, result.Body)
	}
	if body, err := os.ReadFile(filepath.Join(workspace, "analysis.do")); err != nil || string(body) != "display 42\n" {
		t.Fatalf("published file has body %q, error %v", body, err)
	}
	outside := filepath.Join(t.TempDir(), "outside.txt")
	if err := os.WriteFile(outside, []byte("outside content"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(workspace, "link.txt")); err == nil {
		if result := send(s, "GET", "/dav/link.txt", ""); result.Code < 400 || strings.Contains(result.Body.String(), "outside content") {
			t.Fatalf("WebDAV served a symlink outside the workspace: HTTP %d", result.Code)
		}
	}
}

func TestStataStringEncodesUnicodeAndMacroDelimiters(t *testing.T) {
	got := stataString("目录 \"'`$\\😀")
	want := `ustrunescape("\u76ee\u5f55 \u0022\u0027\u0060\u0024\u005c\U0001f600")`
	if got != want {
		t.Fatalf("got %s, want %s", got, want)
	}
}
