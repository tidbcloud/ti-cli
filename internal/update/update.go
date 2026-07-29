package update

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/tidbcloud/tdc/internal/apperr"
	"github.com/tidbcloud/tdc/internal/version"
)

const (
	DefaultReleaseAPIBaseURL = "https://api.github.com/repos/tidbcloud/tdc"
	checksumAssetName        = "tdc_checksums.txt"
	drive9ReleaseBaseURL     = "https://drive9.ai/releases"
	drive9ChecksumAssetName  = "checksums.txt"
)

var errNoComparableVersion = errors.New("version is not a comparable semver")

type CheckOptions struct {
	ReleaseAPIBaseURL string
	HTTPClient        *http.Client
}

type ApplyOptions struct {
	Version           string
	DryRun            bool
	ReleaseAPIBaseURL string
	Drive9ReleaseURL  string
	HTTPClient        *http.Client
	ExecutablePath    string

	targetWritable func(string) (bool, error)
}

type CheckResult struct {
	CurrentVersion           string `json:"current_version"`
	LatestVersion            string `json:"latest_version"`
	UpdateAvailable          bool   `json:"update_available"`
	CurrentVersionComparable bool   `json:"current_version_comparable"`
	InstallSource            string `json:"install_source"`
	ReleaseChannel           string `json:"release_channel"`
	ArtifactName             string `json:"artifact_name"`
	DownloadURL              string `json:"download_url"`
	ReleaseURL               string `json:"release_url"`
	ReleaseNotesURL          string `json:"release_notes_url"`
	Drive9CompanionPath      string `json:"drive9_companion_path"`
	Drive9CompanionInstalled bool   `json:"drive9_companion_installed"`
}

