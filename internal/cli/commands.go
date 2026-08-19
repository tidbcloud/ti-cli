package cli

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"runtime"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/tidbcloud/ti-cli/internal/apperr"
	"github.com/tidbcloud/ti-cli/internal/authz"
	"github.com/tidbcloud/ti-cli/internal/config"
	cfgconfigure "github.com/tidbcloud/ti-cli/internal/config/configure"
	"github.com/tidbcloud/ti-cli/internal/config/envcompat"
	"github.com/tidbcloud/ti-cli/internal/db"
	"github.com/tidbcloud/ti-cli/internal/db/connectionstring"
	dbstarter "github.com/tidbcloud/ti-cli/internal/db/product/starter"
	"github.com/tidbcloud/ti-cli/internal/dryrun"
	tifs "github.com/tidbcloud/ti-cli/internal/fs"
	"github.com/tidbcloud/ti-cli/internal/fs/fscred"
	"github.com/tidbcloud/ti-cli/internal/fs/tokenmgmt"
	outputpkg "github.com/tidbcloud/ti-cli/internal/output"
	"github.com/tidbcloud/ti-cli/internal/update"
	"github.com/tidbcloud/ti-cli/internal/version"
)

func newConfigureCommand(info version.Info) *cobra.Command {
	cmd := newCommand(commandSpec{
		Use:   "configure",
		Short: "Configure TiDB Cloud CLI (ti) options. If this command runs with no arguments, you will be prompted for configuration values.",
		RunE: func(cmd *cobra.Command, _ []string) error {
			profile, err := cmd.Flags().GetString("profile")
			if err != nil {
				return err
			}
			profileFlag := cmd.Flag("profile")
			if profileFlag != nil && profileFlag.Changed {
				if strings.TrimSpace(profile) == "" {
					return apperr.New("config.empty_profile", "usage", 2, "--profile cannot be empty")
				}
			} else {
				envProfile, _, _, err := envcompat.ResolveNames(nil, "TI_PROFILE", envcompat.LegacyNameFor("TI_PROFILE"))
				if err != nil {
					return err
				}
				if strings.TrimSpace(envProfile) != "" {
					profile = strings.TrimSpace(envProfile)
				}
			}
			regionCode, err := cmd.Flags().GetString("region-code")
			if err != nil {
				return err
			}
			publicKey, err := cmd.Flags().GetString("tidb-cloud-public-key")
			if err != nil {
				return err
			}
			privateKey, err := cmd.Flags().GetString("tidb-cloud-private-key")
			if err != nil {
				return err
			}
			nonInteractive, err := cmd.Flags().GetBool("non-interactive")
			if err != nil {
				return err
			}
			result, err := cfgconfigure.Run(cmd.Context(), cfgconfigure.Options{
				Profile:             profile,
				RegionCode:          regionCode,
				TiDBCloudPublicKey:  publicKey,
				TiDBCloudPrivateKey: privateKey,
				NonInteractive:      nonInteractive,
				In:                  cmd.InOrStdin(),
				Out:                 cmd.OutOrStdout(),
			})
			if err != nil {
				return err
			}
			return renderStructured(cmd, result)
		},
	}, info)
	cmd.Flags().String("region-code", "", "Default TiDB Cloud region code for the profile, for example aws-us-east-1 or aws-ap-southeast-1.")
	cmd.Flags().String("tidb-cloud-public-key", "", "TiDB Cloud API public key.")
	cmd.Flags().String("tidb-cloud-private-key", "", "TiDB Cloud API private key.")
	cmd.Flags().Bool("non-interactive", false, "Use this option to avoid being prompted for configuration values. You must provide at least three configuration values (--tidb-cloud-public-key, --tidb-cloud-private-key, and --region-code) when using this option. This is useful when running ti in a script or automated environment.")
	return cmd
}

func newUpdateCommand(info version.Info) *cobra.Command {
	cmd := newCommand(commandSpec{
		Use:   "update",
		Short: "Update this tool.",
		RunE: func(cmd *cobra.Command, _ []string) error {
			check, err := cmd.Flags().GetBool("check")
			if err != nil {
				return err
			}
			if check {
				if err := rejectCheckUpdateFlagCombinations(cmd); err != nil {
					return err
				}
				result, err := update.Check(cmd.Context(), info, update.CheckOptions{})
				if err != nil {
					return err
				}
				if err := renderStructured(cmd, result); err != nil {
					return err
				}
				failIfAvailable, err := cmd.Flags().GetBool("fail-if-update-available")
				if err != nil {
					return err
				}
				if failIfAvailable && result.UpdateAvailable {
					return apperr.New("update.available", "runtime", 1, "a newer ti release is available")
				}
				return nil
			}

			if cmd.Flags().Changed("fail-if-update-available") {
				return apperr.New(
					"update.incompatible_flag",
					"usage",
					2,
					"--fail-if-update-available requires --check",
				)
			}
			targetVersion, err := cmd.Flags().GetString("target-version")
			if err != nil {
				return err
			}
			dryRun, err := cmd.Flags().GetBool("dry-run")
			if err != nil {
				return err
			}
			result, err := update.Apply(cmd.Context(), info, update.ApplyOptions{
				Version: targetVersion,
				DryRun:  dryRun,
			})
			if err != nil {
				return err
			}
			return renderStructured(cmd, result)
		},
	}, info)
	cmd.Flags().Bool("check", false, "Check whether a newer TiDB Cloud CLI (ti) release is available without updating.")
	cmd.Flags().Bool("fail-if-update-available", false, "With --check, exit with code 1 when an update is available.")
	cmd.Flags().String("target-version", "latest", "Target TiDB Cloud CLI (ti) version, such as latest or v0.1.0.")
	cmd.Flags().Bool("dry-run", false, "Show the update plan without changing the local binary.")
	return cmd
}

func rejectCheckUpdateFlagCombinations(cmd *cobra.Command) error {
	for _, flagName := range []string{"target-version", "dry-run"} {
		if cmd.Flags().Changed(flagName) {
			return apperr.New(
				"update.incompatible_flag",
				"usage",
				2,
				fmt.Sprintf("--%s cannot be used with --check", flagName),
			)
		}
	}
	return nil
}

func newDBCommand(info version.Info) *cobra.Command {
	cmd := newParentCommand("db", "Manage TiDB Cloud database clusters.", info)
	cmd.AddCommand(
		newDBCreateClusterCommand(info),
		newDBListClustersCommand(info),
		newDBDescribeClusterCommand(info),
		newDBUpdateClusterCommand(info),
		newDBDeleteClusterCommand(info),
		newDBCreateBranchCommand(info),
		newDBListBranchesCommand(info),
		newDBDescribeBranchCommand(info),
		newDBDeleteBranchCommand(info),
		newDBPrepareQueryAccessCommand(info),
		newDBCreateConnectionStringCommand(info),
		newDBExecuteSQLCommand(info),
	)
	return cmd
}

func newDBCreateClusterCommand(info version.Info) *cobra.Command {
	cmd := newControlPlaneCommand(controlPlaneCommandSpec{
		Use:         "create-db-cluster",
		Short:       "Create a Starter database cluster (MySQL-compatible serverless database).",
		Mutation:    mutatingCommand,
		DBOperation: db.OperationClusterCreate,
		Run: func(ctx commandContext) (any, error) {
			service, profile, err := dbServiceAndProfile(ctx)
			if err != nil {
				return nil, err
			}
			opts, err := createClusterOptions(ctx, profile)
			if err != nil {
				return nil, err
			}
			return service.CreateCluster(ctx.cmd.Context(), opts)
		},
		DryRun: func(ctx commandContext) (dryrun.Result, error) {
			service, profile, err := dbServiceAndProfile(ctx)
			if err != nil {
				return dryrun.Result{}, err
			}
			opts, err := createClusterOptions(ctx, profile)
			if err != nil {
				return dryrun.Result{}, err
			}
			return service.DryRunCreateCluster(ctx.cmd.Context(), ctx.CommandPath(), opts)
		},
	}, info)
	cmd.Flags().String("db-cluster-name", "", "Starter database cluster display name.")
	cmd.Flags().String("db-cluster-type", "", "Database cluster type; must be starter.")
	cmd.Flags().Int32("monthly-spending-limit-usd-cents", -1, "The monthly spending limit in USD cents; omit to use the default.")
	cmd.Flags().Bool("wait", false, "Wait until the created cluster becomes ACTIVE before returning")
	markUsageRequired(cmd, "db-cluster-type", "db-cluster-name")
	return cmd
}

func newDBListClustersCommand(info version.Info) *cobra.Command {
	cmd := newControlPlaneCommand(controlPlaneCommandSpec{
		Use:         "list-db-clusters",
		Short:       "List Starter DB clusters.",
		Mutation:    readOnlyCommand,
		DBOperation: db.OperationClusterList,
		Run: func(ctx commandContext) (any, error) {
			service, profile, err := dbServiceAndProfile(ctx)
			if err != nil {
				return nil, err
			}
			clusterType, err := ctx.StringFlag("db-cluster-type")
			if err != nil {
				return nil, err
			}
			pageSize, err := ctx.Int32Flag("page-size")
			if err != nil {
				return nil, err
			}
			pageToken, err := ctx.StringFlag("page-token")
			if err != nil {
				return nil, err
			}
			filter, err := ctx.StringFlag("filter")
			if err != nil {
				return nil, err
			}
			orderBy, err := ctx.StringFlag("order-by")
			if err != nil {
				return nil, err
			}
			return service.ListClusters(ctx.cmd.Context(), db.ListClustersOptions{
				Profile:     profile,
				ClusterType: clusterType,
				PageSize:    pageSize,
				PageToken:   pageToken,
				Filter:      filter,
				OrderBy:     orderBy,
			})
		},
	}, info)
	cmd.Flags().String("db-cluster-type", "", "Database cluster type; must be starter.")
	cmd.Flags().Int32("page-size", 0, "The number of verified database clusters to return; 0 returns 10 and the maximum is 1000.")
	cmd.Flags().String("page-token", "", "For pagination, the page token returned by a previous list-db-clusters call.")
	cmd.Flags().String("filter", "", "The filter expression for the database clusters.")
	cmd.Flags().String("order-by", "", "The orderBy expression for the database clusters.")
	markUsageRequired(cmd, "db-cluster-type")
	return cmd
}

func newDBDescribeClusterCommand(info version.Info) *cobra.Command {
	cmd := newControlPlaneCommand(controlPlaneCommandSpec{
		Use:         "describe-db-cluster",
		Short:       "Describe a specified Starter database cluster.",
		Mutation:    readOnlyCommand,
		DBOperation: db.OperationClusterDescribe,
		Run: func(ctx commandContext) (any, error) {
			service, profile, err := dbServiceAndProfile(ctx)
			if err != nil {
				return nil, err
			}
			clusterID, err := ctx.StringFlag("db-cluster-id")
			if err != nil {
				return nil, err
			}
			view, err := ctx.StringFlag("view")
			if err != nil {
				return nil, err
			}
			return service.DescribeCluster(ctx.cmd.Context(), db.DescribeClusterOptions{
				Profile:   profile,
				ClusterID: clusterID,
				View:      view,
			})
		},
	}, info)
	cmd.Flags().String("db-cluster-id", "", "Starter database cluster ID.")
	cmd.Flags().String("view", "", "Detail level for the view: BASIC or FULL.")
	markUsageRequired(cmd, "db-cluster-id")
	return cmd
}

func newDBUpdateClusterCommand(info version.Info) *cobra.Command {
	cmd := newControlPlaneCommand(controlPlaneCommandSpec{
		Use:         "update-db-cluster",
		Short:       "Update a Starter DB cluster.",
		Mutation:    mutatingCommand,
		DBOperation: db.OperationClusterUpdate,
		Run: func(ctx commandContext) (any, error) {
			service, profile, err := dbServiceAndProfile(ctx)
			if err != nil {
				return nil, err
			}
			opts, err := updateClusterOptions(ctx, profile)
			if err != nil {
				return nil, err
			}
			return service.UpdateCluster(ctx.cmd.Context(), opts)
		},
		DryRun: func(ctx commandContext) (dryrun.Result, error) {
			service, profile, err := dbServiceAndProfile(ctx)
			if err != nil {
				return dryrun.Result{}, err
			}
			opts, err := updateClusterOptions(ctx, profile)
			if err != nil {
				return dryrun.Result{}, err
			}
			return service.DryRunUpdateCluster(ctx.cmd.Context(), ctx.CommandPath(), opts)
		},
	}, info)
	cmd.Flags().String("db-cluster-id", "", "Starter database cluster ID.")
	cmd.Flags().String("db-cluster-name", "", "The new display name.")
	cmd.Flags().Int32("monthly-spending-limit-usd-cents", -1, "The new monthly spending limit in USD cents; omit to leave unchanged.")
	markUsageRequired(cmd, "db-cluster-id")
	return cmd
}

func newDBDeleteClusterCommand(info version.Info) *cobra.Command {
	cmd := newControlPlaneCommand(controlPlaneCommandSpec{
		Use:         "delete-db-cluster",
		Short:       "Delete a Starter DB cluster.",
		Mutation:    mutatingCommand,
		DBOperation: db.OperationClusterDelete,
		Run: func(ctx commandContext) (any, error) {
			service, profile, err := dbServiceAndProfile(ctx)
			if err != nil {
				return nil, err
			}
			opts, err := deleteClusterOptions(ctx, profile)
			if err != nil {
				return nil, err
			}
			return service.DeleteCluster(ctx.cmd.Context(), opts)
		},
		DryRun: func(ctx commandContext) (dryrun.Result, error) {
			service, profile, err := dbServiceAndProfile(ctx)
			if err != nil {
				return dryrun.Result{}, err
			}
			opts, err := deleteClusterOptions(ctx, profile)
			if err != nil {
				return dryrun.Result{}, err
			}
			return service.DryRunDeleteCluster(ctx.cmd.Context(), ctx.CommandPath(), opts)
		},
	}, info)
	cmd.Flags().String("db-cluster-id", "", "Starter database cluster ID.")
	cmd.Flags().Bool("wait", false, "Wait until the cluster is deleted and is no longer accessible.")
	markUsageRequired(cmd, "db-cluster-id")
	return cmd
}

func newDBCreateBranchCommand(info version.Info) *cobra.Command {
	cmd := newControlPlaneCommand(controlPlaneCommandSpec{
		Use:         "create-db-cluster-branch",
		Short:       "Create a Starter database cluster branch.",
		Mutation:    mutatingCommand,
		DBOperation: db.OperationBranchCreate,
		Run: func(ctx commandContext) (any, error) {
			service, profile, err := dbServiceAndProfile(ctx)
			if err != nil {
				return nil, err
			}
			opts, err := createBranchOptions(ctx, profile)
			if err != nil {
				return nil, err
			}
			return service.CreateBranch(ctx.cmd.Context(), opts)
		},
		DryRun: func(ctx commandContext) (dryrun.Result, error) {
			service, profile, err := dbServiceAndProfile(ctx)
			if err != nil {
				return dryrun.Result{}, err
			}
			opts, err := createBranchOptions(ctx, profile)
			if err != nil {
				return dryrun.Result{}, err
			}
			return service.DryRunCreateBranch(ctx.cmd.Context(), ctx.CommandPath(), opts)
		},
	}, info)
	cmd.Flags().String("db-cluster-id", "", "Starter database cluster ID.")
	cmd.Flags().String("db-cluster-branch-name", "", "Branch display name.")
	cmd.Flags().Bool("wait", false, "Wait until the created branch becomes ACTIVE before returning.")
	markUsageRequired(cmd, "db-cluster-id", "db-cluster-branch-name")
	return cmd
}

func newDBListBranchesCommand(info version.Info) *cobra.Command {
	cmd := newControlPlaneCommand(controlPlaneCommandSpec{
		Use:         "list-db-cluster-branches",
		Short:       "List Starter DB cluster branches.",
		Mutation:    readOnlyCommand,
		DBOperation: db.OperationBranchList,
		Run: func(ctx commandContext) (any, error) {
			service, profile, err := dbServiceAndProfile(ctx)
			if err != nil {
				return nil, err
			}
			clusterID, err := ctx.StringFlag("db-cluster-id")
			if err != nil {
				return nil, err
			}
			pageSize, err := ctx.Int32Flag("page-size")
			if err != nil {
				return nil, err
			}
			pageToken, err := ctx.StringFlag("page-token")
			if err != nil {
				return nil, err
			}
			return service.ListBranches(ctx.cmd.Context(), db.ListBranchesOptions{
				Profile:   profile,
				ClusterID: clusterID,
				PageSize:  pageSize,
				PageToken: pageToken,
			})
		},
	}, info)
	cmd.Flags().String("db-cluster-id", "", "Starter database cluster ID.")
	cmd.Flags().Int32("page-size", 0, "The number of branches to request; 0 uses the default.")
	cmd.Flags().String("page-token", "", "For pagination, the page token returned by a previous list-db-cluster-branches call.")
	markUsageRequired(cmd, "db-cluster-id")
	return cmd
}

func newDBDescribeBranchCommand(info version.Info) *cobra.Command {
	cmd := newControlPlaneCommand(controlPlaneCommandSpec{
		Use:         "describe-db-cluster-branch",
		Short:       "Describe a specified Starter database cluster branch.",
		Mutation:    readOnlyCommand,
		DBOperation: db.OperationBranchDescribe,
		Run: func(ctx commandContext) (any, error) {
			service, profile, err := dbServiceAndProfile(ctx)
			if err != nil {
				return nil, err
			}
			clusterID, err := ctx.StringFlag("db-cluster-id")
			if err != nil {
				return nil, err
			}
			branchID, err := ctx.StringFlag("db-cluster-branch-id")
			if err != nil {
				return nil, err
			}
			view, err := ctx.StringFlag("view")
			if err != nil {
				return nil, err
			}
			return service.DescribeBranch(ctx.cmd.Context(), db.DescribeBranchOptions{
				Profile:   profile,
				ClusterID: clusterID,
				BranchID:  branchID,
				View:      view,
			})
		},
	}, info)
	cmd.Flags().String("db-cluster-id", "", "Starter database cluster ID.")
	cmd.Flags().String("db-cluster-branch-id", "", "Starter database cluster branch ID.")
	cmd.Flags().String("view", "", "Detail level for the view: BASIC or FULL.")
	markUsageRequired(cmd, "db-cluster-id", "db-cluster-branch-id")
	return cmd
}

func newDBDeleteBranchCommand(info version.Info) *cobra.Command {
	cmd := newControlPlaneCommand(controlPlaneCommandSpec{
		Use:         "delete-db-cluster-branch",
		Short:       "Delete a Starter DB cluster branch.",
		Mutation:    mutatingCommand,
		DBOperation: db.OperationBranchDelete,
		Run: func(ctx commandContext) (any, error) {
			service, profile, err := dbServiceAndProfile(ctx)
			if err != nil {
				return nil, err
			}
			opts, err := deleteBranchOptions(ctx, profile)
			if err != nil {
				return nil, err
			}
			return service.DeleteBranch(ctx.cmd.Context(), opts)
		},
		DryRun: func(ctx commandContext) (dryrun.Result, error) {
			service, profile, err := dbServiceAndProfile(ctx)
			if err != nil {
				return dryrun.Result{}, err
			}
			opts, err := deleteBranchOptions(ctx, profile)
			if err != nil {
				return dryrun.Result{}, err
			}
			return service.DryRunDeleteBranch(ctx.cmd.Context(), ctx.CommandPath(), opts)
		},
	}, info)
	cmd.Flags().String("db-cluster-id", "", "Starter database cluster ID.")
	cmd.Flags().String("db-cluster-branch-id", "", "Starter database cluster branch ID.")
	markUsageRequired(cmd, "db-cluster-id", "db-cluster-branch-id")
	return cmd
}

