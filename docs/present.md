# ti 中文演示脚本

这份脚本使用本地二进制 `bin/ti` 展示当前 Preview 版本。故事线是：配置默认 profile，创建 Starter 数据库并按角色执行 SQL，创建一个 TiDB Cloud Filesystem workspace，通过 data plane 和 mount 双向操作，再用 Git、Journal 和 Vault 完成 agent 工作流。

## 演示前准备

确认版本和帮助：

```bash
bin/ti --version
bin/ti help
```

交互式配置默认 profile：

```bash
bin/ti configure
```

自动化环境也可以使用：

```bash
TI_REGION_CODE=aws-us-east-1 \
TIDB_CLOUD_PUBLIC_KEY="$TIDB_CLOUD_PUBLIC_KEY" \
TIDB_CLOUD_PRIVATE_KEY="$TIDB_CLOUD_PRIVATE_KEY" \
bin/ti configure --non-interactive
```

`configure` 会验证 TiDB Cloud API key，找到唯一的 `tidbx_virtual` project，并把其 ID 保存为默认 `project_id`。后续创建 Starter cluster 不需要重复传 `--project-id`。

## 1. 查看 Organization Project

```bash
bin/ti organization list-projects --output text
bin/ti organization list-projects --query 'projects[].{id:id,name:name,type:type}'
```

`organization` 当前是只读 control plane，可供人、脚本和 agent 检查 API key 能访问的项目。

## 2. 创建并管理 Starter Cluster

为本次演示生成唯一名字：

```bash
export DEMO_ID="$(date +%Y%m%d%H%M%S)"
export CLUSTER_NAME="ti-demo-${DEMO_ID}"
```

先 dry run，再实际创建：

```bash
bin/ti db create-db-cluster \
  --db-cluster-name "$CLUSTER_NAME" \
  --db-cluster-type starter \
  --dry-run

bin/ti db create-db-cluster \
  --db-cluster-name "$CLUSTER_NAME" \
  --db-cluster-type starter \
  --wait
```

从 JSON 结果中找到 cluster ID：

```bash
bin/ti db list-db-clusters --output text
export CLUSTER_ID="$(bin/ti db list-db-clusters | jq -r --arg name "$CLUSTER_NAME" '.clusters[] | select(.display_name == $name) | .id' | head -n 1)"
bin/ti db describe-db-cluster --db-cluster-id "$CLUSTER_ID" --output text
```

创建命令返回 `ACTIVE` 后，更新名称并重新读取：

```bash
export CLUSTER_RENAMED="${CLUSTER_NAME}-renamed"

bin/ti db update-db-cluster \
  --db-cluster-id "$CLUSTER_ID" \
  --db-cluster-name "$CLUSTER_RENAMED"

bin/ti db describe-db-cluster --db-cluster-id "$CLUSTER_ID" --output text
```

分支生命周期：

```bash
bin/ti db create-db-cluster-branch \
  --db-cluster-id "$CLUSTER_ID" \
  --db-cluster-branch-name demo-branch \
  --wait

bin/ti db list-db-cluster-branches --db-cluster-id "$CLUSTER_ID" --output text
```

## 3. 创建 SQL 用户并按角色执行 SQL

创建或修复 ti 管理的三种稳定 SQL 用户。该操作可重入，不会在每次运行时创建新的一组：

```bash
bin/ti db create-db-sql-users --db-cluster-id "$CLUSTER_ID"
```

格式化三种连接信息。默认角色是 read-write，也可以显式指定：

```bash
bin/ti db format-db-connection-string \
  --db-cluster-id "$CLUSTER_ID" \
  --read-write \
  --format mysql-uri

bin/ti db format-db-connection-string \
  --db-cluster-id "$CLUSTER_ID" \
  --read-only \
  --format env

bin/ti db format-db-connection-string \
  --db-cluster-id "$CLUSTER_ID" \
  --admin \
  --format jdbc
```

使用 admin 建库建表，read-write 写入，read-only 查询：

```bash
bin/ti db execute-sql-statement \
  --db-cluster-id "$CLUSTER_ID" \
  --admin \
  --sql "create database if not exists ti_demo"

bin/ti db execute-sql-statement \
  --db-cluster-id "$CLUSTER_ID" \
  --admin \
  --sql "create table if not exists ti_demo.messages (id int primary key, body varchar(128))"

bin/ti db execute-sql-statement \
  --db-cluster-id "$CLUSTER_ID" \
  --read-write \
  --sql "insert into ti_demo.messages values (1, 'hello from ti') on duplicate key update body = values(body)"

bin/ti db execute-sql-statement \
  --db-cluster-id "$CLUSTER_ID" \
  --read-only \
  --sql "select id, body from ti_demo.messages" \
  --output text
```

