# tdc 中文演示脚本

这份脚本使用本地二进制 `bin/tdc` 展示当前 Preview 版本。故事线是：配置默认 profile，创建 Starter 数据库并按角色执行 SQL，创建一个 TiDB Cloud Filesystem workspace，通过 data plane 和 mount 双向操作，再用 Git、Journal 和 Vault 完成 agent 工作流。

## 演示前准备

确认版本和帮助：

```bash
bin/tdc --version
bin/tdc help
```

交互式配置默认 profile：

```bash
bin/tdc configure
```

自动化环境也可以使用：

```bash
TDC_REGION_CODE=aws-us-east-1 \
TDC_PUBLIC_KEY="$TDC_PUBLIC_KEY" \
TDC_PRIVATE_KEY="$TDC_PRIVATE_KEY" \
bin/tdc configure --non-interactive
```

`configure` 会验证 TiDB Cloud API key，找到唯一的 `tidbx_virtual` project，并把其 ID 保存为默认 `project_id`。后续创建 Starter cluster 不需要重复传 `--project-id`。

## 1. 查看 Organization Project

```bash
bin/tdc organization list-projects --output text
bin/tdc organization list-projects --query 'projects[].{id:id,name:name,type:type}'
```

`organization` 当前是只读 control plane，可供人、脚本和 agent 检查 API key 能访问的项目。

## 2. 创建并管理 Starter Cluster

为本次演示生成唯一名字：

```bash
export DEMO_ID="$(date +%Y%m%d%H%M%S)"
export CLUSTER_NAME="tdc-demo-${DEMO_ID}"
```

先 dry run，再实际创建：

```bash
bin/tdc db create-db-cluster \
  --db-cluster-name "$CLUSTER_NAME" \
  --db-cluster-type starter \
  --dry-run

bin/tdc db create-db-cluster \
  --db-cluster-name "$CLUSTER_NAME" \
  --db-cluster-type starter \
  --wait
```

从 JSON 结果中找到 cluster ID：

```bash
bin/tdc db list-db-clusters --db-cluster-type starter --output text
export CLUSTER_ID="$(bin/tdc db list-db-clusters --db-cluster-type starter | jq -r --arg name "$CLUSTER_NAME" '.clusters[] | select(.display_name == $name) | .id' | head -n 1)"
bin/tdc db describe-db-cluster --db-cluster-id "$CLUSTER_ID" --output text
```

创建命令返回 `ACTIVE` 后，更新名称并重新读取：

```bash
export CLUSTER_RENAMED="${CLUSTER_NAME}-renamed"

bin/tdc db update-db-cluster \
  --db-cluster-id "$CLUSTER_ID" \
  --db-cluster-name "$CLUSTER_RENAMED"

bin/tdc db describe-db-cluster --db-cluster-id "$CLUSTER_ID" --output text
```

分支生命周期：

```bash
bin/tdc db create-db-cluster-branch \
  --db-cluster-id "$CLUSTER_ID" \
  --db-cluster-branch-name demo-branch \
  --wait

bin/tdc db list-db-cluster-branches --db-cluster-id "$CLUSTER_ID" --output text
```

## 3. 创建 SQL 用户并按角色执行 SQL

创建或修复 tdc 管理的三种稳定 SQL 用户。该操作可重入，不会在每次运行时创建新的一组：

```bash
bin/tdc db create-db-sql-users --db-cluster-id "$CLUSTER_ID"
```

格式化三种连接信息。默认角色是 read-write，也可以显式指定：

```bash
bin/tdc db format-db-connection-string \
  --db-cluster-id "$CLUSTER_ID" \
  --read-write \
  --format mysql-uri

bin/tdc db format-db-connection-string \
  --db-cluster-id "$CLUSTER_ID" \
  --read-only \
  --format env

bin/tdc db format-db-connection-string \
  --db-cluster-id "$CLUSTER_ID" \
  --admin \
  --format jdbc
```

使用 admin 建库建表，read-write 写入，read-only 查询：

```bash
bin/tdc db execute-sql-statement \
  --db-cluster-id "$CLUSTER_ID" \
  --admin \
  --sql "create database if not exists tdc_demo"

bin/tdc db execute-sql-statement \
  --db-cluster-id "$CLUSTER_ID" \
  --admin \
  --sql "create table if not exists tdc_demo.messages (id int primary key, body varchar(128))"

bin/tdc db execute-sql-statement \
  --db-cluster-id "$CLUSTER_ID" \
  --read-write \
  --sql "insert into tdc_demo.messages values (1, 'hello from tdc') on duplicate key update body = values(body)"

bin/tdc db execute-sql-statement \
  --db-cluster-id "$CLUSTER_ID" \
  --read-only \
  --sql "select id, body from tdc_demo.messages" \
  --output text
```

