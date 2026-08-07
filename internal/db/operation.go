package db

type Operation string

const (
	OperationClusterCreate          Operation = "cluster.create"
	OperationClusterList            Operation = "cluster.list"
	OperationClusterDiscover        Operation = "cluster.discover"
	OperationClusterDescribe        Operation = "cluster.describe"
	OperationClusterUpdate          Operation = "cluster.update"
	OperationClusterDelete          Operation = "cluster.delete"
	OperationBranchCreate           Operation = "branch.create"
	OperationBranchList             Operation = "branch.list"
	OperationBranchDescribe         Operation = "branch.describe"
	OperationBranchDelete           Operation = "branch.delete"
	OperationSQLUserCreate          Operation = "sql_user.create"
	OperationConnectionStringFormat Operation = "connection_string.format"
	OperationSQLExecute             Operation = "sql.execute"
)
