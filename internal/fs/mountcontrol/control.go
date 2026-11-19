package mountcontrol

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"time"
)

const DefaultDrainTimeout = 30 * time.Second

type DrainRequest struct {
	TimeoutMS int64 `json:"timeout_ms,omitempty"`
}

func (r DrainRequest) Timeout() time.Duration {
	if r.TimeoutMS <= 0 {
		return DefaultDrainTimeout
	}
	return time.Duration(r.TimeoutMS) * time.Millisecond
}

type DrainResponse struct {
	OK                    bool         `json:"ok"`
	MountPoint            string       `json:"mount_point,omitempty"`
	StartedAt             time.Time    `json:"started_at"`
	CompletedAt           time.Time    `json:"completed_at"`
	DurationMS            int64        `json:"duration_ms"`
	Error                 string       `json:"error,omitempty"`
	ErrorKind             string       `json:"error_kind,omitempty"`
	FailedPath            string       `json:"failed_path,omitempty"`
	FUSEProtocolMajor     uint32       `json:"fuse_protocol_major,omitempty"`
	FUSEProtocolMinor     uint32       `json:"fuse_protocol_minor,omitempty"`
	NativeSyncFSSupported bool         `json:"native_syncfs_supported"`
	Pending               DrainPending `json:"pending"`
	Phases                []DrainPhase `json:"phases,omitempty"`
}

type DrainPending struct {
	OpenHandles          int   `json:"open_handles"`
	DirtyHandles         int   `json:"dirty_handles"`
	CommitQueuePending   int   `json:"commit_queue_pending"`
	CommitQueueBytes     int64 `json:"commit_queue_bytes"`
	CommitQueueInFlight  int   `json:"commit_queue_in_flight"`
	CommitQueueDelayed   int   `json:"commit_queue_delayed"`
	CommitQueueConflicts int   `json:"commit_queue_conflicts"`
	UploaderQueued       int   `json:"uploader_queued"`
	UploaderInFlight     int   `json:"uploader_in_flight"`
	UploaderCached       int   `json:"uploader_cached"`
	UploaderCachedBytes  int64 `json:"uploader_cached_bytes"`
}

// HasPendingWork reports whether the snapshot lists work the engine has not made
// durable remotely.
//
// The engine's own drain-failure exit record counts exactly
// dirty_handles + commit_queue_pending + uploader_queued as pending files and
// commit_queue_bytes + uploader_cached_bytes as pending bytes, so those counters
// are undrained work by construction. The remaining counters are informational,
// but they are non-zero only while the mount still has unfinished work, so ti
// treats any non-zero counter as pending: over-blocking an unmount is safe,
// silently detaching from queued write-back is not. Keep this conservative until
// the engine publishes a loss-carrying/informational split.
func (p DrainPending) HasPendingWork() bool {
	return p.OpenHandles != 0 ||
		p.DirtyHandles != 0 ||
		p.CommitQueuePending != 0 ||
		p.CommitQueueBytes != 0 ||
		p.CommitQueueInFlight != 0 ||
		p.CommitQueueDelayed != 0 ||
		p.CommitQueueConflicts != 0 ||
		p.UploaderQueued != 0 ||
		p.UploaderInFlight != 0 ||
		p.UploaderCached != 0 ||
		p.UploaderCachedBytes != 0
}

type DrainPhase struct {
	Name       string `json:"name"`
	DurationMS int64  `json:"duration_ms"`
	Error      string `json:"error,omitempty"`
}

func NewDrainResponse(mountPoint string, startedAt time.Time) DrainResponse {
	return DrainResponse{
		OK:         true,
		MountPoint: mountPoint,
		StartedAt:  startedAt,
	}
}

func (r *DrainResponse) Finish(now time.Time) {
	r.CompletedAt = now
	r.DurationMS = now.Sub(r.StartedAt).Milliseconds()
}

func (r *DrainResponse) Fail(kind, path string, err error) {
	r.OK = false
	r.ErrorKind = kind
	r.FailedPath = path
	if err != nil {
		r.Error = err.Error()
	}
}

func RequestDrain(ctx context.Context, socketPath string, timeout time.Duration) (*DrainResponse, error) {
	if socketPath == "" {
		return nil, fmt.Errorf("missing mount control socket")
	}
	if timeout <= 0 {
		timeout = DefaultDrainTimeout
	}
	dialer := net.Dialer{}
	conn, err := dialer.DialContext(ctx, "unix", socketPath)
	if err != nil {
		return nil, fmt.Errorf("dial mount control socket %s: %w", socketPath, err)
	}
	defer func() { _ = conn.Close() }()
	if deadline, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(deadline)
	}

	if err := json.NewEncoder(conn).Encode(DrainRequest{TimeoutMS: timeout.Milliseconds()}); err != nil {
		return nil, fmt.Errorf("write drain request: %w", err)
	}
	var resp DrainResponse
	if err := json.NewDecoder(bufio.NewReader(conn)).Decode(&resp); err != nil {
		return nil, fmt.Errorf("read drain response: %w", err)
	}
	return &resp, nil
}