func newDBPrepareQueryAccessCommand(info version.Info) *cobra.Command {
	cmd := newControlPlaneCommand(controlPlaneCommandSpec{
		Use:         "create-db-sql-users",
		Short:       "Provision a group of database users managed by ti locally, with roles (admin, read-only, and read-write) for developer and agent access. And then, you can call format-db-connection-string to get the connection string for the users selectively.",
		Mutation:    mutatingCommand,
		DBOperation: db.OperationSQLUserCreate,
		Run: func(ctx commandContext) (any, error) {
			service, profile, err := dbServiceAndProfile(ctx)
			if err != nil {
				return nil, err
			}
			clusterID, err := ctx.StringFlag("db-cluster-id")
			if err != nil {
				return nil, err
			}
			return service.PrepareQueryAccess(ctx.cmd.Context(), db.PrepareQueryAccessOptions{
				Profile:   profile,
				ClusterID: clusterID,
			})
		},
		DryRun: func(ctx commandContext) (dryrun.Result, error) {
			service, profile, err := dbServiceAndProfile(ctx)
			if err != nil {
				return dryrun.Result{}, err
			}
			clusterID, err := ctx.StringFlag("db-cluster-id")
			if err != nil {
				return dryrun.Result{}, err
			}
			return service.DryRunPrepareQueryAccess(ctx.cmd.Context(), ctx.CommandPath(), db.PrepareQueryAccessOptions{
				Profile:   profile,
				ClusterID: clusterID,
			})
		},
	}, info)
	cmd.Flags().String("db-cluster-id", "", "Starter database cluster ID.")
	markUsageRequired(cmd, "db-cluster-id")
	return cmd
}

func newDBCreateConnectionStringCommand(info version.Info) *cobra.Command {
	cmd := newControlPlaneCommand(controlPlaneCommandSpec{
		Use:         "format-db-connection-string",
		Short:       "Generate a database connection string for ti-managed database user.",
		Mutation:    readOnlyCommand,
		DBOperation: db.OperationConnectionStringFormat,
		Run: func(ctx commandContext) (any, error) {
			service, profile, err := dbServiceAndProfile(ctx)
			if err != nil {
				return nil, err
			}
			opts, err := connectionStringOptions(ctx, profile)
			if err != nil {
				return nil, err
			}
			result, err := service.CreateConnectionString(ctx.cmd.Context(), opts)
			if err != nil {
				return nil, err
			}
			if result.Format == connectionstring.FormatEnv {
				return outputpkg.Raw{Bytes: []byte(result.ConnectionString)}, nil
			}
			return result, nil
		},
	}, info)
	addSQLCredentialFlags(cmd)
	cmd.Flags().String("database", "", "Default database (aka. schema) name.")
	cmd.Flags().String("format", connectionstring.FormatMySQLURI, "Connection string format: mysql-uri, jdbc, go-sql-driver, sqlalchemy, or env.")
	cmd.Flags().String("env-prefix", "TIDB_", "Use with --format env, the prefix for the environment variables.")
	cmd.Flags().Bool("env-include-database-url", false, "Use with --format env, include an additional database URL variable.")
	cmd.Flags().String("env-database-url-name", "DATABASE_URL", "Use with --format env, the name for the database URL variable.")
	markUsageRequired(cmd, "db-cluster-id")
	return cmd
}

func newDBExecuteSQLCommand(info version.Info) *cobra.Command {
	cmd := newControlPlaneCommand(controlPlaneCommandSpec{
		Use:         "execute-sql-statement",
		Short:       "Execute single SQL statement. You need to call create-db-sql-users at least once to prepare the credentials.",
		Mutation:    readOnlyCommand,
		DBOperation: db.OperationSQLExecute,
		Run: func(ctx commandContext) (any, error) {
			service, profile, err := dbServiceAndProfile(ctx)
			if err != nil {
				return nil, err
			}
			opts, err := executeSQLOptions(ctx, profile)
			if err != nil {
				return nil, err
			}
			return service.ExecuteSQL(ctx.cmd.Context(), opts)
		},
	}, info)
	addSQLCredentialFlags(cmd)
	cmd.Flags().String("database", "", "Default database (aka. schema) name.")
	cmd.Flags().String("sql", "", "The SQL statement to execute.")
	cmd.Flags().String("transport", "https", "SQL execution transport protocol: https or mysql.")
	markUsageRequired(cmd, "db-cluster-id", "sql")
	return cmd
}

func addSQLCredentialFlags(cmd *cobra.Command) {
	cmd.Flags().String("db-cluster-id", "", "Starter database cluster ID.")
	cmd.Flags().Bool("read-only", false, "Use the prepared read_only role for credentials.")
	cmd.Flags().Bool("read-write", false, "Use the prepared read_write role for credentials.")
	cmd.Flags().Bool("admin", false, "Use the prepared admin role for credentials.")
}

func dbServiceAndProfile(ctx commandContext) (*db.Dispatcher, *config.Profile, error) {
	profile, err := ctx.LoadProfile()
	if err != nil {
		return nil, nil, err
	}
	debug, err := ctx.BoolFlag("debug")
	if err != nil {
		return nil, nil, err
	}
	starterService := dbstarter.Service{
		Timeout:     30 * time.Second,
		Debug:       debug,
		DebugWriter: ctx.cmd.ErrOrStderr(),
	}
	dispatcher, err := db.NewDispatcher([]db.ClusterResolver{starterService}, starterService)
	if err != nil {
		return nil, nil, err
	}
	return dispatcher, profile, nil
}

func createClusterOptions(ctx commandContext, profile *config.Profile) (db.CreateClusterOptions, error) {
	name, err := ctx.StringFlag("db-cluster-name")
	if err != nil {
		return db.CreateClusterOptions{}, err
	}
	clusterType, err := ctx.StringFlag("db-cluster-type")
	if err != nil {
		return db.CreateClusterOptions{}, err
	}
	spendingLimit, err := ctx.Int32Flag("monthly-spending-limit-usd-cents")
	if err != nil {
		return db.CreateClusterOptions{}, err
	}
	waitUntilActive, err := ctx.BoolFlag("wait")
	if err != nil {
		return db.CreateClusterOptions{}, err
	}
	return db.CreateClusterOptions{
		Profile:         profile,
		DisplayName:     name,
		ClusterType:     clusterType,
		WaitUntilActive: waitUntilActive,
		Product: dbstarter.CreateOptions{
			MonthlySpendingLimitUSDCents: spendingLimit,
		},
	}, nil
}

func updateClusterOptions(ctx commandContext, profile *config.Profile) (db.UpdateClusterOptions, error) {
	clusterID, err := ctx.StringFlag("db-cluster-id")
	if err != nil {
		return db.UpdateClusterOptions{}, err
	}
	name, err := ctx.StringFlag("db-cluster-name")
	if err != nil {
		return db.UpdateClusterOptions{}, err
	}
	spendingLimit, err := ctx.Int32Flag("monthly-spending-limit-usd-cents")
	if err != nil {
		return db.UpdateClusterOptions{}, err
	}
	return db.UpdateClusterOptions{
		Profile:     profile,
		ClusterID:   clusterID,
		DisplayName: name,
		Product: dbstarter.UpdateOptions{
			MonthlySpendingLimitUSDCents: spendingLimit,
		},
	}, nil
}

func deleteClusterOptions(ctx commandContext, profile *config.Profile) (db.DeleteClusterOptions, error) {
	clusterID, err := ctx.StringFlag("db-cluster-id")
	if err != nil {
		return db.DeleteClusterOptions{}, err
	}
	waitUntilDeleted, err := ctx.BoolFlag("wait")
	if err != nil {
		return db.DeleteClusterOptions{}, err
	}
	return db.DeleteClusterOptions{
		Profile:          profile,
		ClusterID:        clusterID,
		WaitUntilDeleted: waitUntilDeleted,
	}, nil
}

func createBranchOptions(ctx commandContext, profile *config.Profile) (db.CreateBranchOptions, error) {
	clusterID, err := ctx.StringFlag("db-cluster-id")
	if err != nil {
		return db.CreateBranchOptions{}, err
	}
	name, err := ctx.StringFlag("db-cluster-branch-name")
	if err != nil {
		return db.CreateBranchOptions{}, err
	}
	waitUntilActive, err := ctx.BoolFlag("wait")
	if err != nil {
		return db.CreateBranchOptions{}, err
	}
	return db.CreateBranchOptions{
		Profile:         profile,
		ClusterID:       clusterID,
		DisplayName:     name,
		WaitUntilActive: waitUntilActive,
	}, nil
}

func deleteBranchOptions(ctx commandContext, profile *config.Profile) (db.DeleteBranchOptions, error) {
	clusterID, err := ctx.StringFlag("db-cluster-id")
	if err != nil {
		return db.DeleteBranchOptions{}, err
	}
	branchID, err := ctx.StringFlag("db-cluster-branch-id")
	if err != nil {
		return db.DeleteBranchOptions{}, err
	}
	return db.DeleteBranchOptions{
		Profile:   profile,
		ClusterID: clusterID,
		BranchID:  branchID,
	}, nil
}

func connectionStringOptions(ctx commandContext, profile *config.Profile) (db.CreateConnectionStringOptions, error) {
	common, err := sqlCommonOptions(ctx)
	if err != nil {
		return db.CreateConnectionStringOptions{}, err
	}
	format, err := ctx.StringFlag("format")
	if err != nil {
		return db.CreateConnectionStringOptions{}, err
	}
	envPrefix, err := ctx.StringFlag("env-prefix")
	if err != nil {
		return db.CreateConnectionStringOptions{}, err
	}
	envIncludeURL, err := ctx.BoolFlag("env-include-database-url")
	if err != nil {
		return db.CreateConnectionStringOptions{}, err
	}
	envURLName, err := ctx.StringFlag("env-database-url-name")
	if err != nil {
		return db.CreateConnectionStringOptions{}, err
	}
	return db.CreateConnectionStringOptions{
		Profile:                profile,
		ClusterID:              common.clusterID,
		Database:               common.database,
		ReadOnly:               common.readOnly,
		ReadWrite:              common.readWrite,
		Admin:                  common.admin,
		Format:                 format,
		EnvPrefix:              envPrefix,
		EnvIncludeDatabaseURL:  envIncludeURL,
		EnvDatabaseURLVariable: envURLName,
	}, nil
}

func executeSQLOptions(ctx commandContext, profile *config.Profile) (db.ExecuteSQLOptions, error) {
	common, err := sqlCommonOptions(ctx)
	if err != nil {
		return db.ExecuteSQLOptions{}, err
	}
	sql, err := ctx.StringFlag("sql")
	if err != nil {
		return db.ExecuteSQLOptions{}, err
	}
	transport, err := ctx.StringFlag("transport")
	if err != nil {
		return db.ExecuteSQLOptions{}, err
	}
	return db.ExecuteSQLOptions{
		Profile:   profile,
		ClusterID: common.clusterID,
		Database:  common.database,
		SQL:       sql,
		ReadOnly:  common.readOnly,
		ReadWrite: common.readWrite,
		Admin:     common.admin,
		Transport: transport,
	}, nil
}

type sqlCommon struct {
	clusterID string
	database  string
	readOnly  bool
	readWrite bool
	admin     bool
}

func sqlCommonOptions(ctx commandContext) (sqlCommon, error) {
	clusterID, err := ctx.StringFlag("db-cluster-id")
	if err != nil {
		return sqlCommon{}, err
	}
	database, err := ctx.StringFlag("database")
	if err != nil {
		return sqlCommon{}, err
	}
	readOnly, err := ctx.BoolFlag("read-only")
	if err != nil {
		return sqlCommon{}, err
	}
	readWrite, err := ctx.BoolFlag("read-write")
	if err != nil {
		return sqlCommon{}, err
	}
	admin, err := ctx.BoolFlag("admin")
	if err != nil {
		return sqlCommon{}, err
	}
	return sqlCommon{
		clusterID: clusterID,
		database:  database,
		readOnly:  readOnly,
		readWrite: readWrite,
		admin:     admin,
	}, nil
}

func newFSCommand(info version.Info) *cobra.Command {
	cmd := newParentCommand("fs", "Manage TiDB Cloud Filesystem — serverless, distributed POSIX-compatible file systems.", info)
	commands := []*cobra.Command{
		newFSCreateFileSystemCommand(info),
		newFSDeleteFileSystemCommand(info),
		newFSListFileSystemsCommand(info),
		newFSDescribeFileSystemCommand(info),
		newFSImportFileSystemTokenCommand(info),
		newFSGenerateFileSystemTokenCommand(info),
		newFSGenerateFileSystemScopedTokenCommand(info),
		newFSListFileSystemTokensCommand(info),
		newFSEnableFileSystemTokenCommand(info),
		newFSDisableFileSystemTokenCommand(info),
		newFSDeleteFileSystemTokenCommand(info),
		newFSRefreshFileSystemTokenCommand(info),
		newFSCheckFileSystemCommand(info),
		newFSCopyFileCommand(info),
		newFSReadFileCommand(info),
		newFSListFilesCommand(info),
		newFSDescribeFileCommand(info),
		newFSMoveFileCommand(info),
		newFSDeleteFileCommand(info),
		newFSCreateDirectoryCommand(info),
		newFSChmodFileCommand(info),
		newFSSymlinkFileCommand(info),
		newFSHardlinkFileCommand(info),
		newFSSearchFileContentCommand(info),
		newFSFindFilesCommand(info),
		newFSCreateLayerCommand(info),
		newFSListLayersCommand(info),
		newFSDescribeLayerCommand(info),
		newFSDiffLayerCommand(info),
		newFSCreateLayerCheckpointCommand(info),
		newFSRollbackLayerCommand(info),
		newFSCommitLayerCommand(info),
		newFSPackFileSystemCommand(info),
		newFSUnpackFileSystemCommand(info),
		newFSMountFileSystemCommand(info),
		newFSDrainFileSystemCommand(info),
		newFSUnmountFileSystemCommand(info),
	}
	tokenCommands := []string{"generate-file-system-token", "generate-file-system-scoped-token", "list-file-system-tokens", "enable-file-system-token", "disable-file-system-token", "delete-file-system-token", "refresh-file-system-token"}
	selectorExclusions := append([]string{"create-file-system", "list-file-systems", "describe-file-system", "delete-file-system", "import-file-system-token", "drain-file-system", "unmount-file-system"}, tokenCommands...)
	addFSSelectorFlags(commands, selectorExclusions...)
	addFSAuthFlags(commands,
		"create-file-system",
		"list-file-systems",
		"describe-file-system",
		"delete-file-system",
		"import-file-system-token",
		"drain-file-system",
		"unmount-file-system",
		"generate-file-system-token",
		"generate-file-system-scoped-token",
		"list-file-system-tokens",
		"enable-file-system-token",
		"disable-file-system-token",
		"delete-file-system-token",
		"refresh-file-system-token",
	)
	cmd.AddCommand(commands...)
	return cmd
}

func newFSGenerateFileSystemTokenCommand(info version.Info) *cobra.Command {
	cmd := newControlPlaneCommand(controlPlaneCommandSpec{
		Use: "generate-file-system-token", Short: "Generate an owner token for one file system.", Mutation: mutatingCommand, Permission: authz.FSTokenGenerate,
		Run: func(ctx commandContext) (any, error) {
			service, profile, err := fsTokenTIServiceAndProfile(ctx)
			if err != nil {
				return nil, err
			}
			opts, err := fsGenerateTokenOptions(ctx, profile)
			if err != nil {
				return nil, err
			}
			return service.Generate(ctx.cmd.Context(), opts)
		},
		DryRun: func(ctx commandContext) (dryrun.Result, error) {
			service, profile, err := fsTokenTIServiceAndProfile(ctx)
			if err != nil {
				return dryrun.Result{}, err
			}
			opts, err := fsGenerateTokenOptions(ctx, profile)
			if err != nil {
				return dryrun.Result{}, err
			}
			return service.DryRunGenerate(ctx.CommandPath(), opts)
		},
	}, info)
	cmd.Flags().String("file-system-id", "", "The file system ID that owns the generated token. Owner token generation requires TiDB Cloud API credentials.")
	cmd.Flags().String("token-name", "", "An operational name for the token (maximum 64 bytes).")
	cmd.Flags().Duration("ttl", 0, "Token lifetime as a positive duration of whole seconds, up to 365 days.")
	cmd.Flags().Bool("no-expiration", false, "Generate an owner token without an expiry.")
	cmd.Flags().Bool("store-locally", false, "Select and store the generated token in this profile's local credentials.")
	cmd.Flags().Bool("replace", false, "Replace the selected local token; the previous remote token remains active.")
	markUsageRequired(cmd, "file-system-id", "token-name")
	return cmd
}

func newFSGenerateFileSystemScopedTokenCommand(info version.Info) *cobra.Command {
	cmd := newControlPlaneCommand(controlPlaneCommandSpec{
		Use: "generate-file-system-scoped-token", Short: "Generate a path-and-operation-limited token using an owner token.", Mutation: mutatingCommand, Permission: authz.FSTokenIssueScoped,
		Run: func(ctx commandContext) (any, error) {
			service, profile, err := fsTokenLocalServiceAndProfile(ctx)
			if err != nil {
				return nil, err
			}
			opts, err := fsGenerateScopedTokenOptions(ctx, profile)
			if err != nil {
				return nil, err
			}
			return service.GenerateScoped(ctx.cmd.Context(), opts)
		},
		DryRun: func(ctx commandContext) (dryrun.Result, error) {
			service, profile, err := fsTokenLocalServiceAndProfile(ctx)
			if err != nil {
				return dryrun.Result{}, err
			}
			opts, err := fsGenerateScopedTokenOptions(ctx, profile)
			if err != nil {
				return dryrun.Result{}, err
			}
			return service.DryRunGenerateScoped(ctx.CommandPath(), opts)
		},
	}, info)
	cmd.Flags().String("file-system-id", "", "Optional file system ID assertion; required when using a locally stored owner token.")
	cmd.Flags().String("fs-token", "", "Owner file system token. Default: TI_FS_TOKEN, then the selected local credential.")
	cmd.Flags().String("subject", "", "Optional server-side audit label (maximum 64 bytes).")
	cmd.Flags().Duration("ttl", 0, "Scoped token lifetime as a positive duration of whole seconds.")
	cmd.Flags().StringArray("allow", nil, "Allowed path prefix and operations as <prefix>:<ops>; repeatable. Operations: read,list,search,write,delete.")
	cmd.Flags().Bool("store-locally", false, "Select and store the generated scoped token in this profile's local credentials.")
	cmd.Flags().Bool("replace", false, "Replace the selected local token; the previous remote token remains active.")
	markUsageRequired(cmd, "ttl", "allow")
	return cmd
}

