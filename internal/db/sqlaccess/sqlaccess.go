package sqlaccess

import (
	"context"
	"crypto/rand"
	"fmt"
	"math/big"
	"strings"
	"text/tabwriter"

	apiiam "github.com/tidbcloud/ti-cli/internal/api/iam"
	"github.com/tidbcloud/ti-cli/internal/apperr"
	"github.com/tidbcloud/ti-cli/internal/db/sqlcred"
)

const AuthMethodMySQLNativePassword = "mysql_native_password"

type UserClient interface {
	ListSQLUsers(ctx context.Context, clusterID string, opts apiiam.ListSQLUsersOptions) (apiiam.ListSQLUsersResponse, error)
	CreateSQLUser(ctx context.Context, clusterID string, input apiiam.CreateSQLUserRequest) (apiiam.SQLUser, error)
	UpdateSQLUser(ctx context.Context, clusterID, userName string, input apiiam.UpdateSQLUserRequest) (apiiam.SQLUser, error)
}

type Options struct {
	ClusterID string
	Local     sqlcred.Document
	DryRun    bool
}

type Result struct {
	ClusterID string                    `json:"cluster_id"`
	Users     map[string]RoleUserStatus `json:"users"`
}

type RoleUserStatus struct {
	AccessMode  sqlcred.AccessMode `json:"access_mode"`
	Username    string             `json:"username,omitempty"`
	Suffix      string             `json:"suffix"`
	BuiltinRole string             `json:"builtin_role"`
	AuthMethod  string             `json:"auth_method"`
	Status      string             `json:"status"`
}

func (r Result) Human() string {
	var out strings.Builder
	_, _ = fmt.Fprintf(&out, "Cluster ID: %s\n", r.ClusterID)
	writer := tabwriter.NewWriter(&out, 0, 0, 2, ' ', 0)
	_, _ = fmt.Fprintln(writer, "ACCESS_MODE\tUSERNAME\tBUILTIN_ROLE\tAUTH_METHOD\tSTATUS")
	for _, plan := range Plans() {
		user, ok := r.Users[string(plan.Mode)]
		if !ok {
			continue
		}
		_, _ = fmt.Fprintf(writer, "%s\t%s\t%s\t%s\t%s\n", user.AccessMode, user.Username, user.BuiltinRole, user.AuthMethod, user.Status)
	}
	_ = writer.Flush()
	return strings.TrimRight(out.String(), "\n")
}

type Plan struct {
	Mode        sqlcred.AccessMode
	Suffix      string
	BuiltinRole string
}

func Plans() []Plan {
	return []Plan{
		{Mode: sqlcred.ReadOnly, Suffix: "ti_ro", BuiltinRole: "role_readonly"},
		{Mode: sqlcred.ReadWrite, Suffix: "ti_rw", BuiltinRole: "role_readwrite"},
		{Mode: sqlcred.Admin, Suffix: "ti_admin", BuiltinRole: "role_admin"},
	}
}

