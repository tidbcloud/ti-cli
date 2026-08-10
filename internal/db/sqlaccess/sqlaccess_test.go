package sqlaccess

import (
	"context"
	"testing"

	apiiam "github.com/tidbcloud/ti-cli/internal/api/iam"
	"github.com/tidbcloud/ti-cli/internal/apperr"
	"github.com/tidbcloud/ti-cli/internal/db/sqlcred"
)

func TestPrepareCreatesMissingUsers(t *testing.T) {
	client := &fakeUserClient{}
	result, doc, err := Prepare(context.Background(), client, Options{ClusterID: "cluster-1"})
	if err != nil {
		t.Fatalf("Prepare failed: %v", err)
	}
	if len(client.created) != 3 {
		t.Fatalf("expected 3 created users, got %#v", client.created)
	}
	for _, mode := range []sqlcred.AccessMode{sqlcred.ReadOnly, sqlcred.ReadWrite, sqlcred.Admin} {
		status := result.Users[string(mode)]
		if status.Status != "created" || status.Username == "" {
			t.Fatalf("unexpected status for %s: %#v", mode, status)
		}
		credential, ok := doc.Credential(mode)
		if !ok || credential.Password == "" || credential.Username != status.Username {
			t.Fatalf("missing local credential for %s: %#v ok=%t", mode, credential, ok)
		}
	}
}

func TestPrepareExistingUsersDoesNotCreateDuplicates(t *testing.T) {
	client := &fakeUserClient{
		users: []apiiam.SQLUser{
			{UserName: "prefix.ti_ro", AuthMethod: AuthMethodMySQLNativePassword, BuiltinRole: "prefix.role_readonly"},
			{UserName: "prefix.ti_rw", AuthMethod: AuthMethodMySQLNativePassword, BuiltinRole: "prefix.role_readwrite"},
			{UserName: "prefix.ti_admin", AuthMethod: AuthMethodMySQLNativePassword, BuiltinRole: "prefix.role_admin"},
		},
	}
	local := sqlcred.Document{
		ReadOnly:  sqlcred.Credential{Username: "prefix.ti_ro", Password: "ro"},
		ReadWrite: sqlcred.Credential{Username: "prefix.ti_rw", Password: "rw"},
		Admin:     sqlcred.Credential{Username: "prefix.ti_admin", Password: "admin"},
	}
	result, _, err := Prepare(context.Background(), client, Options{ClusterID: "cluster-1", Local: local})
	if err != nil {
		t.Fatalf("Prepare failed: %v", err)
	}
	if len(client.created) != 0 || len(client.updated) != 0 {
		t.Fatalf("expected no remote mutations, created=%#v updated=%#v", client.created, client.updated)
	}
	if result.Users[string(sqlcred.ReadWrite)].Status != "exists" {
		t.Fatalf("unexpected result: %#v", result)
	}
}

func TestPrepareRotatesPasswordWhenLocalCredentialMissing(t *testing.T) {
	client := &fakeUserClient{
		users: []apiiam.SQLUser{
			{UserName: "prefix.ti_ro", AuthMethod: AuthMethodMySQLNativePassword, BuiltinRole: "role_readonly"},
			{UserName: "prefix.ti_rw", AuthMethod: AuthMethodMySQLNativePassword, BuiltinRole: "role_readwrite"},
			{UserName: "prefix.ti_admin", AuthMethod: AuthMethodMySQLNativePassword, BuiltinRole: "role_admin"},
		},
	}
	result, doc, err := Prepare(context.Background(), client, Options{ClusterID: "cluster-1"})
	if err != nil {
		t.Fatalf("Prepare failed: %v", err)
	}
	if len(client.created) != 0 || len(client.updated) != 3 {
		t.Fatalf("expected 3 updates and no creates, created=%#v updated=%#v", client.created, client.updated)
	}
	if result.Users[string(sqlcred.ReadOnly)].Status != "updated_password" {
		t.Fatalf("unexpected result: %#v", result)
	}
	if credential, ok := doc.Credential(sqlcred.Admin); !ok || credential.Password == "" {
		t.Fatalf("expected admin credential after rotation: %#v ok=%t", credential, ok)
	}
}

func TestPrepareConflictingRemoteUserFails(t *testing.T) {
	client := &fakeUserClient{
		users: []apiiam.SQLUser{
			{UserName: "prefix.ti_rw", AuthMethod: AuthMethodMySQLNativePassword, BuiltinRole: "role_admin"},
		},
	}
	_, _, err := Prepare(context.Background(), client, Options{ClusterID: "cluster-1"})
	if err == nil {
		t.Fatal("expected conflict")
	}
	if got := apperr.ExitCodeFor(err); got != 2 {
		t.Fatalf("expected exit code 2, got %d", got)
	}
}

func TestPrepareDryRunDoesNotCallRemote(t *testing.T) {
	client := &fakeUserClient{failOnList: true}
	result, _, err := Prepare(context.Background(), client, Options{ClusterID: "cluster-1", DryRun: true})
	if err != nil {
		t.Fatalf("Prepare dry-run failed: %v", err)
	}
	if result.Users[string(sqlcred.ReadWrite)].Status != "skipped_by_dry_run" {
		t.Fatalf("unexpected dry-run result: %#v", result)
	}
}

type fakeUserClient struct {
	users      []apiiam.SQLUser
	created    []apiiam.CreateSQLUserRequest
	updated    []string
	failOnList bool
}

func (c *fakeUserClient) ListSQLUsers(ctx context.Context, clusterID string, opts apiiam.ListSQLUsersOptions) (apiiam.ListSQLUsersResponse, error) {
	if c.failOnList {
		t := ctx.Value("testing")
		_ = t
		return apiiam.ListSQLUsersResponse{}, apperr.New("test.fail", "test", 1, "list should not be called")
	}
	return apiiam.ListSQLUsersResponse{SQLUsers: c.users}, nil
}

func (c *fakeUserClient) CreateSQLUser(ctx context.Context, clusterID string, input apiiam.CreateSQLUserRequest) (apiiam.SQLUser, error) {
	c.created = append(c.created, input)
	return apiiam.SQLUser{
		UserName:    "prefix." + input.UserName,
		AuthMethod:  input.AuthMethod,
		BuiltinRole: input.BuiltinRole,
	}, nil
}

func (c *fakeUserClient) UpdateSQLUser(ctx context.Context, clusterID, userName string, input apiiam.UpdateSQLUserRequest) (apiiam.SQLUser, error) {
	c.updated = append(c.updated, userName)
	for _, user := range c.users {
		if user.UserName == userName {
			return user, nil
		}
	}
	return apiiam.SQLUser{UserName: userName, AuthMethod: AuthMethodMySQLNativePassword}, nil
}
