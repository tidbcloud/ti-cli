package fs

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/tidbcloud/ti-cli/internal/api"
	apifs "github.com/tidbcloud/ti-cli/internal/api/fs"
	"github.com/tidbcloud/ti-cli/internal/apperr"
	"github.com/tidbcloud/ti-cli/internal/authz"
	"github.com/tidbcloud/ti-cli/internal/config"
	"github.com/tidbcloud/ti-cli/internal/dryrun"
	"github.com/tidbcloud/ti-cli/internal/fs/mountdriver"
	"github.com/tidbcloud/ti-cli/internal/fs/mountstate"
)

const (
	DefaultVaultAuditLimit = 100
	MaxVaultAuditLimit     = 1000
)

type VaultCreateSecretOptions struct {
	Profile    *config.Profile
	SecretName string
	Fields     []string
	Stdin      io.Reader
}

type VaultReplaceSecretOptions struct {
	Profile       *config.Profile
	SecretPath    string
	FromDirectory string
}

type VaultReadSecretOptions struct {
	Profile    *config.Profile
	SecretName string
	Field      string
	Format     string
	VaultToken string
}

type VaultListSecretsOptions struct {
	Profile    *config.Profile
	VaultToken string
}

type VaultDeleteSecretOptions struct {
	Profile    *config.Profile
	SecretName string
}

type VaultCreateGrantOptions struct {
	Profile    *config.Profile
	AgentID    string
	Scopes     []string
	Permission string
	TTL        time.Duration
	LabelHint  string
	TokenOnly  bool
}

type VaultDeleteGrantOptions struct {
	Profile   *config.Profile
	GrantID   string
	RevokedBy string
	Reason    string
}

type VaultAuditOptions struct {
	Profile    *config.Profile
	SecretName string
	AgentID    string
	Since      time.Duration
	Limit      int
}

type VaultRunWithSecretOptions struct {
	Profile    *config.Profile
	SecretPath string
	VaultToken string
	Command    []string
	Stdin      io.Reader
	Stdout     io.Writer
	Stderr     io.Writer
	Env        []string
}

type VaultMountOptions struct {
	Profile      *config.Profile
	MountPath    string
	VaultToken   string
	Foreground   bool
	ReadyTimeout time.Duration
}

type VaultSecretResult struct {
	Secret apifs.VaultSecret `json:"secret"`
	Status string            `json:"status"`
}

type VaultReadSecretResult struct {
	SecretName string            `json:"secret_name"`
	Field      string            `json:"field,omitempty"`
	Value      string            `json:"value,omitempty"`
	Fields     map[string]string `json:"fields,omitempty"`
}

type VaultListSecretsResult struct {
	Secrets []string `json:"secrets"`
}

type VaultDeleteResult struct {
	Operation string `json:"operation"`
	ID        string `json:"id"`
	Status    string `json:"status"`
}

type VaultTokenResult struct {
	Token     string    `json:"token,omitempty"`
	TokenID   string    `json:"token_id,omitempty"`
	GrantID   string    `json:"grant_id,omitempty"`
	ExpiresAt time.Time `json:"expires_at,omitempty"`
	Scope     []string  `json:"scope,omitempty"`
	Perm      string    `json:"perm,omitempty"`
}

type VaultAuditResult struct {
	Events []apifs.VaultAuditEvent `json:"events"`
}

type vaultMountInputs struct {
	profile     *config.Profile
	mountPath   string
	endpoint    string
	client      *apifs.Client
	ownerMode   bool
	stateFile   string
	logFile     string
	homeDir     string
	timeout     time.Duration
	vaultToken  string
	readable    int
	mountDriver mountdriver.Driver
}

func (s Service) MountVault(ctx context.Context, opts VaultMountOptions) (MountResult, error) {
	return s.drive9MountVault(ctx, opts)
}

