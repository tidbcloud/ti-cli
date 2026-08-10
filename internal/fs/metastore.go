package fs

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/tidbcloud/ti-cli/internal/apperr"
	"github.com/tidbcloud/ti-cli/internal/config"
)

type fsMetadataStore struct {
	path string
	mu   sync.Mutex
}

type fsMetadataDoc struct {
	Schema  string                     `json:"schema"`
	Entries map[string]fsMetadataEntry `json:"entries"`
}

type fsMetadataEntry struct {
	Mode             int64  `json:"mode,omitempty"`
	HasMode          bool   `json:"has_mode,omitempty"`
	SymlinkTarget    string `json:"symlink_target,omitempty"`
	HasSymlinkTarget bool   `json:"has_symlink_target,omitempty"`
}

func newFSMetadataStore(homeDir string, profile *config.Profile) (*fsMetadataStore, error) {
	if strings.TrimSpace(homeDir) == "" {
		return nil, apperr.New("fs.metadata_missing_home", "config", 1, "home directory is required for ti fs metadata")
	}
	key := "profile=" + profileNameForMetadata(profile) +
		"\x00tenant=" + strings.TrimSpace(profileValue(profile, func(p *config.Profile) string { return p.FSTenantID })) +
		"\x00resource=" + strings.TrimSpace(profileValue(profile, func(p *config.Profile) string { return p.FSResourceName })) +
		"\x00cloud=" + strings.TrimSpace(profileValue(profile, func(p *config.Profile) string { return p.FSCloudProvider })) +
		"\x00region=" + strings.TrimSpace(profileValue(profile, func(p *config.Profile) string { return p.FSRegionCode }))
	sum := sha256.Sum256([]byte(key))
	return &fsMetadataStore{path: filepath.Join(homeDir, ".ti", "fs_metadata", hex.EncodeToString(sum[:8])+".json")}, nil
}

func profileNameForMetadata(profile *config.Profile) string {
	if profile == nil || strings.TrimSpace(profile.Name) == "" {
		return "unknown"
	}
	return strings.TrimSpace(profile.Name)
}

func profileValue(profile *config.Profile, getter func(*config.Profile) string) string {
	if profile == nil {
		return ""
	}
	return getter(profile)
}

func (s *fsMetadataStore) applyDescribe(result DescribeFileResult) DescribeFileResult {
	entry, ok := s.lookup(result.Path)
	if !ok {
		return result
	}
	if entry.HasMode && !result.HasMode {
		result.Mode = entry.Mode
		result.HasMode = true
	}
	return result
}

func (s *fsMetadataStore) applyFileInfo(remotePath string, info remoteFileInfo) remoteFileInfo {
	entry, ok := s.lookup(remotePath)
	if !ok {
		return info
	}
	if entry.HasMode {
		info.mode = (info.mode &^ 0o777) | os.FileMode(entry.Mode&0o777)
	}
	if entry.HasSymlinkTarget {
		info.mode = os.ModeSymlink | 0o777
		info.size = int64(len(entry.SymlinkTarget))
	}
	return info
}

func (s *fsMetadataStore) symlinkTarget(remotePath string) (string, bool) {
	entry, ok := s.lookup(remotePath)
	if !ok || !entry.HasSymlinkTarget {
		return "", false
	}
	return entry.SymlinkTarget, true
}