func (r CheckResult) Human() string {
	status := "up to date"
	if r.UpdateAvailable {
		status = "update available"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Status: %s\n", status)
	fmt.Fprintf(&b, "Current version: %s\n", r.CurrentVersion)
	fmt.Fprintf(&b, "Latest version: %s\n", r.LatestVersion)
	fmt.Fprintf(&b, "Install source: %s\n", r.InstallSource)
	if r.DownloadURL != "" {
		fmt.Fprintf(&b, "Artifact: %s\n", r.ArtifactName)
	}
	if r.ReleaseURL != "" {
		fmt.Fprintf(&b, "Release: %s\n", r.ReleaseURL)
	}
	fmt.Fprintf(&b, "Drive9 companion: %s\n", companionStatusText(r.Drive9CompanionPath, r.Drive9CompanionInstalled))
	return b.String()
}

type ApplyResult struct {
	CurrentVersion   string `json:"current_version"`
	TargetVersion    string `json:"target_version"`
	Updated          bool   `json:"updated"`
	DryRun           bool   `json:"dry_run"`
	InstallSource    string `json:"install_source"`
	ReleaseChannel   string `json:"release_channel"`
	ArtifactName     string `json:"artifact_name"`
	DownloadURL      string `json:"download_url"`
	ChecksumSHA256   string `json:"checksum_sha256"`
	TargetPath       string `json:"target_path"`
	ReleaseURL       string `json:"release_url"`
	ReleaseNotesURL  string `json:"release_notes_url"`
	CompanionPath    string `json:"drive9_companion_path"`
	CompanionUpdated bool   `json:"drive9_companion_updated"`
}

func (r ApplyResult) Human() string {
	var b strings.Builder
	if r.DryRun {
		fmt.Fprintf(&b, "Dry run: update tdc from %s to %s\n", r.CurrentVersion, r.TargetVersion)
	} else if r.Updated {
		fmt.Fprintf(&b, "Updated tdc from %s to %s\n", r.CurrentVersion, r.TargetVersion)
	} else {
		fmt.Fprintf(&b, "tdc is already at %s\n", r.CurrentVersion)
	}
	fmt.Fprintf(&b, "Install source: %s\n", r.InstallSource)
	fmt.Fprintf(&b, "Target path: %s\n", r.TargetPath)
	if r.ArtifactName != "" {
		fmt.Fprintf(&b, "Artifact: %s\n", r.ArtifactName)
	}
	if r.CompanionPath != "" {
		fmt.Fprintf(&b, "Drive9 companion: %s\n", r.CompanionPath)
	}
	return b.String()
}

type release struct {
	TagName string         `json:"tag_name"`
	Name    string         `json:"name"`
	HTMLURL string         `json:"html_url"`
	Assets  []releaseAsset `json:"assets"`
}

type releaseAsset struct {
	Name               string `json:"name"`
	BrowserDownloadURL string `json:"browser_download_url"`
}

type client struct {
	baseURL    string
	httpClient *http.Client
}

func Check(ctx context.Context, info version.Info, opts CheckOptions) (CheckResult, error) {
	c := newClient(opts.ReleaseAPIBaseURL, opts.HTTPClient)
	rel, err := c.fetchRelease(ctx, "latest")
	if err != nil {
		return CheckResult{}, err
	}

	artifactName, err := artifactName(info.OS, info.Arch)
	if err != nil {
		return CheckResult{}, err
	}
	artifact, err := rel.asset(artifactName)
	if err != nil {
		return CheckResult{}, err
	}
	companionPath := companionPathForExecutable("")

	updateAvailable, comparable := updateAvailable(info.Version, rel.version())
	return CheckResult{
		CurrentVersion:           info.Version,
		LatestVersion:            rel.version(),
		UpdateAvailable:          updateAvailable,
		CurrentVersionComparable: comparable,
		InstallSource:            normalizedInstallSource(info.InstallSource),
		ReleaseChannel:           releaseChannel(info),
		ArtifactName:             artifact.Name,
		DownloadURL:              artifact.BrowserDownloadURL,
		ReleaseURL:               rel.HTMLURL,
		ReleaseNotesURL:          rel.HTMLURL,
		Drive9CompanionPath:      companionPath,
		Drive9CompanionInstalled: fileExists(companionPath),
	}, nil
}

func Apply(ctx context.Context, info version.Info, opts ApplyOptions) (ApplyResult, error) {
	targetPath, err := executablePath(opts.ExecutablePath)
	if err != nil {
		return ApplyResult{}, err
	}
	source := detectInstallSource(info.InstallSource, targetPath)
	if source.Managed {
		return ApplyResult{}, managedInstallError(source.Name)
	}
	if !source.Owned {
		return ApplyResult{}, apperr.New(
			"update.unknown_install",
			"runtime",
			1,
			fmt.Sprintf("tdc install source %q is not owned by tdc; reinstall with scripts/install.sh or scripts/install.ps1 before using tdc update", source.Name),
		)
	}

	if runtime.GOOS == "windows" && !opts.DryRun {
		return ApplyResult{}, apperr.New(
			"update.unsupported_platform",
			"runtime",
			1,
			"tdc update cannot safely replace the running Windows executable yet; rerun the PowerShell installer for the target version",
		)
	}

	c := newClient(opts.ReleaseAPIBaseURL, opts.HTTPClient)
	targetVersion := strings.TrimSpace(opts.Version)
	if targetVersion == "" {
		targetVersion = "latest"
	}
	rel, err := c.fetchRelease(ctx, targetVersion)
	if err != nil {
		return ApplyResult{}, err
	}

	if versionsEqual(info.Version, rel.version()) {
		return ApplyResult{}, apperr.New(
			"update.no_update_available",
			"runtime",
			1,
			fmt.Sprintf("The latest version of tdc (%s) is already installed", info.Version),
		)
	}

	artifactName, err := artifactName(info.OS, info.Arch)
	if err != nil {
		return ApplyResult{}, err
	}
	artifact, err := rel.asset(artifactName)
	if err != nil {
		return ApplyResult{}, err
	}
	checksums, err := rel.asset(checksumAssetName)
	if err != nil {
		return ApplyResult{}, err
	}
	checksumBytes, err := c.download(ctx, checksums.BrowserDownloadURL)
	if err != nil {
		return ApplyResult{}, err
	}
	expectedSHA, err := checksumFor(checksumBytes, artifact.Name)
	if err != nil {
		return ApplyResult{}, err
	}

	result := ApplyResult{
		CurrentVersion:  info.Version,
		TargetVersion:   rel.version(),
		DryRun:          opts.DryRun,
		InstallSource:   source.Name,
		ReleaseChannel:  releaseChannel(info),
		ArtifactName:    artifact.Name,
		DownloadURL:     artifact.BrowserDownloadURL,
		ChecksumSHA256:  expectedSHA,
		TargetPath:      targetPath,
		ReleaseURL:      rel.HTMLURL,
		ReleaseNotesURL: rel.HTMLURL,
		CompanionPath:   companionPathForExecutable(targetPath),
	}
	if opts.DryRun {
		return result, nil
	}

	writableCheck := opts.targetWritable
	if writableCheck == nil {
		writableCheck = isTargetWritable
	}
	writable, err := writableCheck(targetPath)
	if err != nil {
		return ApplyResult{}, err
	}
	if !writable {
		return ApplyResult{}, apperr.New(
			"update.permission_denied",
			"runtime",
			1,
			fmt.Sprintf("tdc cannot update the protected installation at %s; rerun the installer to migrate to ~/.tdc/bin", targetPath),
		)
	}

	archiveBytes, err := c.download(ctx, artifact.BrowserDownloadURL)
	if err != nil {
		return ApplyResult{}, err
	}
	if err := verifyChecksum(archiveBytes, expectedSHA, artifact.Name); err != nil {
		return ApplyResult{}, err
	}
	extracted, cleanup, err := extractBinary(archiveBytes, artifact.Name)
	if err != nil {
		return ApplyResult{}, err
	}
	defer cleanup()

	companion, companionCleanup, err := c.downloadDrive9Companion(ctx, opts.Drive9ReleaseURL)
	if err != nil {
		return ApplyResult{}, err
	}
	defer companionCleanup()

	stagedTDC, err := stageBinary(extracted, targetPath)
	if err != nil {
		return ApplyResult{}, err
	}
	defer os.Remove(stagedTDC)
	companionTarget := companionPathForExecutable(targetPath)
	stagedCompanion, err := stageBinary(companion.Path, companionTarget)
	if err != nil {
		return ApplyResult{}, err
	}
	defer os.Remove(stagedCompanion)
	if err := replaceBinary(ctx, stagedTDC, targetPath); err != nil {
		return ApplyResult{}, err
	}
	if err := replaceFile(stagedCompanion, companionTarget); err != nil {
		return ApplyResult{}, err
	}
	result.Updated = true
	result.CompanionUpdated = true
	return result, nil
}

func newClient(baseURL string, httpClient *http.Client) client {
	if strings.TrimSpace(baseURL) == "" {
		baseURL = strings.TrimRight(os.Getenv("TDC_RELEASE_API_BASE_URL"), "/")
	}
	if strings.TrimSpace(baseURL) == "" {
		baseURL = DefaultReleaseAPIBaseURL
	}
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	return client{
		baseURL:    strings.TrimRight(baseURL, "/"),
		httpClient: httpClient,
	}
}

func (c client) fetchRelease(ctx context.Context, target string) (release, error) {
	path := "/releases/latest"
	if target != "" && target != "latest" {
		path = "/releases/tags/" + normalizeTag(target)
	}
	var rel release
	if err := c.getJSON(ctx, c.baseURL+path, &rel); err != nil {
		return release{}, err
	}
	if rel.TagName == "" {
		return release{}, apperr.New("update.invalid_release", "runtime", 1, "release metadata did not include tag_name")
	}
	return rel, nil
}

func (c client) getJSON(ctx context.Context, url string, dst any) error {
	body, err := c.download(ctx, url)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(body, dst); err != nil {
		return apperr.Wrap("update.invalid_release", "runtime", 1, "decode release metadata", err)
	}
	return nil
}

func (c client) download(ctx context.Context, url string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, apperr.Wrap("update.invalid_url", "runtime", 1, "build release request", err)
	}
	req.Header.Set("User-Agent", "tdc")
	req.Header.Set("Accept", "application/json, application/octet-stream")
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, apperr.Wrap("update.network_error", "runtime", 1, "download release metadata or artifact", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		message := strings.TrimSpace(string(body))
		if message == "" {
			message = resp.Status
		}
		code := "update.network_error"
		if resp.StatusCode == http.StatusNotFound {
			code = "update.release_not_found"
		}
		return nil, apperr.New(
			code,
			"runtime",
			1,
			fmt.Sprintf("download release metadata or artifact: HTTP %d: %s", resp.StatusCode, message),
		)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, apperr.Wrap("update.network_error", "runtime", 1, "read release response", err)
	}
	return body, nil
}

