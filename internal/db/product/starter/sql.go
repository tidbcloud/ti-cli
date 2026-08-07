package starter

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/tidbcloud/tdc/internal/api"
	"github.com/tidbcloud/tdc/internal/api/endpoints"
	apiiam "github.com/tidbcloud/tdc/internal/api/iam"
	apistarter "github.com/tidbcloud/tdc/internal/api/starter"
	"github.com/tidbcloud/tdc/internal/apperr"
	"github.com/tidbcloud/tdc/internal/authz"
	"github.com/tidbcloud/tdc/internal/config"
	rootdb "github.com/tidbcloud/tdc/internal/db"
	"github.com/tidbcloud/tdc/internal/db/connectionstring"
	"github.com/tidbcloud/tdc/internal/db/sqlaccess"
	"github.com/tidbcloud/tdc/internal/db/sqlcred"
	"github.com/tidbcloud/tdc/internal/db/sqlhttp"
	"github.com/tidbcloud/tdc/internal/db/sqlmysql"
	"github.com/tidbcloud/tdc/internal/db/sqlresult"
	"github.com/tidbcloud/tdc/internal/db/sqlsingle"
	"github.com/tidbcloud/tdc/internal/db/validate"
	"github.com/tidbcloud/tdc/internal/dryrun"
)

const (
	transportHTTPS = "https"
	transportMySQL = "mysql"
)

func (s Service) PrepareQueryAccess(ctx context.Context, opts PrepareQueryAccessOptions) (PrepareQueryAccessResult, error) {
	clusterID, err := validate.ClusterID(opts.ClusterID)
	if err != nil {
		return PrepareQueryAccessResult{}, err
	}
	if _, err := sqlcred.SafeClusterID(clusterID); err != nil {
		return PrepareQueryAccessResult{}, err
	}
	permission := operationPermission(opts.Dispatch, authz.StarterSQLUserCreate)
	if _, err := s.clusterFromDispatchOrRead(ctx, opts.Profile, opts.Dispatch, clusterID, "FULL", permission, "create Starter DB SQL users"); err != nil {
		return PrepareQueryAccessResult{}, err
	}
	iamClient, err := s.iamClient(opts.Profile, permission, "create Starter DB SQL users")
	if err != nil {
		return PrepareQueryAccessResult{}, err
	}
	homeDir, err := s.homeDir()
	if err != nil {
		return PrepareQueryAccessResult{}, err
	}
	local, err := sqlcred.Read(homeDir, clusterID)
	if err != nil {
		return PrepareQueryAccessResult{}, err
	}
	result, updatedLocal, err := sqlaccess.Prepare(ctx, iamClient, sqlaccess.Options{
		ClusterID: clusterID,
		Local:     local,
	})
	if err != nil {
		return PrepareQueryAccessResult{}, err
	}
	if err := sqlcred.Write(homeDir, clusterID, updatedLocal); err != nil {
		return PrepareQueryAccessResult{}, err
	}
	return PrepareQueryAccessResult{Result: result}, nil
}

func (s Service) DryRunPrepareQueryAccess(ctx context.Context, commandPath string, opts PrepareQueryAccessOptions) (dryrun.Result, error) {
	if err := validateProfile(opts.Profile); err != nil {
		return dryrun.Result{}, err
	}
	clusterID, err := validate.ClusterID(opts.ClusterID)
	if err != nil {
		return dryrun.Result{}, err
	}
	if _, err := sqlcred.SafeClusterID(clusterID); err != nil {
		return dryrun.Result{}, err
	}
	endpoint, err := s.resolveIAM()
	if err != nil {
		return dryrun.Result{}, err
	}
	prepareResult, _, err := sqlaccess.Prepare(ctx, nil, sqlaccess.Options{
		ClusterID: clusterID,
		DryRun:    true,
	})
	if err != nil {
		return dryrun.Result{}, err
	}
	return dryrun.New(
		commandPath,
		"create_db_sql_users",
		dryrun.RequestSummary{
			Method:      "GET/POST/PATCH",
			Path:        "/v1beta1/clusters/" + clusterID + "/sqlUsers",
			Description: "normal execution verifies the cluster, lists SQL users, creates missing tdc-managed read_only/read_write/admin users, and rotates passwords for missing local credentials",
			Body:        prepareResult,
		},
		dryrun.Check{Name: "config_and_credentials", Status: "passed", Message: fmt.Sprintf("profile %q loaded", profileName(opts.Profile))},
		dryrun.Check{Name: "endpoint_selection", Status: "passed", Message: string(endpoint.Service)},
		dryrun.Check{Name: "cluster_discovery_permission", Status: "passed", Message: string(opts.Dispatch.DiscoveryPermission)},
		dryrun.Check{Name: "operation_permission", Status: "passed", Message: string(opts.Dispatch.OperationPermission)},
		dryrun.Check{Name: "cluster_id", Status: "passed", Message: clusterID},
		dryrun.Check{Name: "starter_cluster_precondition", Status: "passed", Message: "normal execution verifies the cluster is Starter before IAM requests or local credential writes"},
	), nil
}

