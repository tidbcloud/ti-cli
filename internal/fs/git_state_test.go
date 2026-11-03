package fs

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/tidbcloud/ti-cli/internal/api"
	"github.com/tidbcloud/ti-cli/internal/api/endpoints"
	apifs "github.com/tidbcloud/ti-cli/internal/api/fs"
	"github.com/tidbcloud/ti-cli/internal/apperr"
)

type gitStateTestEntry struct {
	name     string
	body     string
	mode     int64
	typeflag byte
}

func buildGitStateTestArchive(t *testing.T, entries []gitStateTestEntry) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for _, entry := range entries {
		size := int64(len(entry.body))
		if entry.typeflag == tar.TypeDir {
			size = 0
		}
		hdr := &tar.Header{
			Name:     entry.name,
			Mode:     entry.mode,
			Typeflag: entry.typeflag,
			Size:     size,
			ModTime:  time.Unix(0, 0),
		}
		if err := tw.WriteHeader(hdr); err != nil {
			t.Fatal(err)
		}
		if size > 0 {
			if _, err := tw.Write([]byte(entry.body)); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// TestRestoreGitStateArchiveDropsRemoteConfigAndHooks covers the reported
// finding: the remote git state archive was extracted into .git verbatim, so a
// hostile archive could ship a git config command or an executable hook that
// runs on the next git invocation in the restored workspace.
func TestRestoreGitStateArchiveDropsRemoteConfigAndHooks(t *testing.T) {
	content := buildGitStateTestArchive(t, []gitStateTestEntry{
		{name: "HEAD", body: "ref: refs/heads/main\n", mode: 0o644, typeflag: tar.TypeReg},
		{name: "logs/HEAD", body: "state\n", mode: 0o644, typeflag: tar.TypeReg},
		{name: "config", body: "[core]\n\tfsmonitor = /tmp/ti-evil\n", mode: 0o644, typeflag: tar.TypeReg},
		{name: "config.worktree", body: "[core]\n\tfsmonitor = /tmp/ti-evil\n", mode: 0o644, typeflag: tar.TypeReg},
		{name: "hooks", mode: 0o755, typeflag: tar.TypeDir},
		{name: "hooks/pre-commit", body: "#!/bin/sh\nid > /tmp/ti-evil\n", mode: 0o755, typeflag: tar.TypeReg},
		{name: "worktrees/feature-x/config.worktree", body: "[core]\n\tfsmonitor = /tmp/ti-evil\n", mode: 0o644, typeflag: tar.TypeReg},
	})
	gitDir := filepath.Join(t.TempDir(), ".git")
	if err := restoreGitStateArchive(content, gitDir); err != nil {
		t.Fatalf("restoreGitStateArchive error = %v", err)
	}
	head, err := os.ReadFile(filepath.Join(gitDir, "HEAD"))
	if err != nil || string(head) != "ref: refs/heads/main\n" {
		t.Fatalf("HEAD = %q err = %v", string(head), err)
	}
	if _, err := os.Stat(filepath.Join(gitDir, "logs", "HEAD")); err != nil {
		t.Fatalf("legitimate git state was not restored: %v", err)
	}
	for _, reserved := range []string{
		"config",
		"config.worktree",
		"hooks",
		filepath.Join("hooks", "pre-commit"),
		filepath.Join("worktrees", "feature-x", "config.worktree"),
	} {
		if _, err := os.Lstat(filepath.Join(gitDir, reserved)); !os.IsNotExist(err) {
			t.Fatalf("reserved git state entry %q was restored (err = %v)", reserved, err)
		}
	}
}

func TestRestoreGitStateArchiveStillRejectsObjectsAndEscapingEntries(t *testing.T) {
	cases := []struct {
		name string
		body string
	}{
		{name: "objects/pack/pack-test.pack"},
		{name: "objects"},
		{name: "../escape"},
	}
	for _, tc := range cases {
		content := buildGitStateTestArchive(t, []gitStateTestEntry{
			{name: tc.name, body: tc.body, mode: 0o644, typeflag: tar.TypeReg},
		})
		gitDir := filepath.Join(t.TempDir(), ".git")
		err := restoreGitStateArchive(content, gitDir)
		if err == nil {
			t.Fatalf("git state entry %q restored without an error", tc.name)
		}
		if code := apperr.CodeFor(err); code != "git.invalid_git_state_entry" {
			t.Fatalf("git state entry %q error code = %q: %v", tc.name, code, err)
		}
	}
}

func TestSafeGitObjectSHA(t *testing.T) {
	valid := []string{
		"0123456789abcdef0123456789abcdef01234567",
		"0123456789ABCDEF0123456789ABCDEF01234567",
		"0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
		"  0123456789abcdef0123456789abcdef01234567  ",
	}
	for _, value := range valid {
		got, err := safeGitObjectSHA(value)
		if err != nil {
			t.Fatalf("safeGitObjectSHA(%q) error = %v", value, err)
		}
		if got != strings.TrimSpace(value) {
			t.Fatalf("safeGitObjectSHA(%q) = %q", value, got)
		}
	}
	invalid := []string{
		"",
		"   ",
		"-h",
		"--batch",
		"--upload-pack=/tmp/evil",
		"0123456789abcdef0123456789abcdef0123456",
		"0123456789abcdef0123456789abcdef012345678",
		"0123456789abcdef0123456789abcdef0123456g",
		"0123456789abcdef0123456789abcdef01234567^{object}",
		"0123456789abcdef 0123456789abcdef01234567",
		"${EVIL}",
	}
	for _, value := range invalid {
		if got, err := safeGitObjectSHA(value); err == nil {
			t.Fatalf("safeGitObjectSHA(%q) = %q without an error", value, got)
		}
	}
}

func newGitStateTestClient(t *testing.T, handler http.Handler) *apifs.Client {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	client, err := api.New(api.Options{
		Endpoint:   endpoints.Endpoint{BaseURL: server.URL},
		HTTPClient: server.Client(),
	})
	if err != nil {
		t.Fatal(err)
	}
	return apifs.New(client)
}

// TestHydrateGitWorkspaceRejectsInjectedObjectSHA proves the remote object id
// cannot reach git argv as an option.
func TestHydrateGitWorkspaceRejectsInjectedObjectSHA(t *testing.T) {
	client := newGitStateTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || !strings.HasPrefix(r.URL.Path, "/v1/git-workspaces/wt-1/tree") {
			t.Errorf("request = %s %s", r.Method, r.URL.RequestURI())
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(map[string]any{
			"nodes": []map[string]any{
				{"path": "evil.txt", "parent_path": "", "name": "evil.txt", "kind": "file", "mode": "100644", "object_sha": "--batch"},
			},
		}); err != nil {
			t.Errorf("encode tree: %v", err)
		}
	}))
	workspace := apifs.GitWorkspace{WorkspaceID: "wt-1", HeadCommit: "0123456789abcdef0123456789abcdef01234567"}
	_, err := Service{}.hydrateGitWorkspaceWithClient(context.Background(), client, GitWorkspaceHydrateOptions{
		TargetPath: t.TempDir(),
		Timeout:    time.Minute,
	}, mountedGitTarget{}, workspace)
	if err == nil {
		t.Fatal("hydrate accepted an injected object id")
	}
	if !strings.Contains(err.Error(), "invalid git object id") {
		t.Fatalf("error = %v", err)
	}
}