func (r release) version() string {
	return strings.TrimPrefix(strings.TrimSpace(r.TagName), "v")
}

func (r release) asset(name string) (releaseAsset, error) {
	for _, asset := range r.Assets {
		if asset.Name == name {
			return asset, nil
		}
	}
	return releaseAsset{}, apperr.New(
		"update.artifact_not_found",
		"runtime",
		1,
		fmt.Sprintf("release %s does not include required asset %q", r.TagName, name),
	)
}

func artifactName(goos, goarch string) (string, error) {
	if goos == "" {
		goos = runtime.GOOS
	}
	if goarch == "" {
		goarch = runtime.GOARCH
	}
	switch goos {
	case "darwin", "linux":
		if goarch != "amd64" && goarch != "arm64" {
			return "", unsupportedTarget(goos, goarch)
		}
		return fmt.Sprintf("tdc_%s_%s.tar.gz", goos, goarch), nil
	case "windows":
		if goarch != "amd64" {
			return "", unsupportedTarget(goos, goarch)
		}
		return fmt.Sprintf("tdc_%s_%s.zip", goos, goarch), nil
	default:
		return "", unsupportedTarget(goos, goarch)
	}
}

func unsupportedTarget(goos, goarch string) error {
	return apperr.New(
		"update.unsupported_target",
		"runtime",
		1,
		fmt.Sprintf("no tdc release artifact is defined for %s/%s", goos, goarch),
	)
}