func (s Service) CreateConnectionString(ctx context.Context, opts CreateConnectionStringOptions) (connectionstring.Result, error) {
	clusterID, mode, credential, cluster, err := s.sqlConnectionInputs(ctx, opts.Profile, opts.ClusterID, opts.ReadOnly, opts.ReadWrite, opts.Admin, opts.Dispatch, authz.StarterSQLUserRead, "create Starter DB connection string")
	if err != nil {
		return connectionstring.Result{}, err
	}
	host, port, err := clusterPublicEndpoint(cluster)
	if err != nil {
		return connectionstring.Result{}, err
	}
	return connectionstring.Build(connectionstring.Input{
		ClusterID:              clusterID,
		AccessMode:             mode,
		Username:               credential.Username,
		Password:               credential.Password,
		Host:                   host,
		Port:                   port,
		Database:               opts.Database,
		Format:                 opts.Format,
		EnvPrefix:              opts.EnvPrefix,
		EnvIncludeDatabaseURL:  opts.EnvIncludeDatabaseURL,
		EnvDatabaseURLVariable: opts.EnvDatabaseURLVariable,
	})
}

func (s Service) ExecuteSQL(ctx context.Context, opts ExecuteSQLOptions) (sqlresult.Result, error) {
	if err := sqlsingle.Validate(opts.SQL); err != nil {
		return sqlresult.Result{}, err
	}
	transport, err := validateTransport(opts.Transport)
	if err != nil {
		return sqlresult.Result{}, err
	}
	clusterID, mode, credential, cluster, err := s.sqlConnectionInputs(ctx, opts.Profile, opts.ClusterID, opts.ReadOnly, opts.ReadWrite, opts.Admin, opts.Dispatch, authz.StarterSQLExecute, "execute Starter DB SQL statement")
	if err != nil {
		return sqlresult.Result{}, err
	}
	host, port, err := clusterPublicEndpoint(cluster)
	if err != nil {
		return sqlresult.Result{}, err
	}
	switch transport {
	case transportHTTPS:
		return sqlhttp.Execute(ctx, sqlhttp.Options{
			ClusterID:   clusterID,
			AccessMode:  mode,
			Username:    credential.Username,
			Password:    credential.Password,
			Host:        host,
			Database:    opts.Database,
			SQL:         opts.SQL,
			BaseURL:     s.SQLHTTPBaseURL,
			HTTPClient:  s.HTTPClient,
			Debug:       s.Debug,
			DebugWriter: s.DebugWriter,
			UserAgent:   "tdc db execute-sql-statement",
		})
	case transportMySQL:
		if err := sqlmysql.ValidateEndpoint(host, port); err != nil {
			return sqlresult.Result{}, err
		}
		return sqlmysql.Execute(ctx, sqlmysql.Options{
			ClusterID:  clusterID,
			AccessMode: mode,
			Username:   credential.Username,
			Password:   credential.Password,
			Host:       host,
			Port:       port,
			Database:   opts.Database,
			SQL:        opts.SQL,
			DriverName: s.MySQLDriverName,
		})
	default:
		panic("unreachable SQL transport")
	}
}