// TestGitStateReservedEntryMatchingIsCaseAndSuffixInsensitive pins the
// normalization that stops a hostile archive from renaming "config" or "hooks"
// to values that resolve to the same path on case-insensitive filesystems and on
// Windows.
func TestGitStateReservedEntryMatchingIsCaseAndSuffixInsensitive(t *testing.T) {
	reserved := []string{
		"config",
		"CONFIG",
		"Config",
		"config.",
		"config ",
		"config.worktree",
		"Config.Worktree",
		"hooks",
		"Hooks",
		"hooks/pre-commit",
		"worktrees/feature-x/Hooks/pre-commit",
		"worktrees/feature-x/config.worktree",
		"worktrees/feature-x/CONFIG",
	}
	for _, rel := range reserved {
		if !isReservedGitStateEntry(rel) {
			t.Fatalf("isReservedGitStateEntry(%q) = false", rel)
		}
	}
	allowed := []string{
		"HEAD",
		"index",
		"logs/HEAD",
		"refs/heads/main",
		"worktrees/feature-x/HEAD",
		"config-value",
		"hooks.txt",
	}
	for _, rel := range allowed {
		if isReservedGitStateEntry(rel) {
			t.Fatalf("isReservedGitStateEntry(%q) = true", rel)
		}
	}
	if !containsGitStateName("objects", "objects") || !containsGitStateName("Objects/pack/x", "objects") || !containsGitStateName("modules/sub/objects/info", "objects") {
		t.Fatal("objects component was not detected")
	}
	if containsGitStateName("worktrees/feature-x/HEAD", "objects") {
		t.Fatal("false positive for a legitimate entry")
	}
}