func Prepare(ctx context.Context, client UserClient, opts Options) (Result, sqlcred.Document, error) {
	if opts.DryRun {
		return dryRunResult(opts.ClusterID), opts.Local, nil
	}
	local := opts.Local
	remote, err := listAllSQLUsers(ctx, client, opts.ClusterID)
	if err != nil {
		return Result{}, sqlcred.Document{}, err
	}

	result := Result{
		ClusterID: opts.ClusterID,
		Users:     map[string]RoleUserStatus{},
	}
	for _, plan := range Plans() {
		remoteUser, found, err := findManagedUser(remote, plan)
		if err != nil {
			return Result{}, sqlcred.Document{}, err
		}
		status := RoleUserStatus{
			AccessMode:  plan.Mode,
			Suffix:      plan.Suffix,
			BuiltinRole: plan.BuiltinRole,
			AuthMethod:  AuthMethodMySQLNativePassword,
		}

		localCred, hasLocal := local.Credential(plan.Mode)
		switch {
		case found && hasLocal && localCred.Username == remoteUser.UserName:
			status.Username = remoteUser.UserName
			status.Status = "exists"
		case found:
			password, err := GeneratePassword()
			if err != nil {
				return Result{}, sqlcred.Document{}, err
			}
			updated, err := client.UpdateSQLUser(ctx, opts.ClusterID, remoteUser.UserName, apiiam.UpdateSQLUserRequest{Password: password})
			if err != nil {
				return Result{}, sqlcred.Document{}, err
			}
			username := updated.UserName
			if username == "" {
				username = remoteUser.UserName
			}
			local.SetCredential(plan.Mode, sqlcred.Credential{Username: username, Password: password})
			status.Username = username
			status.Status = "updated_password"
		default:
			password, err := GeneratePassword()
			if err != nil {
				return Result{}, sqlcred.Document{}, err
			}
			created, err := client.CreateSQLUser(ctx, opts.ClusterID, apiiam.CreateSQLUserRequest{
				UserName:    plan.Suffix,
				Password:    password,
				AuthMethod:  AuthMethodMySQLNativePassword,
				AutoPrefix:  true,
				BuiltinRole: plan.BuiltinRole,
			})
			if err != nil {
				return Result{}, sqlcred.Document{}, err
			}
			username := created.UserName
			if username == "" {
				username = plan.Suffix
			}
			local.SetCredential(plan.Mode, sqlcred.Credential{Username: username, Password: password})
			status.Username = username
			status.Status = "created"
		}
		result.Users[string(plan.Mode)] = status
	}
	return result, local, nil
}

func dryRunResult(clusterID string) Result {
	result := Result{
		ClusterID: clusterID,
		Users:     map[string]RoleUserStatus{},
	}
	for _, plan := range Plans() {
		result.Users[string(plan.Mode)] = RoleUserStatus{
			AccessMode:  plan.Mode,
			Suffix:      plan.Suffix,
			BuiltinRole: plan.BuiltinRole,
			AuthMethod:  AuthMethodMySQLNativePassword,
			Status:      "skipped_by_dry_run",
		}
	}
	return result
}

func listAllSQLUsers(ctx context.Context, client UserClient, clusterID string) ([]apiiam.SQLUser, error) {
	var users []apiiam.SQLUser
	pageToken := ""
	for {
		response, err := client.ListSQLUsers(ctx, clusterID, apiiam.ListSQLUsersOptions{
			PageSize:  100,
			PageToken: pageToken,
		})
		if err != nil {
			return nil, err
		}
		users = append(users, response.SQLUsers...)
		if response.NextPageToken == "" {
			return users, nil
		}
		pageToken = response.NextPageToken
	}
}

func findManagedUser(users []apiiam.SQLUser, plan Plan) (apiiam.SQLUser, bool, error) {
	for _, user := range users {
		if !matchesSuffix(user.UserName, plan.Suffix) {
			continue
		}
		if !matchesRole(user.BuiltinRole, plan.BuiltinRole) || user.AuthMethod != AuthMethodMySQLNativePassword {
			return apiiam.SQLUser{}, false, apperr.New(
				"db.sql_user_conflict",
				"usage",
				2,
				fmt.Sprintf("remote SQL user %q matches ti suffix %q but has builtinRole=%q authMethod=%q; refusing to modify it", user.UserName, plan.Suffix, user.BuiltinRole, user.AuthMethod),
			)
		}
		return user, true, nil
	}
	return apiiam.SQLUser{}, false, nil
}

func matchesSuffix(userName, suffix string) bool {
	return userName == suffix || strings.HasSuffix(userName, "."+suffix)
}

func matchesRole(role, expected string) bool {
	return role == expected || strings.HasSuffix(role, "."+expected)
}

func GeneratePassword() (string, error) {
	const alphabet = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"
	const length = 32
	var out strings.Builder
	out.Grow(length)
	max := big.NewInt(int64(len(alphabet)))
	for i := 0; i < length; i++ {
		n, err := rand.Int(rand.Reader, max)
		if err != nil {
			return "", apperr.Wrap("db.sql_password_random", "runtime", 1, "generate SQL user password", err)
		}
		out.WriteByte(alphabet[n.Int64()])
	}
	return out.String(), nil
}
