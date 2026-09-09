package fs

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"sort"
	"strings"
	"text/tabwriter"
	"unicode"

	apifs "github.com/tidbcloud/ti-cli/internal/api/fs"
	"github.com/tidbcloud/ti-cli/internal/apperr"
	"github.com/tidbcloud/ti-cli/internal/authz"
	"github.com/tidbcloud/ti-cli/internal/config"
	"github.com/tidbcloud/ti-cli/internal/dryrun"
	"github.com/tidbcloud/ti-cli/internal/fs/fscred"
)

const (
	LayerCapabilityFork   = drive9CapabilityLayerFork
	LayerCapabilityDelete = drive9CapabilityLayerDelete
)

type CreateLayerOptions struct {
	Profile        *config.Profile
	LayerID        string
	BaseRootPath   string
	LayerName      string
	Tags           []string
	DurabilityMode string
	ActorID        string
}

type DescribeLayerOptions struct {
	Profile *config.Profile
	LayerID string
}

type ListLayersOptions struct {
	Profile *config.Profile
}

type ForkLayerOptions struct {
	Profile        *config.Profile
	ParentLayerRef string
	LayerID        string
	LayerName      string
	CheckpointID   string
	ActorID        string
}

type ListLayerChainOptions struct {
	Profile  *config.Profile
	LayerRef string
}

type DeleteLayerOptions struct {
	Profile  *config.Profile
	LayerRef string
	Cascade  bool
}

type LayerEntriesOptions struct {
	Profile *config.Profile
	LayerID string
	MaxSeq  int64
}

type CreateLayerEntryOptions struct {
	Profile                *config.Profile
	LayerID                string
	Path                   string
	Operation              string
	ResourceKind           string
	BaseInodeID            string
	BaseRevision           int64
	StorageType            string
	StorageRef             string
	StorageRefHash         string
	StorageEncryptionMode  string
	StorageEncryptionKeyID string
	Content                string
	ContentSet             bool
	ContentType            string
	ContentText            string
	ChecksumSHA256         string
	SizeBytes              int64
	Mode                   string
}

type UploadLayerFileOptions struct {
	Profile      *config.Profile
	LayerID      string
	FromLocal    string
	ToLayerPath  string
	BaseRevision int64
	Mode         string
	ModeSet      bool
}

type ReadLayerFileOptions struct {
	Profile *config.Profile
	LayerID string
	Path    string
	MaxSeq  int64
}

type DescribeLayerEntryOptions struct {
	Profile *config.Profile
	LayerID string
	Path    string
	MaxSeq  int64
}

type CreateLayerCheckpointOptions struct {
	Profile      *config.Profile
	LayerID      string
	CheckpointID string
	Label        string
}

type DescribeLayerCheckpointOptions struct {
	Profile      *config.Profile
	CheckpointID string
}

type ListLayerEventsOptions struct {
	Profile *config.Profile
	LayerID string
	Since   int64
}

type LayerActionOptions struct {
	Profile *config.Profile
	LayerID string
}

type LayerResult struct {
	apifs.FSLayer
}

type LayerListResult struct {
	Layers []apifs.FSLayer `json:"layers"`
}

type LayerChainResult struct {
	Chain []apifs.FSLayerChainFrame `json:"chain"`
}

type DeleteLayerResult struct {
	Operation string `json:"operation"`
	LayerRef  string `json:"layer_ref"`
	Status    string `json:"status"`
	Cascade   bool   `json:"cascade"`
}

type LayerEntriesResult struct {
	LayerID string               `json:"layer_id"`
	Entries []apifs.FSLayerEntry `json:"entries"`
}

type LayerEntryResult struct {
	apifs.FSLayerEntry
}

type LayerCheckpointResult struct {
	apifs.FSLayerCheckpoint
}

type LayerEventsResult struct {
	LayerID string               `json:"layer_id"`
	Events  []apifs.FSLayerEvent `json:"events"`
}

type LayerActionResult struct {
	Operation string `json:"operation"`
	LayerID   string `json:"layer_id"`
	Status    string `json:"status"`
}

type LayerCommitResult struct {
	apifs.FSLayerCommit
}

func (s Service) CreateLayer(ctx context.Context, opts CreateLayerOptions) (LayerResult, error) {
	return s.drive9CreateLayer(ctx, opts)
}