func newFSListFileSystemTokensCommand(info version.Info) *cobra.Command {
	cmd := newControlPlaneCommand(controlPlaneCommandSpec{
		Use: "list-file-system-tokens", Short: "List token metadata for one file system.", Mutation: readOnlyCommand, Permission: authz.FSTokenList,
		Run: func(ctx commandContext) (any, error) {
			token, err := ctx.StringFlag("fs-token")
			if err != nil {
				return nil, err
			}
			service, profile, err := fsTokenManagementServiceAndProfile(ctx, token, ctx.FlagChanged("fs-token"))
			if err != nil {
				return nil, err
			}
			fileSystemID, err := ctx.StringFlag("file-system-id")
			if err != nil {
				return nil, err
			}
			offset, err := ctx.Int32Flag("offset")
			if err != nil {
				return nil, err
			}
			limit, err := ctx.Int32Flag("limit")
			if err != nil {
				return nil, err
			}
			includeExpired, err := ctx.BoolFlag("include-expired")
			if err != nil {
				return nil, err
			}
			regionOverride, err := fsExplicitRegionOverride(ctx)
			if err != nil {
				return nil, err
			}
			return service.List(ctx.cmd.Context(), tokenmgmt.ListOptions{Profile: profile, FileSystemID: fileSystemID, Token: token, TokenExplicit: ctx.FlagChanged("fs-token"), Offset: int(offset), Limit: int(limit), IncludeExpired: includeExpired, RegionOverride: regionOverride})
		},
	}, info)
	cmd.Flags().String("file-system-id", "", "The file system ID whose tokens are listed. Required with TiDB Cloud API credentials; optional with an owner FS token.")
	cmd.Flags().String("fs-token", "", "Optional owner FS token. Default: TI_FS_TOKEN; otherwise TiDB Cloud API keys are used.")
	cmd.Flags().Bool("include-expired", false, "Include expired token metadata.")
	cmd.Flags().Int32("offset", 0, "The zero-based token offset.")
	cmd.Flags().Int32("limit", tokenmgmt.DefaultListLimit, "The maximum number of tokens to return (maximum 200).")
	return cmd
}

func newFSEnableFileSystemTokenCommand(info version.Info) *cobra.Command {
	return newFSTokenMutationCommand("enable-file-system-token", "Enable a disabled file system token.", "enable_file_system_token", http.MethodPost, "/v1/tokens/<token-id>/activate", authz.FSTokenEnable, false, info)
}

func newFSDisableFileSystemTokenCommand(info version.Info) *cobra.Command {
	return newFSTokenMutationCommand("disable-file-system-token", "Disable an active file system token.", "disable_file_system_token", http.MethodPost, "/v1/tokens/<token-id>/deactivate", authz.FSTokenDisable, true, info)
}

func newFSDeleteFileSystemTokenCommand(info version.Info) *cobra.Command {
	return newFSTokenMutationCommand("delete-file-system-token", "Permanently revoke a file system token.", "delete_file_system_token", http.MethodDelete, "/v1/tokens/<token-id>", authz.FSTokenDelete, true, info)
}

func newFSTokenMutationCommand(use, short, operation, method, path string, permission authz.Permission, mountGuard bool, info version.Info) *cobra.Command {
	cmd := newControlPlaneCommand(controlPlaneCommandSpec{
		Use: use, Short: short, Mutation: mutatingCommand, Permission: permission,
		Run: func(ctx commandContext) (any, error) {
			token, err := ctx.StringFlag("fs-token")
			if err != nil {
				return nil, err
			}
			service, profile, err := fsTokenManagementServiceAndProfile(ctx, token, ctx.FlagChanged("fs-token"))
			if err != nil {
				return nil, err
			}
			opts, err := fsTokenMutationOptions(ctx, profile)
			if err != nil {
				return nil, err
			}
			switch permission {
			case authz.FSTokenEnable:
				return service.Enable(ctx.cmd.Context(), opts)
			case authz.FSTokenDisable:
				return service.Disable(ctx.cmd.Context(), opts)
			case authz.FSTokenDelete:
				return service.Delete(ctx.cmd.Context(), opts)
			default:
				panic("unsupported FS token mutation")
			}
		},
		DryRun: func(ctx commandContext) (dryrun.Result, error) {
			token, err := ctx.StringFlag("fs-token")
			if err != nil {
				return dryrun.Result{}, err
			}
			service, profile, err := fsTokenManagementServiceAndProfile(ctx, token, ctx.FlagChanged("fs-token"))
			if err != nil {
				return dryrun.Result{}, err
			}
			opts, err := fsTokenMutationOptions(ctx, profile)
			if err != nil {
				return dryrun.Result{}, err
			}
			return service.DryRunMutation(ctx.CommandPath(), operation, method, path, opts, permission, mountGuard)
		},
	}, info)
	cmd.Flags().String("file-system-id", "", "The file system ID that owns the token. Required with TiDB Cloud API credentials; optional with an owner FS token.")
	cmd.Flags().String("token-id", "", "The immutable token ID.")
	cmd.Flags().String("fs-token", "", "Optional owner FS token. Default: TI_FS_TOKEN; otherwise TiDB Cloud API keys are used.")
	markUsageRequired(cmd, "token-id")
	return cmd
}

func newFSRefreshFileSystemTokenCommand(info version.Info) *cobra.Command {
	cmd := newControlPlaneCommand(controlPlaneCommandSpec{
		Use: "refresh-file-system-token", Short: "Rotate the supplied file system token and return its new plaintext once.", Mutation: mutatingCommand, Permission: authz.FSTokenRefresh,
		Run: func(ctx commandContext) (any, error) {
			service, profile, err := fsTokenLocalServiceAndProfile(ctx)
			if err != nil {
				return nil, err
			}
			opts, err := fsRefreshTokenOptions(ctx, profile)
			if err != nil {
				return nil, err
			}
			return service.Refresh(ctx.cmd.Context(), opts)
		},
		DryRun: func(ctx commandContext) (dryrun.Result, error) {
			service, profile, err := fsTokenLocalServiceAndProfile(ctx)
			if err != nil {
				return dryrun.Result{}, err
			}
			opts, err := fsRefreshTokenOptions(ctx, profile)
			if err != nil {
				return dryrun.Result{}, err
			}
			opts.DryRun = true
			return service.DryRunRefresh(ctx.CommandPath(), opts)
		},
	}, info)
	cmd.Flags().String("file-system-id", "", "Optional file system ID assertion; required when using a locally stored token.")
	cmd.Flags().String("fs-token", "", "Current file system token. Default: TI_FS_TOKEN, then the selected local credential.")
	cmd.Flags().Duration("ttl", 0, "Optional new lifetime as a positive duration of whole seconds, up to 365 days.")
	return cmd
}

func fsGenerateTokenOptions(ctx commandContext, profile *config.Profile) (tokenmgmt.GenerateOptions, error) {
	fileSystemID, err := ctx.StringFlag("file-system-id")
	if err != nil {
		return tokenmgmt.GenerateOptions{}, err
	}
	tokenName, err := ctx.StringFlag("token-name")
	if err != nil {
		return tokenmgmt.GenerateOptions{}, err
	}
	noExpiration, err := ctx.BoolFlag("no-expiration")
	if err != nil {
		return tokenmgmt.GenerateOptions{}, err
	}
	storeLocally, err := ctx.BoolFlag("store-locally")
	if err != nil {
		return tokenmgmt.GenerateOptions{}, err
	}
	replace, err := ctx.BoolFlag("replace")
	if err != nil {
		return tokenmgmt.GenerateOptions{}, err
	}
	var ttl *time.Duration
	if ctx.FlagChanged("ttl") {
		value, err := ctx.DurationFlag("ttl")
		if err != nil {
			return tokenmgmt.GenerateOptions{}, err
		}
		ttl = &value
	}
	regionOverride, err := fsExplicitRegionOverride(ctx)
	if err != nil {
		return tokenmgmt.GenerateOptions{}, err
	}
	return tokenmgmt.GenerateOptions{Profile: profile, FileSystemID: fileSystemID, TokenName: tokenName, TTL: ttl, NoExpiration: noExpiration, StoreLocally: storeLocally, Replace: replace, RegionOverride: regionOverride}, nil
}

func fsGenerateScopedTokenOptions(ctx commandContext, profile *config.Profile) (tokenmgmt.GenerateScopedOptions, error) {
	fileSystemID, err := ctx.StringFlag("file-system-id")
	if err != nil {
		return tokenmgmt.GenerateScopedOptions{}, err
	}
	token, err := ctx.StringFlag("fs-token")
	if err != nil {
		return tokenmgmt.GenerateScopedOptions{}, err
	}
	subject, err := ctx.StringFlag("subject")
	if err != nil {
		return tokenmgmt.GenerateScopedOptions{}, err
	}
	allows, err := ctx.StringArrayFlag("allow")
	if err != nil {
		return tokenmgmt.GenerateScopedOptions{}, err
	}
	storeLocally, err := ctx.BoolFlag("store-locally")
	if err != nil {
		return tokenmgmt.GenerateScopedOptions{}, err
	}
	replace, err := ctx.BoolFlag("replace")
	if err != nil {
		return tokenmgmt.GenerateScopedOptions{}, err
	}
	var ttl *time.Duration
	if ctx.FlagChanged("ttl") {
		value, err := ctx.DurationFlag("ttl")
		if err != nil {
			return tokenmgmt.GenerateScopedOptions{}, err
		}
		ttl = &value
	}
	regionOverride, err := fsExplicitRegionOverride(ctx)
	if err != nil {
		return tokenmgmt.GenerateScopedOptions{}, err
	}
	return tokenmgmt.GenerateScopedOptions{Profile: profile, FileSystemID: fileSystemID, Token: token, TokenExplicit: ctx.FlagChanged("fs-token"), Subject: subject, TTL: ttl, Allows: allows, StoreLocally: storeLocally, Replace: replace, RegionOverride: regionOverride}, nil
}

func fsTokenMutationOptions(ctx commandContext, profile *config.Profile) (tokenmgmt.MutationOptions, error) {
	fileSystemID, err := ctx.StringFlag("file-system-id")
	if err != nil {
		return tokenmgmt.MutationOptions{}, err
	}
	tokenID, err := ctx.StringFlag("token-id")
	if err != nil {
		return tokenmgmt.MutationOptions{}, err
	}
	token, err := ctx.StringFlag("fs-token")
	if err != nil {
		return tokenmgmt.MutationOptions{}, err
	}
	regionOverride, err := fsExplicitRegionOverride(ctx)
	if err != nil {
		return tokenmgmt.MutationOptions{}, err
	}
	return tokenmgmt.MutationOptions{Profile: profile, FileSystemID: fileSystemID, TokenID: tokenID, Token: token, TokenExplicit: ctx.FlagChanged("fs-token"), RegionOverride: regionOverride}, nil
}

func fsRefreshTokenOptions(ctx commandContext, profile *config.Profile) (tokenmgmt.RefreshOptions, error) {
	fileSystemID, err := ctx.StringFlag("file-system-id")
	if err != nil {
		return tokenmgmt.RefreshOptions{}, err
	}
	token, err := ctx.StringFlag("fs-token")
	if err != nil {
		return tokenmgmt.RefreshOptions{}, err
	}
	var ttl *time.Duration
	if ctx.FlagChanged("ttl") {
		value, err := ctx.DurationFlag("ttl")
		if err != nil {
			return tokenmgmt.RefreshOptions{}, err
		}
		ttl = &value
	}
	regionOverride, err := fsExplicitRegionOverride(ctx)
	if err != nil {
		return tokenmgmt.RefreshOptions{}, err
	}
	return tokenmgmt.RefreshOptions{Profile: profile, FileSystemID: fileSystemID, Token: token, TokenExplicit: ctx.FlagChanged("fs-token"), RegionOverride: regionOverride, TTL: ttl}, nil
}

func fsExplicitRegionOverride(ctx commandContext) (string, error) {
	if flag := ctx.cmd.Flag("region"); flag != nil && flag.Changed {
		return strings.TrimSpace(flag.Value.String()), nil
	}
	value, _, _, err := envcompat.ResolveNames(nil, "TI_REGION_CODE", envcompat.LegacyNameFor("TI_REGION_CODE"))
	return strings.TrimSpace(value), err
}

func addFSSelectorFlags(commands []*cobra.Command, excluded ...string) {
	skip := make(map[string]struct{}, len(excluded))
	for _, name := range excluded {
		skip[name] = struct{}{}
	}
	for _, command := range commands {
		if _, ok := skip[command.Name()]; ok {
			continue
		}
		if command.Flags().Lookup("file-system-id") == nil {
			command.Flags().String("file-system-id", "", "The file system ID. Can also be supplied through TI_FS_FILE_SYSTEM_ID or derived from an explicitly supplied FS token.")
		}
	}
}

func addFSAuthFlags(commands []*cobra.Command, excluded ...string) {
	skip := make(map[string]struct{}, len(excluded))
	for _, name := range excluded {
		skip[name] = struct{}{}
	}
	for _, command := range commands {
		if _, ok := skip[command.Name()]; ok {
			continue
		}
		if command.Flags().Lookup("fs-token") == nil {
			command.Flags().String("fs-token", "", "File system user token. Default: value taken from the TI_FS_TOKEN environment variable if not provided.")
		}
	}
}

func newFSCreateFileSystemCommand(info version.Info) *cobra.Command {
	cmd := newControlPlaneCommand(controlPlaneCommandSpec{
		Use:        "create-file-system",
		Short:      "Create a TiDB Cloud Filesystem.",
		Mutation:   mutatingCommand,
		Permission: authz.FSVolumeCreate,
		Run: func(ctx commandContext) (any, error) {
			service, profile, err := fsTIServiceAndProfile(ctx)
			if err != nil {
				return nil, err
			}
			if err := fscred.MigrateNameRegistry(profile.HomeDir, profile); err != nil {
				return nil, err
			}
			opts, err := fsCreateFileSystemOptions(ctx, profile)
			if err != nil {
				return nil, err
			}
			return service.CreateFileSystem(ctx.cmd.Context(), opts)
		},
		DryRun: func(ctx commandContext) (dryrun.Result, error) {
			service, profile, err := fsTIServiceAndProfile(ctx)
			if err != nil {
				return dryrun.Result{}, err
			}
			opts, err := fsCreateFileSystemOptions(ctx, profile)
			if err != nil {
				return dryrun.Result{}, err
			}
			return service.DryRunCreateFileSystem(ctx.cmd.Context(), ctx.CommandPath(), opts)
		},
	}, info)
	cmd.Flags().String("display-name", "", "Organization-visible file system display name. It is metadata, not a resource selector.")
	cmd.Flags().StringArray("label", nil, "Organization-visible metadata label in key=value form. Repeat up to 30 times; do not store secrets.")
	cmd.Flags().Bool("wait", false, "Wait until the created file system is active.")
	return cmd
}

func newFSListFileSystemsCommand(info version.Info) *cobra.Command {
	cmd := newControlPlaneCommand(controlPlaneCommandSpec{
		Use:        "list-file-systems",
		Short:      "List remote file systems in the selected region. (preview)",
		Mutation:   readOnlyCommand,
		Permission: authz.FSVolumeRead,
		Run: func(ctx commandContext) (any, error) {
			service, profile, err := fsTIServiceAndProfile(ctx)
			if err != nil {
				return nil, err
			}
			displayName, label, err := fsListFileSystemFilters(ctx)
			if err != nil {
				return nil, err
			}
			return service.ListFileSystems(ctx.cmd.Context(), tifs.ListFileSystemsOptions{Profile: profile, DisplayName: displayName, Label: label})
		},
	}, info)
	cmd.Flags().String("display-name", "", "Filter by a substring of the effective display name.")
	cmd.Flags().StringArray("label", nil, "Filter by one exact organization-visible label in key=value form.")
	return cmd
}

func newFSDescribeFileSystemCommand(info version.Info) *cobra.Command {
	cmd := newControlPlaneCommand(controlPlaneCommandSpec{
		Use:        "describe-file-system",
		Short:      "Describe an existing file system.",
		Mutation:   readOnlyCommand,
		Permission: authz.FSVolumeRead,
		Run: func(ctx commandContext) (any, error) {
			service, profile, err := fsTIServiceAndProfile(ctx)
			if err != nil {
				return nil, err
			}
			fileSystemID, err := ctx.StringFlag("file-system-id")
			if err != nil {
				return nil, err
			}
			return service.DescribeFileSystem(ctx.cmd.Context(), profile, fileSystemID)
		},
	}, info)
	cmd.Flags().String("file-system-id", "", "The file system ID. Describing a file system requires TiDB Cloud API credentials.")
	markUsageRequired(cmd, "file-system-id")
	return cmd
}

func newFSDeleteFileSystemCommand(info version.Info) *cobra.Command {
	cmd := newControlPlaneCommand(controlPlaneCommandSpec{
		Use:        "delete-file-system",
		Short:      "Delete a file system from TiDB Cloud.",
		Mutation:   mutatingCommand,
		Permission: authz.FSVolumeDelete,
		Run: func(ctx commandContext) (any, error) {
			service, profile, err := fsTIServiceAndProfile(ctx)
			if err != nil {
				return nil, err
			}
			fileSystemID, err := ctx.StringFlag("file-system-id")
			if err != nil {
				return nil, err
			}
			return service.DeleteFileSystem(ctx.cmd.Context(), tifs.DeleteFileSystemOptions{
				Profile:      profile,
				FileSystemID: fileSystemID,
			})
		},
		DryRun: func(ctx commandContext) (dryrun.Result, error) {
			service, profile, err := fsTIServiceAndProfile(ctx)
			if err != nil {
				return dryrun.Result{}, err
			}
			fileSystemID, err := ctx.StringFlag("file-system-id")
			if err != nil {
				return dryrun.Result{}, err
			}
			return service.DryRunDeleteFileSystem(ctx.cmd.Context(), ctx.CommandPath(), tifs.DeleteFileSystemOptions{
				Profile:      profile,
				FileSystemID: fileSystemID,
			})
		},
	}, info)
	cmd.Flags().String("file-system-id", "", "The file system ID. FS tokens cannot select or authorize file system deletion.")
	markUsageRequired(cmd, "file-system-id")
	return cmd
}

func newFSImportFileSystemTokenCommand(info version.Info) *cobra.Command {
	cmd := newControlPlaneCommand(controlPlaneCommandSpec{
		Use:        "import-file-system-token",
		Short:      "Import an existing file system token into local credentials.",
		Mutation:   mutatingCommand,
		Permission: authz.FSVolumeRead,
		Run: func(ctx commandContext) (any, error) {
			service, profile, err := fsLocalServiceAndProfile(ctx)
			if err != nil {
				return nil, err
			}
			opts, err := fsImportTokenOptions(ctx, profile)
			if err != nil {
				return nil, err
			}
			return service.ImportFileSystemToken(ctx.cmd.Context(), opts)
		},
		DryRun: func(ctx commandContext) (dryrun.Result, error) {
			service, profile, err := fsLocalServiceAndProfile(ctx)
			if err != nil {
				return dryrun.Result{}, err
			}
			opts, err := fsImportTokenOptions(ctx, profile)
			if err != nil {
				return dryrun.Result{}, err
			}
			return service.DryRunImportFileSystemToken(ctx.cmd.Context(), ctx.CommandPath(), opts)
		},
	}, info)
	cmd.Flags().String("file-system-id", "", "Optional file system ID assertion; it must match the verified token.")
	cmd.Flags().String("fs-token", "", "File system token. Prefer TI_FS_TOKEN or --from-file to avoid exposing it in process arguments.")
	cmd.Flags().String("from-file", "", "Read the file system token from an owner-only file, or use - for stdin.")
	cmd.Flags().Bool("replace", false, "Replace an existing local token for the same file system after validation.")
	return cmd
}