func (s Service) DryRunMountVault(ctx context.Context, commandPath string, opts VaultMountOptions) (dryrun.Result, error) {
	inputs, checks, err := s.vaultMountInputs(ctx, opts)
	if err != nil {
		return dryrun.Result{}, err
	}
	dryChecks := make([]dryrun.Check, 0, len(checks)+1)
	for _, check := range checks {
		dryChecks = append(dryChecks, dryrun.Check{Name: check.Name, Status: check.Status, Message: check.Message})
	}
	if err := inputs.mountDriver.CheckPrerequisites(); err != nil {
		dryChecks = append(dryChecks, dryrun.Check{Name: "mount_driver", Status: "failed", Message: err.Error()})
	} else {
		dryChecks = append(dryChecks, dryrun.Check{Name: "mount_driver", Status: "passed", Message: inputs.mountDriver.Name()})
	}
	return dryrun.New(
		commandPath,
		"mount_vault",
		dryrun.RequestSummary{
			Description: "normal execution starts a local read-only FUSE runtime exposing ti fs-vault secrets as /<secret>/<field>",
			Method:      "GET",
			Path:        "/v1/vault/secrets or /v1/vault/read",
			Body: map[string]any{
				"mount_path": inputs.mountPath,
				"readable":   inputs.readable,
			},
		},
		dryChecks...,
	), nil
}

func (s Service) mountVaultBackground(ctx context.Context, inputs vaultMountInputs, checks []MountRuntimeCheck) (MountResult, error) {
	executable, err := os.Executable()
	if err != nil {
		return MountResult{}, apperr.Wrap("vault.executable_path", "runtime", 1, "determine ti executable path for background vault mount", err)
	}
	if err := os.MkdirAll(filepath.Dir(inputs.logFile), 0o700); err != nil {
		return MountResult{}, apperr.Wrap("vault.mount_log_dir", "runtime", 1, fmt.Sprintf("create mount log directory %q", filepath.Dir(inputs.logFile)), err)
	}
	args := []string{
		"--profile", inputs.profile.Name,
		"fs-vault", "mount-vault",
		"--mount-path", inputs.mountPath,
		"--foreground",
	}
	env := []string(nil)
	if strings.TrimSpace(inputs.vaultToken) != "" {
		env = append(env, "TI_VAULT_TOKEN="+strings.TrimSpace(inputs.vaultToken))
	}
	pid, err := startBackgroundMount(ctx, backgroundMountRequest{
		Executable: executable,
		Args:       args,
		Env:        env,
		LogFile:    inputs.logFile,
		StateFile:  inputs.stateFile,
		MountPath:  inputs.mountPath,
		Timeout:    inputs.timeout,
	})
	if err != nil {
		return MountResult{}, err
	}
	checks = append(checks, MountRuntimeCheck{Name: "background_process", Status: "passed", Message: fmt.Sprintf("pid %d", pid)})
	checks = append(checks, MountRuntimeCheck{Name: "mount_state", Status: "passed", Message: inputs.stateFile})
	return vaultMountResult("mounted", inputs, checks, pid, inputs.stateFile, inputs.logFile), nil
}