func (s Service) ListLayers(ctx context.Context, opts ListLayersOptions) (LayerListResult, error) {
	return s.drive9ListLayers(ctx, opts)
}

func (s Service) ForkLayer(ctx context.Context, opts ForkLayerOptions) (LayerResult, error) {
	if err := validateLayerReference(opts.ParentLayerRef, "--parent-layer-ref"); err != nil {
		return LayerResult{}, err
	}
	if strings.TrimSpace(opts.CheckpointID) != "" {
		if err := validateLayerReference(opts.CheckpointID, "--checkpoint-id"); err != nil {
			return LayerResult{}, err
		}
	}
	return s.drive9ForkLayer(ctx, opts)
}

func (s Service) ListLayerChain(ctx context.Context, opts ListLayerChainOptions) (LayerChainResult, error) {
	if err := validateLayerReference(opts.LayerRef, "--layer-ref"); err != nil {
		return LayerChainResult{}, err
	}
	return s.drive9ListLayerChain(ctx, opts)
}

func (s Service) DeleteLayer(ctx context.Context, opts DeleteLayerOptions) (DeleteLayerResult, error) {
	if err := validateLayerReference(opts.LayerRef, "--layer-ref"); err != nil {
		return DeleteLayerResult{}, err
	}
	return s.drive9DeleteLayer(ctx, opts)
}

func (s Service) DescribeLayer(ctx context.Context, opts DescribeLayerOptions) (LayerResult, error) {
	return s.drive9DescribeLayer(ctx, opts)
}

func (s Service) DiffLayer(ctx context.Context, opts LayerEntriesOptions) (LayerEntriesResult, error) {
	return s.drive9DiffLayer(ctx, opts)
}

func (s Service) ReplayLayer(ctx context.Context, opts LayerEntriesOptions) (LayerEntriesResult, error) {
	client, err := s.dataClient(opts.Profile, authz.FSFileRead, "replay ti fs layer")
	if err != nil {
		return LayerEntriesResult{}, err
	}
	layerID := strings.TrimSpace(opts.LayerID)
	var entries []apifs.FSLayerEntry
	if opts.MaxSeq > 0 {
		entries, err = client.ReplayFSLayerAtSeq(ctx, layerID, opts.MaxSeq)
	} else {
		entries, err = client.ReplayFSLayer(ctx, layerID)
	}
	if err != nil {
		return LayerEntriesResult{}, err
	}
	return LayerEntriesResult{LayerID: layerID, Entries: entries}, nil
}

func (s Service) CreateLayerEntry(ctx context.Context, opts CreateLayerEntryOptions) (LayerEntryResult, error) {
	client, err := s.dataClient(opts.Profile, authz.FSFileWrite, "write ti fs layer entry")
	if err != nil {
		return LayerEntryResult{}, err
	}
	request, err := layerEntryRequest(opts)
	if err != nil {
		return LayerEntryResult{}, err
	}
	entry, err := client.UpsertFSLayerEntry(ctx, strings.TrimSpace(opts.LayerID), request)
	if err != nil {
		return LayerEntryResult{}, err
	}
	return LayerEntryResult{FSLayerEntry: entry}, nil
}

func (s Service) UploadLayerFile(ctx context.Context, opts UploadLayerFileOptions) (LayerEntryResult, error) {
	client, err := s.dataClient(opts.Profile, authz.FSFileWrite, "upload ti fs layer file")
	if err != nil {
		return LayerEntryResult{}, err
	}
	entry, err := uploadLocalFileToLayer(ctx, client, opts)
	if err != nil {
		return LayerEntryResult{}, err
	}
	return LayerEntryResult{FSLayerEntry: entry}, nil
}

func (s Service) ReadLayerFile(ctx context.Context, opts ReadLayerFileOptions) ([]byte, error) {
	client, err := s.dataClient(opts.Profile, authz.FSFileRead, "read ti fs layer file")
	if err != nil {
		return nil, err
	}
	remotePath, err := normalizeRemotePath(opts.Path)
	if err != nil {
		return nil, err
	}
	var maxSeq *int64
	if opts.MaxSeq > 0 {
		maxSeq = &opts.MaxSeq
	}
	return client.ReadFSLayerFile(ctx, strings.TrimSpace(opts.LayerID), remotePath, maxSeq)
}