func newFSCheckFileSystemCommand(info version.Info) *cobra.Command {
	return newControlPlaneCommand(controlPlaneCommandSpec{
		Use:        "check-file-system",
		Short:      "Check file system health.",
		Mutation:   readOnlyCommand,
		Permission: authz.FSVolumeRead,
		Run: func(ctx commandContext) (any, error) {
			service, profile, err := fsServiceAndProfile(ctx)
			if err != nil {
				return nil, err
			}
			return service.CheckFileSystem(ctx.cmd.Context(), tifs.CheckFileSystemOptions{
				Profile: profile,
			})
		},
	}, info)
}

func newFSCopyFileCommand(info version.Info) *cobra.Command {
	cmd := newControlPlaneCommand(controlPlaneCommandSpec{
		Use:        "copy-file",
		Aliases:    []string{"cp"},
		Short:      "Copy files.",
		Mutation:   mutatingCommand,
		Permission: authz.FSFileWrite,
		Run: func(ctx commandContext) (any, error) {
			service, profile, err := fsServiceAndProfile(ctx)
			if err != nil {
				return nil, err
			}
			opts, err := fsCopyFileOptions(ctx, profile)
			if err != nil {
				return nil, err
			}
			if opts.ToStdout {
				if opts.FromRemote == "" {
					return nil, apperr.New("fs.invalid_copy_flags", "usage", 2, "--to-stdout requires --from-remote")
				}
				data, err := service.ReadFile(ctx.cmd.Context(), tifs.ReadFileOptions{Profile: profile, Path: opts.FromRemote})
				if err != nil {
					return nil, err
				}
				return outputpkg.Raw{Bytes: data}, nil
			}
			return service.CopyFile(ctx.cmd.Context(), opts)
		},
	}, info)
	cmd.Flags().String("from-local", "", "The local source path.")
	cmd.Flags().String("from-remote", "", "The source path in the TiDB Cloud file system.")
	cmd.Flags().String("to-local", "", "The local destination path.")
	cmd.Flags().String("to-remote", "", "The destination path in the TiDB Cloud file system.")
	cmd.Flags().Bool("from-stdin", false, "Read from stdin and write to --to-remote.")
	cmd.Flags().Bool("to-stdout", false, "Write --from-remote to stdout.")
	cmd.Flags().Bool("overwrite", false, "Replace an existing destination file.")
	cmd.Flags().Bool("create-parents", false, "Create missing local parent directories when copying from a TiDB Cloud file system.")
	cmd.Flags().Bool("append", false, "Append a local file content to a file in the TiDB Cloud file system.")
	cmd.Flags().Bool("recursive", false, "Copy directory structure recursively.")
	cmd.Flags().Bool("resume", false, "Resume an active copy operation.")
	cmd.Flags().String("layer-id", "", "Write the copied file content into a file system layer instead of the base file system.")
	cmd.Flags().StringArray("tag", nil, "Create tag(s) key=value for --to-remote operation; repeatable.")
	cmd.Flags().String("description", "", "The file description for --to-remote operation.")
	return cmd
}

func newFSReadFileCommand(info version.Info) *cobra.Command {
	cmd := newControlPlaneCommand(controlPlaneCommandSpec{
		Use:        "read-file",
		Aliases:    []string{"cat"},
		Short:      "Read a file from a specific file system.",
		Mutation:   readOnlyCommand,
		Permission: authz.FSFileRead,
		Run: func(ctx commandContext) (any, error) {
			service, profile, err := fsServiceAndProfile(ctx)
			if err != nil {
				return nil, err
			}
			path, err := ctx.StringFlag("path")
			if err != nil {
				return nil, err
			}
			offset, err := ctx.Int64Flag("offset")
			if err != nil {
				return nil, err
			}
			length, err := ctx.Int64Flag("length")
			if err != nil {
				return nil, err
			}
			rangeSet := ctx.FlagChanged("offset") || ctx.FlagChanged("length")
			if ctx.FlagChanged("offset") != ctx.FlagChanged("length") {
				return nil, apperr.New("fs.invalid_range", "usage", 2, "--offset and --length must be provided together")
			}
			data, err := service.ReadFile(ctx.cmd.Context(), tifs.ReadFileOptions{
				Profile: profile,
				Path:    path,
				Range:   rangeSet,
				Offset:  offset,
				Length:  length,
			})
			if err != nil {
				return nil, err
			}
			return outputpkg.Raw{Bytes: data}, nil
		},
	}, info)
	cmd.Flags().String("path", "", "ti fs file path")
	cmd.Flags().Int64("offset", 0, "zero-based byte offset for a ranged read")
	cmd.Flags().Int64("length", 0, "byte length for a ranged read")
	markUsageRequired(cmd, "path")
	return cmd
}

func newFSListFilesCommand(info version.Info) *cobra.Command {
	cmd := newControlPlaneCommand(controlPlaneCommandSpec{
		Use:        "list-files",
		Aliases:    []string{"ls"},
		Short:      "List files in a specific file system.",
		Mutation:   readOnlyCommand,
		Permission: authz.FSFileRead,
		Run: func(ctx commandContext) (any, error) {
			service, profile, err := fsServiceAndProfile(ctx)
			if err != nil {
				return nil, err
			}
			path, err := ctx.StringFlag("path")
			if err != nil {
				return nil, err
			}
			return service.ListFiles(ctx.cmd.Context(), tifs.ListFilesOptions{
				Profile: profile,
				Path:    path,
			})
		},
	}, info)
	cmd.Flags().String("path", "/", "File system directory path.")
	return cmd
}

func newFSDescribeFileCommand(info version.Info) *cobra.Command {
	cmd := newControlPlaneCommand(controlPlaneCommandSpec{
		Use:        "describe-file",
		Aliases:    []string{"stat"},
		Short:      "Describe a file.",
		Mutation:   readOnlyCommand,
		Permission: authz.FSFileRead,
		Run: func(ctx commandContext) (any, error) {
			service, profile, err := fsServiceAndProfile(ctx)
			if err != nil {
				return nil, err
			}
			path, err := ctx.StringFlag("path")
			if err != nil {
				return nil, err
			}
			return service.DescribeFile(ctx.cmd.Context(), tifs.DescribeFileOptions{
				Profile: profile,
				Path:    path,
			})
		},
	}, info)
	cmd.Flags().String("path", "", "File or directory path in the TiDB Cloud file system.")
	markUsageRequired(cmd, "path")
	return cmd
}

func newFSMoveFileCommand(info version.Info) *cobra.Command {
	cmd := newControlPlaneCommand(controlPlaneCommandSpec{
		Use:        "move-file",
		Aliases:    []string{"mv"},
		Short:      "Move a file to a new location on the file system.",
		Mutation:   mutatingCommand,
		Permission: authz.FSFileWrite,
		Run: func(ctx commandContext) (any, error) {
			service, profile, err := fsServiceAndProfile(ctx)
			if err != nil {
				return nil, err
			}
			fromRemote, err := ctx.StringFlag("from-remote")
			if err != nil {
				return nil, err
			}
			toRemote, err := ctx.StringFlag("to-remote")
			if err != nil {
				return nil, err
			}
			overwrite, err := ctx.BoolFlag("overwrite")
			if err != nil {
				return nil, err
			}
			return service.MoveFile(ctx.cmd.Context(), tifs.MoveFileOptions{
				Profile:    profile,
				FromRemote: fromRemote,
				ToRemote:   toRemote,
				Overwrite:  overwrite,
			})
		},
	}, info)
	cmd.Flags().String("from-remote", "", "Source file path.")
	cmd.Flags().String("to-remote", "", "Destination file path.")
	cmd.Flags().Bool("overwrite", false, "Replace an existing destination file.")
	markUsageRequired(cmd, "from-remote", "to-remote")
	return cmd
}

func newFSDeleteFileCommand(info version.Info) *cobra.Command {
	cmd := newControlPlaneCommand(controlPlaneCommandSpec{
		Use:        "delete-file",
		Aliases:    []string{"rm"},
		Short:      "Delete a file.",
		Mutation:   mutatingCommand,
		Permission: authz.FSFileWrite,
		Run: func(ctx commandContext) (any, error) {
			service, profile, err := fsServiceAndProfile(ctx)
			if err != nil {
				return nil, err
			}
			path, err := ctx.StringFlag("path")
			if err != nil {
				return nil, err
			}
			recursive, err := ctx.BoolFlag("recursive")
			if err != nil {
				return nil, err
			}
			return service.DeleteFile(ctx.cmd.Context(), tifs.DeleteFileOptions{
				Profile:   profile,
				Path:      path,
				Recursive: recursive,
			})
		},
	}, info)
	cmd.Flags().String("path", "", "File or directory path in the TiDB Cloud file system.")
	cmd.Flags().Bool("recursive", false, "Delete a directory recursively.")
	markUsageRequired(cmd, "path")
	return cmd
}

func newFSCreateDirectoryCommand(info version.Info) *cobra.Command {
	cmd := newControlPlaneCommand(controlPlaneCommandSpec{
		Use:        "create-directory",
		Aliases:    []string{"mkdir"},
		Short:      "Create a directory.",
		Mutation:   mutatingCommand,
		Permission: authz.FSFileWrite,
		Run: func(ctx commandContext) (any, error) {
			service, profile, err := fsServiceAndProfile(ctx)
			if err != nil {
				return nil, err
			}
			path, err := ctx.StringFlag("path")
			if err != nil {
				return nil, err
			}
			mode, err := ctx.StringFlag("mode")
			if err != nil {
				return nil, err
			}
			return service.CreateDirectory(ctx.cmd.Context(), tifs.CreateDirectoryOptions{
				Profile: profile,
				Path:    path,
				Mode:    mode,
			})
		},
	}, info)
	cmd.Flags().String("path", "", "The file system path of the directory to create.")
	cmd.Flags().String("mode", "", "The directory mode as an octal value such as 0755.")
	markUsageRequired(cmd, "path")
	return cmd
}

func newFSChmodFileCommand(info version.Info) *cobra.Command {
	cmd := newControlPlaneCommand(controlPlaneCommandSpec{
		Use:        "chmod-file",
		Aliases:    []string{"chmod"},
		Short:      "Change file permissions.",
		Mutation:   mutatingCommand,
		Permission: authz.FSFileWrite,
		Run: func(ctx commandContext) (any, error) {
			service, profile, err := fsServiceAndProfile(ctx)
			if err != nil {
				return nil, err
			}
			path, err := ctx.StringFlag("path")
			if err != nil {
				return nil, err
			}
			mode, err := ctx.StringFlag("mode")
			if err != nil {
				return nil, err
			}
			return service.ChmodFile(ctx.cmd.Context(), tifs.ChmodFileOptions{Profile: profile, Path: path, Mode: mode})
		},
	}, info)
	cmd.Flags().String("path", "", "File or directory path.")
	cmd.Flags().String("mode", "", "The permission mode as an octal value such as 0644.")
	markUsageRequired(cmd, "path", "mode")
	return cmd
}

func newFSSymlinkFileCommand(info version.Info) *cobra.Command {
	cmd := newControlPlaneCommand(controlPlaneCommandSpec{
		Use:        "create-symlink",
		Aliases:    []string{"symlink"},
		Short:      "Create a symbolic link.",
		Mutation:   mutatingCommand,
		Permission: authz.FSFileWrite,
		Run: func(ctx commandContext) (any, error) {
			service, profile, err := fsServiceAndProfile(ctx)
			if err != nil {
				return nil, err
			}
			target, err := ctx.StringFlag("target")
			if err != nil {
				return nil, err
			}
			link, err := ctx.StringFlag("link-path")
			if err != nil {
				return nil, err
			}
			return service.SymlinkFile(ctx.cmd.Context(), tifs.SymlinkFileOptions{Profile: profile, Target: target, Link: link})
		},
	}, info)
	cmd.Flags().String("target", "", "The actual file path being linked to.")
	cmd.Flags().String("link-path", "", "The file path for the created symbolic link.")
	markUsageRequired(cmd, "target", "link-path")
	return cmd
}

func newFSHardlinkFileCommand(info version.Info) *cobra.Command {
	cmd := newControlPlaneCommand(controlPlaneCommandSpec{
		Use:        "create-hardlink",
		Aliases:    []string{"hardlink"},
		Short:      "Create hard link.",
		Mutation:   mutatingCommand,
		Permission: authz.FSFileWrite,
		Run: func(ctx commandContext) (any, error) {
			service, profile, err := fsServiceAndProfile(ctx)
			if err != nil {
				return nil, err
			}
			source, err := ctx.StringFlag("source-path")
			if err != nil {
				return nil, err
			}
			link, err := ctx.StringFlag("link-path")
			if err != nil {
				return nil, err
			}
			return service.HardlinkFile(ctx.cmd.Context(), tifs.HardlinkFileOptions{Profile: profile, Source: source, Link: link})
		},
	}, info)
	cmd.Flags().String("source-path", "", "The existing file path in the TiDB Cloud file system.")
	cmd.Flags().String("link-path", "", "The file path for the hard link being created in the TiDB Cloud file system.")
	markUsageRequired(cmd, "source-path", "link-path")
	return cmd
}

func newFSSearchFileContentCommand(info version.Info) *cobra.Command {
	cmd := newControlPlaneCommand(controlPlaneCommandSpec{
		Use:        "search-file-content",
		Aliases:    []string{"grep"},
		Short:      "Search file content in a specific file system.",
		Mutation:   readOnlyCommand,
		Permission: authz.FSFileRead,
		Run: func(ctx commandContext) (any, error) {
			service, profile, err := fsServiceAndProfile(ctx)
			if err != nil {
				return nil, err
			}
			path, err := ctx.StringFlag("path")
			if err != nil {
				return nil, err
			}
			pattern, err := ctx.StringFlag("pattern")
			if err != nil {
				return nil, err
			}
			limit, err := ctx.Int32Flag("limit")
			if err != nil {
				return nil, err
			}
			layerID, err := ctx.StringFlag("layer-id")
			if err != nil {
				return nil, err
			}
			return service.SearchFileContent(ctx.cmd.Context(), tifs.SearchFileContentOptions{
				Profile: profile,
				Path:    path,
				Pattern: pattern,
				Limit:   limit,
				LayerID: layerID,
			})
		},
	}, info)
	cmd.Flags().String("path", "/", "File path prefix to be searched.")
	cmd.Flags().String("pattern", "", "Content search matching pattern.")
	cmd.Flags().Int32("limit", 0, "Maximum number of search results; 0 uses the service default.")
	cmd.Flags().String("layer-id", "", "Search within a file system layer.")
	markUsageRequired(cmd, "pattern")
	return cmd
}

func newFSFindFilesCommand(info version.Info) *cobra.Command {
	cmd := newControlPlaneCommand(controlPlaneCommandSpec{
		Use:        "find-files",
		Aliases:    []string{"find"},
		Short:      "Find files using optional conditions.",
		Mutation:   readOnlyCommand,
		Permission: authz.FSFileRead,
		Run: func(ctx commandContext) (any, error) {
			service, profile, err := fsServiceAndProfile(ctx)
			if err != nil {
				return nil, err
			}
			opts, err := fsFindFilesOptions(ctx, profile)
			if err != nil {
				return nil, err
			}
			return service.FindFiles(ctx.cmd.Context(), opts)
		},
	}, info)
	cmd.Flags().String("path", "/", "File path prefix.")
	cmd.Flags().String("file-name-pattern", "", "File name pattern filter, such as *.md.")
	cmd.Flags().String("resource-type", "", "Resource type filter: file or directory.")
	cmd.Flags().String("tag", "", "Tag filter.")
	cmd.Flags().String("layer-id", "", "Search files and directories within a specific file system layer.")
	cmd.Flags().String("newer", "", "Only return files newer than the filter.")
	cmd.Flags().String("older", "", "Only return files older than the filter.")
	cmd.Flags().Int64("min-size-bytes", 0, "Minimum file size in bytes.")
	cmd.Flags().Int64("max-size-bytes", 0, "Maximum file size in bytes.")
	cmd.Flags().Int32("limit", 0, "Maximum number of results; 0 uses the service default.")
	return cmd
}

func newFSCreateLayerCommand(info version.Info) *cobra.Command {
	cmd := newControlPlaneCommand(controlPlaneCommandSpec{
		Use:        "create-layer",
		Short:      "Create a file system layer. (preview)",
		Mutation:   mutatingCommand,
		Permission: authz.FSFileWrite,
		Run: func(ctx commandContext) (any, error) {
			service, profile, err := fsServiceAndProfile(ctx)
			if err != nil {
				return nil, err
			}
			opts, err := fsCreateLayerOptions(ctx, profile)
			if err != nil {
				return nil, err
			}
			return service.CreateLayer(ctx.cmd.Context(), opts)
		},
		DryRun: func(ctx commandContext) (dryrun.Result, error) {
			service, profile, err := fsServiceAndProfile(ctx)
			if err != nil {
				return dryrun.Result{}, err
			}
			opts, err := fsCreateLayerOptions(ctx, profile)
			if err != nil {
				return dryrun.Result{}, err
			}
			tags, err := tifs.ParseLayerTagsForDryRun(opts.Tags)
			if err != nil {
				return dryrun.Result{}, err
			}
			body := map[string]any{
				"layer_id":        opts.LayerID,
				"base_root_path":  opts.BaseRootPath,
				"name":            opts.LayerName,
				"tags":            tags,
				"durability_mode": opts.DurabilityMode,
				"actor_id":        opts.ActorID,
			}
			return service.DryRunLayerMutation(ctx.cmd.Context(), ctx.CommandPath(), "create_layer", "POST", "/v1/layers", body, profile, authz.FSFileWrite)
		},
	}, info)
	cmd.Flags().String("layer-id", "", "The layer ID. Normally it is generated by the service automatically.")
	cmd.Flags().String("base-root-path", "", "Base root path in the TiDB Cloud file system.")
	cmd.Flags().String("layer-name", "", "The name of the layer.")
	cmd.Flags().StringArray("tag", nil, "Tag(s) for the layer, key=value; repeatable.")
	cmd.Flags().String("durability-mode", "", "Layer durability mode, must be restore-safe.")
	cmd.Flags().String("actor-id", "", "Actor ID identifying the layer owner (for example, the agent name).")
	markUsageRequired(cmd, "base-root-path")
	return cmd
}

func newFSListLayersCommand(info version.Info) *cobra.Command {
	return newControlPlaneCommand(controlPlaneCommandSpec{
		Use:        "list-layers",
		Short:      "List file system layers for a specific file system. (preview)",
		Mutation:   readOnlyCommand,
		Permission: authz.FSFileRead,
		Run: func(ctx commandContext) (any, error) {
			service, profile, err := fsServiceAndProfile(ctx)
			if err != nil {
				return nil, err
			}
			return service.ListLayers(ctx.cmd.Context(), tifs.ListLayersOptions{Profile: profile})
		},
	}, info)
}