SQL 默认通过 HTTPS SQL API 执行，一次命令只执行一个 statement。`--transport mysql` 是显式的一次性连接模式，不是隐藏 fallback。

## 4. 创建并管理 Filesystem

创建名为 `ti-demo-workspace` 的资源。一个 profile 可以注册多个 Filesystem；每个资源都必须通过名称显式选择：

```bash
bin/ti fs create-file-system \
  --file-system-name ti-demo-workspace \
  --dry-run

bin/ti fs create-file-system \
  --file-system-name ti-demo-workspace \
  --wait

bin/ti fs list-file-systems --output text
bin/ti fs describe-file-system \
  --file-system-name ti-demo-workspace \
  --output text

bin/ti fs check-file-system \
  --file-system-name ti-demo-workspace \
  --output text
```

创建命令的 JSON 结果包含一次性的 `fs_token`。它是资源 owner credential，不能写入日志或公开传递。资源元数据和凭证分别存储在 `~/.ti/fs_resources/<profile-key>/<resource-key>/` 下，不写入主 `~/.ti/credentials`。

## 5. 使用 Data Plane 操作文件

```bash
bin/ti fs create-directory \
  --file-system-name ti-demo-workspace \
  --path /demo

printf 'hello from data plane\n' | bin/ti fs copy-file \
  --file-system-name ti-demo-workspace \
  --from-stdin \
  --to-remote /demo/from-data-plane.txt \
  --tag source=data-plane \
  --description "created through ti fs data plane"

bin/ti fs list-files \
  --file-system-name ti-demo-workspace \
  --path /demo \
  --output text

bin/ti fs read-file \
  --file-system-name ti-demo-workspace \
  --path /demo/from-data-plane.txt

bin/ti fs describe-file \
  --file-system-name ti-demo-workspace \
  --path /demo/from-data-plane.txt \
  --output text
```

Unix-style alias 只缩短命令名，flags 仍使用完整名称：

```bash
bin/ti fs ls --file-system-name ti-demo-workspace --path /demo --output text
bin/ti fs cat --file-system-name ti-demo-workspace --path /demo/from-data-plane.txt
```

## 6. 挂载并验证双向可见性

```bash
export MOUNT_PATH="/tmp/ti-demo-${DEMO_ID}"
mkdir -p "$MOUNT_PATH"

bin/ti fs mount-file-system \
  --file-system-name ti-demo-workspace \
  --mount-path "$MOUNT_PATH"
```

自动 driver 在 Linux 上使用 FUSE，在 macOS 和 Windows 上使用 WebDAV。macOS 安装 macFUSE 后，可以显式增加 `--driver fuse` 使用完整的 FUSE 体验。

通过 mount 读取 data plane 写入的文件：

```bash
cat "$MOUNT_PATH/demo/from-data-plane.txt"
```

通过 mount 写入，再从 data plane 读取：

```bash
printf 'hello from mount\n' > "$MOUNT_PATH/demo/from-mount.txt"

bin/ti fs read-file \
  --file-system-name ti-demo-workspace \
  --path /demo/from-mount.txt
```

FUSE mount 可以在卸载前 drain dirty state。WebDAV 通过正常的 file close 刷新，不支持 drain：

```bash
bin/ti fs drain-file-system --mount-path "$MOUNT_PATH"
```

## 7. 使用 Filesystem Git Workspace

在挂载目录内执行快速 clone 和 hydrate：

```bash
mkdir -p "$MOUNT_PATH/repos"

bin/ti fs-git clone-git-workspace \
  --file-system-name ti-demo-workspace \
  --repo-url https://github.com/octocat/Hello-World.git \
  --target-path "$MOUNT_PATH/repos/hello" \
  --blobless \
  --hydrate background

bin/ti fs-git hydrate-git-workspace \
  --file-system-name ti-demo-workspace \
  --target-path "$MOUNT_PATH/repos/hello"

git -C "$MOUNT_PATH/repos/hello" status --short
```

创建隔离的 linked worktree：

```bash
bin/ti fs-git add-git-worktree \
  --file-system-name ti-demo-workspace \
  --base-path "$MOUNT_PATH/repos/hello" \
  --worktree-path "$MOUNT_PATH/repos/hello-feature" \
  --branch-name demo-feature

git -C "$MOUNT_PATH/repos/hello-feature" status --short

bin/ti fs-git remove-git-worktree \
  --file-system-name ti-demo-workspace \
  --worktree-path "$MOUNT_PATH/repos/hello-feature" \
  --force
```