func (s Service) DescribeLayerEntry(ctx context.Context, opts DescribeLayerEntryOptions) (LayerEntryResult, error) {
	client, err := s.dataClient(opts.Profile, authz.FSFileRead, "describe ti fs layer entry")
	if err != nil {
		return LayerEntryResult{}, err
	}
	remotePath, err := normalizeRemotePath(opts.Path)
	if err != nil {
		return LayerEntryResult{}, err
	}
	var entry apifs.FSLayerEntry
	if opts.MaxSeq > 0 {
		entry, err = client.GetFSLayerEntryAtSeq(ctx, strings.TrimSpace(opts.LayerID), remotePath, opts.MaxSeq)
	} else {
		entry, err = client.GetFSLayerEntry(ctx, strings.TrimSpace(opts.LayerID), remotePath)
	}
	if err != nil {
		return LayerEntryResult{}, err
	}
	return LayerEntryResult{FSLayerEntry: entry}, nil
}

func (s Service) CreateLayerCheckpoint(ctx context.Context, opts CreateLayerCheckpointOptions) (LayerCheckpointResult, error) {
	return s.drive9CreateLayerCheckpoint(ctx, opts)
}

func (s Service) DescribeLayerCheckpoint(ctx context.Context, opts DescribeLayerCheckpointOptions) (LayerCheckpointResult, error) {
	client, err := s.dataClient(opts.Profile, authz.FSFileRead, "describe ti fs layer checkpoint")
	if err != nil {
		return LayerCheckpointResult{}, err
	}
	checkpoint, err := client.GetFSLayerCheckpoint(ctx, strings.TrimSpace(opts.CheckpointID))
	if err != nil {
		return LayerCheckpointResult{}, err
	}
	return LayerCheckpointResult{FSLayerCheckpoint: checkpoint}, nil
}

func (s Service) ListLayerEvents(ctx context.Context, opts ListLayerEventsOptions) (LayerEventsResult, error) {
	client, err := s.dataClient(opts.Profile, authz.FSFileRead, "list ti fs layer events")
	if err != nil {
		return LayerEventsResult{}, err
	}
	layerID := strings.TrimSpace(opts.LayerID)
	events, err := client.ListFSLayerEvents(ctx, layerID, opts.Since)
	if err != nil {
		return LayerEventsResult{}, err
	}
	return LayerEventsResult{LayerID: layerID, Events: events}, nil
}

func (s Service) RollbackLayer(ctx context.Context, opts LayerActionOptions) (LayerActionResult, error) {
	return s.drive9RollbackLayer(ctx, opts)
}

func (s Service) CommitLayer(ctx context.Context, opts LayerActionOptions) (LayerCommitResult, error) {
	return s.drive9CommitLayer(ctx, opts)
}

func (s Service) DryRunLayerMutation(ctx context.Context, commandPath, operation, method, requestPath string, body any, profile *config.Profile, permission authz.Permission, capabilities ...string) (dryrun.Result, error) {
	if profile == nil {
		return dryrun.Result{}, apperr.New("fs.missing_profile", "config", 2, "active profile is required")
	}
	endpoint, err := s.resolveFS(profile)
	if err != nil {
		return dryrun.Result{}, err
	}
	checks := []dryrun.Check{
		{Name: "config_and_credentials", Status: "passed", Message: fmt.Sprintf("profile %q loaded", profileName(profile))},
		{Name: "permission_requirement", Status: "passed", Message: string(permission)},
		{Name: "endpoint_selection", Status: "passed", Message: fmt.Sprintf("%s %s", endpoint.Provider, endpoint.RegionCode)},
	}
	if resource := fscred.FromProfile(profile); resource.HasAPIKey {
		checks = append(checks, dryrun.Check{Name: "fs_resource_credentials", Status: "passed", Message: resource.Name})
	} else {
		return dryrun.Result{}, apperr.New("auth.missing_fs_api_key", "authentication", 3, fmt.Sprintf("authentication required: missing fs_api_key for profile %q. Create or configure a ti fs resource first.", profileName(profile)))
	}
	if len(capabilities) > 0 {
		if err := s.requireDrive9Capabilities(ctx, profile, capabilities...); err != nil {
			return dryrun.Result{}, err
		}
		checks = append(checks, dryrun.Check{Name: "companion_capability", Status: "passed", Message: strings.Join(capabilities, ", ")})
	}
	return dryrun.New(
		commandPath,
		operation,
		dryrun.RequestSummary{
			Method: method,
			Path:   requestPath,
			Body:   body,
		},
		checks...,
	), nil
}

