package authz

import (
	"fmt"

	"github.com/tidbcloud/ti-cli/internal/apperr"
	"github.com/tidbcloud/ti-cli/internal/config"
)

type Permission string

const (
	DBClusterDiscover    Permission = "db.cluster.discover"
	StarterClusterRead   Permission = "starter.cluster.read"
	StarterClusterCreate Permission = "starter.cluster.create"
	StarterClusterUpdate Permission = "starter.cluster.update"
	StarterClusterDelete Permission = "starter.cluster.delete"
	StarterBranchRead    Permission = "starter.branch.read"
	StarterBranchCreate  Permission = "starter.branch.create"
	StarterBranchDelete  Permission = "starter.branch.delete"
	StarterSQLUserRead   Permission = "starter.sql_user.read"
	StarterSQLUserCreate Permission = "starter.sql_user.create"
	StarterSQLUserUpdate Permission = "starter.sql_user.update"
	StarterSQLExecute    Permission = "starter.sql.execute"
	FSVolumeRead         Permission = "fs.volume.read"
	FSVolumeCreate       Permission = "fs.volume.create"
	FSVolumeDelete       Permission = "fs.volume.delete"
	FSTokenList          Permission = "fs.token.list"
	FSTokenGenerate      Permission = "fs.token.generate"
	FSTokenEnable        Permission = "fs.token.enable"
	FSTokenDisable       Permission = "fs.token.disable"
	FSTokenDelete        Permission = "fs.token.delete"
	FSTokenRefresh       Permission = "fs.token.refresh"
	FSFileRead           Permission = "fs.file.read"
	FSFileWrite          Permission = "fs.file.write"
	FSVaultSecretRead    Permission = "fs.vault.secret.read"
	FSVaultSecretCreate  Permission = "fs.vault.secret.create"
	FSVaultSecretUpdate  Permission = "fs.vault.secret.update"
	FSVaultSecretDelete  Permission = "fs.vault.secret.delete"
	FSVaultGrantCreate   Permission = "fs.vault.grant.create"
	FSVaultGrantDelete   Permission = "fs.vault.grant.delete"
	FSVaultAuditRead     Permission = "fs.vault.audit.read"
	FSJournalCreate      Permission = "fs.journal.create"
	FSJournalAppend      Permission = "fs.journal.append"
	FSJournalRead        Permission = "fs.journal.read"
	FSJournalSearch      Permission = "fs.journal.search"
	FSJournalVerify      Permission = "fs.journal.verify"
	FSGitWorkspaceRead   Permission = "fs.git_workspace.read"
	FSGitWorkspaceWrite  Permission = "fs.git_workspace.write"
	FSMount              Permission = "fs.mount"
)

var commandPermissions = map[string]Permission{
	"ti fs create-file-system":             FSVolumeCreate,
	"ti fs delete-file-system":             FSVolumeDelete,
	"ti fs list-file-systems":              FSVolumeRead,
	"ti fs describe-file-system":           FSVolumeRead,
	"ti fs check-file-system":              FSVolumeRead,
	"ti fs generate-file-system-token":     FSTokenGenerate,
	"ti fs list-file-system-tokens":        FSTokenList,
	"ti fs enable-file-system-token":       FSTokenEnable,
	"ti fs disable-file-system-token":      FSTokenDisable,
	"ti fs delete-file-system-token":       FSTokenDelete,
	"ti fs refresh-file-system-token":      FSTokenRefresh,
	"ti fs copy-file":                      FSFileWrite,
	"ti fs read-file":                      FSFileRead,
	"ti fs list-files":                     FSFileRead,
	"ti fs describe-file":                  FSFileRead,
	"ti fs move-file":                      FSFileWrite,
	"ti fs delete-file":                    FSFileWrite,
	"ti fs create-directory":               FSFileWrite,
	"ti fs chmod-file":                     FSFileWrite,
	"ti fs create-symlink":                 FSFileWrite,
	"ti fs create-hardlink":                FSFileWrite,
	"ti fs search-file-content":            FSFileRead,
	"ti fs find-files":                     FSFileRead,
	"ti fs create-layer":                   FSFileWrite,
	"ti fs list-layers":                    FSFileRead,
	"ti fs describe-layer":                 FSFileRead,
	"ti fs diff-layer":                     FSFileRead,
	"ti fs create-layer-checkpoint":        FSFileWrite,
	"ti fs rollback-layer":                 FSFileWrite,
	"ti fs commit-layer":                   FSFileWrite,
	"ti fs pack-file-system":               FSFileWrite,
	"ti fs unpack-file-system":             FSFileRead,
	"ti fs mount-file-system":              FSMount,
	"ti fs drain-file-system":              FSMount,
	"ti fs unmount-file-system":            FSMount,
	"ti fs-vault create-secret":            FSVaultSecretCreate,
	"ti fs-vault replace-secret":           FSVaultSecretUpdate,
	"ti fs-vault read-secret":              FSVaultSecretRead,
	"ti fs-vault list-secrets":             FSVaultSecretRead,
	"ti fs-vault delete-secret":            FSVaultSecretDelete,
	"ti fs-vault create-grant":             FSVaultGrantCreate,
	"ti fs-vault delete-grant":             FSVaultGrantDelete,
	"ti fs-vault list-audit-events":        FSVaultAuditRead,
	"ti fs-vault run-with-secret":          FSVaultSecretRead,
	"ti fs-vault mount-vault":              FSVaultSecretRead,
	"ti fs-vault unmount-vault":            FSVaultSecretRead,
	"ti fs-journal create-journal":         FSJournalCreate,
	"ti fs-journal append-journal-entries": FSJournalAppend,
	"ti fs-journal read-journal-entries":   FSJournalRead,
	"ti fs-journal search-journal-entries": FSJournalSearch,
	"ti fs-journal verify-journal":         FSJournalVerify,
	"ti fs-git clone-git-workspace":        FSGitWorkspaceWrite,
	"ti fs-git hydrate-git-workspace":      FSGitWorkspaceRead,
	"ti fs-git add-git-worktree":           FSGitWorkspaceWrite,
	"ti fs-git remove-git-worktree":        FSGitWorkspaceWrite,
}

func ForCommand(commandPath string) (Permission, error) {
	permission, ok := commandPermissions[commandPath]
	if !ok {
		return "", apperr.New(
			"authz.permission_mapping_missing",
			"usage",
			2,
			fmt.Sprintf("internal permission mapping missing for %s", commandPath),
		)
	}
	return permission, nil
}

func PermissionDenied(profileName string, permission Permission, action, provider, regionCode string) error {
	if profileName == "" {
		profileName = config.DefaultProfile
	}
	if action == "" {
		action = string(permission)
	}
	location := provider
	if regionCode != "" {
		location = provider + "/" + regionCode
	}
	return apperr.New(
		"authz.permission_denied",
		"authorization",
		4,
		fmt.Sprintf("permission denied: profile %q is not allowed to %s in %s. Ask an organization admin for %s permission or use another profile.", profileName, action, location, permission),
	)
}
