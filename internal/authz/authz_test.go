package authz

import (
	"strings"
	"testing"

	"github.com/tidbcloud/ti-cli/internal/apperr"
)

func TestForCommand(t *testing.T) {
	tests := map[string]Permission{
		"ti organization list-projects": OrganizationProjectRead,
		"ti fs create-file-system":      FSVolumeCreate,
		"ti fs search-file-content":     FSFileRead,
		"ti fs mount-file-system":       FSMount,
		"ti fs-vault mount-vault":       FSVaultSecretRead,
	}

	for command, want := range tests {
		t.Run(command, func(t *testing.T) {
			got, err := ForCommand(command)
			if err != nil {
				t.Fatalf("ForCommand failed: %v", err)
			}
			if got != want {
				t.Fatalf("expected %s, got %s", want, got)
			}
		})
	}
}

func TestPermissionDenied(t *testing.T) {
	err := PermissionDenied("stage", StarterClusterCreate, "create Starter clusters", "aws", "us-east-1")
	if got := apperr.ExitCodeFor(err); got != 4 {
		t.Fatalf("expected exit code 4, got %d", got)
	}
	message := apperr.MessageFor(err)
	if !strings.Contains(message, "permission denied") || !strings.Contains(message, string(StarterClusterCreate)) {
		t.Fatalf("unexpected message %q", message)
	}
}

func TestDBCommandsDoNotUseStaticCommandPermissions(t *testing.T) {
	_, err := ForCommand("ti db describe-db-cluster")
	if apperr.CodeFor(err) != "authz.permission_mapping_missing" {
		t.Fatalf("expected DB permission to be dynamically resolved, got %v", err)
	}
}