func ValidateLayerReference(value, flagName string) error {
	return validateLayerReference(value, flagName)
}

func validateLayerReference(value, flagName string) error {
	value = strings.TrimSpace(value)
	if value == "" {
		return apperr.New("fs.missing_layer_reference", "usage", 2, flagName+" is required")
	}
	if strings.ContainsAny(value, "/\\") {
		return apperr.New("fs.invalid_layer_reference", "usage", 2, fmt.Sprintf("%s must not contain path separators", flagName))
	}
	for _, r := range value {
		if unicode.IsControl(r) {
			return apperr.New("fs.invalid_layer_reference", "usage", 2, fmt.Sprintf("%s must not contain control characters", flagName))
		}
	}
	return nil
}

func layerEntryRequest(opts CreateLayerEntryOptions) (apifs.FSLayerEntryRequest, error) {
	remotePath, err := normalizeRemotePath(opts.Path)
	if err != nil {
		return apifs.FSLayerEntryRequest{}, err
	}
	mode, err := parseMode(opts.Mode)
	if err != nil {
		return apifs.FSLayerEntryRequest{}, err
	}
	request := apifs.FSLayerEntryRequest{
		Path:                   remotePath,
		Op:                     strings.TrimSpace(opts.Operation),
		Kind:                   strings.TrimSpace(opts.ResourceKind),
		BaseInodeID:            strings.TrimSpace(opts.BaseInodeID),
		BaseRevision:           opts.BaseRevision,
		StorageType:            strings.TrimSpace(opts.StorageType),
		StorageRef:             strings.TrimSpace(opts.StorageRef),
		StorageRefHash:         strings.TrimSpace(opts.StorageRefHash),
		StorageEncryptionMode:  strings.TrimSpace(opts.StorageEncryptionMode),
		StorageEncryptionKeyID: strings.TrimSpace(opts.StorageEncryptionKeyID),
		ContentType:            strings.TrimSpace(opts.ContentType),
		ContentText:            opts.ContentText,
		ChecksumSHA256:         strings.TrimSpace(opts.ChecksumSHA256),
		SizeBytes:              opts.SizeBytes,
		Mode:                   uint32(mode),
	}
	if request.Op == "" {
		request.Op = "upsert"
	}
	if request.Kind == "" {
		request.Kind = "file"
	}
	if opts.ContentSet {
		request.Content = []byte(opts.Content)
	}
	return request, nil
}

func uploadLocalFileToLayer(ctx context.Context, client *apifs.Client, opts UploadLayerFileOptions) (apifs.FSLayerEntry, error) {
	file, size, err := openLocalRegularFile(opts.FromLocal)
	if err != nil {
		return apifs.FSLayerEntry{}, err
	}
	defer file.Close()
	remotePath, err := normalizeRemotePath(opts.ToLayerPath)
	if err != nil {
		return apifs.FSLayerEntry{}, err
	}
	mode, hasMode, err := layerUploadMode(opts.Mode, opts.ModeSet, file)
	if err != nil {
		return apifs.FSLayerEntry{}, err
	}
	return client.UploadFSLayerFile(ctx, strings.TrimSpace(opts.LayerID), remotePath, file, size, opts.BaseRevision, mode, hasMode)
}

func uploadBytesToLayer(ctx context.Context, client *apifs.Client, layerID, remotePath string, data []byte, mode uint32, hasMode bool) (apifs.FSLayerEntry, error) {
	remotePath, err := normalizeRemotePath(remotePath)
	if err != nil {
		return apifs.FSLayerEntry{}, err
	}
	return client.UploadFSLayerFile(ctx, strings.TrimSpace(layerID), remotePath, bytes.NewReader(data), int64(len(data)), 0, mode, hasMode)
}