func newFSDescribeLayerCommand(info version.Info) *cobra.Command {
	cmd := newControlPlaneCommand(controlPlaneCommandSpec{
		Use:        "describe-layer",
		Short:      "Describe a specified file system layer. (preview)",
		Mutation:   readOnlyCommand,
		Permission: authz.FSFileRead,
		Run: func(ctx commandContext) (any, error) {
			service, profile, err := fsServiceAndProfile(ctx)
			if err != nil {
				return nil, err
			}
			layerID, err := ctx.StringFlag("layer-id")
			if err != nil {
				return nil, err
			}
			return service.DescribeLayer(ctx.cmd.Context(), tifs.DescribeLayerOptions{Profile: profile, LayerID: layerID})
		},
	}, info)
	cmd.Flags().String("layer-id", "", "The ID of the specified file system layer.")
	markUsageRequired(cmd, "layer-id")
	return cmd
}

func newFSDiffLayerCommand(info version.Info) *cobra.Command {
	cmd := newControlPlaneCommand(controlPlaneCommandSpec{
		Use:        "diff-layer",
		Short:      "Show changed entries in a file system layer. (preview)",
		Mutation:   readOnlyCommand,
		Permission: authz.FSFileRead,
		Run: func(ctx commandContext) (any, error) {
			service, profile, err := fsServiceAndProfile(ctx)
			if err != nil {
				return nil, err
			}
			opts, err := fsLayerEntriesOptions(ctx, profile)
			if err != nil {
				return nil, err
			}
			return service.DiffLayer(ctx.cmd.Context(), opts)
		},
	}, info)
	cmd.Flags().String("layer-id", "", "The ID of the layer.")
	cmd.Flags().Int64("max-seq", 0, "The highest layer sequence to include; 0 includes all layers.")
	markUsageRequired(cmd, "layer-id")
	return cmd
}

func newFSCreateLayerCheckpointCommand(info version.Info) *cobra.Command {
	cmd := newControlPlaneCommand(controlPlaneCommandSpec{
		Use:        "create-layer-checkpoint",
		Short:      "Create a layer checkpoint. (preview)",
		Mutation:   mutatingCommand,
		Permission: authz.FSFileWrite,
		Run: func(ctx commandContext) (any, error) {
			service, profile, err := fsServiceAndProfile(ctx)
			if err != nil {
				return nil, err
			}
			opts, err := fsCreateLayerCheckpointOptions(ctx, profile)
			if err != nil {
				return nil, err
			}
			return service.CreateLayerCheckpoint(ctx.cmd.Context(), opts)
		},
		DryRun: func(ctx commandContext) (dryrun.Result, error) {
			service, profile, err := fsServiceAndProfile(ctx)
			if err != nil {
				return dryrun.Result{}, err
			}
			opts, err := fsCreateLayerCheckpointOptions(ctx, profile)
			if err != nil {
				return dryrun.Result{}, err
			}
			return service.DryRunLayerMutation(ctx.cmd.Context(), ctx.CommandPath(), "create_layer_checkpoint", "POST", "/v1/layers/"+opts.LayerID+"/checkpoints", map[string]any{
				"checkpoint_id": opts.CheckpointID,
				"label":         opts.Label,
			}, profile, authz.FSFileWrite)
		},
	}, info)
	cmd.Flags().String("layer-id", "", "The layer ID identifying the layer.")
	cmd.Flags().String("checkpoint-id", "", "Checkpoint ID. Normally it is generated by the service automatically.")
	cmd.Flags().String("label", "", "The checkpoint label.")
	markUsageRequired(cmd, "layer-id")
	return cmd
}

func newFSRollbackLayerCommand(info version.Info) *cobra.Command {
	cmd := newControlPlaneCommand(controlPlaneCommandSpec{
		Use:        "rollback-layer",
		Short:      "Rollback a file system layer. (preview)",
		Mutation:   mutatingCommand,
		Permission: authz.FSFileWrite,
		Run: func(ctx commandContext) (any, error) {
			service, profile, err := fsServiceAndProfile(ctx)
			if err != nil {
				return nil, err
			}
			layerID, err := ctx.StringFlag("layer-id")
			if err != nil {
				return nil, err
			}
			return service.RollbackLayer(ctx.cmd.Context(), tifs.LayerActionOptions{Profile: profile, LayerID: layerID})
		},
		DryRun: func(ctx commandContext) (dryrun.Result, error) {
			service, profile, err := fsServiceAndProfile(ctx)
			if err != nil {
				return dryrun.Result{}, err
			}
			layerID, err := ctx.StringFlag("layer-id")
			if err != nil {
				return dryrun.Result{}, err
			}
			return service.DryRunLayerMutation(ctx.cmd.Context(), ctx.CommandPath(), "rollback_layer", "POST", "/v1/layers/"+layerID+"/rollback", nil, profile, authz.FSFileWrite)
		},
	}, info)
	cmd.Flags().String("layer-id", "", "The ID of the layer.")
	markUsageRequired(cmd, "layer-id")
	return cmd
}

func newFSCommitLayerCommand(info version.Info) *cobra.Command {
	cmd := newControlPlaneCommand(controlPlaneCommandSpec{
		Use:        "commit-layer",
		Short:      "Commit a layer into the base file system. (preview)",
		Mutation:   mutatingCommand,
		Permission: authz.FSFileWrite,
		Run: func(ctx commandContext) (any, error) {
			service, profile, err := fsServiceAndProfile(ctx)
			if err != nil {
				return nil, err
			}
			layerID, err := ctx.StringFlag("layer-id")
			if err != nil {
				return nil, err
			}
			return service.CommitLayer(ctx.cmd.Context(), tifs.LayerActionOptions{Profile: profile, LayerID: layerID})
		},
		DryRun: func(ctx commandContext) (dryrun.Result, error) {
			service, profile, err := fsServiceAndProfile(ctx)
			if err != nil {
				return dryrun.Result{}, err
			}
			layerID, err := ctx.StringFlag("layer-id")
			if err != nil {
				return dryrun.Result{}, err
			}
			return service.DryRunLayerMutation(ctx.cmd.Context(), ctx.CommandPath(), "commit_layer", "POST", "/v1/layers/"+layerID+"/commit", map[string]any{}, profile, authz.FSFileWrite)
		},
	}, info)
	cmd.Flags().String("layer-id", "", "ti fs layer id")
	markUsageRequired(cmd, "layer-id")
	return cmd
}

func newFSPackFileSystemCommand(info version.Info) *cobra.Command {
	cmd := newControlPlaneCommand(controlPlaneCommandSpec{
		Use:        "pack-file-system",
		Short:      "Pack local overlay state into a remote archive for future unpacking.",
		Mutation:   mutatingCommand,
		Permission: authz.FSFileWrite,
		Run: func(ctx commandContext) (any, error) {
			service, profile, err := fsServiceAndProfile(ctx)
			if err != nil {
				return nil, err
			}
			opts, err := fsPackFileSystemOptions(ctx, profile)
			if err != nil {
				return nil, err
			}
			return service.PackFileSystem(ctx.cmd.Context(), opts)
		},
		DryRun: func(ctx commandContext) (dryrun.Result, error) {
			service, profile, err := fsServiceAndProfile(ctx)
			if err != nil {
				return dryrun.Result{}, err
			}
			opts, err := fsPackFileSystemOptions(ctx, profile)
			if err != nil {
				return dryrun.Result{}, err
			}
			return service.DryRunPackFileSystem(ctx.cmd.Context(), ctx.CommandPath(), opts)
		},
	}, info)
	cmd.Flags().String("local-root", "", "Local overlay root containing the overlay directory.")
	cmd.Flags().String("remote-root", "/", "The TiDB Cloud file system root represented by the local overlay.")
	cmd.Flags().String("mount-path", "", "The local mounted path.")
	cmd.Flags().String("mount-profile", "", "The mount profile: coding-agent, portable, or none. Default: none.")
	cmd.Flags().String("archive-path", "", "The path for the packed archive.")
	cmd.Flags().StringArray("path", nil, "Local overlay path(s) for packing.")
	return cmd
}

func newFSUnpackFileSystemCommand(info version.Info) *cobra.Command {
	cmd := newControlPlaneCommand(controlPlaneCommandSpec{
		Use:        "unpack-file-system",
		Short:      "Restore local overlay state from a packed archive.",
		Mutation:   mutatingCommand,
		Permission: authz.FSFileRead,
		Run: func(ctx commandContext) (any, error) {
			service, profile, err := fsServiceAndProfile(ctx)
			if err != nil {
				return nil, err
			}
			opts, err := fsUnpackFileSystemOptions(ctx, profile)
			if err != nil {
				return nil, err
			}
			return service.UnpackFileSystem(ctx.cmd.Context(), opts)
		},
		DryRun: func(ctx commandContext) (dryrun.Result, error) {
			service, profile, err := fsServiceAndProfile(ctx)
			if err != nil {
				return dryrun.Result{}, err
			}
			opts, err := fsUnpackFileSystemOptions(ctx, profile)
			if err != nil {
				return dryrun.Result{}, err
			}
			return service.DryRunUnpackFileSystem(ctx.cmd.Context(), ctx.CommandPath(), opts)
		},
	}, info)
	cmd.Flags().String("local-root", "", "The local overlay root to restore into.")
	cmd.Flags().String("remote-root", "/", "Find the packed archive under the specified root path when --archive-path is omitted.")
	cmd.Flags().String("mount-path", "", "The local mounted path.")
	cmd.Flags().String("mount-profile", "", "Mount profile: coding-agent, portable, or none. Default: none.")
	cmd.Flags().String("archive-path", "", "The path for the packed archive.")
	cmd.Flags().Bool("no-replace", false, "Merge archive entries instead of replacing them.")
	return cmd
}

func newFSMountFileSystemCommand(info version.Info) *cobra.Command {
	cmd := newControlPlaneCommand(controlPlaneCommandSpec{
		Use:        "mount-file-system",
		Aliases:    []string{"mount"},
		Short:      "Mount a file system to a local path.",
		Mutation:   mutatingCommand,
		Permission: authz.FSMount,
		Run: func(ctx commandContext) (any, error) {
			service, profile, err := fsServiceAndProfile(ctx)
			if err != nil {
				return nil, err
			}
			opts, err := fsMountOptions(ctx, profile)
			if err != nil {
				return nil, err
			}
			return service.MountFileSystem(ctx.cmd.Context(), opts)
		},
		DryRun: func(ctx commandContext) (dryrun.Result, error) {
			service, profile, err := fsServiceAndProfile(ctx)
			if err != nil {
				return dryrun.Result{}, err
			}
			opts, err := fsMountOptions(ctx, profile)
			if err != nil {
				return dryrun.Result{}, err
			}
			return service.DryRunMountFileSystem(ctx.cmd.Context(), ctx.CommandPath(), opts)
		},
	}, info)
	cmd.Flags().String("file-system-id", "", "The file system ID. Can also be supplied through TI_FS_FILE_SYSTEM_ID or derived from an explicitly supplied FS token.")
	cmd.Flags().String("mount-path", "", "Local mount path.")
	cmd.Flags().String("remote-path", "/", "The TiDB Cloud file system root path to mount.")
	cmd.Flags().String("driver", "auto", "Mount driver: auto, fuse, or webdav.")
	cmd.Flags().Bool("foreground", false, "Run the mount runtime in the foreground until interrupted.")
	cmd.Flags().Bool("read-only", false, "Read-only mount mode.")
	cmd.Flags().Duration("ready-timeout", 30*time.Second, "Time to wait for a background mount to become ready.")
	cmd.Flags().String("cache-dir", "", "Local FUSE cache directory. Default: ~/.ti/cache/mounts/<mount-hash>.")
	cmd.Flags().Int64("read-cache-size-mb", 128, "FUSE read cache size in MiB. 0 uses the default.")
	cmd.Flags().Int64("read-cache-max-file-mb", 4, "Maximum file size admitted to the FUSE read cache in MiB. 0 uses the default.")
	cmd.Flags().Duration("read-cache-ttl", 30*time.Second, "FUSE read cache Time-to-Live.")
	cmd.Flags().Bool("write-back-cache", true, "Persist FUSE writes locally before writing them to the file system on flush.")
	cmd.Flags().String("mount-profile", "", "Mount profile: coding-agent, portable, or none. Default: none.")
	cmd.Flags().String("local-root", "", "Local overlay root. Default: ~/.ti/local/fs/<mount-hash>.")
	cmd.Flags().StringArray("pack-path", nil, "Local overlay path included by automatic or manual pack. Repeatable.")
	cmd.Flags().String("unpack-archive-path", "", "Restore the pack archive before mounting.")
	cmd.Flags().Bool("no-auto-unpack", false, "Skip default auto-unpack for portable mount profile before mounting.")
	markUsageRequired(cmd, "mount-path")
	return cmd
}

func newFSUnmountFileSystemCommand(info version.Info) *cobra.Command {
	cmd := newControlPlaneCommand(controlPlaneCommandSpec{
		Use:        "unmount-file-system",
		Aliases:    []string{"umount"},
		Short:      "Unmount a file system from a local path.",
		Mutation:   mutatingCommand,
		Permission: authz.FSMount,
		Run: func(ctx commandContext) (any, error) {
			service, profile, err := fsLocalServiceAndProfile(ctx)
			if err != nil {
				return nil, err
			}
			opts, err := fsUnmountOptions(ctx, profile)
			if err != nil {
				return nil, err
			}
			return service.UnmountFileSystem(ctx.cmd.Context(), opts)
		},
		DryRun: func(ctx commandContext) (dryrun.Result, error) {
			service, profile, err := fsLocalServiceAndProfile(ctx)
			if err != nil {
				return dryrun.Result{}, err
			}
			opts, err := fsUnmountOptions(ctx, profile)
			if err != nil {
				return dryrun.Result{}, err
			}
			return service.DryRunUnmountFileSystem(ctx.cmd.Context(), ctx.CommandPath(), opts)
		},
	}, info)
	cmd.Flags().String("mount-path", "", "The local mounted path.")
	cmd.Flags().Duration("timeout", 30*time.Second, "Time to wait for the mount process to exit.")
	cmd.Flags().Bool("force", false, "Kill the mount process if graceful unmount times out.")
	cmd.Flags().Bool("ignore-absent", false, "Return success when no file system mount state exists for the specified path.")
	cmd.Flags().String("pack-archive-path", "", "Pack archive to write after unmount")
	cmd.Flags().Bool("no-auto-pack", false, "Skip the portable mount profile's default auto-pack action.")
	markUsageRequired(cmd, "mount-path")
	return cmd
}

func fsUnmountOptions(ctx commandContext, profile *config.Profile) (tifs.UnmountFileSystemOptions, error) {
	mountPath, err := ctx.StringFlag("mount-path")
	if err != nil {
		return tifs.UnmountFileSystemOptions{}, err
	}
	timeout, err := ctx.DurationFlag("timeout")
	if err != nil {
		return tifs.UnmountFileSystemOptions{}, err
	}
	force, err := ctx.BoolFlag("force")
	if err != nil {
		return tifs.UnmountFileSystemOptions{}, err
	}
	ignoreAbsent, err := ctx.BoolFlag("ignore-absent")
	if err != nil {
		return tifs.UnmountFileSystemOptions{}, err
	}
	packArchivePath, err := ctx.StringFlag("pack-archive-path")
	if err != nil {
		return tifs.UnmountFileSystemOptions{}, err
	}
	noAutoPack, err := ctx.BoolFlag("no-auto-pack")
	if err != nil {
		return tifs.UnmountFileSystemOptions{}, err
	}
	return tifs.UnmountFileSystemOptions{
		Profile:         profile,
		MountPath:       mountPath,
		Timeout:         timeout,
		Force:           force,
		IgnoreAbsent:    ignoreAbsent,
		PackArchivePath: packArchivePath,
		NoAutoPack:      noAutoPack,
	}, nil
}

func newFSDrainFileSystemCommand(info version.Info) *cobra.Command {
	cmd := newControlPlaneCommand(controlPlaneCommandSpec{
		Use:        "drain-file-system",
		Aliases:    []string{"drain"},
		Short:      "Flush dirty FUSE mount state for a mounted file system.",
		Mutation:   mutatingCommand,
		Permission: authz.FSMount,
		Run: func(ctx commandContext) (any, error) {
			service, profile, err := fsLocalServiceAndProfile(ctx)
			if err != nil {
				return nil, err
			}
			mountPath, err := ctx.StringFlag("mount-path")
			if err != nil {
				return nil, err
			}
			timeout, err := ctx.DurationFlag("timeout")
			if err != nil {
				return nil, err
			}
			return service.DrainFileSystem(ctx.cmd.Context(), tifs.DrainFileSystemOptions{
				Profile:   profile,
				MountPath: mountPath,
				Timeout:   timeout,
			})
		},
		DryRun: func(ctx commandContext) (dryrun.Result, error) {
			service, profile, err := fsLocalServiceAndProfile(ctx)
			if err != nil {
				return dryrun.Result{}, err
			}
			mountPath, err := ctx.StringFlag("mount-path")
			if err != nil {
				return dryrun.Result{}, err
			}
			timeout, err := ctx.DurationFlag("timeout")
			if err != nil {
				return dryrun.Result{}, err
			}
			return service.DryRunDrainFileSystem(ctx.cmd.Context(), ctx.CommandPath(), tifs.DrainFileSystemOptions{
				Profile:   profile,
				MountPath: mountPath,
				Timeout:   timeout,
			})
		},
	}, info)
	cmd.Flags().String("mount-path", "", "Local FUSE mount path.")
	cmd.Flags().Duration("timeout", 30*time.Second, "The time to wait for dirty handles and pending writes to drain.")
	markUsageRequired(cmd, "mount-path")
	return cmd
}

func fsPackFileSystemOptions(ctx commandContext, profile *config.Profile) (tifs.PackFileSystemOptions, error) {
	localRoot, err := ctx.StringFlag("local-root")
	if err != nil {
		return tifs.PackFileSystemOptions{}, err
	}
	remoteRoot, err := ctx.StringFlag("remote-root")
	if err != nil {
		return tifs.PackFileSystemOptions{}, err
	}
	mountPath, err := ctx.StringFlag("mount-path")
	if err != nil {
		return tifs.PackFileSystemOptions{}, err
	}
	mountProfile, err := ctx.StringFlag("mount-profile")
	if err != nil {
		return tifs.PackFileSystemOptions{}, err
	}
	archivePath, err := ctx.StringFlag("archive-path")
	if err != nil {
		return tifs.PackFileSystemOptions{}, err
	}
	paths, err := ctx.StringArrayFlag("path")
	if err != nil {
		return tifs.PackFileSystemOptions{}, err
	}
	return tifs.PackFileSystemOptions{
		Profile:      profile,
		LocalRoot:    localRoot,
		RemoteRoot:   remoteRoot,
		MountPath:    mountPath,
		MountProfile: mountProfile,
		ArchivePath:  archivePath,
		Paths:        paths,
	}, nil
}