`ti fs-git` 不替代 Git。它负责为 Filesystem mount 准备 clone、hydrate 和 worktree 工作流；普通提交和分支操作仍使用 `git`。

## 8. 使用 Journal 记录 Agent 工作流

```bash
export JOURNAL_ID="jrn-demo-${DEMO_ID}"

bin/ti fs-journal create-journal \
  --file-system-name ti-demo-workspace \
  --journal-id "$JOURNAL_ID" \
  --journal-kind agent \
  --title "ti demo ${DEMO_ID}" \
  --actor agent:demo \
  --label demo=present

bin/ti fs-journal append-journal-entries \
  --file-system-name ti-demo-workspace \
  --journal-id "$JOURNAL_ID" \
  --entry-json '{"type":"demo.started"}' \
  --entry-json '{"type":"demo.completed"}'

bin/ti fs-journal read-journal-entries \
  --file-system-name ti-demo-workspace \
  --journal-id "$JOURNAL_ID" \
  --output text

bin/ti fs-journal search-journal-entries \
  --file-system-name ti-demo-workspace \
  --entry-type demo.completed \
  --include-entries

bin/ti fs-journal verify-journal \
  --file-system-name ti-demo-workspace \
  --journal-id "$JOURNAL_ID" \
  --output text
```

Journal 是 append-only、可验证的 workflow ledger，不是普通文本日志文件。

## 9. 使用 Vault 管理和委派 Secret

```bash
printf 'demo-token\n' > /tmp/ti-demo-token.txt

bin/ti fs-vault create-secret \
  --file-system-name ti-demo-workspace \
  --secret-name demo-service \
  --field ENDPOINT=https://example.invalid \
  --field API_TOKEN=@/tmp/ti-demo-token.txt

bin/ti fs-vault read-secret \
  --file-system-name ti-demo-workspace \
  --secret-name demo-service \
  --field ENDPOINT \
  --format raw
```

向 agent 委派一个字段的临时只读访问：

```bash
export TI_VAULT_TOKEN="$(bin/ti fs-vault create-grant \
  --file-system-name ti-demo-workspace \
  --agent-id demo-agent \
  --scope demo-service/ENDPOINT \
  --permission read \
  --ttl 10m \
  --token-only)"

bin/ti fs-vault read-secret \
  --file-system-name ti-demo-workspace \
  --secret-name demo-service \
  --field ENDPOINT \
  --format raw \
  --vault-token "$TI_VAULT_TOKEN"

bin/ti fs-vault list-audit-events \
  --file-system-name ti-demo-workspace \
  --secret-name demo-service \
  --limit 20 \
  --output text
```

Vault mount 是只读 FUSE view，需要 delegated Vault token；Windows 不支持，macOS 需要 macFUSE。`run-with-secret` 可在不把值写进命令行的情况下将 secret 注入子进程。

## 10. 清理

FUSE mount 先 drain，再卸载。WebDAV mount 跳过 drain：

```bash
bin/ti fs drain-file-system --mount-path "$MOUNT_PATH"
bin/ti fs unmount-file-system --mount-path "$MOUNT_PATH" --ignore-absent
rm -rf "$MOUNT_PATH"
```

删除演示 secret 和临时文件：

```bash
bin/ti fs-vault delete-secret \
  --file-system-name ti-demo-workspace \
  --secret-name demo-service

rm -f /tmp/ti-demo-token.txt
```

删除 Filesystem 资源及其本地 registry entry：

```bash
bin/ti fs delete-file-system \
  --file-system-name ti-demo-workspace
```

删除演示 cluster：

```bash
bin/ti db delete-db-cluster \
  --db-cluster-id "$CLUSTER_ID" \
  --wait
```

## 讲解重点

- ti 使用完整命令名、长 flags、JSON 默认输出、JMESPath `--query`、`--output text` 和 control-plane `--dry-run`，同时适合人和 agent。
- TiDB Cloud control-plane API key、SQL 用户凭证和 FS owner token 是三个不同的认证边界。
- 一个 profile 可以注册多个 Filesystem；data plane 和 mount 对同一个远端 namespace 双向可见。
- ti 负责命令、配置、资源选择、输出和安装；随附的 `ti-drive9` 负责 Filesystem runtime 正确性。
- `ti fs-git`、`ti fs-journal` 和 `ti fs-vault` 是 Filesystem 的附属一级域，分别服务于 Git workspace、可验证 workflow ledger 和 secret delegation。