func (s Service) vaultMountInputs(ctx context.Context, opts VaultMountOptions) (vaultMountInputs, []MountRuntimeCheck, error) {
	if opts.Profile == nil {
		return vaultMountInputs{}, nil, apperr.New("vault.missing_profile", "config", 2, "active profile is required")
	}
	mountPath, err := mountstate.CanonicalMountPath(opts.MountPath)
	if err != nil {
		return vaultMountInputs{}, nil, apperr.New("vault.missing_mount_path", "usage", 2, "--mount-path is required")
	}
	endpoint, err := s.resolveFS(opts.Profile)
	if err != nil {
		return vaultMountInputs{}, nil, err
	}
	client, ownerMode, err := s.vaultReadClient(opts.Profile, opts.VaultToken, authz.FSVaultSecretRead, "mount ti fs-vault")
	if err != nil {
		return vaultMountInputs{}, nil, err
	}
	readable, err := probeVaultReadableSecrets(ctx, client, ownerMode)
	if err != nil {
		return vaultMountInputs{}, nil, err
	}
	homeDir, err := s.homeDir()
	if err != nil {
		return vaultMountInputs{}, nil, err
	}
	stateFile, err := mountstate.Path(homeDir, mountPath)
	if err != nil {
		return vaultMountInputs{}, nil, err
	}
	logFile, err := mountstate.LogPath(homeDir, mountPath)
	if err != nil {
		return vaultMountInputs{}, nil, err
	}
	timeout := opts.ReadyTimeout
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	driver, err := mountdriver.Resolve("fuse")
	if err != nil {
		return vaultMountInputs{}, nil, apperr.New("vault.invalid_mount_driver", "usage", 2, err.Error())
	}
	inputs := vaultMountInputs{
		profile:     opts.Profile,
		mountPath:   mountPath,
		endpoint:    endpoint.BaseURL,
		client:      client,
		ownerMode:   ownerMode,
		stateFile:   stateFile,
		logFile:     logFile,
		homeDir:     homeDir,
		timeout:     timeout,
		vaultToken:  opts.VaultToken,
		readable:    readable,
		mountDriver: driver,
	}
	checks := []MountRuntimeCheck{
		{Name: "config_and_credentials", Status: "passed", Message: fmt.Sprintf("profile %q loaded", opts.Profile.Name)},
		{Name: "permission_requirement", Status: "passed", Message: string(authz.FSVaultSecretRead)},
		{Name: "endpoint_selection", Status: "passed", Message: fmt.Sprintf("%s %s", opts.Profile.CloudProvider, opts.Profile.RegionCode)},
		{Name: "vault_probe", Status: "passed", Message: fmt.Sprintf("%d readable secrets", readable)},
	}
	return inputs, checks, nil
}

func probeVaultReadableSecrets(ctx context.Context, client *apifs.Client, ownerMode bool) (int, error) {
	if ownerMode {
		secrets, err := client.ListVaultSecrets(ctx)
		if err != nil {
			return 0, err
		}
		return len(secrets), nil
	}
	secrets, err := client.ListReadableVaultSecrets(ctx)
	if err != nil {
		return 0, err
	}
	return len(secrets), nil
}

func vaultMountResult(status string, inputs vaultMountInputs, checks []MountRuntimeCheck, pid int, stateFile, logFile string) MountResult {
	return MountResult{
		Status:         status,
		Profile:        inputs.profile.Name,
		FileSystemName: "vault",
		MountPath:      inputs.mountPath,
		RemotePath:     "/n/vault",
		Driver:         "fuse",
		PID:            pid,
		StateFile:      stateFile,
		LogFile:        logFile,
		Endpoint:       nil,
		Checks:         checks,
		WriteBackCache: false,
	}
}

func (s Service) CreateVaultSecret(ctx context.Context, opts VaultCreateSecretOptions) (VaultSecretResult, error) {
	return s.drive9CreateVaultSecret(ctx, opts)
}

func (s Service) ReplaceVaultSecret(ctx context.Context, opts VaultReplaceSecretOptions) (VaultSecretResult, error) {
	return s.drive9ReplaceVaultSecret(ctx, opts)
}

func (s Service) ReadVaultSecret(ctx context.Context, opts VaultReadSecretOptions) (any, error) {
	return s.drive9ReadVaultSecret(ctx, opts)
}

func (s Service) ListVaultSecrets(ctx context.Context, opts VaultListSecretsOptions) (VaultListSecretsResult, error) {
	return s.drive9ListVaultSecrets(ctx, opts)
}

func (s Service) DeleteVaultSecret(ctx context.Context, opts VaultDeleteSecretOptions) (VaultDeleteResult, error) {
	return s.drive9DeleteVaultSecret(ctx, opts)
}

func (s Service) CreateVaultGrant(ctx context.Context, opts VaultCreateGrantOptions) (VaultTokenResult, error) {
	return s.drive9CreateVaultGrant(ctx, opts)
}

func (s Service) DeleteVaultGrant(ctx context.Context, opts VaultDeleteGrantOptions) (VaultDeleteResult, error) {
	return s.drive9DeleteVaultGrant(ctx, opts)
}