func fsUnpackFileSystemOptions(ctx commandContext, profile *config.Profile) (tifs.UnpackFileSystemOptions, error) {
	localRoot, err := ctx.StringFlag("local-root")
	if err != nil {
		return tifs.UnpackFileSystemOptions{}, err
	}
	remoteRoot, err := ctx.StringFlag("remote-root")
	if err != nil {
		return tifs.UnpackFileSystemOptions{}, err
	}
	mountPath, err := ctx.StringFlag("mount-path")
	if err != nil {
		return tifs.UnpackFileSystemOptions{}, err
	}
	mountProfile, err := ctx.StringFlag("mount-profile")
	if err != nil {
		return tifs.UnpackFileSystemOptions{}, err
	}
	archivePath, err := ctx.StringFlag("archive-path")
	if err != nil {
		return tifs.UnpackFileSystemOptions{}, err
	}
	noReplace, err := ctx.BoolFlag("no-replace")
	if err != nil {
		return tifs.UnpackFileSystemOptions{}, err
	}
	return tifs.UnpackFileSystemOptions{
		Profile:      profile,
		LocalRoot:    localRoot,
		RemoteRoot:   remoteRoot,
		MountPath:    mountPath,
		MountProfile: mountProfile,
		ArchivePath:  archivePath,
		NoReplace:    noReplace,
	}, nil
}

func fsCopyFileOptions(ctx commandContext, profile *config.Profile) (tifs.CopyFileOptions, error) {
	fromLocal, err := ctx.StringFlag("from-local")
	if err != nil {
		return tifs.CopyFileOptions{}, err
	}
	fromRemote, err := ctx.StringFlag("from-remote")
	if err != nil {
		return tifs.CopyFileOptions{}, err
	}
	toLocal, err := ctx.StringFlag("to-local")
	if err != nil {
		return tifs.CopyFileOptions{}, err
	}
	toRemote, err := ctx.StringFlag("to-remote")
	if err != nil {
		return tifs.CopyFileOptions{}, err
	}
	fromStdin, err := ctx.BoolFlag("from-stdin")
	if err != nil {
		return tifs.CopyFileOptions{}, err
	}
	toStdout, err := ctx.BoolFlag("to-stdout")
	if err != nil {
		return tifs.CopyFileOptions{}, err
	}
	overwrite, err := ctx.BoolFlag("overwrite")
	if err != nil {
		return tifs.CopyFileOptions{}, err
	}
	createParents, err := ctx.BoolFlag("create-parents")
	if err != nil {
		return tifs.CopyFileOptions{}, err
	}
	appendFile, err := ctx.BoolFlag("append")
	if err != nil {
		return tifs.CopyFileOptions{}, err
	}
	recursive, err := ctx.BoolFlag("recursive")
	if err != nil {
		return tifs.CopyFileOptions{}, err
	}
	resume, err := ctx.BoolFlag("resume")
	if err != nil {
		return tifs.CopyFileOptions{}, err
	}
	layerID, err := ctx.StringFlag("layer-id")
	if err != nil {
		return tifs.CopyFileOptions{}, err
	}
	tagValues, err := ctx.StringArrayFlag("tag")
	if err != nil {
		return tifs.CopyFileOptions{}, err
	}
	tags, err := tifs.ParseFileTags(tagValues)
	if err != nil {
		return tifs.CopyFileOptions{}, err
	}
	description, err := ctx.StringFlag("description")
	if err != nil {
		return tifs.CopyFileOptions{}, err
	}
	return tifs.CopyFileOptions{
		Profile:       profile,
		FromLocal:     fromLocal,
		FromRemote:    fromRemote,
		ToLocal:       toLocal,
		ToRemote:      toRemote,
		FromStdin:     fromStdin,
		ToStdout:      toStdout,
		LayerID:       layerID,
		Overwrite:     overwrite,
		CreateParents: createParents,
		Append:        appendFile,
		Recursive:     recursive,
		Resume:        resume,
		Tags:          tags,
		Description:   description,
	}, nil
}

func fsFindFilesOptions(ctx commandContext, profile *config.Profile) (tifs.FindFilesOptions, error) {
	path, err := ctx.StringFlag("path")
	if err != nil {
		return tifs.FindFilesOptions{}, err
	}
	fileNamePattern, err := ctx.StringFlag("file-name-pattern")
	if err != nil {
		return tifs.FindFilesOptions{}, err
	}
	resourceType, err := ctx.StringFlag("resource-type")
	if err != nil {
		return tifs.FindFilesOptions{}, err
	}
	tag, err := ctx.StringFlag("tag")
	if err != nil {
		return tifs.FindFilesOptions{}, err
	}
	layerID, err := ctx.StringFlag("layer-id")
	if err != nil {
		return tifs.FindFilesOptions{}, err
	}
	newer, err := ctx.StringFlag("newer")
	if err != nil {
		return tifs.FindFilesOptions{}, err
	}
	older, err := ctx.StringFlag("older")
	if err != nil {
		return tifs.FindFilesOptions{}, err
	}
	minSizeBytes, err := ctx.Int64Flag("min-size-bytes")
	if err != nil {
		return tifs.FindFilesOptions{}, err
	}
	maxSizeBytes, err := ctx.Int64Flag("max-size-bytes")
	if err != nil {
		return tifs.FindFilesOptions{}, err
	}
	limit, err := ctx.Int32Flag("limit")
	if err != nil {
		return tifs.FindFilesOptions{}, err
	}
	return tifs.FindFilesOptions{
		Profile:         profile,
		Path:            path,
		FileNamePattern: fileNamePattern,
		ResourceType:    resourceType,
		Tag:             tag,
		LayerID:         layerID,
		Newer:           newer,
		Older:           older,
		MinSizeBytes:    minSizeBytes,
		MaxSizeBytes:    maxSizeBytes,
		Limit:           limit,
	}, nil
}

func fsCreateLayerOptions(ctx commandContext, profile *config.Profile) (tifs.CreateLayerOptions, error) {
	layerID, err := ctx.StringFlag("layer-id")
	if err != nil {
		return tifs.CreateLayerOptions{}, err
	}
	baseRootPath, err := ctx.StringFlag("base-root-path")
	if err != nil {
		return tifs.CreateLayerOptions{}, err
	}
	layerName, err := ctx.StringFlag("layer-name")
	if err != nil {
		return tifs.CreateLayerOptions{}, err
	}
	tags, err := ctx.StringArrayFlag("tag")
	if err != nil {
		return tifs.CreateLayerOptions{}, err
	}
	durabilityMode, err := ctx.StringFlag("durability-mode")
	if err != nil {
		return tifs.CreateLayerOptions{}, err
	}
	actorID, err := ctx.StringFlag("actor-id")
	if err != nil {
		return tifs.CreateLayerOptions{}, err
	}
	return tifs.CreateLayerOptions{
		Profile:        profile,
		LayerID:        layerID,
		BaseRootPath:   baseRootPath,
		LayerName:      layerName,
		Tags:           tags,
		DurabilityMode: durabilityMode,
		ActorID:        actorID,
	}, nil
}

func fsLayerEntriesOptions(ctx commandContext, profile *config.Profile) (tifs.LayerEntriesOptions, error) {
	layerID, err := ctx.StringFlag("layer-id")
	if err != nil {
		return tifs.LayerEntriesOptions{}, err
	}
	maxSeq, err := ctx.Int64Flag("max-seq")
	if err != nil {
		return tifs.LayerEntriesOptions{}, err
	}
	return tifs.LayerEntriesOptions{
		Profile: profile,
		LayerID: layerID,
		MaxSeq:  maxSeq,
	}, nil
}

func fsCreateLayerCheckpointOptions(ctx commandContext, profile *config.Profile) (tifs.CreateLayerCheckpointOptions, error) {
	layerID, err := ctx.StringFlag("layer-id")
	if err != nil {
		return tifs.CreateLayerCheckpointOptions{}, err
	}
	checkpointID, err := ctx.StringFlag("checkpoint-id")
	if err != nil {
		return tifs.CreateLayerCheckpointOptions{}, err
	}
	label, err := ctx.StringFlag("label")
	if err != nil {
		return tifs.CreateLayerCheckpointOptions{}, err
	}
	return tifs.CreateLayerCheckpointOptions{
		Profile:      profile,
		LayerID:      layerID,
		CheckpointID: checkpointID,
		Label:        label,
	}, nil
}

func fsMountOptions(ctx commandContext, profile *config.Profile) (tifs.MountFileSystemOptions, error) {
	fileSystemID, err := ctx.StringFlag("file-system-id")
	if err != nil {
		return tifs.MountFileSystemOptions{}, err
	}
	mountPath, err := ctx.StringFlag("mount-path")
	if err != nil {
		return tifs.MountFileSystemOptions{}, err
	}
	remotePath, err := ctx.StringFlag("remote-path")
	if err != nil {
		return tifs.MountFileSystemOptions{}, err
	}
	driver, err := ctx.StringFlag("driver")
	if err != nil {
		return tifs.MountFileSystemOptions{}, err
	}
	foreground, err := ctx.BoolFlag("foreground")
	if err != nil {
		return tifs.MountFileSystemOptions{}, err
	}
	readOnly, err := ctx.BoolFlag("read-only")
	if err != nil {
		return tifs.MountFileSystemOptions{}, err
	}
	readyTimeout, err := ctx.DurationFlag("ready-timeout")
	if err != nil {
		return tifs.MountFileSystemOptions{}, err
	}
	cacheDir, err := ctx.StringFlag("cache-dir")
	if err != nil {
		return tifs.MountFileSystemOptions{}, err
	}
	readCacheMB, err := ctx.Int64Flag("read-cache-size-mb")
	if err != nil {
		return tifs.MountFileSystemOptions{}, err
	}
	readCacheFileMB, err := ctx.Int64Flag("read-cache-max-file-mb")
	if err != nil {
		return tifs.MountFileSystemOptions{}, err
	}
	readCacheTTL, err := ctx.DurationFlag("read-cache-ttl")
	if err != nil {
		return tifs.MountFileSystemOptions{}, err
	}
	writeBackCache, err := ctx.BoolFlag("write-back-cache")
	if err != nil {
		return tifs.MountFileSystemOptions{}, err
	}
	mountProfile, err := ctx.StringFlag("mount-profile")
	if err != nil {
		return tifs.MountFileSystemOptions{}, err
	}
	localRoot, err := ctx.StringFlag("local-root")
	if err != nil {
		return tifs.MountFileSystemOptions{}, err
	}
	packPaths, err := ctx.StringArrayFlag("pack-path")
	if err != nil {
		return tifs.MountFileSystemOptions{}, err
	}
	unpackArchivePath, err := ctx.StringFlag("unpack-archive-path")
	if err != nil {
		return tifs.MountFileSystemOptions{}, err
	}
	noAutoUnpack, err := ctx.BoolFlag("no-auto-unpack")
	if err != nil {
		return tifs.MountFileSystemOptions{}, err
	}
	return tifs.MountFileSystemOptions{
		Profile:           profile,
		FileSystemName:    fileSystemID,
		MountPath:         mountPath,
		RemotePath:        remotePath,
		Driver:            driver,
		Foreground:        foreground,
		ReadOnly:          readOnly,
		ReadyTimeout:      readyTimeout,
		CacheDir:          cacheDir,
		ReadCacheMB:       readCacheMB,
		ReadCacheFileMB:   readCacheFileMB,
		ReadCacheTTL:      readCacheTTL,
		WriteBackCache:    writeBackCache,
		MountProfile:      mountProfile,
		LocalRoot:         localRoot,
		PackPaths:         packPaths,
		UnpackArchivePath: unpackArchivePath,
		NoAutoUnpack:      noAutoUnpack,
	}, nil
}

func fsTIServiceAndProfile(ctx commandContext) (tifs.Service, *config.Profile, error) {
	profile, err := ctx.LoadProfile()
	if err != nil {
		return tifs.Service{}, nil, err
	}
	return fsService(ctx, profile)
}

func fsLocalServiceAndProfile(ctx commandContext) (tifs.Service, *config.Profile, error) {
	profile, err := ctx.LoadLocalProfile()
	if err != nil {
		return tifs.Service{}, nil, err
	}
	return fsService(ctx, profile)
}

func fsTokenTIServiceAndProfile(ctx commandContext) (tokenmgmt.Service, *config.Profile, error) {
	profile, err := ctx.LoadProfile()
	if err != nil {
		return tokenmgmt.Service{}, nil, err
	}
	return fsTokenService(ctx, profile)
}

func fsTokenLocalServiceAndProfile(ctx commandContext) (tokenmgmt.Service, *config.Profile, error) {
	profile, err := ctx.LoadLocalProfile()
	if err != nil {
		return tokenmgmt.Service{}, nil, err
	}
	return fsTokenService(ctx, profile)
}

func fsTokenManagementServiceAndProfile(ctx commandContext, token string, tokenExplicit bool) (tokenmgmt.Service, *config.Profile, error) {
	useBearer := tokenExplicit || strings.TrimSpace(token) != ""
	if !useBearer {
		envToken, _, _, err := envcompat.ResolveNames(nil, "TI_FS_TOKEN", envcompat.LegacyNameFor("TI_FS_TOKEN"))
		if err != nil {
			return tokenmgmt.Service{}, nil, err
		}
		useBearer = strings.TrimSpace(envToken) != ""
	}
	if useBearer {
		return fsTokenLocalServiceAndProfile(ctx)
	}
	return fsTokenTIServiceAndProfile(ctx)
}

func fsTokenService(ctx commandContext, profile *config.Profile) (tokenmgmt.Service, *config.Profile, error) {
	debug, err := ctx.BoolFlag("debug")
	if err != nil {
		return tokenmgmt.Service{}, nil, err
	}
	return tokenmgmt.Service{
		Timeout: 30 * time.Second, Debug: debug, DebugWriter: ctx.cmd.ErrOrStderr(), HomeDir: profile.HomeDir,
	}, profile, nil
}

func fsService(ctx commandContext, profile *config.Profile) (tifs.Service, *config.Profile, error) {
	debug, err := ctx.BoolFlag("debug")
	if err != nil {
		return tifs.Service{}, nil, err
	}
	service := tifs.Service{
		Timeout:     30 * time.Second,
		Debug:       debug,
		DebugWriter: ctx.cmd.ErrOrStderr(),
		Stdin:       ctx.cmd.InOrStdin(),
		Stdout:      ctx.cmd.OutOrStdout(),
		Stderr:      ctx.cmd.ErrOrStderr(),
		HomeDir:     profile.HomeDir,
	}
	return service, profile, nil
}

func fsServiceAndProfile(ctx commandContext) (tifs.Service, *config.Profile, error) {
	return fsAuthenticatedServiceAndProfile(ctx, true)
}

func fsCreateFileSystemOptions(ctx commandContext, profile *config.Profile) (tifs.CreateFileSystemOptions, error) {
	waitUntilReady, err := ctx.BoolFlag("wait")
	if err != nil {
		return tifs.CreateFileSystemOptions{}, err
	}
	displayName, err := ctx.StringFlag("display-name")
	if err != nil {
		return tifs.CreateFileSystemOptions{}, err
	}
	labels, err := ctx.StringArrayFlag("label")
	if err != nil {
		return tifs.CreateFileSystemOptions{}, err
	}
	parsedDisplayName, parsedLabels, err := tifs.ParseTenantMetadata(displayName, ctx.FlagChanged("display-name"), labels)
	if err != nil {
		return tifs.CreateFileSystemOptions{}, err
	}
	return tifs.CreateFileSystemOptions{Profile: profile, WaitUntilReady: waitUntilReady, DisplayName: parsedDisplayName, Labels: parsedLabels}, nil
}

func fsListFileSystemFilters(ctx commandContext) (*string, *tifs.LabelFilter, error) {
	displayName, err := ctx.StringFlag("display-name")
	if err != nil {
		return nil, nil, err
	}
	labels, err := ctx.StringArrayFlag("label")
	if err != nil {
		return nil, nil, err
	}
	return tifs.ParseTenantListFilters(displayName, ctx.FlagChanged("display-name"), labels)
}

func fsAuthenticatedServiceAndProfile(ctx commandContext, tokenRequired bool) (tifs.Service, *config.Profile, error) {
	service, profile, err := fsLocalServiceAndProfile(ctx)
	if err != nil {
		return tifs.Service{}, nil, err
	}
	selected, err := fsResolveAuthenticatedProfile(ctx, profile, tokenRequired)
	if err != nil {
		return tifs.Service{}, nil, err
	}
	return service, selected, nil
}

func fsAdjunctServiceAndProfile(ctx commandContext) (tifs.Service, *config.Profile, error) {
	return fsServiceAndProfile(ctx)
}

func fsVaultServiceAndProfile(ctx commandContext) (tifs.Service, *config.Profile, error) {
	token, err := vaultToken(ctx)
	if err != nil {
		return tifs.Service{}, nil, err
	}
	return fsAuthenticatedServiceAndProfile(ctx, strings.TrimSpace(token) == "")
}

func fsResolveAuthenticatedProfile(ctx commandContext, profile *config.Profile, tokenRequired bool) (*config.Profile, error) {
	selector := ""
	selectorExplicit := false
	if ctx.cmd.Flag("file-system-id") != nil {
		var err error
		selector, err = ctx.StringFlag("file-system-id")
		if err != nil {
			return nil, err
		}
		selectorExplicit = ctx.FlagChanged("file-system-id")
	}
	token := ""
	tokenExplicit := false
	if ctx.cmd.Flag("fs-token") != nil {
		var err error
		token, err = ctx.StringFlag("fs-token")
		if err != nil {
			return nil, err
		}
		tokenExplicit = ctx.FlagChanged("fs-token")
	}
	regionOverride := ""
	if flag := ctx.cmd.Flag("region"); flag != nil && flag.Changed {
		regionOverride = strings.TrimSpace(flag.Value.String())
	} else {
		value, _, _, err := envcompat.ResolveNames(nil, "TI_REGION_CODE", envcompat.LegacyNameFor("TI_REGION_CODE"))
		if err != nil {
			return nil, err
		}
		regionOverride = strings.TrimSpace(value)
	}
	dryRun, _ := ctx.BoolFlag("dry-run")
	if !dryRun {
		if err := fscred.MigrateNameRegistry(profile.HomeDir, profile); err != nil {
			return nil, err
		}
	}
	selected, _, err := fscred.ResolveCredential(profile.HomeDir, profile, fscred.ResolveCredentialOptions{
		FileSystemID:         selector,
		FileSystemIDExplicit: selectorExplicit,
		Token:                token,
		TokenExplicit:        tokenExplicit,
		RegionOverride:       regionOverride,
		TokenRequired:        tokenRequired,
		DryRun:               dryRun,
	})
	if err != nil {
		return nil, err
	}
	return selected, nil
}

