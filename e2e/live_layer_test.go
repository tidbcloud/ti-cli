package e2e

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	apifs "github.com/tidbcloud/ti-cli/internal/api/fs"
	"github.com/tidbcloud/ti-cli/internal/fs/mountdriver"
)

func TestLiveFSLayerForkWorkflow(t *testing.T) {
	requireLive(t)
	if runtime.GOOS != "darwin" && runtime.GOOS != "linux" {
		t.Skip("filesystem layer mount live e2e requires macOS or Linux FUSE")
	}
	driver, err := mountdriver.Resolve("fuse")
	if err != nil {
		t.Fatalf("resolve FUSE driver: %v", err)
	}
	if err := driver.CheckPrerequisites(); err != nil {
		t.Skipf("filesystem layer non-mount operations are covered by TestLiveFSDataPlaneLifecycle; full layer workflow requires FUSE: %v", err)
	}

	bin := tiBinary(t)
	profileName := liveProfileName(t)
	selected := ensureLiveFSResource(t, bin, profileName)
	fileSystemID := selected.FSTenantID
	if fileSystemID == "" {
		t.Fatal("selected live filesystem has no file system ID")
	}
	suffix := fmt.Sprintf("%d-%d", os.Getpid(), time.Now().UnixNano())
	remoteRoot := "/ti-e2e-layer-fork-" + suffix
	rootLayerID := "ti-e2e-root-" + suffix
	rootCheckpointID := "ti-e2e-seed-" + suffix

	runFS := func(command string, args ...string) commandResult {
		cliArgs := []string{"--profile", profileName, "fs", command, "--file-system-id", fileSystemID}
		cliArgs = append(cliArgs, args...)
		return runTI(t, bin, cliArgs...)
	}
	runMountControl := func(command string, args ...string) commandResult {
		cliArgs := []string{"--profile", profileName, "fs", command}
		cliArgs = append(cliArgs, args...)
		return runTI(t, bin, cliArgs...)
	}

	mounted := map[string]bool{}
	rootCreated := false
	defer func() {
		for mountPath, active := range mounted {
			if !active {
				continue
			}
			cleanup := runMountControl("unmount-file-system", "--mount-path", mountPath, "--ignore-absent", "--force")
			if cleanup.exitCode != 0 {
				t.Logf("cleanup layer mount failed for %s: exit=%d stderr=%s", mountPath, cleanup.exitCode, strings.TrimSpace(cleanup.stderr))
			}
		}
		if rootCreated {
			cleanupLayer := runFS("delete-layer", "--layer-ref", rootLayerID, "--cascade")
			if cleanupLayer.exitCode != 0 && cleanupLayer.exitCode != 5 {
				t.Logf("cleanup root layer failed for %s: exit=%d stderr=%s", rootLayerID, cleanupLayer.exitCode, strings.TrimSpace(cleanupLayer.stderr))
				rollbackLayer := runFS("rollback-layer", "--layer-id", rootLayerID)
				if rollbackLayer.exitCode != 0 && rollbackLayer.exitCode != 5 {
					t.Logf("cleanup root layer rollback failed for %s: exit=%d stderr=%s", rootLayerID, rollbackLayer.exitCode, strings.TrimSpace(rollbackLayer.stderr))
				}
			}
		}
		cleanupRoot := runFS("delete-file", "--path", remoteRoot, "--recursive")
		if cleanupRoot.exitCode != 0 && cleanupRoot.exitCode != 5 && !isLiveFSNotFound(cleanupRoot.stderr) {
			t.Logf("cleanup layer workflow root failed for %s: exit=%d stderr=%s", remoteRoot, cleanupRoot.exitCode, strings.TrimSpace(cleanupRoot.stderr))
		}
	}()

	unmount := func(mountPath string) {
		t.Helper()
		result := runMountControl("unmount-file-system", "--mount-path", mountPath)
		result.wantExitCode(0)
		result.wantStdoutContains(`"status": "unmounted"`)
		mounted[mountPath] = false
	}
	drain := func(mountPath string) {
		t.Helper()
		result := runMountControl("drain-file-system", "--mount-path", mountPath, "--timeout", "60s")
		result.wantExitCode(0)
		result.wantStdoutContains(`"status": "drained"`)
	}
	mountLayer := func(layerRef, checkpointID, mountPath string) {
		t.Helper()
		args := []string{
			"--mount-path", mountPath,
			"--remote-path", remoteRoot,
			"--driver", "fuse",
			"--layer-ref", layerRef,
			"--ready-timeout", "60s",
		}
		if checkpointID != "" {
			args = append(args, "--checkpoint-id", checkpointID)
		}
		result := runFS("mount-file-system", args...)
		result.wantExitCode(0)
		result.wantStdoutContains(`"status": "mounted"`)
		result.wantStdoutContains(`"layer_ref": "` + layerRef + `"`)
		if checkpointID != "" {
			result.wantStdoutContains(`"checkpoint_id": "` + checkpointID + `"`)
			result.wantStdoutContains(`"read_only": true`)
		}
		mounted[mountPath] = true
	}
	checkpoint := func(layerID, checkpointID, label string) {
		t.Helper()
		result := runFS(
			"create-layer-checkpoint",
			"--layer-id", layerID,
			"--checkpoint-id", checkpointID,
			"--label", label,
		)
		result.wantExitCode(0)
		result.wantStdoutContains(checkpointID)
	}
	fork := func(parentID, childID, checkpointID, name string) apifs.FSLayer {
		t.Helper()
		args := []string{
			"--parent-layer-ref", parentID,
			"--layer-id", childID,
			"--layer-name", name,
			"--actor-id", "ti-live-e2e",
		}
		if checkpointID != "" {
			args = append(args, "--checkpoint-id", checkpointID)
		}
		result := runFS("fork-layer", args...)
		result.wantExitCode(0)
		var layer apifs.FSLayer
		if err := json.Unmarshal([]byte(result.stdout), &layer); err != nil {
			t.Fatalf("decode forked layer %s: %v\n%s", childID, err, result.stdout)
		}
		if layer.LayerID != childID || layer.ParentLayerID != parentID {
			t.Fatalf("forked layer identity mismatch: %+v", layer)
		}
		if checkpointID != "" && layer.OriginCheckpointID != checkpointID {
			t.Fatalf("forked layer checkpoint = %q, want %q: %+v", layer.OriginCheckpointID, checkpointID, layer)
		}
		return layer
	}
	assertBaseAbsent := func(remotePath string) {
		t.Helper()
		result := runFS("read-file", "--path", remotePath)
		if result.exitCode == 0 {
			result.fail("base filesystem unexpectedly contains %s", remotePath)
		}
		if !isLiveFSNotFound(result.stderr) {
			result.fail("base filesystem absence check for %s failed for an unexpected reason", remotePath)
		}
	}
	waitBaseRead := func(remotePath, want string) {
		t.Helper()
		deadline := time.Now().Add(60 * time.Second)
		var last commandResult
		for {
			last = runFS("read-file", "--path", remotePath)
			if last.exitCode == 0 && last.stdout == want {
				return
			}
			if time.Now().After(deadline) {
				last.fail("timed out waiting for committed base file %s", remotePath)
			}
			time.Sleep(time.Second)
		}
	}

	createRoot := runFS("create-directory", "--path", remoteRoot, "--mode", "0755")
	createRoot.wantExitCode(0)
	createLayer := runFS(
		"create-layer",
		"--layer-id", rootLayerID,
		"--base-root-path", remoteRoot,
		"--layer-name", "research-base-"+suffix,
		"--actor-id", "ti-live-e2e-seed",
		"--tag", "test=ti-e2e",
		"--tag", "run="+suffix,
	)
	createLayer.wantExitCode(0)
	rootCreated = true

	seedSource := filepath.Join(t.TempDir(), "seed")
	if err := os.MkdirAll(filepath.Join(seedSource, "reports"), 0o755); err != nil {
		t.Fatalf("create local seed reports: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(seedSource, "data"), 0o755); err != nil {
		t.Fatalf("create local seed data: %v", err)
	}
	seedReport := "seed report " + suffix + "\n"
	seedData := "seed data " + suffix + "\n"
	if err := os.WriteFile(filepath.Join(seedSource, "reports", "report.md"), []byte(seedReport), 0o644); err != nil {
		t.Fatalf("write local seed report: %v", err)
	}
	if err := os.WriteFile(filepath.Join(seedSource, "data", "input.txt"), []byte(seedData), 0o644); err != nil {
		t.Fatalf("write local seed data: %v", err)
	}
	seedMount := filepath.Join(t.TempDir(), "seed-mount")
	if err := os.MkdirAll(seedMount, 0o755); err != nil {
		t.Fatalf("create seed mount path: %v", err)
	}
	mountLayer(rootLayerID, "", seedMount)
	copyLiveDirectoryTree(t, seedSource, seedMount)
	drain(seedMount)
	unmount(seedMount)
	assertBaseAbsent(remoteRoot + "/reports/report.md")
	assertBaseAbsent(remoteRoot + "/data/input.txt")
	seedFind := runFS("find-files", "--path", remoteRoot, "--file-name-pattern", "report.md", "--layer-id", rootLayerID)
	seedFind.wantExitCode(0)
	seedFind.wantStdoutContains(remoteRoot + "/reports/report.md")
	checkpoint(rootLayerID, rootCheckpointID, "workspace-seed")

	briefID := "ti-e2e-brief-" + suffix
	longformID := "ti-e2e-longform-" + suffix
	analystID := "ti-e2e-analyst-" + suffix
	fork(rootLayerID, briefID, rootCheckpointID, "style-brief-"+suffix)
	fork(rootLayerID, longformID, rootCheckpointID, "style-longform-"+suffix)
	fork(rootLayerID, analystID, rootCheckpointID, "style-analyst-"+suffix)

	for _, childID := range []string{briefID, longformID, analystID} {
		chainResult := runFS("list-layer-chain", "--layer-ref", childID)
		chainResult.wantExitCode(0)
		var chain struct {
			Chain []apifs.FSLayerChainFrame `json:"chain"`
		}
		if err := json.Unmarshal([]byte(chainResult.stdout), &chain); err != nil {
			t.Fatalf("decode layer chain for %s: %v\n%s", childID, err, chainResult.stdout)
		}
		if len(chain.Chain) != 2 || chain.Chain[0].LayerID != rootLayerID || chain.Chain[1].LayerID != childID || chain.Chain[1].OriginCheckpointID != rootCheckpointID {
			t.Fatalf("unexpected pinned chain for %s: %+v", childID, chain.Chain)
		}
	}

	parentLatePath := remoteRoot + "/parent-late.txt"
	parentLateFile := filepath.Join(t.TempDir(), "parent-late.txt")
	if err := os.WriteFile(parentLateFile, []byte("late parent write\n"), 0o644); err != nil {
		t.Fatalf("write parent-late source: %v", err)
	}
	parentLate := runFS("copy-file", "--from-local", parentLateFile, "--to-remote", parentLatePath, "--layer-id", rootLayerID)
	parentLate.wantExitCode(0)
	for _, childID := range []string{briefID, longformID, analystID} {
		findLate := runFS("find-files", "--path", remoteRoot, "--file-name-pattern", "parent-late.txt", "--layer-id", childID)
		findLate.wantExitCode(0)
		findLate.wantStdoutNotContains(parentLatePath)
	}

	briefMount := filepath.Join(t.TempDir(), "brief")
	longformMount := filepath.Join(t.TempDir(), "longform")
	analystMount := filepath.Join(t.TempDir(), "analyst")
	for _, mountPath := range []string{briefMount, longformMount, analystMount} {
		if err := os.MkdirAll(mountPath, 0o755); err != nil {
			t.Fatalf("create child mount path: %v", err)
		}
	}
	mountLayer(briefID, "", briefMount)
	mountLayer(longformID, "", longformMount)
	mountLayer(analystID, "", analystMount)
	for _, mountPath := range []string{briefMount, longformMount, analystMount} {
		waitLiveLocalFile(t, filepath.Join(mountPath, "reports", "report.md"), seedReport, 30*time.Second)
	}
	briefContent := "brief report " + suffix + "\n"
	longformContent := "longform report " + suffix + "\n"
	analystV1 := "analyst v1 TAM " + suffix + "\n"
	if err := os.WriteFile(filepath.Join(briefMount, "reports", "brief.md"), []byte(briefContent), 0o644); err != nil {
		t.Fatalf("write brief report: %v", err)
	}
	if err := os.WriteFile(filepath.Join(longformMount, "reports", "longform.md"), []byte(longformContent), 0o644); err != nil {
		t.Fatalf("write longform report: %v", err)
	}
	if err := os.WriteFile(filepath.Join(analystMount, "reports", "report.md"), []byte(analystV1), 0o644); err != nil {
		t.Fatalf("write analyst report: %v", err)
	}
	for _, mountPath := range []string{briefMount, longformMount, analystMount} {
		drain(mountPath)
	}
	if data, err := os.ReadFile(filepath.Join(analystMount, "reports", "report.md")); err != nil || string(data) != analystV1 {
		t.Fatalf("analyst mount report mismatch: %q, %v", data, err)
	}
	if _, err := os.Stat(filepath.Join(analystMount, "reports", "brief.md")); !os.IsNotExist(err) {
		t.Fatalf("brief child write leaked into analyst child: %v", err)
	}
	waitLiveFSResult(t, bin, []string{"--profile", profileName, "fs", "search-file-content", "--file-system-id", fileSystemID, "--path", remoteRoot, "--pattern", "TAM", "--layer-id", analystID}, "report.md", 2*time.Minute, "search analyst layer")
	analystDiff := runFS("diff-layer", "--layer-id", analystID)
	analystDiff.wantExitCode(0)
	analystDiff.wantStdoutContains(remoteRoot + "/reports/report.md")

	unmount(briefMount)
	unmount(longformMount)
	for _, childID := range []string{briefID, longformID} {
		deleted := runFS("delete-layer", "--layer-ref", childID)
		deleted.wantExitCode(0)
		described := runFS("describe-layer", "--layer-id", childID)
		described.wantExitCode(0)
		described.wantStdoutContains(`"state": "abandoned"`)
	}
	listAbandoned := runFS("list-layers")
	listAbandoned.wantExitCode(0)
	listAbandoned.wantStdoutContains(briefID)
	listAbandoned.wantStdoutContains(longformID)

	cpV1 := "ti-e2e-v1-" + suffix
	cpV5 := "ti-e2e-v5-" + suffix
	cpV7 := "ti-e2e-v7-" + suffix
	drain(analystMount)
	checkpoint(analystID, cpV1, "first-draft")
	analystV5 := "analyst v5 narrative " + suffix + "\n"
	if err := os.WriteFile(filepath.Join(analystMount, "reports", "report.md"), []byte(analystV5), 0o644); err != nil {
		t.Fatalf("write analyst v5: %v", err)
	}
	drain(analystMount)
	checkpoint(analystID, cpV5, "narrative-ok")
	analystV7 := "analyst v7 latest " + suffix + "\n"
	if err := os.WriteFile(filepath.Join(analystMount, "reports", "report.md"), []byte(analystV7), 0o644); err != nil {
		t.Fatalf("write analyst v7: %v", err)
	}
	drain(analystMount)
	checkpoint(analystID, cpV7, "latest-tip")

	v5Mount := filepath.Join(t.TempDir(), "v5")
	if err := os.MkdirAll(v5Mount, 0o755); err != nil {
		t.Fatalf("create v5 mount path: %v", err)
	}
	mountLayer(analystID, cpV5, v5Mount)
	waitLiveLocalFile(t, filepath.Join(v5Mount, "reports", "report.md"), analystV5, 30*time.Second)
	waitLiveLocalFile(t, filepath.Join(analystMount, "reports", "report.md"), analystV7, 30*time.Second)
	if err := os.WriteFile(filepath.Join(v5Mount, "reports", "report.md"), []byte("must fail\n"), 0o644); err == nil {
		t.Fatal("historical checkpoint mount accepted a write")
	}

	fromV5ID := "ti-e2e-from-v5-" + suffix
	fork(analystID, fromV5ID, cpV5, "from-v5-"+suffix)
	fromV5Chain := runFS("list-layer-chain", "--layer-ref", fromV5ID)
	fromV5Chain.wantExitCode(0)
	fromV5Chain.wantStdoutContains(`"origin_checkpoint_id": "` + cpV5 + `"`)
	fromV5Mount := filepath.Join(t.TempDir(), "from-v5")
	if err := os.MkdirAll(fromV5Mount, 0o755); err != nil {
		t.Fatalf("create from-v5 mount path: %v", err)
	}
	mountLayer(fromV5ID, "", fromV5Mount)
	waitLiveLocalFile(t, filepath.Join(fromV5Mount, "reports", "report.md"), analystV5, 30*time.Second)
	selectedContent := "selected rewrite from v5 " + suffix + "\n"
	if err := os.WriteFile(filepath.Join(fromV5Mount, "reports", "report.md"), []byte(selectedContent), 0o644); err != nil {
		t.Fatalf("write selected from-v5 report: %v", err)
	}
	drain(fromV5Mount)
	checkpoint(fromV5ID, "ti-e2e-v5b1-"+suffix, "rewrite-from-v5")

	deleteParent := runFS("delete-layer", "--layer-ref", analystID)
	if deleteParent.exitCode == 0 {
		deleteParent.fail("deleting a parent with live descendant %s unexpectedly succeeded", fromV5ID)
	}
	if !strings.Contains(strings.ToLower(deleteParent.stderr), "descendant") {
		deleteParent.fail("deleting a parent with live descendant %s failed for an unrelated reason", fromV5ID)
	}

	cascadeParentID := "ti-e2e-cascade-parent-" + suffix
	cascadeChildID := "ti-e2e-cascade-child-" + suffix
	fork(rootLayerID, cascadeParentID, rootCheckpointID, "cascade-parent-"+suffix)
	fork(cascadeParentID, cascadeChildID, "", "cascade-child-"+suffix)
	cascadeDelete := runFS("delete-layer", "--layer-ref", cascadeParentID, "--cascade")
	cascadeDelete.wantExitCode(0)
	for _, layerID := range []string{cascadeParentID, cascadeChildID} {
		described := runFS("describe-layer", "--layer-id", layerID)
		described.wantExitCode(0)
		described.wantStdoutContains(`"state": "abandoned"`)
	}

	unmount(v5Mount)
	unmount(analystMount)
	rollbackParent := runFS("rollback-layer", "--layer-id", analystID)
	rollbackParent.wantExitCode(0)
	waitLiveLocalFile(t, filepath.Join(fromV5Mount, "reports", "report.md"), selectedContent, 30*time.Second)
	continuedContent := "continued after parent rollback " + suffix + "\n"
	if err := os.WriteFile(filepath.Join(fromV5Mount, "reports", "continued.md"), []byte(continuedContent), 0o644); err != nil {
		t.Fatalf("write child after parent rollback: %v", err)
	}
	drain(fromV5Mount)
	waitLiveLocalFile(t, filepath.Join(fromV5Mount, "reports", "continued.md"), continuedContent, 30*time.Second)
	unmount(fromV5Mount)

	commitSelected := runFS("commit-layer", "--layer-id", fromV5ID)
	commitSelected.wantExitCode(0)
	waitBaseRead(remoteRoot+"/reports/report.md", selectedContent)
	waitBaseRead(remoteRoot+"/reports/continued.md", continuedContent)
	waitBaseRead(remoteRoot+"/data/input.txt", seedData)
	assertBaseAbsent(remoteRoot + "/reports/brief.md")
	assertBaseAbsent(remoteRoot + "/reports/longform.md")
	assertBaseAbsent(parentLatePath)
}

func copyLiveDirectoryTree(t *testing.T, sourceRoot, destinationRoot string) {
	t.Helper()
	if err := filepath.Walk(sourceRoot, func(path string, info os.FileInfo, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		relative, err := filepath.Rel(sourceRoot, path)
		if err != nil {
			return err
		}
		if relative == "." {
			return nil
		}
		destination := filepath.Join(destinationRoot, relative)
		if info.IsDir() {
			return os.MkdirAll(destination, info.Mode().Perm())
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("unsupported seed entry %s (%s)", path, info.Mode())
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		return os.WriteFile(destination, data, info.Mode().Perm())
	}); err != nil {
		t.Fatalf("copy seed tree through layer mount: %v", err)
	}
}
