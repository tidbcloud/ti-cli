//go:build !windows

package fs

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	gofs "github.com/hanwen/go-fuse/v2/fs"
	"github.com/tidbcloud/ti-cli/internal/fs/mountcontrol"
	"github.com/tidbcloud/ti-cli/internal/fs/mountstate"
)

const drainPendingWorkKind = "pending_work_remaining"

type mountControlServer struct {
	runtime    *remoteFuseRuntime
	listener   net.Listener
	socketPath string
	done       chan struct{}
	drainMu    sync.Mutex
}

func startMountControlServer(mountPath string, runtime *remoteFuseRuntime) (*mountControlServer, error) {
	socketPath, err := mountstate.ControlSocketPath(mountPath)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(socketPath), 0o700); err != nil {
		return nil, err
	}
	_ = os.Chmod(filepath.Dir(socketPath), 0o700)
	_ = os.Remove(socketPath)
	listener, err := net.Listen("unix", socketPath)
	if err != nil {
		return nil, err
	}
	_ = os.Chmod(socketPath, 0o600)
	server := &mountControlServer{
		runtime:    runtime,
		listener:   listener,
		socketPath: socketPath,
		done:       make(chan struct{}),
	}
	go server.serve()
	return server, nil
}

func (s *mountControlServer) SocketPath() string {
	if s == nil {
		return ""
	}
	return s.socketPath
}

func (s *mountControlServer) Close() {
	if s == nil {
		return
	}
	_ = s.listener.Close()
	<-s.done
	if s.socketPath != "" {
		_ = os.Remove(s.socketPath)
	}
}

func (s *mountControlServer) serve() {
	defer close(s.done)
	for {
		conn, err := s.listener.Accept()
		if err != nil {
			if errors.Is(err, net.ErrClosed) {
				return
			}
			continue
		}
		go s.handleConn(conn)
	}
}

func (s *mountControlServer) handleConn(conn net.Conn) {
	defer func() { _ = conn.Close() }()
	_ = conn.SetDeadline(time.Now().Add(mountcontrol.DefaultDrainTimeout + 5*time.Second))
	var req mountcontrol.DrainRequest
	if err := json.NewDecoder(bufio.NewReader(conn)).Decode(&req); err != nil {
		resp := mountcontrol.NewDrainResponse(s.mountPoint(), time.Now().UTC())
		resp.Fail("bad_request", "", err)
		resp.Finish(time.Now().UTC())
		_ = json.NewEncoder(conn).Encode(resp)
		return
	}
	timeout := req.Timeout()
	_ = conn.SetDeadline(time.Now().Add(timeout + 5*time.Second))
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	s.drainMu.Lock()
	resp := s.runtime.Drain(ctx)
	s.drainMu.Unlock()
	if resp.MountPoint == "" {
		resp.MountPoint = s.mountPoint()
	}
	_ = json.NewEncoder(conn).Encode(resp)
}

func (s *mountControlServer) mountPoint() string {
	if s == nil || s.runtime == nil {
		return ""
	}
	return s.runtime.mountPath
}

type drainError struct {
	kind string
	path string
	err  error
}

func (e *drainError) Error() string {
	if e == nil || e.err == nil {
		return ""
	}
	return e.err.Error()
}

func (e *drainError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.err
}

func newDrainContextError(err error) *drainError {
	if errors.Is(err, context.DeadlineExceeded) {
		return &drainError{kind: "timeout", err: err}
	}
	if errors.Is(err, context.Canceled) {
		return &drainError{kind: "canceled", err: err}
	}
	return &drainError{kind: "error", err: err}
}

func newDrainStatusError(path string, errno syscall.Errno) *drainError {
	kind := "fuse_status"
	if errno == syscall.EAGAIN {
		kind = "remote_timeout_or_retryable"
	}
	return &drainError{
		kind: kind,
		path: path,
		err:  fmt.Errorf("flush returned FUSE errno %d", errno),
	}
}

func (r *remoteFuseRuntime) Drain(ctx context.Context) mountcontrol.DrainResponse {
	startedAt := time.Now().UTC()
	resp := mountcontrol.NewDrainResponse(r.mountPath, startedAt)
	runPhase := func(name string, fn func() error) bool {
		phaseStart := time.Now()
		err := fn()
		phase := mountcontrol.DrainPhase{Name: name, DurationMS: time.Since(phaseStart).Milliseconds()}
		if err != nil {
			phase.Error = err.Error()
		}
		resp.Phases = append(resp.Phases, phase)
		if err == nil {
			return true
		}
		var dErr *drainError
		if errors.As(err, &dErr) {
			resp.Fail(dErr.kind, dErr.path, dErr.err)
		} else {
			resp.Fail("error", "", err)
		}
		return false
	}

	if !runPhase("flush_open_handles", func() error {
		return r.drainOpenHandles(ctx)
	}) {
		resp.Pending = r.snapshotDrainPending()
		resp.Finish(time.Now().UTC())
		return resp
	}
	if !runPhase("writeback_recovery", func() error {
		if err := ctx.Err(); err != nil {
			return newDrainContextError(err)
		}
		_, err := r.recoverPending(ctx)
		return err
	}) {
		resp.Pending = r.snapshotDrainPending()
		resp.Finish(time.Now().UTC())
		return resp
	}

	resp.Pending = r.snapshotDrainPending()
	if err := drainPendingWorkError(resp.Pending); err != nil {
		resp.Fail(drainPendingWorkKind, "", err)
	}
	resp.Finish(time.Now().UTC())
	return resp
}

func (r *remoteFuseRuntime) drainOpenHandles(ctx context.Context) error {
	for _, handle := range r.openHandleSnapshot() {
		if err := ctx.Err(); err != nil {
			return newDrainContextError(err)
		}
		handle.mu.Lock()
		remotePath := handle.remotePath
		errno := handle.flushLocked(ctx)
		handle.mu.Unlock()
		if errno != gofs.OK {
			return newDrainStatusError(remotePath, errno)
		}
	}
	return nil
}

func (r *remoteFuseRuntime) snapshotDrainPending() mountcontrol.DrainPending {
	var pending mountcontrol.DrainPending
	for _, handle := range r.openHandleSnapshot() {
		pending.OpenHandles++
		handle.mu.Lock()
		if handle.dirty {
			pending.DirtyHandles++
		}
		handle.mu.Unlock()
	}
	if r != nil && r.writeBack != nil {
		stats := r.writeBack.pendingStats()
		pending.UploaderCached = stats.Count
		pending.UploaderCachedBytes = stats.Bytes
	}
	return pending
}

func drainPendingWorkError(p mountcontrol.DrainPending) error {
	if p.DirtyHandles == 0 &&
		p.CommitQueuePending == 0 &&
		p.CommitQueueInFlight == 0 &&
		p.CommitQueueDelayed == 0 &&
		p.CommitQueueConflicts == 0 &&
		p.UploaderQueued == 0 &&
		p.UploaderInFlight == 0 &&
		p.UploaderCached == 0 {
		return nil
	}
	return fmt.Errorf(
		"pending work remains after drain: dirty_handles=%d commit_queue_pending=%d commit_queue_in_flight=%d commit_queue_delayed=%d commit_queue_conflicts=%d uploader_queued=%d uploader_in_flight=%d uploader_cached=%d",
		p.DirtyHandles,
		p.CommitQueuePending,
		p.CommitQueueInFlight,
		p.CommitQueueDelayed,
		p.CommitQueueConflicts,
		p.UploaderQueued,
		p.UploaderInFlight,
		p.UploaderCached,
	)
}