SQL 默认通过 HTTPS SQL API 执行，一次命令只执行一个 statement。`--transport mysql` 是显式的一次性连接模式，不是隐藏 fallback。

## 4. 创建并管理 Filesystem

创建一个由服务端分配稳定 ID 的资源。远端 inventory 是资源状态的权威来源，本地只保存按 ID 索引的访问凭证：

```bash
bin/tdc fs create-file-system --dry-run

export FILE_SYSTEM_ID="$(bin/tdc fs create-file-system \
  --wait \
  --query file_system_id \
  --output text)"

bin/tdc fs list-file-systems --output text
bin/tdc fs describe-file-system \
  --file-system-id "$FILE_SYSTEM_ID" \
  --output text

bin/tdc fs check-file-system \
  --file-system-id "$FILE_SYSTEM_ID" \
  --output text
```

创建命令的 JSON 结果包含一次性的 `fs_token`。它是资源 owner credential，不能写入日志或公开传递。凭证存储在 `~/.tdc/fs_credentials/<profile-key>/<file-system-id-key>/credentials`，不写入主 `~/.tdc/credentials`。

## 5. 使用 Data Plane 操作文件

```bash
bin/tdc fs create-directory \
  --file-system-id "$FILE_SYSTEM_ID" \
  --path /demo

printf 'hello from data plane\n' | bin/tdc fs copy-file \
  --file-system-id "$FILE_SYSTEM_ID" \
  --from-stdin \
  --to-remote /demo/from-data-plane.txt \
  --tag source=data-plane \
  --description "created through tdc fs data plane"

bin/tdc fs list-files \
  --file-system-id "$FILE_SYSTEM_ID" \
  --path /demo \
  --output text

bin/tdc fs read-file \
  --file-system-id "$FILE_SYSTEM_ID" \
  --path /demo/from-data-plane.txt

bin/tdc fs describe-file \
  --file-system-id "$FILE_SYSTEM_ID" \
  --path /demo/from-data-plane.txt \
  --output text
```

Unix-style alias 只缩短命令名，flags 仍使用完整名称：

```bash
bin/tdc fs ls --file-system-id "$FILE_SYSTEM_ID" --path /demo --output text
bin/tdc fs cat --file-system-id "$FILE_SYSTEM_ID" --path /demo/from-data-plane.txt
```

## 6. 挂载并验证双向可见性

```bash
export MOUNT_PATH="/tmp/tdc-demo-${DEMO_ID}"
mkdir -p "$MOUNT_PATH"

bin/tdc fs mount-file-system \
  --file-system-id "$FILE_SYSTEM_ID" \
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

bin/tdc fs read-file \
  --file-system-id "$FILE_SYSTEM_ID" \
  --path /demo/from-mount.txt
```

FUSE mount 可以在卸载前 drain dirty state。WebDAV 通过正常的 file close 刷新，不支持 drain：

```bash
bin/tdc fs drain-file-system --mount-path "$MOUNT_PATH"
```

## 7. 使用 Filesystem Git Workspace

在挂载目录内执行快速 clone 和 hydrate：

```bash
mkdir -p "$MOUNT_PATH/repos"

bin/tdc fs-git clone-git-workspace \
  --file-system-id "$FILE_SYSTEM_ID" \
  --repo-url https://github.com/octocat/Hello-World.git \
  --target-path "$MOUNT_PATH/repos/hello" \
  --blobless \
  --hydrate background

bin/tdc fs-git hydrate-git-workspace \
  --file-system-id "$FILE_SYSTEM_ID" \
  --target-path "$MOUNT_PATH/repos/hello"

git -C "$MOUNT_PATH/repos/hello" status --short
```

创建隔离的 linked worktree：

```bash
bin/tdc fs-git add-git-worktree \
  --file-system-id "$FILE_SYSTEM_ID" \
  --base-path "$MOUNT_PATH/repos/hello" \
  --worktree-path "$MOUNT_PATH/repos/hello-feature" \
  --branch-name demo-feature

git -C "$MOUNT_PATH/repos/hello-feature" status --short

bin/tdc fs-git remove-git-worktree \
  --file-system-id "$FILE_SYSTEM_ID" \
  --worktree-path "$MOUNT_PATH/repos/hello-feature" \
  --force
```

`tdc fs-git` 不替代 Git。它负责为 Filesystem mount 准备 clone、hydrate 和 worktree 工作流；普通提交和分支操作仍使用 `git`。