func (s *fsMetadataStore) lookup(remotePath string) (fsMetadataEntry, bool) {
	if s == nil {
		return fsMetadataEntry{}, false
	}
	clean, err := normalizeRemotePath(remotePath)
	if err != nil {
		return fsMetadataEntry{}, false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	doc, err := s.readLocked()
	if err != nil {
		return fsMetadataEntry{}, false
	}
	entry, ok := doc.Entries[clean]
	return entry, ok
}

func (s *fsMetadataStore) setMode(remotePath string, mode int64) error {
	return s.update(remotePath, func(entry fsMetadataEntry) (fsMetadataEntry, bool) {
		entry.Mode = mode & 0o777
		entry.HasMode = true
		return entry, false
	})
}

func (s *fsMetadataStore) setSymlink(remotePath, target string) error {
	return s.update(remotePath, func(entry fsMetadataEntry) (fsMetadataEntry, bool) {
		entry.Mode = 0o777
		entry.HasMode = true
		entry.SymlinkTarget = target
		entry.HasSymlinkTarget = true
		return entry, false
	})
}

func (s *fsMetadataStore) remove(remotePath string, recursive bool) error {
	return s.update(remotePath, func(entry fsMetadataEntry) (fsMetadataEntry, bool) {
		return entry, true
	}, metadataUpdateRecursive(recursive))
}

func (s *fsMetadataStore) move(source, target string) error {
	if s == nil {
		return nil
	}
	source, err := normalizeRemotePath(source)
	if err != nil {
		return err
	}
	target, err = normalizeRemotePath(target)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	doc, err := s.readLocked()
	if err != nil {
		return err
	}
	next := make(map[string]fsMetadataEntry, len(doc.Entries))
	prefix := treePrefix(source)
	for p, entry := range doc.Entries {
		switch {
		case p == source:
			next[target] = entry
		case strings.HasPrefix(p, prefix):
			next[target+strings.TrimPrefix(p, source)] = entry
		default:
			next[p] = entry
		}
	}
	doc.Entries = next
	return s.writeLocked(doc)
}

func (s *fsMetadataStore) copyMetadata(source, target string) error {
	entry, ok := s.lookup(source)
	if !ok {
		return nil
	}
	return s.update(target, func(current fsMetadataEntry) (fsMetadataEntry, bool) {
		return entry, false
	})
}

type metadataUpdateOption func(*metadataUpdateOptions)

type metadataUpdateOptions struct {
	recursive bool
}

func metadataUpdateRecursive(value bool) metadataUpdateOption {
	return func(opts *metadataUpdateOptions) {
		opts.recursive = value
	}
}

func (s *fsMetadataStore) update(remotePath string, fn func(fsMetadataEntry) (fsMetadataEntry, bool), options ...metadataUpdateOption) error {
	if s == nil {
		return nil
	}
	remotePath, err := normalizeRemotePath(remotePath)
	if err != nil {
		return err
	}
	var opts metadataUpdateOptions
	for _, option := range options {
		option(&opts)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	doc, err := s.readLocked()
	if err != nil {
		return err
	}
	if opts.recursive {
		prefix := treePrefix(remotePath)
		for p := range doc.Entries {
			if p == remotePath || strings.HasPrefix(p, prefix) {
				delete(doc.Entries, p)
			}
		}
		return s.writeLocked(doc)
	}
	next, remove := fn(doc.Entries[remotePath])
	if remove {
		delete(doc.Entries, remotePath)
	} else {
		doc.Entries[remotePath] = next
	}
	return s.writeLocked(doc)
}

func (s *fsMetadataStore) readLocked() (fsMetadataDoc, error) {
	doc := fsMetadataDoc{Schema: "ti.fs.metadata/v1", Entries: map[string]fsMetadataEntry{}}
	data, err := os.ReadFile(s.path)
	if err != nil {
		if os.IsNotExist(err) {
			return doc, nil
		}
		return fsMetadataDoc{}, fmt.Errorf("read ti fs metadata %q: %w", s.path, err)
	}
	if len(strings.TrimSpace(string(data))) == 0 {
		return doc, nil
	}
	if err := json.Unmarshal(data, &doc); err != nil {
		return fsMetadataDoc{}, fmt.Errorf("parse ti fs metadata %q: %w", s.path, err)
	}
	if doc.Entries == nil {
		doc.Entries = map[string]fsMetadataEntry{}
	}
	return doc, nil
}

func (s *fsMetadataStore) writeLocked(doc fsMetadataDoc) error {
	if doc.Schema == "" {
		doc.Schema = "ti.fs.metadata/v1"
	}
	if doc.Entries == nil {
		doc.Entries = map[string]fsMetadataEntry{}
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0o755); err != nil {
		return fmt.Errorf("create ti fs metadata directory: %w", err)
	}
	data, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return fmt.Errorf("encode ti fs metadata: %w", err)
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, append(data, '\n'), 0o644); err != nil {
		return fmt.Errorf("write ti fs metadata temp file: %w", err)
	}
	if err := os.Rename(tmp, s.path); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("replace ti fs metadata file: %w", err)
	}
	return nil
}