func layerUploadMode(modeValue string, modeSet bool, file *os.File) (uint32, bool, error) {
	if modeSet {
		mode, err := parseMode(modeValue)
		if err != nil {
			return 0, false, err
		}
		return uint32(mode), true, nil
	}
	info, err := file.Stat()
	if err != nil {
		return 0, false, apperr.Wrap("fs.stat_local_file", "runtime", 1, fmt.Sprintf("stat local file %q", file.Name()), err)
	}
	return uint32(info.Mode().Perm()), true, nil
}

func parseLayerTags(values []string) (map[string]string, error) {
	if len(values) == 0 {
		return nil, nil
	}
	out := make(map[string]string, len(values))
	for _, raw := range values {
		key, value, ok := strings.Cut(raw, "=")
		if !ok {
			return nil, apperr.New("fs.invalid_layer_tag", "usage", 2, fmt.Sprintf("invalid layer tag %q; expected key=value", raw))
		}
		key = strings.TrimSpace(key)
		if key == "" {
			return nil, apperr.New("fs.invalid_layer_tag", "usage", 2, fmt.Sprintf("invalid layer tag %q; key is empty", raw))
		}
		if _, exists := out[key]; exists {
			return nil, apperr.New("fs.duplicate_layer_tag", "usage", 2, fmt.Sprintf("duplicate layer tag %q", key))
		}
		out[key] = strings.TrimSpace(value)
	}
	return out, nil
}

func ParseLayerTagsForDryRun(values []string) (map[string]string, error) {
	return parseLayerTags(values)
}

