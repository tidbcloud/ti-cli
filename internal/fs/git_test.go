package fs

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tidbcloud/ti-cli/internal/api"
	"github.com/tidbcloud/ti-cli/internal/api/endpoints"
	apifs "github.com/tidbcloud/ti-cli/internal/api/fs"
	"github.com/tidbcloud/ti-cli/internal/apperr"
)

func newTestGitWorkspaceClient(t *testing.T, handler http.Handler) *apifs.Client {
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

func commonWorkspaceHandler(t *testing.T, commonRootPath string) http.Handler {
	t.Helper()
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/v1/git-workspaces/common-1" {
			t.Errorf("request = %s %s", r.Method, r.URL.RequestURI())
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(map[string]any{
			"workspace_id":   "common-1",
			"root_path":      commonRootPath,
			"workspace_kind": "common",
		}); err != nil {
			t.Errorf("encode common workspace: %v", err)
		}
	})
}

func linkedTestWorkspace(gitDirRel string) apifs.GitWorkspace {
	return apifs.GitWorkspace{
		WorkspaceID:       "wt-1",
		RootPath:          "/workspace/feature",
		WorkspaceKind:     "linked",
		CommonWorkspaceID: "common-1",
		GitDirRel:         gitDirRel,
	}
}

func linkedTestTarget(root string) mountedGitTarget {
	return mountedGitTarget{
		MountPoint: filepath.Join(root, "mnt"),
		MountRel:   "feature",
		RemoteRoot: "/workspace",
		RemotePath: "/workspace/feature",
		LocalRoot:  filepath.Join(root, "fs"),
	}
}

// gitDirRelTestName turns a hostile gitdir_rel value into a readable subtest
// name. Control characters, including the NUL byte used in these cases, would
// otherwise truncate or garble CI output.
func gitDirRelTestName(gitDirRel string) string {
	name := strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f || r == '/' || r == '\\' {
			return '_'
		}
		return r
	}, gitDirRel)
	if name == "" {
		return "empty"
	}
	return name
}

// TestRestoreGitLayoutRejectsRemoteGitDirRelTraversal covers the reported
// information-protection finding: gitdir_rel comes from the FS API response and
// must not name a local directory, because the resolved directory is created,
// recursively cleared, and written into during restore.
func TestRestoreGitLayoutRejectsRemoteGitDirRelTraversal(t *testing.T) {
	unsafeValues := []string{
		"../../../../../../home/victim/.ssh",
		"../../../victim",
		"..",
		".",
		"hooks",
		"refs/heads",
		"objects",
		".git/config",
		"worktrees",
		"worktrees/..",
		"../victim",
		"/etc/victim",
		"..\\..\\victim",
		"worktrees\\x",
		"worktrees/x\x00y",
	}
	for _, gitDirRel := range unsafeValues {
		gitDirRel := gitDirRel
		t.Run(gitDirRelTestName(gitDirRel), func(t *testing.T) {
			root := t.TempDir()
			victimDir := filepath.Join(root, "victim")
			if err := os.MkdirAll(victimDir, 0o755); err != nil {
				t.Fatal(err)
			}
			sentinel := filepath.Join(victimDir, "keep")
			if err := os.WriteFile(sentinel, []byte("keep"), 0o600); err != nil {
				t.Fatal(err)
			}
			client := newTestGitWorkspaceClient(t, commonWorkspaceHandler(t, "/workspace"))
			worktreeRoot, gitDir, err := restoreGitLayout(context.Background(), client, linkedTestWorkspace(gitDirRel), linkedTestTarget(root))
			if err == nil {
				t.Fatalf("gitdir_rel %q resolved to worktree %q git dir %q without an error", gitDirRel, worktreeRoot, gitDir)
			}
			if code := apperr.CodeFor(err); code != "git.invalid_gitdir_rel" && code != "git.gitdir_rel_escape" {
				t.Fatalf("gitdir_rel %q error code = %q, want git.invalid_gitdir_rel or git.gitdir_rel_escape: %v", gitDirRel, code, err)
			}
			data, readErr := os.ReadFile(sentinel)
			if readErr != nil || string(data) != "keep" {
				t.Fatalf("outside directory was modified: data=%q err=%v", string(data), readErr)
			}
			entries, readErr := os.ReadDir(victimDir)
			if readErr != nil {
				t.Fatal(readErr)
			}
			if len(entries) != 1 || entries[0].Name() != "keep" {
				t.Fatalf("outside directory gained entries: %#v", entries)
			}
			overlayRoot := filepath.Join(root, "fs", "overlay")
			if gitDir != "" && !strings.HasPrefix(filepath.Clean(gitDir), overlayRoot+string(filepath.Separator)) {
				t.Fatalf("gitdir_rel %q escaped the local overlay: %q", gitDirRel, gitDir)
			}
		})
	}
}