func TestRestoreGitStateArchiveNormalizesCaseAndTrailingDots(t *testing.T) {
	content := buildGitStateTestArchive(t, []gitStateTestEntry{
		{name: "HEAD", body: "ref: refs/heads/main\n", mode: 0o644, typeflag: tar.TypeReg},
		{name: "CONFIG", body: "[core]\n\tfsmonitor = /tmp/ti-evil\n", mode: 0o644, typeflag: tar.TypeReg},
		{name: "config.", body: "[core]\n\tfsmonitor = /tmp/ti-evil\n", mode: 0o644, typeflag: tar.TypeReg},
		{name: "Hooks", mode: 0o755, typeflag: tar.TypeDir},
		{name: "Hooks/pre-commit", body: "#!/bin/sh\nid\n", mode: 0o755, typeflag: tar.TypeReg},
		{name: "Objects/pack/pack-test.pack", body: "not an object", mode: 0o644, typeflag: tar.TypeReg},
	})
	gitDir := filepath.Join(t.TempDir(), ".git")
	err := restoreGitStateArchive(content, gitDir)
	if err == nil {
		t.Fatal("mixed archive with an objects entry was accepted")
	}
	if code := apperr.CodeFor(err); code != "git.invalid_git_state_entry" {
		t.Fatalf("error code = %q: %v", code, err)
	}
	for _, rel := range []string{"CONFIG", "config.", "Hooks", "Hooks/pre-commit"} {
		if _, statErr := os.Lstat(filepath.Join(gitDir, rel)); !os.IsNotExist(statErr) {
			t.Fatalf("reserved entry %q was restored (err = %v)", rel, err)
		}
	}
}

// TestArchiveGitStateDirDropsReservedEntriesForUpload keeps local git config,
// which can embed credentials or a core.sshCommand, out of the state that is
// uploaded to the FS backend, and keeps save/restore symmetric.
func TestArchiveGitStateDirDropsReservedEntriesForUpload(t *testing.T) {
	root := t.TempDir()
	gitDir := filepath.Join(root, ".git")
	for _, dir := range []string{
		filepath.Join(gitDir, "hooks"),
		filepath.Join(gitDir, "objects", "pack"),
		filepath.Join(gitDir, "logs"),
		filepath.Join(gitDir, "worktrees", "feature-x"),
	} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	files := map[string]string{
		filepath.Join(gitDir, "HEAD"):                                      "ref: refs/heads/main\n",
		filepath.Join(gitDir, "config"):                                    "[remote \"origin\"]\n\turl = https://user:secret@example.com/repo\n",
		filepath.Join(gitDir, "hooks", "pre-commit"):                       "#!/bin/sh\nid\n",
		filepath.Join(gitDir, "objects", "pack", "pack-test.pack"):         "object",
		filepath.Join(gitDir, "logs", "HEAD"):                              "log\n",
		filepath.Join(gitDir, "worktrees", "feature-x", "config.worktree"): "[core]\n\tfsmonitor = /tmp/ti-evil\n",
	}
	for path, body := range files {
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	archive, err := archiveGitStateDir(gitDir)
	if err != nil {
		t.Fatal(err)
	}
	names := readGitStateArchiveNames(t, archive)
	for _, name := range []string{"HEAD", "logs/HEAD"} {
		if !names[name] {
			t.Fatalf("archive is missing %q: %v", name, names)
		}
	}
	for _, name := range []string{"config", "hooks", "hooks/pre-commit", "objects", "objects/pack", "objects/pack/pack-test.pack", "worktrees/feature-x/config.worktree"} {
		if names[name] {
			t.Fatalf("archive still contains %q: %v", name, names)
		}
	}
}

func readGitStateArchiveNames(t *testing.T, content []byte) map[string]bool {
	t.Helper()
	gz, err := gzip.NewReader(bytes.NewReader(content))
	if err != nil {
		t.Fatal(err)
	}
	defer gz.Close()
	names := map[string]bool{}
	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		names[hdr.Name] = true
	}
	return names
}