func (s Service) sqlConnectionInputs(ctx context.Context, profile *config.Profile, clusterIDValue string, readOnly, readWrite, admin bool, dispatch rootdb.DispatchContext, fallbackPermission authz.Permission, action string) (string, sqlcred.AccessMode, sqlcred.Credential, apistarter.Cluster, error) {
	clusterID, err := validate.ClusterID(clusterIDValue)
	if err != nil {
		return "", "", sqlcred.Credential{}, apistarter.Cluster{}, err
	}
	if _, err := sqlcred.SafeClusterID(clusterID); err != nil {
		return "", "", sqlcred.Credential{}, apistarter.Cluster{}, err
	}
	mode, err := accessMode(readOnly, readWrite, admin)
	if err != nil {
		return "", "", sqlcred.Credential{}, apistarter.Cluster{}, err
	}
	cluster, err := s.clusterFromDispatchOrRead(ctx, profile, dispatch, clusterID, "FULL", fallbackPermission, action)
	if err != nil {
		return "", "", sqlcred.Credential{}, apistarter.Cluster{}, err
	}
	homeDir, err := s.homeDir()
	if err != nil {
		return "", "", sqlcred.Credential{}, apistarter.Cluster{}, err
	}
	local, err := sqlcred.Read(homeDir, clusterID)
	if err != nil {
		return "", "", sqlcred.Credential{}, apistarter.Cluster{}, err
	}
	credential, ok := local.Credential(mode)
	if !ok {
		return "", "", sqlcred.Credential{}, apistarter.Cluster{}, apperr.New(
			"db.sql_credentials_missing",
			"config",
			2,
			fmt.Sprintf("missing prepared %s DB SQL credentials for cluster %s; run tdc db create-db-sql-users --db-cluster-id %s", mode, clusterID, clusterID),
		)
	}
	return clusterID, mode, credential, cluster, nil
}

func accessMode(readOnly, readWrite, admin bool) (sqlcred.AccessMode, error) {
	count := 0
	for _, selected := range []bool{readOnly, readWrite, admin} {
		if selected {
			count++
		}
	}
	if count > 1 {
		return "", apperr.New("db.sql_access_mode_conflict", "usage", 2, "--read-only, --read-write, and --admin are mutually exclusive")
	}
	switch {
	case readOnly:
		return sqlcred.ReadOnly, nil
	case admin:
		return sqlcred.Admin, nil
	default:
		return sqlcred.ReadWrite, nil
	}
}

func validateTransport(value string) (string, error) {
	if value == "" {
		return transportHTTPS, nil
	}
	switch value {
	case transportHTTPS, transportMySQL:
		return value, nil
	default:
		return "", apperr.New("db.invalid_sql_transport", "usage", 2, "--transport must be https or mysql")
	}
}

func clusterPublicEndpoint(cluster apistarter.Cluster) (string, int32, error) {
	if cluster.Endpoints == nil || cluster.Endpoints.Public == nil {
		return "", 0, apperr.New("db.cluster_public_endpoint_missing", "api", 1, "cluster public endpoint is missing; try again after the Starter cluster becomes ACTIVE")
	}
	host := strings.TrimSpace(cluster.Endpoints.Public.Host)
	port := cluster.Endpoints.Public.Port
	if host == "" || port <= 0 {
		return "", 0, apperr.New("db.cluster_public_endpoint_missing", "api", 1, "cluster public endpoint host or port is missing; try again after the Starter cluster becomes ACTIVE")
	}
	return host, port, nil
}

func (s Service) iamClient(profile *config.Profile, permission authz.Permission, action string) (*apiiam.Client, error) {
	if err := validateProfile(profile); err != nil {
		return nil, err
	}
	endpoint, err := s.resolveIAM()
	if err != nil {
		return nil, err
	}
	client, err := api.NewDigestClient(profile, endpoint, permission, api.Options{
		Action:      action,
		HTTPClient:  s.HTTPClient,
		Transport:   s.Transport,
		Timeout:     s.Timeout,
		Debug:       s.Debug,
		DebugWriter: s.DebugWriter,
		UserAgent:   "tdc db sql",
	})
	if err != nil {
		return nil, err
	}
	return apiiam.New(client), nil
}

func (s Service) resolveIAM() (endpoints.Endpoint, error) {
	return s.resolver().ResolveIAM()
}

func (s Service) homeDir() (string, error) {
	if s.HomeDir != "" {
		return s.HomeDir, nil
	}
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return "", apperr.Wrap("config.home_dir", "config", 1, "cannot determine home directory", err)
	}
	return homeDir, nil
}