func fsImportTokenOptions(ctx commandContext, profile *config.Profile) (tifs.ImportFileSystemTokenOptions, error) {
	fileSystemID, err := ctx.StringFlag("file-system-id")
	if err != nil {
		return tifs.ImportFileSystemTokenOptions{}, err
	}
	flagToken, err := ctx.StringFlag("fs-token")
	if err != nil {
		return tifs.ImportFileSystemTokenOptions{}, err
	}
	fromFile, err := ctx.StringFlag("from-file")
	if err != nil {
		return tifs.ImportFileSystemTokenOptions{}, err
	}
	envTokenValue, _, _, err := envcompat.ResolveNames(nil, "TI_FS_TOKEN", envcompat.LegacyNameFor("TI_FS_TOKEN"))
	if err != nil {
		return tifs.ImportFileSystemTokenOptions{}, err
	}
	envToken := strings.TrimSpace(envTokenValue)
	sources := 0
	if ctx.FlagChanged("fs-token") {
		sources++
	}
	if strings.TrimSpace(fromFile) != "" {
		sources++
	}
	if envToken != "" {
		sources++
	}
	if sources == 0 {
		return tifs.ImportFileSystemTokenOptions{}, apperr.New("fs.missing_token", "authentication", 3, "authentication required: pass --fs-token, set TI_FS_TOKEN, or use --from-file")
	}
	if sources > 1 {
		return tifs.ImportFileSystemTokenOptions{}, apperr.New("fs.multiple_token_sources", "usage", 2, "provide exactly one of --fs-token, TI_FS_TOKEN, or --from-file")
	}
	token := strings.TrimSpace(flagToken)
	if strings.TrimSpace(fromFile) != "" {
		token, err = readFSImportToken(ctx, fromFile)
		if err != nil {
			return tifs.ImportFileSystemTokenOptions{}, err
		}
	} else if token == "" {
		token = envToken
	}
	replace, err := ctx.BoolFlag("replace")
	if err != nil {
		return tifs.ImportFileSystemTokenOptions{}, err
	}
	return tifs.ImportFileSystemTokenOptions{Profile: profile, FileSystemID: fileSystemID, Token: token, Replace: replace}, nil
}

func readFSImportToken(ctx commandContext, path string) (string, error) {
	var data []byte
	var err error
	if path == "-" {
		data, err = io.ReadAll(io.LimitReader(ctx.cmd.InOrStdin(), 1<<20))
	} else {
		info, statErr := os.Stat(path)
		if statErr != nil {
			return "", apperr.Wrap("fs.token_file", "usage", 2, "cannot inspect FS token file", statErr)
		}
		if !info.Mode().IsRegular() {
			return "", apperr.New("fs.token_file", "usage", 2, "FS token file must be a regular file")
		}
		if runtime.GOOS != "windows" && info.Mode().Perm()&0o077 != 0 {
			return "", apperr.New("fs.token_file_permissions", "usage", 2, fmt.Sprintf("FS token file %s must have mode 0600 or stricter", path))
		}
		data, err = os.ReadFile(path)
	}
	if err != nil {
		return "", apperr.Wrap("fs.token_file", "usage", 2, "cannot read FS token", err)
	}
	token := strings.TrimSpace(string(data))
	if token == "" {
		return "", apperr.New("fs.empty_token", "usage", 2, "FS token input is empty")
	}
	return token, nil
}

func newFSVaultCommand(info version.Info) *cobra.Command {
	cmd := newParentCommand("fs-vault", "Manage file system vault secrets and delegated access.", info)
	commands := []*cobra.Command{
		newVaultCreateSecretCommand(info),
		newVaultReplaceSecretCommand(info),
		newVaultReadSecretCommand(info),
		newVaultListSecretsCommand(info),
		newVaultDeleteSecretCommand(info),
		newVaultCreateGrantCommand(info),
		newVaultDeleteGrantCommand(info),
		newVaultListAuditEventsCommand(info),
		newVaultRunWithSecretCommand(info),
		newVaultMountCommand(info),
		newVaultUnmountCommand(info),
	}
	addFSSelectorFlags(commands, "unmount-vault")
	addFSAuthFlags(commands, "unmount-vault")
	cmd.AddCommand(commands...)
	return cmd
}

func newVaultCreateSecretCommand(info version.Info) *cobra.Command {
	cmd := newControlPlaneCommand(controlPlaneCommandSpec{
		Use:        "create-secret",
		Short:      "Create a file system vault secret.",
		Mutation:   mutatingCommand,
		Permission: authz.FSVaultSecretCreate,
		Run: func(ctx commandContext) (any, error) {
			service, profile, err := fsAdjunctServiceAndProfile(ctx)
			if err != nil {
				return nil, err
			}
			name, err := ctx.StringFlag("secret-name")
			if err != nil {
				return nil, err
			}
			fields, err := ctx.StringArrayFlag("field")
			if err != nil {
				return nil, err
			}
			return service.CreateVaultSecret(ctx.cmd.Context(), tifs.VaultCreateSecretOptions{
				Profile:    profile,
				SecretName: name,
				Fields:     fields,
				Stdin:      ctx.cmd.InOrStdin(),
			})
		},
	}, info)
	cmd.Flags().String("secret-name", "", "Vault secret name.")
	cmd.Flags().StringArray("field", nil, "Secret field assignment key=value, key=@file, or key=-; repeatable.")
	markUsageRequired(cmd, "secret-name", "field")
	return cmd
}

func newVaultReplaceSecretCommand(info version.Info) *cobra.Command {
	cmd := newControlPlaneCommand(controlPlaneCommandSpec{
		Use:        "replace-secret",
		Short:      "Replace all fields in a file system vault secret from a directory.",
		Mutation:   mutatingCommand,
		Permission: authz.FSVaultSecretUpdate,
		Run: func(ctx commandContext) (any, error) {
			service, profile, err := fsAdjunctServiceAndProfile(ctx)
			if err != nil {
				return nil, err
			}
			secretPath, err := ctx.StringFlag("secret-path")
			if err != nil {
				return nil, err
			}
			fromDirectory, err := ctx.StringFlag("from-directory")
			if err != nil {
				return nil, err
			}
			return service.ReplaceVaultSecret(ctx.cmd.Context(), tifs.VaultReplaceSecretOptions{
				Profile:       profile,
				SecretPath:    secretPath,
				FromDirectory: fromDirectory,
			})
		},
	}, info)
	cmd.Flags().String("secret-path", "", "Vault path in the form /n/vault/<secret>.")
	cmd.Flags().String("from-directory", "", "Directory that contains files to become secret fields.")
	markUsageRequired(cmd, "secret-path", "from-directory")
	return cmd
}

func newVaultReadSecretCommand(info version.Info) *cobra.Command {
	cmd := newControlPlaneCommand(controlPlaneCommandSpec{
		Use:        "read-secret",
		Short:      "Read a file system vault secret.",
		Mutation:   readOnlyCommand,
		Permission: authz.FSVaultSecretRead,
		Run: func(ctx commandContext) (any, error) {
			service, profile, err := fsVaultServiceAndProfile(ctx)
			if err != nil {
				return nil, err
			}
			name, err := ctx.StringFlag("secret-name")
			if err != nil {
				return nil, err
			}
			field, err := ctx.StringFlag("field")
			if err != nil {
				return nil, err
			}
			format, err := ctx.StringFlag("format")
			if err != nil {
				return nil, err
			}
			token, err := vaultToken(ctx)
			if err != nil {
				return nil, err
			}
			result, err := service.ReadVaultSecret(ctx.cmd.Context(), tifs.VaultReadSecretOptions{
				Profile:    profile,
				SecretName: name,
				Field:      field,
				Format:     format,
				VaultToken: token,
			})
			if err != nil {
				return nil, err
			}
			if data, ok := result.([]byte); ok {
				return outputpkg.Raw{Bytes: data}, nil
			}
			return result, nil
		},
	}, info)
	cmd.Flags().String("secret-name", "", "Vault secret name.")
	cmd.Flags().String("field", "", "Field name to read.")
	cmd.Flags().String("format", "json", "Read output format: json, raw, or env.")
	cmd.Flags().String("vault-token", "", "Delegated file system vault token; prefer TI_VAULT_TOKEN environment variable.")
	markUsageRequired(cmd, "secret-name")
	return cmd
}

func newVaultListSecretsCommand(info version.Info) *cobra.Command {
	cmd := newControlPlaneCommand(controlPlaneCommandSpec{
		Use:        "list-secrets",
		Short:      "List file system vault secrets visible to the active credentials.",
		Mutation:   readOnlyCommand,
		Permission: authz.FSVaultSecretRead,
		Run: func(ctx commandContext) (any, error) {
			service, profile, err := fsVaultServiceAndProfile(ctx)
			if err != nil {
				return nil, err
			}
			token, err := vaultToken(ctx)
			if err != nil {
				return nil, err
			}
			return service.ListVaultSecrets(ctx.cmd.Context(), tifs.VaultListSecretsOptions{Profile: profile, VaultToken: token})
		},
	}, info)
	cmd.Flags().String("vault-token", "", "Delegated file system vault token; prefer TI_VAULT_TOKEN environment variable.")
	return cmd
}

func newVaultDeleteSecretCommand(info version.Info) *cobra.Command {
	cmd := newControlPlaneCommand(controlPlaneCommandSpec{
		Use:        "delete-secret",
		Short:      "Delete a file system vault secret.",
		Mutation:   mutatingCommand,
		Permission: authz.FSVaultSecretDelete,
		Run: func(ctx commandContext) (any, error) {
			service, profile, err := fsAdjunctServiceAndProfile(ctx)
			if err != nil {
				return nil, err
			}
			name, err := ctx.StringFlag("secret-name")
			if err != nil {
				return nil, err
			}
			return service.DeleteVaultSecret(ctx.cmd.Context(), tifs.VaultDeleteSecretOptions{Profile: profile, SecretName: name})
		},
	}, info)
	cmd.Flags().String("secret-name", "", "vault secret name")
	markUsageRequired(cmd, "secret-name")
	return cmd
}

func newVaultCreateGrantCommand(info version.Info) *cobra.Command {
	cmd := newControlPlaneCommand(controlPlaneCommandSpec{
		Use:        "create-grant",
		Short:      "Create a delegated file system vault grant.",
		Mutation:   mutatingCommand,
		Permission: authz.FSVaultGrantCreate,
		Run: func(ctx commandContext) (any, error) {
			service, profile, err := fsAdjunctServiceAndProfile(ctx)
			if err != nil {
				return nil, err
			}
			opts, err := vaultCreateGrantOptions(ctx, profile)
			if err != nil {
				return nil, err
			}
			result, err := service.CreateVaultGrant(ctx.cmd.Context(), opts)
			if err != nil {
				return nil, err
			}
			if opts.TokenOnly {
				return outputpkg.Raw{Bytes: []byte(result.Token + "\n")}, nil
			}
			return result, nil
		},
	}, info)
	cmd.Flags().String("agent-id", "", "Agent ID for the delegated grant.")
	cmd.Flags().StringArray("scope", nil, "The vault scope such as secret or secret/field; repeatable.")
	cmd.Flags().String("permission", "", "Grant permission: read or write.")
	cmd.Flags().Duration("ttl", 0, "Grant time to live, for example 1h.")
	cmd.Flags().String("label-hint", "", "Grant label hint.")
	cmd.Flags().Bool("token-only", false, "Print the delegated bearer token only.")
	markUsageRequired(cmd, "agent-id", "scope", "permission", "ttl")
	return cmd
}

func newVaultDeleteGrantCommand(info version.Info) *cobra.Command {
	cmd := newControlPlaneCommand(controlPlaneCommandSpec{
		Use:        "delete-grant",
		Short:      "Delete a file system vault grant.",
		Mutation:   mutatingCommand,
		Permission: authz.FSVaultGrantDelete,
		Run: func(ctx commandContext) (any, error) {
			service, profile, err := fsAdjunctServiceAndProfile(ctx)
			if err != nil {
				return nil, err
			}
			grantID, err := ctx.StringFlag("grant-id")
			if err != nil {
				return nil, err
			}
			revokedBy, err := ctx.StringFlag("revoked-by")
			if err != nil {
				return nil, err
			}
			reason, err := ctx.StringFlag("reason")
			if err != nil {
				return nil, err
			}
			return service.DeleteVaultGrant(ctx.cmd.Context(), tifs.VaultDeleteGrantOptions{Profile: profile, GrantID: grantID, RevokedBy: revokedBy, Reason: reason})
		},
	}, info)
	cmd.Flags().String("grant-id", "", "Vault grant ID.")
	cmd.Flags().String("revoked-by", "ti", "Actor label for the revoke audit entry.")
	cmd.Flags().String("reason", "", "The reason for the revoke.")
	markUsageRequired(cmd, "grant-id")
	return cmd
}

func newVaultListAuditEventsCommand(info version.Info) *cobra.Command {
	cmd := newControlPlaneCommand(controlPlaneCommandSpec{
		Use:        "list-audit-events",
		Short:      "List file system vault audit events.",
		Mutation:   readOnlyCommand,
		Permission: authz.FSVaultAuditRead,
		Run: func(ctx commandContext) (any, error) {
			service, profile, err := fsAdjunctServiceAndProfile(ctx)
			if err != nil {
				return nil, err
			}
			secretName, err := ctx.StringFlag("secret-name")
			if err != nil {
				return nil, err
			}
			agentID, err := ctx.StringFlag("agent-id")
			if err != nil {
				return nil, err
			}
			since, err := ctx.DurationFlag("since")
			if err != nil {
				return nil, err
			}
			limit, err := ctx.Int32Flag("limit")
			if err != nil {
				return nil, err
			}
			return service.ListVaultAuditEvents(ctx.cmd.Context(), tifs.VaultAuditOptions{
				Profile:    profile,
				SecretName: secretName,
				AgentID:    agentID,
				Since:      since,
				Limit:      int(limit),
			})
		},
	}, info)
	cmd.Flags().String("secret-name", "", "Filter by vault secret name.")
	cmd.Flags().String("agent-id", "", "Filter by agent ID.")
	cmd.Flags().Duration("since", 0, "Relative time filter for client-side time, for example 24h.")
	cmd.Flags().Int32("limit", int32(tifs.DefaultVaultAuditLimit), "The maximum number of events to return.")
	return cmd
}

func newVaultRunWithSecretCommand(info version.Info) *cobra.Command {
	cmd := newCommand(commandSpec{
		Use:   "run-with-secret",
		Short: "Run a command with one file system vault secret injected into its environment.",
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := commandContext{cmd: cmd}
			service, profile, err := fsVaultServiceAndProfile(ctx)
			if err != nil {
				return err
			}
			secretPath, err := ctx.StringFlag("secret-path")
			if err != nil {
				return err
			}
			token, err := vaultToken(ctx)
			if err != nil {
				return err
			}
			return service.RunWithVaultSecret(cmd.Context(), tifs.VaultRunWithSecretOptions{
				Profile:    profile,
				SecretPath: secretPath,
				VaultToken: token,
				Command:    args,
				Stdin:      cmd.InOrStdin(),
				Stdout:     cmd.OutOrStdout(),
				Stderr:     cmd.ErrOrStderr(),
			})
		},
	}, info)
	cmd.Args = cobra.ArbitraryArgs
	cmd.Flags().String("secret-path", "", "Vault path in the form /n/vault/<secret>.")
	cmd.Flags().String("vault-token", "", "Delegated file system vault token; prefer TI_VAULT_TOKEN.")
	markUsageRequired(cmd, "secret-path")
	return cmd
}

func newVaultMountCommand(info version.Info) *cobra.Command {
	cmd := newControlPlaneCommand(controlPlaneCommandSpec{
		Use:        "mount-vault",
		Short:      "Mount readable file system vault secrets as a local read-only FUSE filesystem.",
		Mutation:   mutatingCommand,
		Permission: authz.FSVaultSecretRead,
		Run: func(ctx commandContext) (any, error) {
			service, profile, err := fsVaultServiceAndProfile(ctx)
			if err != nil {
				return nil, err
			}
			opts, err := vaultMountOptions(ctx, profile)
			if err != nil {
				return nil, err
			}
			return service.MountVault(ctx.cmd.Context(), opts)
		},
		DryRun: func(ctx commandContext) (dryrun.Result, error) {
			service, profile, err := fsVaultServiceAndProfile(ctx)
			if err != nil {
				return dryrun.Result{}, err
			}
			opts, err := vaultMountOptions(ctx, profile)
			if err != nil {
				return dryrun.Result{}, err
			}
			return service.DryRunMountVault(ctx.cmd.Context(), ctx.CommandPath(), opts)
		},
	}, info)
	cmd.Flags().String("mount-path", "", "The local mount path.")
	cmd.Flags().Bool("foreground", false, "Run mount runtime in the foreground until interrupted.")
	cmd.Flags().Duration("ready-timeout", 30*time.Second, "The time to wait for a background mount to become ready.")
	cmd.Flags().String("vault-token", "", "Delegated file system vault token; prefer TI_VAULT_TOKEN environment variable.")
	markUsageRequired(cmd, "mount-path")
	return cmd
}

func newVaultUnmountCommand(info version.Info) *cobra.Command {
	cmd := newControlPlaneCommand(controlPlaneCommandSpec{
		Use:        "unmount-vault",
		Short:      "Unmount a local vault file system.",
		Mutation:   mutatingCommand,
		Permission: authz.FSVaultSecretRead,
		Run: func(ctx commandContext) (any, error) {
			service, profile, err := fsLocalServiceAndProfile(ctx)
			if err != nil {
				return nil, err
			}
			mountPath, err := ctx.StringFlag("mount-path")
			if err != nil {
				return nil, err
			}
			timeout, err := ctx.DurationFlag("timeout")
			if err != nil {
				return nil, err
			}
			force, err := ctx.BoolFlag("force")
			if err != nil {
				return nil, err
			}
			ignoreAbsent, err := ctx.BoolFlag("ignore-absent")
			if err != nil {
				return nil, err
			}
			return service.UnmountFileSystem(ctx.cmd.Context(), tifs.UnmountFileSystemOptions{
				Profile:      profile,
				MountPath:    mountPath,
				Timeout:      timeout,
				Force:        force,
				IgnoreAbsent: ignoreAbsent,
				NoAutoPack:   true,
			})
		},
		DryRun: func(ctx commandContext) (dryrun.Result, error) {
			service, profile, err := fsLocalServiceAndProfile(ctx)
			if err != nil {
				return dryrun.Result{}, err
			}
			mountPath, err := ctx.StringFlag("mount-path")
			if err != nil {
				return dryrun.Result{}, err
			}
			timeout, err := ctx.DurationFlag("timeout")
			if err != nil {
				return dryrun.Result{}, err
			}
			force, err := ctx.BoolFlag("force")
			if err != nil {
				return dryrun.Result{}, err
			}
			ignoreAbsent, err := ctx.BoolFlag("ignore-absent")
			if err != nil {
				return dryrun.Result{}, err
			}
			return service.DryRunUnmountFileSystem(ctx.cmd.Context(), ctx.CommandPath(), tifs.UnmountFileSystemOptions{
				Profile:      profile,
				MountPath:    mountPath,
				Timeout:      timeout,
				Force:        force,
				IgnoreAbsent: ignoreAbsent,
				NoAutoPack:   true,
			})
		},
	}, info)
	cmd.Flags().String("mount-path", "", "The local mount path.")
	cmd.Flags().Duration("timeout", 30*time.Second, "The time to wait for the mount process to exit.")
	cmd.Flags().Bool("force", false, "Forcefully kill the mount process if graceful unmount times out.")
	cmd.Flags().Bool("ignore-absent", false, "Return success when no file system vault mount state exists for the path.")
	markUsageRequired(cmd, "mount-path")
	return cmd
}

