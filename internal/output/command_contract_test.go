package output_test

import (
	"bytes"
	"strings"
	"testing"

	configure "github.com/tidbcloud/ti-cli/internal/config/configure"
	"github.com/tidbcloud/ti-cli/internal/db"
	"github.com/tidbcloud/ti-cli/internal/db/connectionstring"
	"github.com/tidbcloud/ti-cli/internal/db/sqlresult"
	"github.com/tidbcloud/ti-cli/internal/dryrun"
	"github.com/tidbcloud/ti-cli/internal/fs"
	"github.com/tidbcloud/ti-cli/internal/fs/tokenmgmt"
	"github.com/tidbcloud/ti-cli/internal/output"
	"github.com/tidbcloud/ti-cli/internal/update"
)

func TestRegisteredCommandResultsSupportTextOutput(t *testing.T) {
	results := map[string]any{
		"configure":                       configure.Result{},
		"update check":                    update.CheckResult{},
		"update apply":                    update.ApplyResult{},
		"dry run":                         dryrun.Result{},
		"db list clusters":                db.ListClustersResult{},
		"db cluster":                      db.ClusterResult{},
		"db list branches":                db.ListBranchesResult{},
		"db branch":                       db.BranchResult{},
		"db create sql users":             db.PrepareQueryAccessResult{},
		"db connection string":            connectionstring.Result{},
		"db execute sql":                  sqlresult.Result{},
		"fs create":                       fs.FileSystemResult{},
		"fs list file systems":            fs.ListFileSystemsResult{},
		"fs describe file system":         fs.DescribeFileSystemResult{},
		"fs delete":                       fs.DeleteResult{},
		"fs import token":                 fs.ImportFileSystemTokenResult{},
		"fs check":                        fs.CheckResult{},
		"fs file operation":               fs.FileOperationResult{},
		"fs list files":                   fs.ListFilesResult{},
		"fs describe file":                fs.DescribeFileResult{},
		"fs search files":                 fs.SearchFilesResult{},
		"fs layer":                        fs.LayerResult{},
		"fs list layers":                  fs.LayerListResult{},
		"fs layer entries":                fs.LayerEntriesResult{},
		"fs layer entry":                  fs.LayerEntryResult{},
		"fs layer checkpoint":             fs.LayerCheckpointResult{},
		"fs layer events":                 fs.LayerEventsResult{},
		"fs layer action":                 fs.LayerActionResult{},
		"fs layer commit":                 fs.LayerCommitResult{},
		"fs pack":                         fs.PackFileSystemResult{},
		"fs unpack":                       fs.UnpackFileSystemResult{},
		"fs mount":                        fs.MountResult{},
		"fs unmount":                      fs.UnmountResult{},
		"fs drain":                        fs.DrainResult{},
		"fs generate owner token":         tokenmgmt.GenerateResult{},
		"fs generate scoped token":        tokenmgmt.GenerateScopedResult{},
		"fs list tokens":                  tokenmgmt.ListResult{},
		"fs mutate token":                 tokenmgmt.MutationResult{},
		"fs refresh token":                tokenmgmt.RefreshResult{},
		"fs vault secret":                 fs.VaultSecretResult{},
		"fs vault read secret":            fs.VaultReadSecretResult{},
		"fs vault list secrets":           fs.VaultListSecretsResult{},
		"fs vault delete":                 fs.VaultDeleteResult{},
		"fs vault token":                  fs.VaultTokenResult{},
		"fs vault audit":                  fs.VaultAuditResult{},
		"fs journal":                      fs.JournalResult{},
		"fs journal entries":              fs.JournalEntriesResult{},
		"fs journal search":               fs.JournalSearchResult{},
		"fs journal append":               fs.JournalAppendResult{},
		"fs journal verify":               fs.JournalVerifyResult{},
		"fs git clone or add worktree":    fs.GitWorkspaceCloneResult{},
		"fs git hydrate":                  fs.GitHydrateResult{},
		"fs git restore":                  fs.GitRestoreResult{},
		"fs git remove worktree":          fs.GitWorktreeRemoveResult{},
		"fs git delete internal resource": fs.GitDeleteResult{},
	}

	for name, result := range results {
		t.Run(name, func(t *testing.T) {
			var rendered bytes.Buffer
			if err := output.Render(&rendered, result, output.Options{Format: output.FormatText}); err != nil {
				t.Fatalf("render %T: %v", result, err)
			}
			trimmed := strings.TrimSpace(rendered.String())
			if strings.HasPrefix(trimmed, "{") || strings.HasPrefix(trimmed, "[") {
				t.Fatalf("text output fell back to JSON:\n%s", rendered.String())
			}
		})
	}
}