func formatLayerTags(tags map[string]string) string {
	if len(tags) == 0 {
		return ""
	}
	keys := make([]string, 0, len(tags))
	for key := range tags {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, key := range keys {
		parts = append(parts, key+"="+tags[key])
	}
	return strings.Join(parts, ",")
}

func (r LayerResult) Human() string {
	lines := []string{
		"Layer ID: " + r.LayerID,
		"State: " + r.State,
		"Base root: " + r.BaseRootPath,
	}
	if r.Name != "" {
		lines = append(lines, "Name: "+r.Name)
	}
	if r.DurabilityMode != "" {
		lines = append(lines, "Durability: "+r.DurabilityMode)
	}
	if r.ActorID != "" {
		lines = append(lines, "Actor: "+r.ActorID)
	}
	if r.ParentLayerID != "" {
		lines = append(lines,
			"Parent layer ID: "+r.ParentLayerID,
			fmt.Sprintf("Origin seq: %d", r.OriginSeq),
			fmt.Sprintf("Depth: %d", r.Depth),
		)
	}
	if r.OriginCheckpointID != "" {
		lines = append(lines, "Origin checkpoint ID: "+r.OriginCheckpointID)
	}
	if r.RootLayerID != "" {
		lines = append(lines, "Root layer ID: "+r.RootLayerID)
	}
	if r.Origin != "" {
		lines = append(lines, "Origin: "+r.Origin)
	}
	if r.DurableSeq != 0 {
		lines = append(lines, fmt.Sprintf("Durable seq: %d", r.DurableSeq))
	}
	if tags := formatLayerTags(r.Tags); tags != "" {
		lines = append(lines, "Tags: "+tags)
	}
	return strings.Join(lines, "\n")
}

func (r LayerChainResult) Human() string {
	var out strings.Builder
	writer := tabwriter.NewWriter(&out, 0, 0, 2, ' ', 0)
	_, _ = fmt.Fprintln(writer, "LAYER_ID\tNAME\tSTATE\tDEPTH\tPARENT_LAYER_ID\tORIGIN_SEQ\tLIMIT_SEQ\tORIGIN_CHECKPOINT_ID\tBASE_ROOT")
	for _, frame := range r.Chain {
		_, _ = fmt.Fprintf(writer, "%s\t%s\t%s\t%d\t%s\t%d\t%d\t%s\t%s\n", frame.LayerID, frame.Name, frame.State, frame.Depth, frame.ParentLayerID, frame.OriginSeq, frame.LimitSeq, frame.OriginCheckpointID, frame.BaseRootPath)
	}
	_ = writer.Flush()
	return strings.TrimRight(out.String(), "\n")
}

func (r DeleteLayerResult) Human() string {
	return fmt.Sprintf("%s layer=%s status=%s cascade=%t", r.Operation, r.LayerRef, r.Status, r.Cascade)
}

func (r LayerListResult) Human() string {
	var out strings.Builder
	writer := tabwriter.NewWriter(&out, 0, 0, 2, ' ', 0)
	_, _ = fmt.Fprintln(writer, "LAYER_ID\tSTATE\tDURABILITY\tBASE_ROOT\tNAME\tTAGS")
	for _, layer := range r.Layers {
		_, _ = fmt.Fprintf(writer, "%s\t%s\t%s\t%s\t%s\t%s\n", layer.LayerID, layer.State, layer.DurabilityMode, layer.BaseRootPath, layer.Name, formatLayerTags(layer.Tags))
	}
	_ = writer.Flush()
	return strings.TrimRight(out.String(), "\n")
}

func (r LayerEntriesResult) Human() string {
	var out strings.Builder
	writer := tabwriter.NewWriter(&out, 0, 0, 2, ' ', 0)
	_, _ = fmt.Fprintln(writer, "SEQ\tOP\tKIND\tMODE\tSIZE\tPATH")
	for _, entry := range r.Entries {
		_, _ = fmt.Fprintf(writer, "%d\t%s\t%s\t%04o\t%d\t%s\n", entry.EntrySeq, entry.Op, entry.Kind, entry.Mode, entry.SizeBytes, entry.Path)
	}
	_ = writer.Flush()
	return strings.TrimRight(out.String(), "\n")
}

func (r LayerEntryResult) Human() string {
	lines := []string{
		"Layer ID: " + r.LayerID,
		"Path: " + r.Path,
		"Operation: " + r.Op,
		"Kind: " + r.Kind,
	}
	if r.EntrySeq != 0 {
		lines = append(lines, fmt.Sprintf("Entry seq: %d", r.EntrySeq))
	}
	if r.SizeBytes != 0 {
		lines = append(lines, fmt.Sprintf("Size: %d", r.SizeBytes))
	}
	if r.Mode != 0 {
		lines = append(lines, fmt.Sprintf("Mode: %04o", r.Mode))
	}
	return strings.Join(lines, "\n")
}

func (r LayerCheckpointResult) Human() string {
	lines := []string{
		"Checkpoint ID: " + r.CheckpointID,
		"Layer ID: " + r.LayerID,
		fmt.Sprintf("Durable seq: %d", r.DurableSeq),
	}
	if r.Label != "" {
		lines = append(lines, "Label: "+r.Label)
	}
	return strings.Join(lines, "\n")
}

func (r LayerEventsResult) Human() string {
	var out strings.Builder
	writer := tabwriter.NewWriter(&out, 0, 0, 2, ' ', 0)
	_, _ = fmt.Fprintln(writer, "SEQ\tEVENT_ID\tACTOR\tOP\tPATH")
	for _, event := range r.Events {
		_, _ = fmt.Fprintf(writer, "%d\t%s\t%s\t%s\t%s\n", event.Seq, event.EventID, event.ActorID, event.Op, event.Path)
	}
	_ = writer.Flush()
	return strings.TrimRight(out.String(), "\n")
}

func (r LayerActionResult) Human() string {
	return fmt.Sprintf("%s layer=%s status=%s", r.Operation, r.LayerID, r.Status)
}

func (r LayerCommitResult) Human() string {
	if len(r.Conflicts) == 0 {
		return fmt.Sprintf("%s layer=%s applied=%d", r.Status, r.LayerID, r.Applied)
	}
	var out strings.Builder
	_, _ = fmt.Fprintf(&out, "%s layer=%s applied=%d conflicts=%d\n", r.Status, r.LayerID, r.Applied, len(r.Conflicts))
	writer := tabwriter.NewWriter(&out, 0, 0, 2, ' ', 0)
	_, _ = fmt.Fprintln(writer, "PATH\tREASON\tBASE_REVISION\tWANT_REVISION")
	for _, conflict := range r.Conflicts {
		_, _ = fmt.Fprintf(writer, "%s\t%s\t%d\t%d\n", conflict.Path, conflict.Reason, conflict.BaseRevision, conflict.WantRevision)
	}
	_ = writer.Flush()
	return strings.TrimRight(out.String(), "\n")
}