func vaultCreateGrantOptions(ctx commandContext, profile *config.Profile) (tifs.VaultCreateGrantOptions, error) {
	agentID, err := ctx.StringFlag("agent-id")
	if err != nil {
		return tifs.VaultCreateGrantOptions{}, err
	}
	scopes, err := ctx.StringArrayFlag("scope")
	if err != nil {
		return tifs.VaultCreateGrantOptions{}, err
	}
	permission, err := ctx.StringFlag("permission")
	if err != nil {
		return tifs.VaultCreateGrantOptions{}, err
	}
	ttl, err := ctx.DurationFlag("ttl")
	if err != nil {
		return tifs.VaultCreateGrantOptions{}, err
	}
	labelHint, err := ctx.StringFlag("label-hint")
	if err != nil {
		return tifs.VaultCreateGrantOptions{}, err
	}
	tokenOnly, err := ctx.BoolFlag("token-only")
	if err != nil {
		return tifs.VaultCreateGrantOptions{}, err
	}
	return tifs.VaultCreateGrantOptions{Profile: profile, AgentID: agentID, Scopes: scopes, Permission: permission, TTL: ttl, LabelHint: labelHint, TokenOnly: tokenOnly}, nil
}

func vaultMountOptions(ctx commandContext, profile *config.Profile) (tifs.VaultMountOptions, error) {
	mountPath, err := ctx.StringFlag("mount-path")
	if err != nil {
		return tifs.VaultMountOptions{}, err
	}
	foreground, err := ctx.BoolFlag("foreground")
	if err != nil {
		return tifs.VaultMountOptions{}, err
	}
	readyTimeout, err := ctx.DurationFlag("ready-timeout")
	if err != nil {
		return tifs.VaultMountOptions{}, err
	}
	token, err := vaultToken(ctx)
	if err != nil {
		return tifs.VaultMountOptions{}, err
	}
	return tifs.VaultMountOptions{
		Profile:      profile,
		MountPath:    mountPath,
		VaultToken:   token,
		Foreground:   foreground,
		ReadyTimeout: readyTimeout,
	}, nil
}

func vaultToken(ctx commandContext) (string, error) {
	token, err := ctx.StringFlag("vault-token")
	if err != nil {
		return "", err
	}
	if token != "" {
		return token, nil
	}
	value, _, _, err := envcompat.ResolveNames(nil, "TI_VAULT_TOKEN", envcompat.LegacyNameFor("TI_VAULT_TOKEN"))
	return value, err
}

func newFSGitCommand(info version.Info) *cobra.Command {
	cmd := newParentCommand("fs-git", "Manage TiDB Cloud Filesystem git workspaces.", info)
	commands := []*cobra.Command{
		newGitCloneWorkspaceCommand(info),
		newGitHydrateWorkspaceCommand(info),
		newGitAddWorktreeCommand(info),
		newGitRemoveWorktreeCommand(info),
	}
	addFSSelectorFlags(commands)
	addFSAuthFlags(commands)
	cmd.AddCommand(commands...)
	return cmd
}

func newGitCloneWorkspaceCommand(info version.Info) *cobra.Command {
	cmd := newControlPlaneCommand(controlPlaneCommandSpec{
		Use:        "clone-git-workspace",
		Short:      "Fast clone a repository into a mounted file system path.",
		Mutation:   mutatingCommand,
		Permission: authz.FSGitWorkspaceWrite,
		Run: func(ctx commandContext) (any, error) {
			service, profile, err := fsAdjunctServiceAndProfile(ctx)
			if err != nil {
				return nil, err
			}
			opts, err := gitWorkspaceCloneOptions(ctx, profile)
			if err != nil {
				return nil, err
			}
			return service.CloneGitWorkspace(ctx.cmd.Context(), opts)
		},
	}, info)
	cmd.Flags().String("repo-url", "", "Git repository URL.")
	cmd.Flags().String("target-path", "", "The mounted file system path to clone into.")
	cmd.Flags().Bool("blobless", false, "Create a blobless partial local .git and hydrate clean blobs separately.")
	cmd.Flags().String("hydrate", "auto", "Blobless hydrate mode: auto, background, sync, or off")
	markUsageRequired(cmd, "repo-url", "target-path")
	return cmd
}

func newGitHydrateWorkspaceCommand(info version.Info) *cobra.Command {
	cmd := newControlPlaneCommand(controlPlaneCommandSpec{
		Use:        "hydrate-git-workspace",
		Short:      "Hydrate clean git objects for a fs-git workspace.",
		Mutation:   readOnlyCommand,
		Permission: authz.FSGitWorkspaceRead,
		Run: func(ctx commandContext) (any, error) {
			service, profile, err := fsAdjunctServiceAndProfile(ctx)
			if err != nil {
				return nil, err
			}
			targetPath, err := ctx.StringFlag("target-path")
			if err != nil {
				return nil, err
			}
			timeout, err := ctx.DurationFlag("timeout")
			if err != nil {
				return nil, err
			}
			return service.HydrateGitWorkspace(ctx.cmd.Context(), tifs.GitWorkspaceHydrateOptions{Profile: profile, TargetPath: targetPath, Timeout: timeout})
		},
	}, info)
	cmd.Flags().String("target-path", "", "The workspace path with a file system mounted.")
	cmd.Flags().Duration("timeout", 30*time.Minute, "The maximum hydrate duration.")
	markUsageRequired(cmd, "target-path")
	return cmd
}

func newGitAddWorktreeCommand(info version.Info) *cobra.Command {
	cmd := newControlPlaneCommand(controlPlaneCommandSpec{
		Use:        "add-git-worktree",
		Short:      "Fast add a linked fs-git worktree in a mounted file system path.",
		Mutation:   mutatingCommand,
		Permission: authz.FSGitWorkspaceWrite,
		Run: func(ctx commandContext) (any, error) {
			service, profile, err := fsAdjunctServiceAndProfile(ctx)
			if err != nil {
				return nil, err
			}
			opts, err := gitWorktreeAddOptions(ctx, profile)
			if err != nil {
				return nil, err
			}
			return service.AddGitWorktree(ctx.cmd.Context(), opts)
		},
	}, info)
	cmd.Flags().String("base-path", "", "The mounted file system path of the base git workspace.")
	cmd.Flags().String("worktree-path", "", "The mounted file system path for the linked worktree.")
	cmd.Flags().String("branch-name", "", "Create a branch for the linked worktree.")
	cmd.Flags().Bool("detach", false, "Create a detached linked worktree.")
	cmd.Flags().Bool("blobless", false, "Blobless requirement for the base workspace.")
	cmd.Flags().String("hydrate", "auto", "Blobless hydrate mode: auto, background, sync, or off")
	cmd.Flags().String("commit-ish", "", "Optional commit-ish for the linked worktree")
	markUsageRequired(cmd, "base-path", "worktree-path")
	return cmd
}

func newGitRemoveWorktreeCommand(info version.Info) *cobra.Command {
	cmd := newControlPlaneCommand(controlPlaneCommandSpec{
		Use:        "remove-git-worktree",
		Short:      "Remove a linked fs-git worktree without recursive clean-tree deletes.",
		Mutation:   mutatingCommand,
		Permission: authz.FSGitWorkspaceWrite,
		Run: func(ctx commandContext) (any, error) {
			service, profile, err := fsAdjunctServiceAndProfile(ctx)
			if err != nil {
				return nil, err
			}
			worktreePath, err := ctx.StringFlag("worktree-path")
			if err != nil {
				return nil, err
			}
			force, err := ctx.BoolFlag("force")
			if err != nil {
				return nil, err
			}
			return service.RemoveGitWorktree(ctx.cmd.Context(), tifs.GitWorktreeRemoveOptions{Profile: profile, WorktreePath: worktreePath, Force: force})
		},
	}, info)
	cmd.Flags().String("worktree-path", "", "Mounted file system path of the linked worktree.")
	cmd.Flags().Bool("force", false, "Remove even when the linked worktree has local changes.")
	markUsageRequired(cmd, "worktree-path")
	return cmd
}

func gitWorkspaceCloneOptions(ctx commandContext, profile *config.Profile) (tifs.GitWorkspaceCloneOptions, error) {
	repoURL, err := ctx.StringFlag("repo-url")
	if err != nil {
		return tifs.GitWorkspaceCloneOptions{}, err
	}
	targetPath, err := ctx.StringFlag("target-path")
	if err != nil {
		return tifs.GitWorkspaceCloneOptions{}, err
	}
	blobless, err := ctx.BoolFlag("blobless")
	if err != nil {
		return tifs.GitWorkspaceCloneOptions{}, err
	}
	hydrate, err := ctx.StringFlag("hydrate")
	if err != nil {
		return tifs.GitWorkspaceCloneOptions{}, err
	}
	return tifs.GitWorkspaceCloneOptions{Profile: profile, RepoURL: repoURL, TargetPath: targetPath, Blobless: blobless, HydrateMode: hydrate}, nil
}

func gitWorktreeAddOptions(ctx commandContext, profile *config.Profile) (tifs.GitWorktreeAddOptions, error) {
	basePath, err := ctx.StringFlag("base-path")
	if err != nil {
		return tifs.GitWorktreeAddOptions{}, err
	}
	worktreePath, err := ctx.StringFlag("worktree-path")
	if err != nil {
		return tifs.GitWorktreeAddOptions{}, err
	}
	branchName, err := ctx.StringFlag("branch-name")
	if err != nil {
		return tifs.GitWorktreeAddOptions{}, err
	}
	detach, err := ctx.BoolFlag("detach")
	if err != nil {
		return tifs.GitWorktreeAddOptions{}, err
	}
	blobless, err := ctx.BoolFlag("blobless")
	if err != nil {
		return tifs.GitWorktreeAddOptions{}, err
	}
	hydrate, err := ctx.StringFlag("hydrate")
	if err != nil {
		return tifs.GitWorktreeAddOptions{}, err
	}
	commitISH, err := ctx.StringFlag("commit-ish")
	if err != nil {
		return tifs.GitWorktreeAddOptions{}, err
	}
	return tifs.GitWorktreeAddOptions{
		Profile:      profile,
		BasePath:     basePath,
		WorktreePath: worktreePath,
		BranchName:   branchName,
		Detach:       detach,
		Blobless:     blobless,
		HydrateMode:  hydrate,
		CommitISH:    commitISH,
	}, nil
}

func newFSJournalCommand(info version.Info) *cobra.Command {
	cmd := newParentCommand("fs-journal", "Manage FS journals.", info)
	commands := []*cobra.Command{
		newJournalCreateCommand(info),
		newJournalAppendEntriesCommand(info),
		newJournalReadEntriesCommand(info),
		newJournalSearchEntriesCommand(info),
		newJournalVerifyCommand(info),
	}
	addFSSelectorFlags(commands)
	addFSAuthFlags(commands)
	cmd.AddCommand(commands...)
	return cmd
}

func newJournalCreateCommand(info version.Info) *cobra.Command {
	cmd := newControlPlaneCommand(controlPlaneCommandSpec{
		Use:        "create-journal",
		Short:      "Create a file system journal.",
		Mutation:   mutatingCommand,
		Permission: authz.FSJournalCreate,
		Run: func(ctx commandContext) (any, error) {
			service, profile, err := fsAdjunctServiceAndProfile(ctx)
			if err != nil {
				return nil, err
			}
			journalID, err := ctx.StringFlag("journal-id")
			if err != nil {
				return nil, err
			}
			journalKind, err := ctx.StringFlag("journal-kind")
			if err != nil {
				return nil, err
			}
			title, err := ctx.StringFlag("title")
			if err != nil {
				return nil, err
			}
			actor, err := ctx.StringFlag("actor")
			if err != nil {
				return nil, err
			}
			labels, err := ctx.StringArrayFlag("label")
			if err != nil {
				return nil, err
			}
			return service.CreateJournal(ctx.cmd.Context(), tifs.JournalCreateOptions{
				Profile:     profile,
				JournalID:   journalID,
				JournalKind: journalKind,
				Title:       title,
				Actor:       actor,
				Labels:      labels,
			})
		},
	}, info)
	cmd.Flags().String("journal-id", "", "Journal ID; auto generated when omitted.")
	cmd.Flags().String("journal-kind", "agent", "Journal kind.")
	cmd.Flags().String("title", "", "Journal title.")
	cmd.Flags().String("actor", "", "Actor in the form type:id.")
	cmd.Flags().StringArray("label", nil, "Journal label key=value; repeatable.")
	return cmd
}

func newJournalAppendEntriesCommand(info version.Info) *cobra.Command {
	cmd := newControlPlaneCommand(controlPlaneCommandSpec{
		Use:        "append-journal-entries",
		Short:      "Append JSON journal entries.",
		Mutation:   mutatingCommand,
		Permission: authz.FSJournalAppend,
		Run: func(ctx commandContext) (any, error) {
			service, profile, err := fsAdjunctServiceAndProfile(ctx)
			if err != nil {
				return nil, err
			}
			journalID, err := ctx.StringFlag("journal-id")
			if err != nil {
				return nil, err
			}
			idempotencyKey, err := ctx.StringFlag("idempotency-key")
			if err != nil {
				return nil, err
			}
			entryType, err := ctx.StringFlag("entry-type")
			if err != nil {
				return nil, err
			}
			source, err := ctx.StringFlag("source")
			if err != nil {
				return nil, err
			}
			subjects, err := ctx.StringArrayFlag("subject")
			if err != nil {
				return nil, err
			}
			entryJSON, err := ctx.StringArrayFlag("entry-json")
			if err != nil {
				return nil, err
			}
			jsonArray, err := ctx.BoolFlag("json-array")
			if err != nil {
				return nil, err
			}
			return service.AppendJournalEntries(ctx.cmd.Context(), tifs.JournalAppendOptions{
				Profile:        profile,
				JournalID:      journalID,
				IdempotencyKey: idempotencyKey,
				EntryType:      entryType,
				Source:         source,
				Subjects:       subjects,
				EntryJSON:      entryJSON,
				JSONArray:      jsonArray,
				Stdin:          ctx.cmd.InOrStdin(),
			})
		},
	}, info)
	cmd.Flags().String("journal-id", "", "The journal ID.")
	cmd.Flags().String("idempotency-key", "", "Append idempotency key; generated when omitted.")
	cmd.Flags().String("entry-type", "", "Default entry type for entries missing type.")
	cmd.Flags().String("source", "", "Entry source.")
	cmd.Flags().StringArray("subject", nil, "Entry subject; repeatable.")
	cmd.Flags().StringArray("entry-json", nil, "JSON journal entry; repeatable.")
	cmd.Flags().Bool("json-array", false, "Read a JSON array from stdin instead of JSONL.")
	markUsageRequired(cmd, "journal-id")
	return cmd
}

func newJournalReadEntriesCommand(info version.Info) *cobra.Command {
	cmd := newControlPlaneCommand(controlPlaneCommandSpec{
		Use:        "read-journal-entries",
		Short:      "Read entries from a file system journal.",
		Mutation:   readOnlyCommand,
		Permission: authz.FSJournalRead,
		Run: func(ctx commandContext) (any, error) {
			service, profile, err := fsAdjunctServiceAndProfile(ctx)
			if err != nil {
				return nil, err
			}
			journalID, err := ctx.StringFlag("journal-id")
			if err != nil {
				return nil, err
			}
			afterSeq, err := ctx.Int64Flag("after-seq")
			if err != nil {
				return nil, err
			}
			limit, err := ctx.Int32Flag("limit")
			if err != nil {
				return nil, err
			}
			return service.ReadJournalEntries(ctx.cmd.Context(), tifs.JournalReadOptions{
				Profile:   profile,
				JournalID: journalID,
				AfterSeq:  afterSeq,
				Limit:     int(limit),
			})
		},
	}, info)
	cmd.Flags().String("journal-id", "", "The journal ID.")
	cmd.Flags().Int64("after-seq", 0, "Read journal entries after the specified sequence#.")
	cmd.Flags().Int32("limit", 100, "The maximum number of entries to read.")
	markUsageRequired(cmd, "journal-id")
	return cmd
}

func newJournalSearchEntriesCommand(info version.Info) *cobra.Command {
	cmd := newControlPlaneCommand(controlPlaneCommandSpec{
		Use:        "search-journal-entries",
		Short:      "Search file system journal entries.",
		Mutation:   readOnlyCommand,
		Permission: authz.FSJournalSearch,
		Run: func(ctx commandContext) (any, error) {
			service, profile, err := fsAdjunctServiceAndProfile(ctx)
			if err != nil {
				return nil, err
			}
			entryType, err := ctx.StringFlag("entry-type")
			if err != nil {
				return nil, err
			}
			status, err := ctx.StringFlag("status")
			if err != nil {
				return nil, err
			}
			journalKind, err := ctx.StringFlag("journal-kind")
			if err != nil {
				return nil, err
			}
			actor, err := ctx.StringFlag("actor")
			if err != nil {
				return nil, err
			}
			subjects, err := ctx.StringArrayFlag("subject")
			if err != nil {
				return nil, err
			}
			labels, err := ctx.StringArrayFlag("label")
			if err != nil {
				return nil, err
			}
			since, err := ctx.StringFlag("since")
			if err != nil {
				return nil, err
			}
			until, err := ctx.StringFlag("until")
			if err != nil {
				return nil, err
			}
			limit, err := ctx.Int32Flag("limit")
			if err != nil {
				return nil, err
			}
			cursor, err := ctx.StringFlag("cursor")
			if err != nil {
				return nil, err
			}
			includeEntries, err := ctx.BoolFlag("include-entries")
			if err != nil {
				return nil, err
			}
			return service.SearchJournal(ctx.cmd.Context(), tifs.JournalSearchOptions{
				Profile:        profile,
				EntryType:      entryType,
				Status:         status,
				JournalKind:    journalKind,
				Actor:          actor,
				Subjects:       subjects,
				Labels:         labels,
				Since:          since,
				Until:          until,
				Limit:          int(limit),
				Cursor:         cursor,
				IncludeEntries: includeEntries,
			})
		},
	}, info)
	cmd.Flags().String("entry-type", "", "Journal entry type filter.")
	cmd.Flags().String("status", "", "Journal entry status filter.")
	cmd.Flags().String("journal-kind", "", "Journal kind filter.")
	cmd.Flags().String("actor", "", "Actor in the form type:id.")
	cmd.Flags().StringArray("subject", nil, "Journal entry subject filter; repeatable.")
	cmd.Flags().StringArray("label", nil, "Journal entry label filter key=value; repeatable.")
	cmd.Flags().String("since", "", "Relative duration or RFC3339 lower time bound.")
	cmd.Flags().String("until", "", "RFC3339 upper time bound.")
	cmd.Flags().Int32("limit", 100, "The maximum number of matches to read.")
	cmd.Flags().String("cursor", "", "The cursor for pagination.")
	cmd.Flags().Bool("include-entries", false, "Toggle to include full entry payloads.")
	return cmd
}

func newJournalVerifyCommand(info version.Info) *cobra.Command {
	cmd := newControlPlaneCommand(controlPlaneCommandSpec{
		Use:        "verify-journal",
		Short:      "Verify a file system journal hash chain.",
		Mutation:   readOnlyCommand,
		Permission: authz.FSJournalVerify,
		Run: func(ctx commandContext) (any, error) {
			service, profile, err := fsAdjunctServiceAndProfile(ctx)
			if err != nil {
				return nil, err
			}
			journalID, err := ctx.StringFlag("journal-id")
			if err != nil {
				return nil, err
			}
			return service.VerifyJournal(ctx.cmd.Context(), tifs.JournalVerifyOptions{
				Profile:   profile,
				JournalID: journalID,
			})
		},
	}, info)
	cmd.Flags().String("journal-id", "", "Journal ID")
	markUsageRequired(cmd, "journal-id")
	return cmd
}