// TestSafeWorktreeGitDirTargetAcceptsWorktreeLayouts keeps the fix compatible
// with the layouts a linked worktree can legitimately report, including git's
// own worktree-relative form used in .git files.
func TestSafeWorktreeGitDirTargetAcceptsWorktreeLayouts(t *testing.T) {
	base := filepath.Join(t.TempDir(), "overlay", ".git")
	cases := []struct{ rel, name string }{
		{"worktrees/feature-x", "feature-x"},
		{"worktrees/a b", "a b"},
		{"worktrees/0f1e2d", "0f1e2d"},
		{"./worktrees/feature-x", "feature-x"},
		{"worktrees//feature-x", "feature-x"},
		{"worktrees/feature-x/", "feature-x"},
		{"../.git/worktrees/feature-x", "feature-x"},
	}
	for _, tc := range cases {
		want := filepath.Join(base, "worktrees", tc.name)
		target, err := safeWorktreeGitDirTarget(base, tc.rel)
		if err != nil {
			t.Fatalf("safeWorktreeGitDirTarget(%q) error = %v", tc.rel, err)
		}
		if target != want {
			t.Fatalf("safeWorktreeGitDirTarget(%q) = %q, want %q", tc.rel, target, want)
		}
	}
}

func TestSafeWorktreeGitDirTargetRejectsUnsafeValues(t *testing.T) {
	base := filepath.Join(t.TempDir(), "overlay", ".git")
	unsafeValues := []string{
		"",
		"   ",
		".",
		"..",
		"../victim",
		"../../../../../../home/victim/.ssh",
		"../../victim",
		"hooks",
		"hooks/pre-commit",
		"refs",
		"objects",
		"config",
		"worktrees",
		"worktrees/..",
		"worktrees/../hooks",
		"/worktrees/feature-x",
		"\\worktrees\\feature-x",
		"worktrees\\feature-x",
		"worktrees/x\x00y",
	}
	for _, rel := range unsafeValues {
		if target, err := safeWorktreeGitDirTarget(base, rel); err == nil {
			t.Fatalf("safeWorktreeGitDirTarget(%q) = %q without an error", rel, target)
		}
	}
}

func TestRestoreGitLayoutResolvesLinkedWorktreeInsideCommonGitDir(t *testing.T) {
	for _, gitDirRel := range []string{"worktrees/feature-x", "../.git/worktrees/feature-x"} {
		gitDirRel := gitDirRel
		t.Run(strings.ReplaceAll(gitDirRel, "/", "_"), func(t *testing.T) {
			root := t.TempDir()
			client := newTestGitWorkspaceClient(t, commonWorkspaceHandler(t, "/workspace"))
			worktreeRoot, gitDir, err := restoreGitLayout(context.Background(), client, linkedTestWorkspace(gitDirRel), linkedTestTarget(root))
			if err != nil {
				t.Fatal(err)
			}
			commonRoot := filepath.Join(root, "fs", "overlay")
			wantGitDir := filepath.Join(commonRoot, ".git", "worktrees", "feature-x")
			if gitDir != wantGitDir {
				t.Fatalf("gitDir = %q, want %q", gitDir, wantGitDir)
			}
			if worktreeRoot != filepath.Join(commonRoot, "feature") {
				t.Fatalf("worktreeRoot = %q", worktreeRoot)
			}
			pointer, err := os.ReadFile(filepath.Join(worktreeRoot, ".git"))
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(string(pointer), ".git/worktrees/feature-x") {
				t.Fatalf(".git pointer = %q", string(pointer))
			}
		})
	}
}

func TestRestoreGitLayoutRejectsSymlinkedGitDirPath(t *testing.T) {
	root := t.TempDir()
	target := linkedTestTarget(root)
	overlayRoot := filepath.Join(target.LocalRoot, "overlay")
	victimDir := filepath.Join(root, "victim")
	if err := os.MkdirAll(victimDir, 0o755); err != nil {
		t.Fatal(err)
	}
	sentinel := filepath.Join(victimDir, "keep")
	if err := os.WriteFile(sentinel, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(overlayRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(victimDir, filepath.Join(overlayRoot, ".git")); err != nil {
		t.Skipf("symlinks are not supported: %v", err)
	}
	client := newTestGitWorkspaceClient(t, commonWorkspaceHandler(t, "/workspace"))
	_, gitDir, err := restoreGitLayout(context.Background(), client, linkedTestWorkspace("worktrees/feature-x"), target)
	if err == nil {
		t.Fatalf("symlinked git dir %q accepted", gitDir)
	}
	if code := apperr.CodeFor(err); code != "git.unsafe_git_dir_symlink" {
		t.Fatalf("error code = %q: %v", code, err)
	}
	if _, statErr := os.Stat(sentinel); statErr != nil {
		t.Fatalf("victim directory was modified: %v", statErr)
	}
}