func checksumFor(checksums []byte, artifactName string) (string, error) {
	lines := strings.Split(string(checksums), "\n")
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) >= 2 && fields[1] == artifactName && isHexSHA256(fields[0]) {
			return strings.ToLower(fields[0]), nil
		}
		if strings.HasPrefix(line, "SHA256 ("+artifactName+") = ") {
			value := strings.TrimPrefix(line, "SHA256 ("+artifactName+") = ")
			if isHexSHA256(value) {
				return strings.ToLower(value), nil
			}
		}
	}
	return "", apperr.New(
		"update.checksum_missing",
		"runtime",
		1,
		fmt.Sprintf("checksums file does not include %s", artifactName),
	)
}

func verifyChecksum(data []byte, expected, artifactName string) error {
	sum := sha256.Sum256(data)
	actual := hex.EncodeToString(sum[:])
	if !strings.EqualFold(actual, expected) {
		return apperr.New(
			"update.checksum_mismatch",
			"runtime",
			1,
			fmt.Sprintf("downloaded %s failed checksum verification", artifactName),
		)
	}
	return nil
}

func isHexSHA256(value string) bool {
	if len(value) != 64 {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func extractBinary(archiveBytes []byte, artifactName string) (string, func(), error) {
	tempDir, err := os.MkdirTemp("", "tdc-update-*")
	if err != nil {
		return "", func() {}, apperr.Wrap("update.temp_dir", "runtime", 1, "create update temp directory", err)
	}
	cleanup := func() {
		_ = os.RemoveAll(tempDir)
	}
	targetName := "tdc"
	if strings.HasSuffix(artifactName, ".zip") {
		targetName = "tdc.exe"
	}
	targetPath := filepath.Join(tempDir, targetName)
	var extractErr error
	if strings.HasSuffix(artifactName, ".zip") {
		extractErr = extractZipBinary(archiveBytes, targetName, targetPath)
	} else {
		extractErr = extractTarGzBinary(archiveBytes, targetName, targetPath)
	}
	if extractErr != nil {
		cleanup()
		return "", func() {}, extractErr
	}
	if err := os.Chmod(targetPath, 0o755); err != nil {
		cleanup()
		return "", func() {}, apperr.Wrap("update.extract_artifact", "runtime", 1, "make downloaded tdc executable", err)
	}
	return targetPath, cleanup, nil
}

func extractTarGzBinary(archiveBytes []byte, targetName, targetPath string) error {
	gz, err := gzip.NewReader(bytes.NewReader(archiveBytes))
	if err != nil {
		return apperr.Wrap("update.extract_artifact", "runtime", 1, "open tar.gz artifact", err)
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	for {
		header, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return apperr.Wrap("update.extract_artifact", "runtime", 1, "read tar.gz artifact", err)
		}
		if filepath.Base(header.Name) != targetName {
			continue
		}
		return writeExtractedBinary(targetPath, tr)
	}
	return apperr.New("update.extract_artifact", "runtime", 1, "release archive did not contain tdc binary")
}

func extractZipBinary(archiveBytes []byte, targetName, targetPath string) error {
	reader, err := zip.NewReader(bytes.NewReader(archiveBytes), int64(len(archiveBytes)))
	if err != nil {
		return apperr.Wrap("update.extract_artifact", "runtime", 1, "open zip artifact", err)
	}
	for _, file := range reader.File {
		if filepath.Base(file.Name) != targetName {
			continue
		}
		rc, err := file.Open()
		if err != nil {
			return apperr.Wrap("update.extract_artifact", "runtime", 1, "open zipped tdc binary", err)
		}
		defer rc.Close()
		return writeExtractedBinary(targetPath, rc)
	}
	return apperr.New("update.extract_artifact", "runtime", 1, "release archive did not contain tdc binary")
}

func writeExtractedBinary(targetPath string, r io.Reader) error {
	file, err := os.OpenFile(targetPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o755)
	if err != nil {
		return apperr.Wrap("update.extract_artifact", "runtime", 1, "create extracted tdc binary", err)
	}
	defer file.Close()
	if _, err := io.Copy(file, r); err != nil {
		return apperr.Wrap("update.extract_artifact", "runtime", 1, "write extracted tdc binary", err)
	}
	return nil
}

func executablePath(override string) (string, error) {
	path := strings.TrimSpace(override)
	if path == "" {
		current, err := os.Executable()
		if err != nil {
			return "", apperr.Wrap("update.executable_path", "runtime", 1, "resolve current executable path", err)
		}
		path = current
	}
	evaluated, err := filepath.EvalSymlinks(path)
	if err == nil {
		path = evaluated
	}
	return path, nil
}

type downloadedBinary struct {
	Path   string
	SHA256 string
}

func isTargetWritable(path string) (bool, error) {
	info, err := os.Stat(path)
	if err != nil {
		return false, apperr.Wrap("update.permission_denied", "runtime", 1, "check current tdc binary", err)
	}
	if info.IsDir() {
		return false, apperr.New("update.permission_denied", "runtime", 1, "current tdc executable path is a directory")
	}
	dir := filepath.Dir(path)
	temp, err := os.CreateTemp(dir, ".tdc-update-write-test-*")
	if err != nil {
		if errors.Is(err, os.ErrPermission) {
			return false, nil
		}
		return false, apperr.Wrap(
			"update.permission_denied",
			"runtime",
			1,
			fmt.Sprintf("tdc cannot stage an update in %s", dir),
			err,
		)
	}
	name := temp.Name()
	_ = temp.Close()
	_ = os.Remove(name)
	return true, nil
}

func stageBinary(sourcePath, targetPath string) (string, error) {
	source, err := os.Open(sourcePath)
	if err != nil {
		return "", apperr.Wrap("update.permission_denied", "runtime", 1, "open update source", err)
	}
	defer source.Close()
	temp, err := os.CreateTemp(filepath.Dir(targetPath), "."+filepath.Base(targetPath)+".update-*")
	if err != nil {
		return "", apperr.Wrap("update.permission_denied", "runtime", 1, "stage update binary", err)
	}
	stagedPath := temp.Name()
	complete := false
	defer func() {
		_ = temp.Close()
		if !complete {
			_ = os.Remove(stagedPath)
		}
	}()
	if err := temp.Chmod(0o755); err != nil {
		return "", apperr.Wrap("update.permission_denied", "runtime", 1, "set staged update permissions", err)
	}
	if _, err := io.Copy(temp, source); err != nil {
		return "", apperr.Wrap("update.permission_denied", "runtime", 1, "copy staged update binary", err)
	}
	if err := temp.Sync(); err != nil {
		return "", apperr.Wrap("update.permission_denied", "runtime", 1, "sync staged update binary", err)
	}
	if err := temp.Close(); err != nil {
		return "", apperr.Wrap("update.permission_denied", "runtime", 1, "close staged update binary", err)
	}
	complete = true
	return stagedPath, nil
}

func replaceBinary(ctx context.Context, newBinary, targetPath string) error {
	backupPath := targetPath + ".tdc-backup"
	_ = os.Remove(backupPath)
	if err := os.Rename(targetPath, backupPath); err != nil {
		return apperr.Wrap("update.permission_denied", "runtime", 1, "move current tdc binary aside", err)
	}
	installed := false
	defer func() {
		if installed {
			return
		}
		_ = os.Remove(targetPath)
		_ = os.Rename(backupPath, targetPath)
	}()

	if err := os.Rename(newBinary, targetPath); err != nil {
		return apperr.Wrap("update.permission_denied", "runtime", 1, "install new tdc binary", err)
	}
	if err := validateInstalledBinary(ctx, targetPath); err != nil {
		return err
	}
	installed = true
	_ = os.Remove(backupPath)
	return nil
}

func validateInstalledBinary(ctx context.Context, targetPath string) error {
	validateCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(validateCtx, targetPath, "--version")
	cmd.Env = append(os.Environ(), "TDC_LOGGING=off")
	if output, err := cmd.CombinedOutput(); err != nil {
		return apperr.Wrap(
			"update.validation_failed",
			"runtime",
			1,
			fmt.Sprintf("new tdc binary failed --version validation: %s", strings.TrimSpace(string(output))),
			err,
		)
	}
	return nil
}

func (c client) downloadDrive9Companion(ctx context.Context, baseURL string) (downloadedBinary, func(), error) {
	artifactName, err := drive9ArtifactName(runtime.GOOS, runtime.GOARCH)
	if err != nil {
		return downloadedBinary{}, func() {}, err
	}
	baseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if baseURL == "" {
		baseURL = drive9ReleaseBaseURL
	}
	checksumBytes, err := c.download(ctx, baseURL+"/"+drive9ChecksumAssetName)
	if err != nil {
		return downloadedBinary{}, func() {}, err
	}
	expectedSHA, err := checksumFor(checksumBytes, artifactName)
	if err != nil {
		return downloadedBinary{}, func() {}, err
	}
	binaryBytes, err := c.download(ctx, baseURL+"/"+artifactName)
	if err != nil {
		return downloadedBinary{}, func() {}, err
	}
	if err := verifyChecksum(binaryBytes, expectedSHA, artifactName); err != nil {
		return downloadedBinary{}, func() {}, err
	}
	tempDir, err := os.MkdirTemp("", "tdc-drive9-update-*")
	if err != nil {
		return downloadedBinary{}, func() {}, apperr.Wrap("update.temp_dir", "runtime", 1, "create tdc-drive9 update temp directory", err)
	}
	cleanup := func() {
		_ = os.RemoveAll(tempDir)
	}
	tempPath := filepath.Join(tempDir, companionBinaryName())
	if err := os.WriteFile(tempPath, binaryBytes, 0o755); err != nil {
		cleanup()
		return downloadedBinary{}, func() {}, apperr.Wrap("update.extract_artifact", "runtime", 1, "write temporary tdc-drive9 binary", err)
	}
	if err := os.Chmod(tempPath, 0o755); err != nil {
		cleanup()
		return downloadedBinary{}, func() {}, apperr.Wrap("update.extract_artifact", "runtime", 1, "make tdc-drive9 executable", err)
	}
	return downloadedBinary{Path: tempPath, SHA256: expectedSHA}, cleanup, nil
}

func drive9ArtifactName(goos, goarch string) (string, error) {
	if goos == "" {
		goos = runtime.GOOS
	}
	if goarch == "" {
		goarch = runtime.GOARCH
	}
	switch goos {
	case "darwin", "linux":
		if goarch != "amd64" && goarch != "arm64" {
			return "", unsupportedTarget(goos, goarch)
		}
		return fmt.Sprintf("drive9-%s-%s", goos, goarch), nil
	case "windows":
		if goarch != "amd64" && goarch != "arm64" {
			return "", unsupportedTarget(goos, goarch)
		}
		return fmt.Sprintf("drive9-%s-%s.exe", goos, goarch), nil
	default:
		return "", unsupportedTarget(goos, goarch)
	}
}

func companionPathForExecutable(tdcPath string) string {
	path := strings.TrimSpace(tdcPath)
	if path == "" {
		current, err := os.Executable()
		if err == nil {
			path = current
		}
	}
	if path == "" {
		return companionBinaryName()
	}
	return filepath.Join(filepath.Dir(path), companionBinaryName())
}

func companionBinaryName() string {
	if runtime.GOOS == "windows" {
		return "tdc-drive9.exe"
	}
	return "tdc-drive9"
}

func companionStatusText(path string, installed bool) string {
	if installed {
		return path
	}
	return "missing at " + path
}

func fileExists(path string) bool {
	if strings.TrimSpace(path) == "" {
		return false
	}
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

func replaceFile(sourcePath, targetPath string) error {
	backupPath := targetPath + ".tdc-backup"
	_ = os.Remove(backupPath)
	hadTarget := false
	if _, err := os.Stat(targetPath); err == nil {
		hadTarget = true
		if err := os.Rename(targetPath, backupPath); err != nil {
			return apperr.Wrap("update.permission_denied", "runtime", 1, "move current tdc-drive9 binary aside", err)
		}
	}
	installed := false
	defer func() {
		if installed || !hadTarget {
			return
		}
		_ = os.Remove(targetPath)
		_ = os.Rename(backupPath, targetPath)
	}()
	if err := os.Rename(sourcePath, targetPath); err != nil {
		return apperr.Wrap("update.permission_denied", "runtime", 1, "install tdc-drive9 binary", err)
	}
	installed = true
	if hadTarget {
		_ = os.Remove(backupPath)
	}
	return nil
}

type installSource struct {
	Name    string
	Owned   bool
	Managed bool
}

func detectInstallSource(source, path string) installSource {
	name := normalizedInstallSource(source)
	lowerPath := strings.ToLower(filepath.ToSlash(path))
	switch {
	case strings.Contains(lowerPath, "/homebrew/cellar/tdc/"),
		strings.Contains(lowerPath, "/cellar/tdc/"):
		name = "homebrew"
	case strings.Contains(lowerPath, "/scoop/apps/tdc/"):
		name = "scoop"
	}

	switch name {
	case "archive", "script":
		return installSource{Name: name, Owned: true}
	case "homebrew", "scoop", "winget":
		return installSource{Name: name, Managed: true}
	case "":
		return installSource{Name: "unknown"}
	default:
		return installSource{Name: name}
	}
}

func normalizedInstallSource(source string) string {
	source = strings.TrimSpace(strings.ToLower(source))
	if source == "" {
		return "unknown"
	}
	return source
}

func managedInstallError(source string) error {
	command := map[string]string{
		"homebrew": "brew upgrade tidbcloud/tap/tdc",
		"scoop":    "scoop update tdc",
		"winget":   "winget upgrade tidbcloud.tdc",
	}[source]
	if command == "" {
		command = "use the package manager that installed tdc"
	}
	return apperr.New(
		"update.managed_install",
		"runtime",
		1,
		fmt.Sprintf("tdc is managed by %s; update it with `%s`", source, command),
	)
}

func releaseChannel(info version.Info) string {
	channel := strings.TrimSpace(info.ReleaseChannel)
	if channel == "" {
		return "stable"
	}
	return channel
}

func normalizeTag(value string) string {
	value = strings.TrimSpace(value)
	if strings.HasPrefix(value, "v") {
		return value
	}
	return "v" + value
}

func updateAvailable(current, latest string) (bool, bool) {
	cmp, err := compareVersions(current, latest)
	if err != nil {
		return true, false
	}
	return cmp < 0, true
}

func versionsEqual(left, right string) bool {
	cmp, err := compareVersions(left, right)
	if err != nil {
		return strings.TrimPrefix(left, "v") == strings.TrimPrefix(right, "v")
	}
	return cmp == 0
}

func compareVersions(left, right string) (int, error) {
	lv, err := parseVersion(left)
	if err != nil {
		return 0, err
	}
	rv, err := parseVersion(right)
	if err != nil {
		return 0, err
	}
	for i := range lv {
		if lv[i] < rv[i] {
			return -1, nil
		}
		if lv[i] > rv[i] {
			return 1, nil
		}
	}
	return 0, nil
}

func parseVersion(value string) ([3]int, error) {
	var parsed [3]int
	value = strings.TrimSpace(strings.TrimPrefix(value, "v"))
	if value == "" || value == "dev" {
		return parsed, errNoComparableVersion
	}
	if i := strings.IndexAny(value, "-+"); i >= 0 {
		value = value[:i]
	}
	parts := strings.Split(value, ".")
	if len(parts) != 3 {
		return parsed, errNoComparableVersion
	}
	for i, part := range parts {
		n, err := strconv.Atoi(part)
		if err != nil {
			return parsed, errNoComparableVersion
		}
		parsed[i] = n
	}
	return parsed, nil
}
