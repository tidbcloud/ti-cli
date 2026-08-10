//go:build !windows

package fs

import apifs "github.com/tidbcloud/ti-cli/internal/api/fs"

type fuseObjectVersion struct {
	Revision   int64  `json:"revision,omitempty"`
	ResourceID string `json:"resource_id,omitempty"`
}

func fuseVersionFromMetadata(metadata apifs.StatMetadataResponse) fuseObjectVersion {
	return fuseObjectVersion{Revision: metadata.Revision, ResourceID: metadata.ResourceID}
}

func fuseVersionFromStat(stat apifs.StatResponse) fuseObjectVersion {
	return fuseObjectVersion{Revision: stat.Revision, ResourceID: stat.ResourceID}
}

func (v fuseObjectVersion) known() bool {
	return v.Revision > 0 || v.ResourceID != ""
}

func (v fuseObjectVersion) matches(required fuseObjectVersion) bool {
	if !required.known() {
		return true
	}
	if required.ResourceID != "" && v.ResourceID != required.ResourceID {
		return false
	}
	if required.Revision > 0 && v.Revision != required.Revision {
		return false
	}
	return true
}

func (v fuseObjectVersion) conflictsWith(expected fuseObjectVersion) bool {
	if expected.ResourceID != "" && v.ResourceID != "" && v.ResourceID != expected.ResourceID {
		return true
	}
	if expected.Revision > 0 && v.Revision > 0 && v.Revision != expected.Revision {
		return true
	}
	return false
}

func (v fuseObjectVersion) withRevision(revision int64) fuseObjectVersion {
	if revision > 0 {
		v.Revision = revision
	}
	return v
}