func (s Service) ListVaultAuditEvents(ctx context.Context, opts VaultAuditOptions) (VaultAuditResult, error) {
	return s.drive9ListVaultAuditEvents(ctx, opts)
}

func (s Service) RunWithVaultSecret(ctx context.Context, opts VaultRunWithSecretOptions) error {
	return s.drive9RunWithVaultSecret(ctx, opts)
}

func (s Service) vaultManagementClient(profile *config.Profile, permission authz.Permission, action string) (*apifs.Client, error) {
	return s.dataClient(profile, permission, action)
}

func (s Service) vaultReadClient(profile *config.Profile, token string, permission authz.Permission, action string) (*apifs.Client, bool, error) {
	if strings.TrimSpace(token) == "" {
		client, err := s.dataClient(profile, permission, action)
		return client, true, err
	}
	if profile == nil {
		return nil, false, apperr.New("fs.missing_profile", "config", 2, "active profile is required")
	}
	endpoint, err := s.resolveFS(profile)
	if err != nil {
		return nil, false, err
	}
	client, err := api.NewBearerClient(profileName(profile), strings.TrimSpace(token), endpoint, permission, api.Options{
		Action:      action,
		HTTPClient:  s.HTTPClient,
		Transport:   s.Transport,
		Timeout:     s.Timeout,
		Debug:       s.Debug,
		DebugWriter: s.DebugWriter,
		UserAgent:   "ti fs-vault",
	})
	if err != nil {
		return nil, false, err
	}
	return apifs.New(client), false, nil
}

func validateVaultSecretName(name string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return "", apperr.New("vault.missing_secret_name", "usage", 2, "--secret-name is required")
	}
	if strings.Contains(name, "/") {
		return "", apperr.New("vault.invalid_secret_name", "usage", 2, "vault secret names must be flat; use --field for field selection")
	}
	if strings.Contains(name, "*") {
		return "", apperr.New("vault.invalid_secret_name", "usage", 2, "wildcard vault scopes are not supported")
	}
	return name, nil
}

func parseVaultPath(raw string) (string, error) {
	const prefix = "/n/vault/"
	if !strings.HasPrefix(raw, prefix) {
		return "", apperr.New("vault.invalid_path", "usage", 2, fmt.Sprintf("vault path %q must start with %s", raw, prefix))
	}
	rest := strings.TrimPrefix(raw, prefix)
	if rest == "" {
		return "", apperr.New("vault.invalid_path", "usage", 2, fmt.Sprintf("vault path %q is missing a secret name", raw))
	}
	if strings.Contains(rest, "/") {
		return "", apperr.New("vault.invalid_path", "usage", 2, fmt.Sprintf("vault path %q must name exactly one secret", raw))
	}
	return validateVaultSecretName(rest)
}

func parseVaultFieldAssignments(assignments []string, stdin io.Reader) (map[string]string, error) {
	if len(assignments) == 0 {
		return nil, apperr.New("vault.missing_fields", "usage", 2, "at least one --field is required")
	}
	fields := make(map[string]string, len(assignments))
	var stdinValue []byte
	stdinRead := false
	for _, assignment := range assignments {
		key, valueSpec, ok := strings.Cut(assignment, "=")
		if !ok || strings.TrimSpace(key) == "" {
			return nil, apperr.New("vault.invalid_field", "usage", 2, fmt.Sprintf("field assignment must be key=value, key=@file, or key=-: %q", assignment))
		}
		var value string
		switch {
		case valueSpec == "-":
			if stdin == nil {
				return nil, apperr.New("vault.stdin_unavailable", "usage", 2, "field assignment uses '-' but stdin is unavailable")
			}
			if !stdinRead {
				data, err := io.ReadAll(stdin)
				if err != nil {
					return nil, apperr.Wrap("vault.read_stdin", "runtime", 1, "read vault field from stdin", err)
				}
				stdinValue = data
				stdinRead = true
			}
			value = string(stdinValue)
		case strings.HasPrefix(valueSpec, "@"):
			path := strings.TrimPrefix(valueSpec, "@")
			data, err := os.ReadFile(path)
			if err != nil {
				return nil, apperr.Wrap("vault.read_field_file", "runtime", 1, fmt.Sprintf("read vault field file %q", path), err)
			}
			value = string(data)
		default:
			value = valueSpec
		}
		fields[key] = value
	}
	return fields, nil
}

