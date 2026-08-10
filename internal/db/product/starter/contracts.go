package starter

import rootdb "github.com/tidbcloud/ti-cli/internal/db"

type ListClustersOptions = rootdb.ListClustersOptions
type CreateClusterOptions = rootdb.CreateClusterOptions
type DescribeClusterOptions = rootdb.DescribeClusterOptions
type UpdateClusterOptions = rootdb.UpdateClusterOptions
type DeleteClusterOptions = rootdb.DeleteClusterOptions
type ListClustersResult = rootdb.ListClustersResult
type ClusterResult = rootdb.ClusterResult

type ListBranchesOptions = rootdb.ListBranchesOptions
type CreateBranchOptions = rootdb.CreateBranchOptions
type DescribeBranchOptions = rootdb.DescribeBranchOptions
type DeleteBranchOptions = rootdb.DeleteBranchOptions
type ListBranchesResult = rootdb.ListBranchesResult
type BranchResult = rootdb.BranchResult

type PrepareQueryAccessOptions = rootdb.PrepareQueryAccessOptions
type CreateConnectionStringOptions = rootdb.CreateConnectionStringOptions
type ExecuteSQLOptions = rootdb.ExecuteSQLOptions
type PrepareQueryAccessResult = rootdb.PrepareQueryAccessResult

type CreateOptions struct {
	MonthlySpendingLimitUSDCents int32
}

func (CreateOptions) DBProductOptions() {}

type UpdateOptions struct {
	MonthlySpendingLimitUSDCents int32
}

func (UpdateOptions) DBProductOptions() {}

func createOptions(product rootdb.ProductOptions) (CreateOptions, error) {
	options, ok := product.(CreateOptions)
	if !ok {
		return CreateOptions{}, rootdb.InvalidProductOptions(rootdb.ClusterTypeStarter, rootdb.OperationClusterCreate)
	}
	return options, nil
}

func updateOptions(product rootdb.ProductOptions) (UpdateOptions, error) {
	options, ok := product.(UpdateOptions)
	if !ok {
		return UpdateOptions{}, rootdb.InvalidProductOptions(rootdb.ClusterTypeStarter, rootdb.OperationClusterUpdate)
	}
	return options, nil
}
