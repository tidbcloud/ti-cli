package authz

import (
	"fmt"

	"github.com/tidbcloud/tdc/internal/apperr"
	"github.com/tidbcloud/tdc/internal/config"
)

type Permission string

const (
	OrganizationProjectRead Permission = "organization.project.read"
	StarterClusterRead      Permission = "starter.cluster.read"
	StarterClusterCreate    Permission = "starter.cluster.create"
	StarterClusterUpdate    Permission = "starter.cluster.update"
	StarterClusterDelete    Permission = "starter.cluster.delete"
	StarterBranchRead       Permission = "starter.branch.read"
	StarterBranchCreate     Permission = "starter.branch.create"
	StarterBranchDelete     Permission = "starter.branch.delete"
	StarterSQLUserRead      Permission = "starter.sql_user.read"
	StarterSQLUserCreate    Permission = "starter.sql_user.create"
	StarterSQLUserUpdate    Permission = "starter.sql_user.update"
	StarterSQLExecute       Permission = "starter.sql.execute"
	FSVolumeRead            Permission = "fs.volume.read"
	FSVolumeCreate          Permission = "fs.volume.create"
	FSVolumeDelete          Permission = "fs.volume.delete"
	FSFileRead              Permission = "fs.file.read"
	FSFileWrite             Permission = "fs.file.write"
	FSVaultSecretRead       Permission = "fs.vault.secret.read"
	FSVaultSecretCreate     Permission = "fs.vault.secret.create"
	FSVaultSecretUpdate     Permission = "fs.vault.secret.update"
	FSVaultSecretDelete     Permission = "fs.vault.secret.delete"
	FSVaultGrantCreate      Permission = "fs.vault.grant.create"
	FSVaultGrantDelete      Permission = "fs.vault.grant.delete"
	FSVaultAuditRead        Permission = "fs.vault.audit.read"
	FSJournalCreate         Permission = "fs.journal.create"
	FSJournalAppend         Permission = "fs.journal.append"
	FSJournalRead           Permission = "fs.journal.read"
	FSJournalSearch         Permission = "fs.journal.search"
	FSJournalVerify         Permission = "fs.journal.verify"
	FSGitWorkspaceRead      Permission = "fs.git_workspace.read"
	FSGitWorkspaceWrite     Permission = "fs.git_workspace.write"
	FSMount                 Permission = "fs.mount"
)

var commandPermissions = map[string]Permission{
	"tdc organization list-projects":        OrganizationProjectRead,
	"tdc db create-db-cluster":              StarterClusterCreate,
	"tdc db list-db-clusters":               StarterClusterRead,
	"tdc db describe-db-cluster":            StarterClusterRead,
	"tdc db update-db-cluster":              StarterClusterUpdate,
	"tdc db delete-db-cluster":              StarterClusterDelete,
	"tdc db create-db-cluster-branch":       StarterBranchCreate,
	"tdc db list-db-cluster-branches":       StarterBranchRead,
	"tdc db describe-db-cluster-branch":     StarterBranchRead,
	"tdc db delete-db-cluster-branch":       StarterBranchDelete,
	"tdc db create-db-sql-users":            StarterSQLUserCreate,
	"tdc db format-db-connection-string":    StarterSQLUserRead,
	"tdc db execute-sql-statement":          StarterSQLExecute,
	"tdc fs create-file-system":             FSVolumeCreate,
	"tdc fs delete-file-system":             FSVolumeDelete,
	"tdc fs list-file-systems":              FSVolumeRead,
	"tdc fs describe-file-system":           FSVolumeRead,
	"tdc fs check-file-system":              FSVolumeRead,
	"tdc fs copy-file":                      FSFileWrite,
	"tdc fs read-file":                      FSFileRead,
	"tdc fs list-files":                     FSFileRead,
	"tdc fs describe-file":                  FSFileRead,
	"tdc fs move-file":                      FSFileWrite,
	"tdc fs delete-file":                    FSFileWrite,
	"tdc fs create-directory":               FSFileWrite,
	"tdc fs chmod-file":                     FSFileWrite,
	"tdc fs create-symlink":                 FSFileWrite,
	"tdc fs create-hardlink":                FSFileWrite,
	"tdc fs search-file-content":            FSFileRead,
	"tdc fs find-files":                     FSFileRead,
	"tdc fs create-layer":                   FSFileWrite,
	"tdc fs list-layers":                    FSFileRead,
	"tdc fs describe-layer":                 FSFileRead,
	"tdc fs diff-layer":                     FSFileRead,
	"tdc fs create-layer-checkpoint":        FSFileWrite,
	"tdc fs rollback-layer":                 FSFileWrite,
	"tdc fs commit-layer":                   FSFileWrite,
	"tdc fs pack-file-system":               FSFileWrite,
	"tdc fs unpack-file-system":             FSFileRead,
	"tdc fs mount-file-system":              FSMount,
	"tdc fs drain-file-system":              FSMount,
	"tdc fs unmount-file-system":            FSMount,
	"tdc fs-vault create-secret":            FSVaultSecretCreate,
	"tdc fs-vault replace-secret":           FSVaultSecretUpdate,
	"tdc fs-vault read-secret":              FSVaultSecretRead,
	"tdc fs-vault list-secrets":             FSVaultSecretRead,
	"tdc fs-vault delete-secret":            FSVaultSecretDelete,
	"tdc fs-vault create-grant":             FSVaultGrantCreate,
	"tdc fs-vault delete-grant":             FSVaultGrantDelete,
	"tdc fs-vault list-audit-events":        FSVaultAuditRead,
	"tdc fs-vault run-with-secret":          FSVaultSecretRead,
	"tdc fs-vault mount-vault":              FSVaultSecretRead,
	"tdc fs-vault unmount-vault":            FSVaultSecretRead,
	"tdc fs-journal create-journal":         FSJournalCreate,
	"tdc fs-journal append-journal-entries": FSJournalAppend,
	"tdc fs-journal read-journal-entries":   FSJournalRead,
	"tdc fs-journal search-journal-entries": FSJournalSearch,
	"tdc fs-journal verify-journal":         FSJournalVerify,
	"tdc fs-git clone-git-workspace":        FSGitWorkspaceWrite,
	"tdc fs-git hydrate-git-workspace":      FSGitWorkspaceRead,
	"tdc fs-git add-git-worktree":           FSGitWorkspaceWrite,
	"tdc fs-git remove-git-worktree":        FSGitWorkspaceWrite,
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