func readVaultFieldsDirectory(dir string) (map[string]string, error) {
	if strings.TrimSpace(dir) == "" {
		return nil, apperr.New("vault.missing_from_directory", "usage", 2, "--from-directory is required")
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, apperr.Wrap("vault.read_directory", "runtime", 1, fmt.Sprintf("read vault fields directory %q", dir), err)
	}
	fields := map[string]string{}
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		path := filepath.Join(dir, entry.Name())
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, apperr.Wrap("vault.read_field_file", "runtime", 1, fmt.Sprintf("read vault field file %q", path), err)
		}
		fields[entry.Name()] = string(data)
	}
	if len(fields) == 0 {
		return nil, apperr.New("vault.empty_directory", "usage", 2, fmt.Sprintf("--from-directory %q contains no field files", dir))
	}
	return fields, nil
}

func normalizeVaultEnvKey(field string) (string, error) {
	if field == "" {
		return "", apperr.New("vault.invalid_field", "usage", 2, "field name is required")
	}
	var b strings.Builder
	for i, r := range field {
		switch {
		case r >= 'A' && r <= 'Z':
			b.WriteRune(r)
		case r >= 'a' && r <= 'z':
			b.WriteRune(r - 'a' + 'A')
		case r >= '0' && r <= '9':
			if i == 0 {
				b.WriteByte('_')
			}
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	key := b.String()
	if key == "" {
		return "", apperr.New("vault.invalid_field", "usage", 2, "field name cannot normalize to an empty env key")
	}
	return key, nil
}

func renderVaultEnv(fields map[string]string) (string, error) {
	env := make(map[string]string, len(fields))
	owners := make(map[string]string, len(fields))
	for field, value := range fields {
		key, err := normalizeVaultEnvKey(field)
		if err != nil {
			return "", err
		}
		if previous, exists := owners[key]; exists {
			return "", apperr.New("vault.env_collision", "usage", 2, fmt.Sprintf("secret fields %q and %q both normalize to %q", previous, field, key))
		}
		owners[key] = field
		env[key] = value
	}
	keys := make([]string, 0, len(env))
	for key := range env {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	var out strings.Builder
	for _, key := range keys {
		out.WriteString(key)
		out.WriteByte('=')
		out.WriteString(env[key])
		out.WriteByte('\n')
	}
	return out.String(), nil
}

func validateVaultEnvFields(fields map[string]string) (map[string]string, error) {
	env := make(map[string]string, len(fields))
	for key, value := range fields {
		if !isValidVaultEnvKey(key) {
			return nil, apperr.New("vault.invalid_env_key", "usage", 2, fmt.Sprintf("secret key %q violates @env charset [A-Z_][A-Z0-9_]*", key))
		}
		if idx := indexOfForbiddenVaultEnvByte(value); idx >= 0 {
			return nil, apperr.New("vault.invalid_env_value", "usage", 2, fmt.Sprintf("secret value for key %q contains forbidden control byte 0x%02x at offset %d", key, value[idx], idx))
		}
		env[key] = value
	}
	return env, nil
}

func isValidVaultEnvKey(key string) bool {
	if key == "" {
		return false
	}
	for i := 0; i < len(key); i++ {
		ch := key[i]
		switch {
		case ch >= 'A' && ch <= 'Z', ch == '_':
		case ch >= '0' && ch <= '9':
			if i == 0 {
				return false
			}
		default:
			return false
		}
	}
	return true
}

func indexOfForbiddenVaultEnvByte(value string) int {
	for i := 0; i < len(value); i++ {
		b := value[i]
		if b < 0x20 && b != '\t' {
			return i
		}
	}
	return -1
}

func scrubTICredEnv(base []string) []string {
	out := make([]string, 0, len(base))
	for _, entry := range base {
		key, _, ok := strings.Cut(entry, "=")
		if !ok {
			out = append(out, entry)
			continue
		}
		switch key {
		case "TIDB_CLOUD_PRIVATE_KEY", "TIDB_CLOUD_PUBLIC_KEY", "TI_VAULT_TOKEN", "TI_FS_TOKEN", "TI_FS_API_KEY",
			"TDC_PRIVATE_KEY", "TDC_PUBLIC_KEY", "TDC_VAULT_TOKEN", "TDC_FS_TOKEN",
			"DRIVE9_PRIVATE_KEY", "DRIVE9_PUBLIC_KEY", "DRIVE9_VAULT_TOKEN", "DRIVE9_API_KEY":
			continue
		}
		out = append(out, entry)
	}
	return out
}

func mergeEnv(base []string, overrides map[string]string) []string {
	merged := make(map[string]string, len(base)+len(overrides))
	for _, entry := range base {
		key, value, ok := strings.Cut(entry, "=")
		if ok {
			merged[key] = value
		}
	}
	for key, value := range overrides {
		merged[key] = value
	}
	keys := make([]string, 0, len(merged))
	for key := range merged {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	out := make([]string, 0, len(keys))
	for _, key := range keys {
		out = append(out, key+"="+merged[key])
	}
	return out
}

func filterVaultAuditEvents(events []apifs.VaultAuditEvent, agentID string, since time.Duration) []apifs.VaultAuditEvent {
	cutoff := time.Time{}
	if since > 0 {
		cutoff = time.Now().Add(-since)
	}
	filtered := make([]apifs.VaultAuditEvent, 0, len(events))
	for _, event := range events {
		if agentID != "" && event.AgentID != agentID {
			continue
		}
		if !cutoff.IsZero() && event.Timestamp.Before(cutoff) {
			continue
		}
		filtered = append(filtered, event)
	}
	return filtered
}

func (r VaultSecretResult) Human() string {
	lines := []string{
		"Secret: " + r.Secret.Name,
		"Status: " + r.Status,
	}
	if r.Secret.Revision != 0 {
		lines = append(lines, fmt.Sprintf("Revision: %d", r.Secret.Revision))
	}
	return strings.Join(lines, "\n")
}

func (r VaultReadSecretResult) Human() string {
	if r.Field != "" {
		return r.Field + "=" + r.Value
	}
	env, err := renderVaultEnv(r.Fields)
	if err != nil {
		return r.SecretName
	}
	return strings.TrimRight(env, "\n")
}

func (r VaultListSecretsResult) Human() string {
	return strings.Join(r.Secrets, "\n")
}

func (r VaultDeleteResult) Human() string {
	return fmt.Sprintf("%s id=%s status=%s", r.Operation, r.ID, r.Status)
}

func (r VaultTokenResult) Human() string {
	var lines []string
	if r.Token != "" {
		lines = append(lines, "token="+r.Token)
	}
	if r.TokenID != "" {
		lines = append(lines, "token_id="+r.TokenID)
	}
	if r.GrantID != "" {
		lines = append(lines, "grant_id="+r.GrantID)
	}
	if !r.ExpiresAt.IsZero() {
		lines = append(lines, "expires_at="+r.ExpiresAt.Format(time.RFC3339))
	}
	return strings.Join(lines, "\n")
}

func (r VaultAuditResult) Human() string {
	var out strings.Builder
	writer := tabwriter.NewWriter(&out, 0, 4, 2, ' ', 0)
	_, _ = fmt.Fprintln(writer, "TIME\tAGENT\tACTION\tSECRET\tFIELD")
	for _, event := range r.Events {
		_, _ = fmt.Fprintf(writer, "%s\t%s\t%s\t%s\t%s\n", event.Timestamp.Format(time.RFC3339), event.AgentID, event.EventType, event.SecretName, event.FieldName)
	}
	_ = writer.Flush()
	return strings.TrimRight(out.String(), "\n")
}