## 8. 使用 Journal 记录 Agent 工作流

```bash
export JOURNAL_ID="jrn-demo-${DEMO_ID}"

bin/tdc fs-journal create-journal \
  --file-system-id "$FILE_SYSTEM_ID" \
  --journal-id "$JOURNAL_ID" \
  --journal-kind agent \
  --title "tdc demo ${DEMO_ID}" \
  --actor agent:demo \
  --label demo=present

bin/tdc fs-journal append-journal-entries \
  --file-system-id "$FILE_SYSTEM_ID" \
  --journal-id "$JOURNAL_ID" \
  --entry-json '{"type":"demo.started"}' \
  --entry-json '{"type":"demo.completed"}'

bin/tdc fs-journal read-journal-entries \
  --file-system-id "$FILE_SYSTEM_ID" \
  --journal-id "$JOURNAL_ID" \
  --output text

bin/tdc fs-journal search-journal-entries \
  --file-system-id "$FILE_SYSTEM_ID" \
  --entry-type demo.completed \
  --include-entries

bin/tdc fs-journal verify-journal \
  --file-system-id "$FILE_SYSTEM_ID" \
  --journal-id "$JOURNAL_ID" \
  --output text
```

Journal 是 append-only、可验证的 workflow ledger，不是普通文本日志文件。

## 9. 使用 Vault 管理和委派 Secret

```bash
printf 'demo-token\n' > /tmp/tdc-demo-token.txt

bin/tdc fs-vault create-secret \
  --file-system-id "$FILE_SYSTEM_ID" \
  --secret-name demo-service \
  --field ENDPOINT=https://example.invalid \
  --field API_TOKEN=@/tmp/tdc-demo-token.txt

bin/tdc fs-vault read-secret \
  --file-system-id "$FILE_SYSTEM_ID" \
  --secret-name demo-service \
  --field ENDPOINT \
  --format raw
```

向 agent 委派一个字段的临时只读访问：

```bash
export TDC_VAULT_TOKEN="$(bin/tdc fs-vault create-grant \
  --file-system-id "$FILE_SYSTEM_ID" \
  --agent-id demo-agent \
  --scope demo-service/ENDPOINT \
  --permission read \
  --ttl 10m \
  --token-only)"

bin/tdc fs-vault read-secret \
  --file-system-id "$FILE_SYSTEM_ID" \
  --secret-name demo-service \
  --field ENDPOINT \
  --format raw \
  --vault-token "$TDC_VAULT_TOKEN"

bin/tdc fs-vault list-audit-events \
  --file-system-id "$FILE_SYSTEM_ID" \
  --secret-name demo-service \
  --limit 20 \
  --output text
```

Vault mount 是只读 FUSE view，需要 delegated Vault token；Windows 不支持，macOS 需要 macFUSE。`run-with-secret` 可在不把值写进命令行的情况下将 secret 注入子进程。

## 10. 清理

FUSE mount 先 drain，再卸载。WebDAV mount 跳过 drain：

```bash
bin/tdc fs drain-file-system --mount-path "$MOUNT_PATH"
bin/tdc fs unmount-file-system --mount-path "$MOUNT_PATH" --ignore-absent
rm -rf "$MOUNT_PATH"
```

删除演示 secret 和临时文件：

```bash
bin/tdc fs-vault delete-secret \
  --file-system-id "$FILE_SYSTEM_ID" \
  --secret-name demo-service

rm -f /tmp/tdc-demo-token.txt
```

删除 Filesystem 资源及其本地 credential entry：

```bash
bin/tdc fs delete-file-system \
  --file-system-id "$FILE_SYSTEM_ID"
```

删除演示 cluster：

```bash
bin/tdc db delete-db-cluster \
  --db-cluster-id "$CLUSTER_ID" \
  --wait
```

## 讲解重点

- tdc 使用完整命令名、长 flags、JSON 默认输出、JMESPath `--query`、`--output text` 和 control-plane `--dry-run`，同时适合人和 agent。
- TiDB Cloud control-plane API key、SQL 用户凭证和 FS owner token 是三个不同的认证边界。
- 一个 profile 可以注册多个 Filesystem；data plane 和 mount 对同一个远端 namespace 双向可见。
- tdc 负责命令、配置、资源选择、输出和安装；随附的 `tdc-drive9` 负责 Filesystem runtime 正确性。
- `tdc fs-git`、`tdc fs-journal` 和 `tdc fs-vault` 是 Filesystem 的附属一级域，分别服务于 Git workspace、可验证 workflow ledger 和 secret delegation。
