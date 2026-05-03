package main

import (
	"archive/tar"
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"devup/internal/api"
	"devup/internal/cgroup"
	"devup/internal/discovery"
	"devup/internal/logging"
	"devup/internal/mountutil"
	"devup/internal/netns"
	"devup/internal/overlay"
	"devup/internal/ringbuffer"
	"devup/internal/shadow"
	"devup/internal/sysinfo"
	"devup/internal/toolchain"
	"devup/internal/version"
)

const (
	maxConcurrentRuns   = 4
	maxRunTime          = 10 * time.Minute
	idempotencyTTL      = 60 * time.Second
	exitCodePrefix      = "DEVUP_EXIT_CODE="
	jsonRequestMaxBytes = 1 * 1024 * 1024

	// Job constants
	maxConcurrentJobs = 32
	jobRetentionTTL   = 10 * time.Minute
	ringBufferSize    = 64 * 1024
	stopGracePeriod   = 2 * time.Second
	broadcastChanSize = 100

	// Default env for dev tools (Linux-native, writable paths)
	defaultHome         = "/tmp/devup-home"
	defaultNextCacheDir = "/tmp/next-cache"
	defaultNpmCache     = "/tmp/npm-cache"

	// Workspace upload
	workspacesDir   = "/var/lib/devup/workspaces"
	workspaceMaxAge = 1 * time.Hour
	uploadMaxBytes  = 500 * 1024 * 1024 // 500 MB
	mib             = int64(1024 * 1024)
)

type runResult struct {
	exitCode int
	output   string
	done     time.Time
}

type job struct {
	mu                   sync.RWMutex
	info                 api.JobInfo
	cmd                  *exec.Cmd
	logBuf               *ringbuffer.RingBuffer
	broadcast            chan []byte
	mounted              []string // bind mount paths (non-overlay)
	overlays             []*overlay.State
	overlayDirs          []string
	nsName               string // network namespace name (empty = no isolation)
	mountNamespaceSpec   string
	privateMounts        bool
	done                 chan struct{}
	memoryControllerDone chan struct{}
	semAcquired          bool
}

var (
	jobs        = make(map[string]*job)
	jobsMu      sync.RWMutex
	jobSem      = make(chan struct{}, maxConcurrentJobs)
	admissionMu sync.Mutex

	// inflightMounts tracks mount paths that are between applyMounts and job
	// registration. The reconciler skips these to avoid a race where mounts
	// are pruned before the job appears in the jobs map.
	inflightMounts   = make(map[string]bool)
	inflightMountsMu sync.Mutex
)

func main() {
	if handled, err := maybeRunMountNamespaceExec(os.Args[1:]); handled {
		if err != nil {
			logging.Error("mount namespace exec failed", "err", err)
			os.Exit(1)
		}
		return
	}

	token, err := os.ReadFile("/etc/devup/token")
	if err != nil {
		logging.Error("cannot read token", "err", err)
		os.Exit(1)
	}
	expectedToken := strings.TrimSpace(string(token))

	results := make(map[string]*runResult)
	var resultsMu sync.RWMutex
	sem := make(chan struct{}, maxConcurrentRuns)

	auth := func(next http.HandlerFunc) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			tok := r.Header.Get("X-Devup-Token")
			if tok != expectedToken {
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
			if clientVer := r.Header.Get("X-Devup-Version"); clientVer != "" {
				if parts := strings.SplitN(clientVer, ".", 2); len(parts) > 0 {
					if major, err := strconv.Atoi(parts[0]); err == nil && major != version.Major {
						http.Error(w, fmt.Sprintf(
							"version mismatch: agent=%s client=%s (reprovision with: devup vm provision)",
							version.Version, clientVer), http.StatusBadRequest)
						return
					}
				}
			}
			next(w, r)
		}
	}

	http.HandleFunc("/health", auth(handleHealth))
	http.HandleFunc("/run", auth(handleRun(sem, results, &resultsMu)))
	http.HandleFunc("/start", auth(handleStart))
	http.HandleFunc("/ps", auth(handlePs))
	http.HandleFunc("/logs", auth(handleLogs))
	http.HandleFunc("/stop", auth(handleStop))
	http.HandleFunc("/down", auth(handleDown))
	http.HandleFunc("/system/info", auth(handleSystemInfo))

	// Node discovery
	hostname, _ := os.Hostname()
	if hostname == "" {
		hostname = "devup-node"
	}
	disco := discovery.New(hostname, 7777, func() discovery.NodeStats {
		stats := sysinfo.Read()
		active := activeJobCount()
		slotsFree := availableSlots(stats, active)
		return discovery.NodeStats{
			SlotsFree:  slotsFree,
			ActiveJobs: active,
			MemTotalMB: stats.MemTotalMB,
			MemFreeMB:  stats.MemFreeMB,
			LoadAvg1:   stats.LoadAvg1,
		}
	})
	http.HandleFunc("/cluster", auth(handleCluster(disco)))
	http.HandleFunc("/upload", auth(handleUpload))

	go janitor()
	reconcileMounts()

	if err := overlay.Init(); err != nil {
		logging.Error("overlay init failed", "err", err)
	}
	if err := shadow.Init(); err != nil {
		logging.Error("shadow init failed", "err", err)
	}

	if err := cgroup.Init(); err != nil {
		logging.Error("cgroup init failed (resource limits disabled)", "err", err)
	}
	reconcileCgroups()
	reconcileOverlays()
	go reconcileLoop()

	if err := disco.Start(); err != nil {
		logging.Error("discovery start failed (cluster disabled)", "err", err)
	}

	srv := &http.Server{Addr: "0.0.0.0:7777"}

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGTERM, syscall.SIGINT)
	go func() {
		sig := <-sigCh
		logging.Info("received signal, shutting down", "signal", sig.String())
		disco.Stop()
		ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
		defer cancel()
		srv.Shutdown(ctx)
		drainAllJobs()
		logging.Info("agent shutdown complete")
		os.Exit(0)
	}()

	logging.Info("devup-agent starting", "port", 7777, "version", version.Version)
	if err := srv.ListenAndServe(); err != http.ErrServerClosed {
		logging.Error("listen failed", "err", err)
		os.Exit(1)
	}
}

// drainAllJobs sends SIGTERM to all running job process groups, waits up to
// 5 seconds for them to exit gracefully, then force-kills any survivors.
// The existing waiter goroutines handle cgroup/overlay/mount/netns cleanup.
func drainAllJobs() {
	jobsMu.RLock()
	var running []*job
	for _, j := range jobs {
		j.mu.RLock()
		if j.info.Status == "running" {
			running = append(running, j)
		}
		j.mu.RUnlock()
	}
	jobsMu.RUnlock()

	if len(running) == 0 {
		return
	}
	logging.Info("draining jobs", "count", len(running))

	for _, j := range running {
		j.mu.RLock()
		cmd := j.cmd
		done := j.done
		j.mu.RUnlock()
		stopProcessGroup(cmd, done, 5*time.Second, 2*time.Second)
	}
}

func handleHealth(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	// Uptime from start - we'd need a global; for MVP use 0
	resp := api.HealthResponse{
		Status:      "ok",
		Version:     version.Version,
		DefaultHome: defaultHome,
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

func handleUpload(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, uploadMaxBytes)

	id := generateJobID()
	dest := filepath.Join(workspacesDir, id)
	if err := os.MkdirAll(dest, 0755); err != nil {
		http.Error(w, "mkdir: "+err.Error(), http.StatusInternalServerError)
		return
	}

	tr := tar.NewReader(r.Body)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			os.RemoveAll(dest)
			http.Error(w, "tar: "+err.Error(), http.StatusBadRequest)
			return
		}

		target := filepath.Join(dest, filepath.FromSlash(hdr.Name))
		if !strings.HasPrefix(target, dest+string(os.PathSeparator)) && target != dest {
			continue // path traversal guard
		}

		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, os.FileMode(hdr.Mode)|0755); err != nil {
				os.RemoveAll(dest)
				http.Error(w, "mkdir: "+err.Error(), http.StatusInternalServerError)
				return
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(target), 0755); err != nil {
				os.RemoveAll(dest)
				http.Error(w, "mkdir: "+err.Error(), http.StatusInternalServerError)
				return
			}
			f, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, os.FileMode(hdr.Mode)|0644)
			if err != nil {
				os.RemoveAll(dest)
				http.Error(w, "create: "+err.Error(), http.StatusInternalServerError)
				return
			}
			if _, err := io.Copy(f, tr); err != nil {
				f.Close()
				os.RemoveAll(dest)
				http.Error(w, "write: "+err.Error(), http.StatusInternalServerError)
				return
			}
			f.Close()
		case tar.TypeSymlink:
			if err := os.MkdirAll(filepath.Dir(target), 0755); err != nil {
				os.RemoveAll(dest)
				http.Error(w, "mkdir: "+err.Error(), http.StatusInternalServerError)
				return
			}
			os.Remove(target)
			if err := os.Symlink(hdr.Linkname, target); err != nil {
				os.RemoveAll(dest)
				http.Error(w, "symlink: "+err.Error(), http.StatusInternalServerError)
				return
			}
		}
	}

	logging.Info("workspace uploaded", "id", id, "path", dest)
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(api.UploadResponse{WorkspacePath: dest})
}

func handleCluster(disco *discovery.Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		peers := disco.Peers()
		activeCount := activeJobCount()
		localStats := sysinfo.Read()
		localSlotsFree := availableSlots(localStats, activeCount)

		var infos []api.PeerInfo
		localID := disco.NodeID()

		selfFound := false
		for _, p := range peers {
			status := "online"
			if p.NodeID == localID {
				status = "local"
				selfFound = true
			}
			infos = append(infos, api.PeerInfo{
				NodeID:     p.NodeID,
				Addr:       p.Addr,
				Port:       p.Port,
				SlotsFree:  p.SlotsFree,
				Version:    p.Version,
				Status:     status,
				LastSeen:   p.LastSeen.Unix(),
				ActiveJobs: p.ActiveJobs,
				MemTotalMB: p.MemTotalMB,
				MemFreeMB:  p.MemFreeMB,
				LoadAvg1:   p.LoadAvg1,
			})
		}
		if !selfFound {
			infos = append(infos, api.PeerInfo{
				NodeID:     localID,
				Addr:       "127.0.0.1",
				Port:       7777,
				SlotsFree:  localSlotsFree,
				Version:    version.Version,
				Status:     "local",
				LastSeen:   time.Now().Unix(),
				ActiveJobs: activeCount,
				MemTotalMB: localStats.MemTotalMB,
				MemFreeMB:  localStats.MemFreeMB,
				LoadAvg1:   localStats.LoadAvg1,
			})
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(api.ClusterResponse{Peers: infos})
	}
}

func handleRun(sem chan struct{}, results map[string]*runResult, resultsMu *sync.RWMutex) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		var req api.RunRequest
		if !decodeJSONBody(w, r, &req) {
			return
		}
		profile := normalizedProfile(req.Profile, api.ProfileBatch)
		if len(req.Cmd) == 0 {
			http.Error(w, "cmd required", http.StatusBadRequest)
			return
		}

		// Idempotency
		resultsMu.RLock()
		if cached, ok := results[req.RequestID]; ok && time.Since(cached.done) < idempotencyTTL {
			resultsMu.RUnlock()
			streamResult(w, cached.exitCode, cached.output)
			return
		}
		resultsMu.RUnlock()

		// Acquire semaphore
		select {
		case sem <- struct{}{}:
			defer func() { <-sem }()
		default:
			http.Error(w, "too many concurrent runs", http.StatusServiceUnavailable)
			return
		}

		execMounts, mountErr := prepareExecutionMounts(req.Mounts, req.Shadow)
		if mountErr != nil {
			http.Error(w, "shadow: "+mountErr.Error(), http.StatusBadRequest)
			return
		}
		execWorkdir := resolveExecutionWorkdir(execMounts, req.Cwd)

		useOverlay := req.Overlay || req.Shadow
		runID := "run-" + req.RequestID
		usePrivateMounts := privateMountNamespacesAvailable() && len(execMounts) > 0
		// Apply workspace mounts (overlay or bind)
		var mountedPaths []string
		var runOverlays []*overlay.State
		var mountSpecPath string
		runOverlayDirs := overlayStateDirs(runID, execMounts, useOverlay)
		if !usePrivateMounts {
			if useOverlay {
				var mergedPaths []string
				runOverlays, mergedPaths, mountErr = applyOverlayMounts(runID, execMounts)
				if mountErr != nil {
					http.Error(w, "overlay: "+mountErr.Error(), http.StatusBadRequest)
					return
				}
				mountedPaths = mergedPaths
			} else {
				mountedPaths, mountErr = applyMounts(execMounts)
				if mountErr != nil {
					http.Error(w, "mount: "+mountErr.Error(), http.StatusBadRequest)
					return
				}
			}
			markInflight(mountedPaths)
			defer clearInflight(mountedPaths)
			defer func() {
				cleanupOverlays(runOverlays)
				if !useOverlay {
					cleanupMounts(mountedPaths)
				}
			}()
		} else {
			defer cleanupMountNamespaceArtifacts(mountSpecPath, runOverlayDirs)
		}

		if err := ensureDefaultDirs(); err != nil {
			http.Error(w, "ensure dirs: "+err.Error(), http.StatusInternalServerError)
			return
		}

		// JIT toolchain provisioning (non-fatal)
		miseEnv, miseErr := toolchain.EnsureForWorkdir(execWorkdir)
		if miseErr != nil {
			logging.Error("toolchain provision", "err", miseErr)
		}

		// Network isolation: wrap command with ip netns exec
		var runNsName string
		cmdArgs := req.Cmd
		if req.NetIsolate {
			runNsName = "devup-run-" + req.RequestID
			if err := netns.Create(runNsName); err != nil {
				http.Error(w, "netns: "+err.Error(), http.StatusInternalServerError)
				return
			}
			defer netns.Destroy(runNsName)
			cmdArgs = append([]string{"ip", "netns", "exec", runNsName}, cmdArgs...)
		}

		runCtx, cancel := context.WithTimeout(r.Context(), maxRunTime)
		defer cancel()

		cmdEnv := mergeMiseEnv(buildEnv(req.Env), miseEnv)
		var cmd *exec.Cmd
		if usePrivateMounts {
			cmd, mountSpecPath, mountErr = buildMountNamespaceCommand(mountNamespaceExecSpec{
				JobID:   runID,
				Cmd:     cmdArgs,
				Cwd:     req.Cwd,
				Mounts:  execMounts,
				Overlay: useOverlay,
			}, cmdEnv)
			if mountErr != nil {
				http.Error(w, "mount namespace: "+mountErr.Error(), http.StatusInternalServerError)
				return
			}
		} else {
			cmd = exec.Command(cmdArgs[0], cmdArgs[1:]...)
			cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
			cmd.Env = cmdEnv
			if req.Cwd != "" {
				cmd.Dir = req.Cwd
			}
		}
		var buf bytes.Buffer
		cmd.Stdout = &buf
		cmd.Stderr = &buf

		runCgroupID := ""
		start := time.Now()
		if err := cmd.Start(); err != nil {
			if usePrivateMounts {
				cleanupMountNamespaceArtifacts(mountSpecPath, runOverlayDirs)
			}
			http.Error(w, "exec: "+err.Error(), http.StatusInternalServerError)
			return
		}
		if cgroup.Available {
			runCgroupID = runID
			if _, err := applyJobCgroup(runCgroupID, profile, req.Limits, cmd.Process.Pid, sysinfo.Read()); err != nil {
				syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
		}
		// Kill process group when context cancelled (client disconnect, timeout)
		go func() {
			<-runCtx.Done()
			if cmd.Process != nil {
				syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
			}
		}()
		waitErr := cmd.Wait()
		if runCgroupID != "" {
			cgroup.Destroy(runCgroupID)
		}
		duration := time.Since(start)
		exitCode := 0
		if waitErr != nil {
			if exitErr, ok := waitErr.(*exec.ExitError); ok {
				exitCode = exitErr.ExitCode()
			} else {
				exitCode = -1
			}
		}

		output := buf.String()
		logging.Info("run completed", "request_id", req.RequestID, "cmd", req.Cmd, "exit_code", exitCode, "duration_ms", duration.Milliseconds())

		resultsMu.Lock()
		results[req.RequestID] = &runResult{exitCode: exitCode, output: output, done: time.Now()}
		resultsMu.Unlock()

		streamResult(w, exitCode, output)
	}
}

func generateJobID() string {
	b := make([]byte, 4)
	rand.Read(b)
	return hex.EncodeToString(b)
}

func handleStart(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req api.StartRequest
	if !decodeJSONBody(w, r, &req) {
		return
	}
	profile := normalizedProfile(req.Profile, api.ProfileService)
	if len(req.Cmd) == 0 {
		http.Error(w, "cmd required", http.StatusBadRequest)
		return
	}

	// Acquire semaphore (or return 429)
	select {
	case jobSem <- struct{}{}:
		// acquired
	default:
		http.Error(w, "too many concurrent jobs", http.StatusTooManyRequests)
		return
	}
	jobID := generateJobID()
	hostStats := sysinfo.Read()
	if err := reserveStartAdmission(jobID, profile, req.Limits, hostStats); err != nil {
		<-jobSem
		http.Error(w, err.Error(), http.StatusTooManyRequests)
		return
	}
	releaseAdmission := true
	defer func() {
		if releaseAdmission {
			releaseStartAdmission(jobID)
		}
	}()

	execMounts, mountErr := prepareExecutionMounts(req.Mounts, req.Shadow)
	if mountErr != nil {
		<-jobSem
		http.Error(w, "shadow: "+mountErr.Error(), http.StatusBadRequest)
		return
	}
	execWorkdir := resolveExecutionWorkdir(execMounts, req.Cwd)
	useOverlay := req.Overlay || req.Shadow
	usePrivateMounts := privateMountNamespacesAvailable() && len(execMounts) > 0

	// Apply mounts (overlay or bind) -- mark as inflight so the reconciler
	// doesn't prune them before the job is registered.
	var mountedPaths []string
	var jobOverlays []*overlay.State
	jobOverlayDirs := overlayStateDirs(jobID, execMounts, useOverlay)
	var mountSpecPath string
	if !usePrivateMounts {
		if useOverlay {
			var mergedPaths []string
			jobOverlays, mergedPaths, mountErr = applyOverlayMounts(jobID, execMounts)
			if mountErr != nil {
				<-jobSem
				http.Error(w, "overlay: "+mountErr.Error(), http.StatusBadRequest)
				return
			}
			mountedPaths = mergedPaths
		} else {
			mountedPaths, mountErr = applyMounts(execMounts)
			if mountErr != nil {
				<-jobSem
				http.Error(w, "mount: "+mountErr.Error(), http.StatusBadRequest)
				return
			}
		}
		markInflight(mountedPaths)
		defer clearInflight(mountedPaths)
	}

	if err := ensureDefaultDirs(); err != nil {
		cleanupOverlays(jobOverlays)
		cleanupMounts(mountedPaths)
		<-jobSem
		http.Error(w, "ensure dirs: "+err.Error(), http.StatusInternalServerError)
		return
	}

	// JIT toolchain provisioning (non-fatal)
	miseEnv, miseErr := toolchain.EnsureForWorkdir(execWorkdir)
	if miseErr != nil {
		logging.Error("toolchain provision", "err", miseErr)
	}

	// Network namespace (can be used without overlay too)
	var jobNsName string
	cmdArgs := req.Cmd
	if req.NetIsolate {
		jobNsName = "devup-" + jobID
		if err := netns.Create(jobNsName); err != nil {
			cleanupOverlays(jobOverlays)
			cleanupMounts(mountedPaths)
			<-jobSem
			http.Error(w, "netns: "+err.Error(), http.StatusInternalServerError)
			return
		}
		cmdArgs = append([]string{"ip", "netns", "exec", jobNsName}, cmdArgs...)
	}
	now := time.Now().Unix()
	j := &job{
		info: api.JobInfo{
			JobID:     jobID,
			Cmd:       req.Cmd,
			Profile:   profile,
			Status:    "running",
			StartedAt: now,
			Limits:    req.Limits,
		},
		logBuf:             ringbuffer.New(ringBufferSize),
		broadcast:          make(chan []byte, broadcastChanSize),
		mounted:            mountedPaths,
		overlays:           jobOverlays,
		overlayDirs:        jobOverlayDirs,
		nsName:             jobNsName,
		privateMounts:      usePrivateMounts,
		mountNamespaceSpec: mountSpecPath,
		done:               make(chan struct{}),
		semAcquired:        true,
	}

	cmdEnv := mergeMiseEnv(buildEnv(req.Env), miseEnv)
	var cmd *exec.Cmd
	if usePrivateMounts {
		cmd, mountSpecPath, mountErr = buildMountNamespaceCommand(mountNamespaceExecSpec{
			JobID:   jobID,
			Cmd:     cmdArgs,
			Cwd:     req.Cwd,
			Mounts:  execMounts,
			Overlay: useOverlay,
		}, cmdEnv)
		if mountErr != nil {
			if jobNsName != "" {
				netns.Destroy(jobNsName)
			}
			cleanupOverlays(jobOverlays)
			cleanupMounts(mountedPaths)
			<-jobSem
			http.Error(w, "mount namespace: "+mountErr.Error(), http.StatusInternalServerError)
			return
		}
		j.mountNamespaceSpec = mountSpecPath
	} else {
		cmd = exec.Command(cmdArgs[0], cmdArgs[1:]...)
		cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
		cmd.Env = cmdEnv
		if req.Cwd != "" {
			cmd.Dir = req.Cwd
		}
	}
	if usePrivateMounts {
		cmd.Env = cmdEnv
	}

	stdoutPipe, stderrPipe, err := commandOutputPipes(cmd)
	if err != nil {
		if jobNsName != "" {
			netns.Destroy(jobNsName)
		}
		cleanupOverlays(jobOverlays)
		cleanupMounts(mountedPaths)
		cleanupMountNamespaceArtifacts(mountSpecPath, jobOverlayDirs)
		<-jobSem
		http.Error(w, "exec: "+err.Error(), http.StatusInternalServerError)
		return
	}

	if err := cmd.Start(); err != nil {
		if jobNsName != "" {
			netns.Destroy(jobNsName)
		}
		cleanupOverlays(jobOverlays)
		cleanupMounts(mountedPaths)
		cleanupMountNamespaceArtifacts(mountSpecPath, jobOverlayDirs)
		<-jobSem
		http.Error(w, "exec: "+err.Error(), http.StatusInternalServerError)
		return
	}
	j.cmd = cmd

	if cgroup.Available {
		memoryStatus, err := applyJobCgroup(jobID, profile, req.Limits, cmd.Process.Pid, hostStats)
		if err != nil {
			syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
			if jobNsName != "" {
				netns.Destroy(jobNsName)
			}
			cleanupOverlays(jobOverlays)
			cleanupMounts(mountedPaths)
			cleanupMountNamespaceArtifacts(mountSpecPath, jobOverlayDirs)
			<-jobSem
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		j.info.Memory = memoryStatus
	}

	jobsMu.Lock()
	jobs[jobID] = j
	jobsMu.Unlock()
	releaseStartAdmission(jobID)
	releaseAdmission = false

	if cgroup.Available {
		hardMaxBytes := int64(0)
		if req.Limits != nil {
			hardMaxBytes = int64(req.Limits.MemoryMB) * mib
		}
		startAdaptiveMemoryController(j, jobID, profile, hardMaxBytes)
	}

	// Start goroutines to copy stdout/stderr to ring buffer and broadcast
	go copyToBufferAndBroadcast(stdoutPipe, j.logBuf, j.broadcast)
	go copyToBufferAndBroadcast(stderrPipe, j.logBuf, j.broadcast)

	go startJobWaiter(j, jobID)

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(api.StartResponse{JobID: jobID})
}

// startJobWaiter is the single cleanup path for all long-running jobs.
// It waits for the process to exit then tears down cgroups, overlays,
// bind mounts, and network namespaces in the correct order.
func startJobWaiter(j *job, jobID string) {
	defer func() {
		if j.semAcquired {
			<-jobSem
		}
	}()
	err := j.cmd.Wait()
	now := time.Now().Unix()
	exitCode := 0
	status := "exited"
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			exitCode = exitErr.ExitCode()
		} else {
			exitCode = -1
		}
		status = "failed"
	}
	j.mu.Lock()
	j.info.Status = status
	j.info.ExitCode = exitCode
	j.info.FinishedAt = now
	j.mu.Unlock()
	close(j.done)
	if j.memoryControllerDone != nil {
		waitForDone(j.memoryControllerDone, 1*time.Second)
	}
	cgroup.Destroy(jobID)
	if j.privateMounts {
		cleanupMountNamespaceArtifacts(j.mountNamespaceSpec, j.overlayDirs)
	} else {
		cleanupOverlays(j.overlays)
		cleanupMounts(j.mounted)
	}
	if j.nsName != "" {
		netns.Destroy(j.nsName)
	}
	logging.Info("job finished", "job_id", jobID, "status", status, "exit_code", exitCode)
}

func copyToBufferAndBroadcast(r io.Reader, buf *ringbuffer.RingBuffer, broadcast chan []byte) {
	scanner := bufio.NewScanner(r)
	for scanner.Scan() {
		line := scanner.Bytes()
		toWrite := make([]byte, len(line)+1)
		copy(toWrite, line)
		toWrite[len(line)] = '\n'
		buf.Write(toWrite)
		select {
		case broadcast <- toWrite:
		default:
			// drop if slow
		}
	}
}

func handlePs(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	jobsMu.RLock()
	snapshot := make([]api.JobInfo, 0, len(jobs))
	for _, j := range jobs {
		j.mu.RLock()
		snapshot = append(snapshot, j.info)
		j.mu.RUnlock()
	}
	jobsMu.RUnlock()
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(api.PsResponse{Jobs: snapshot})
}

func handleLogs(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	jobID := r.URL.Query().Get("id")
	if jobID == "" {
		http.Error(w, "id required", http.StatusBadRequest)
		return
	}
	follow := r.URL.Query().Get("follow") == "1"

	jobsMu.RLock()
	j, ok := jobs[jobID]
	jobsMu.RUnlock()
	if !ok {
		http.Error(w, "job not found", http.StatusNotFound)
		return
	}

	j.mu.RLock()
	buf := j.logBuf.Bytes()
	j.mu.RUnlock()

	w.Header().Set("Content-Type", "text/plain")
	if follow {
		// Flush headers immediately so client doesn't hit ResponseHeaderTimeout
		// while waiting for first log output
		flusher, ok := w.(http.Flusher)
		if !ok {
			return
		}
		w.WriteHeader(200)
		flusher.Flush()
		sent := 0
		if len(buf) > 0 {
			w.Write(buf)
			sent = len(buf)
			flusher.Flush()
		}
		// Stream new output with optional keepalive
		keepalive := time.NewTicker(5 * time.Second)
		defer keepalive.Stop()
		for {
			select {
			case chunk, ok := <-j.broadcast:
				if !ok {
					flushFollowTail(w, j, &sent)
					flusher.Flush()
					return
				}
				w.Write(chunk)
				sent += len(chunk)
				flusher.Flush()
			case <-j.done:
				flushFollowTail(w, j, &sent)
				flusher.Flush()
				return
			case <-r.Context().Done():
				return
			case <-keepalive.C:
				w.Write([]byte("\n"))
				flusher.Flush()
			}
		}
	}
	if len(buf) > 0 {
		w.Write(buf)
	}
}

func flushFollowTail(w io.Writer, j *job, sent *int) {
	if w == nil || j == nil || sent == nil {
		return
	}

	j.mu.RLock()
	buf := j.logBuf.Bytes()
	j.mu.RUnlock()

	if len(buf) < *sent {
		*sent = 0
	}
	if len(buf) <= *sent {
		return
	}
	w.Write(buf[*sent:])
	*sent = len(buf)
}

func handleStop(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	jobID := r.URL.Query().Get("id")
	if jobID == "" {
		http.Error(w, "id required", http.StatusBadRequest)
		return
	}

	jobsMu.RLock()
	j, ok := jobs[jobID]
	jobsMu.RUnlock()
	if !ok {
		http.Error(w, "job not found", http.StatusNotFound)
		return
	}

	j.mu.Lock()
	status := j.info.Status
	cmd := j.cmd
	done := j.done
	j.mu.Unlock()

	// Idempotent: if already finished, return success
	if status != "running" {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(api.StopResponse{JobID: jobID, Status: status})
		return
	}

	// Signal process group; waiter does cleanup (no unmount here)
	stopProcessGroup(cmd, done, stopGracePeriod, 2*time.Second)

	// Wait for waiter to update status and cleanup
	<-done

	j.mu.Lock()
	j.info.Status = "stopped"
	if j.info.FinishedAt == 0 {
		j.info.FinishedAt = time.Now().Unix()
	}
	j.mu.Unlock()

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(api.StopResponse{JobID: jobID, Status: "stopped"})
}

func handleDown(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	jobsMu.RLock()
	toStop := make([]string, 0)
	for id, j := range jobs {
		j.mu.Lock()
		if j.info.Status == "running" {
			toStop = append(toStop, id)
		}
		j.mu.Unlock()
	}
	jobsMu.RUnlock()

	for _, id := range toStop {
		jobsMu.RLock()
		j, ok := jobs[id]
		jobsMu.RUnlock()
		if !ok {
			continue
		}
		j.mu.Lock()
		cmd := j.cmd
		done := j.done
		j.mu.Unlock()
		stopProcessGroup(cmd, done, stopGracePeriod, 2*time.Second)
		<-done
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]int{"stopped": len(toStop)})
}

func handleSystemInfo(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	tools := []struct{ Name, Cmd string }{
		{"node", "node -v"},
		{"npm", "npm -v"},
		{"python3", "python3 -V"},
		{"pip", "pip -V"},
		{"go", "go version"},
		{"ruby", "ruby -v"},
		{"java", "java -version"},
		{"cargo", "cargo -V"},
		{"rustc", "rustc -V"},
		{"php", "php -v"},
		{"composer", "COMPOSER_ALLOW_SUPERUSER=1 composer --version"},
		{"gcc", "gcc --version"},
		{"g++", "g++ --version"},
		{"make", "make --version"},
		{"cmake", "cmake --version"},
		{"pnpm", "pnpm -v"},
		{"mise", "mise -v"},
	}
	resp := api.SystemInfoResponse{Tools: make(map[string]api.ToolInfo, len(tools))}
	for _, t := range tools {
		out, err := exec.Command("bash", "-lc", t.Cmd).CombinedOutput()
		version := firstLine(strings.TrimSpace(string(out)))
		if err != nil || version == "" {
			resp.Tools[t.Name] = api.ToolInfo{Status: "missing", Version: "-"}
		} else {
			resp.Tools[t.Name] = api.ToolInfo{Status: "ok", Version: version}
		}
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

func firstLine(s string) string {
	if s == "" {
		return ""
	}
	if idx := strings.IndexByte(s, '\n'); idx >= 0 {
		return strings.TrimSpace(s[:idx])
	}
	return s
}

func janitor() {
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for range ticker.C {
		cutoff := time.Now().Add(-jobRetentionTTL).Unix()
		jobsMu.Lock()
		for id, j := range jobs {
			j.mu.Lock()
			fin := j.info.FinishedAt
			j.mu.Unlock()
			if fin > 0 && fin < cutoff {
				delete(jobs, id)
			}
		}
		jobsMu.Unlock()

		pruneStaleWorkspaces()
	}
}

func pruneStaleWorkspaces() {
	entries, err := os.ReadDir(workspacesDir)
	if err != nil {
		return
	}
	cutoff := time.Now().Add(-workspaceMaxAge)
	for _, e := range entries {
		info, err := e.Info()
		if err != nil {
			continue
		}
		if info.ModTime().Before(cutoff) {
			path := filepath.Join(workspacesDir, e.Name())
			os.RemoveAll(path)
			logging.Info("pruned stale workspace", "path", path)
		}
	}
}

func applyMounts(mounts []api.Mount) ([]string, error) {
	if len(mounts) == 0 {
		return nil, nil
	}
	var mounted []string
	for _, m := range mounts {
		hostPath := filepath.Clean(m.HostPath)
		guestPath := filepath.Clean(m.GuestPath)
		if err := validateHostPath(hostPath); err != nil {
			return nil, err
		}
		// Validate GuestPath is under /workspace
		if guestPath != "/workspace" && !strings.HasPrefix(guestPath, "/workspace/") {
			return nil, fmt.Errorf("guest_path %s must be under /workspace (e.g. /workspace or /workspace/foo)", m.GuestPath)
		}
		if err := os.MkdirAll(guestPath, 0755); err != nil {
			return nil, fmt.Errorf("mkdir %s: %w", guestPath, err)
		}
		if err := mountutil.BindMount(hostPath, guestPath, m.ReadOnly); err != nil {
			return nil, err
		}
		mounted = append(mounted, guestPath)
	}
	return mounted, nil
}

// applyOverlayMounts creates OverlayFS mounts for each requested mount.
// The host path becomes the read-only lower layer; writes land in an
// ephemeral upperdir that is destroyed on cleanup. Returns overlay states
// for cleanup and the merged paths for inflight tracking.
func applyOverlayMounts(jobID string, mounts []api.Mount) ([]*overlay.State, []string, error) {
	if len(mounts) == 0 {
		return nil, nil, nil
	}
	var states []*overlay.State
	var merged []string
	for _, m := range mounts {
		hostPath := filepath.Clean(m.HostPath)
		guestPath := filepath.Clean(m.GuestPath)
		if err := validateHostPath(hostPath); err != nil {
			return nil, nil, err
		}
		if guestPath != "/workspace" && !strings.HasPrefix(guestPath, "/workspace/") {
			return nil, nil, fmt.Errorf("guest_path %s must be under /workspace", m.GuestPath)
		}
		// Each mount gets a unique sub-ID to support multiple mounts per job.
		subID := overlaySubID(jobID, len(states), len(mounts))
		st, err := overlay.Mount(subID, hostPath, guestPath)
		if err != nil {
			// Rollback already-created overlays
			for _, prev := range states {
				overlay.Unmount(prev)
			}
			return nil, nil, err
		}
		states = append(states, st)
		merged = append(merged, guestPath)
	}
	return states, merged, nil
}

func cleanupOverlays(states []*overlay.State) {
	for _, s := range states {
		overlay.Unmount(s)
	}
}

func cleanupMounts(paths []string) {
	if paths == nil {
		return
	}
	for i := len(paths) - 1; i >= 0; i-- {
		if err := mountutil.Unmount(paths[i], 0); err != nil {
			logging.Error("umount failed (best-effort)", "path", paths[i], "err", err)
		}
	}
}

func prepareExecutionMounts(mounts []api.Mount, useShadow bool) ([]api.Mount, error) {
	if !useShadow || len(mounts) == 0 {
		return mounts, nil
	}
	execMounts := make([]api.Mount, 0, len(mounts))
	for _, m := range mounts {
		hostPath := filepath.Clean(m.HostPath)
		if hostPath == "/mnt/host" || strings.HasPrefix(hostPath, "/mnt/host/") {
			localPath, err := shadow.Materialize(hostPath)
			if err != nil {
				return nil, err
			}
			m.HostPath = localPath
		}
		execMounts = append(execMounts, m)
	}
	return execMounts, nil
}

func validateHostPath(hostPath string) error {
	if hostPath == "/mnt/host" || strings.HasPrefix(hostPath, "/mnt/host/") {
		return nil
	}
	if shadow.IsManagedPath(hostPath) {
		return nil
	}
	return fmt.Errorf("host_path %s must be under /mnt/host or %s", hostPath, shadow.DataDir)
}

// ensureDefaultDirs creates writable cache dirs for dev tools
func ensureDefaultDirs() error {
	dirs := []string{
		defaultHome,
		defaultHome + "/.cache",
		defaultNextCacheDir,
		defaultNpmCache,
	}
	for _, d := range dirs {
		if err := os.MkdirAll(d, 0755); err != nil {
			return fmt.Errorf("mkdir %s: %w", d, err)
		}
	}
	return nil
}

func decodeJSONBody(w http.ResponseWriter, r *http.Request, dst any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, jsonRequestMaxBytes)
	if err := json.NewDecoder(r.Body).Decode(dst); err != nil {
		var maxBytesErr *http.MaxBytesError
		if errors.As(err, &maxBytesErr) {
			http.Error(w, fmt.Sprintf("request too large (max %d bytes)", jsonRequestMaxBytes), http.StatusRequestEntityTooLarge)
			return false
		}
		http.Error(w, "bad request: "+err.Error(), http.StatusBadRequest)
		return false
	}
	return true
}

func commandOutputPipes(cmd *exec.Cmd) (io.ReadCloser, io.ReadCloser, error) {
	stdoutPipe, err := cmd.StdoutPipe()
	if err != nil {
		return nil, nil, fmt.Errorf("stdout pipe: %w", err)
	}
	stderrPipe, err := cmd.StderrPipe()
	if err != nil {
		return nil, nil, fmt.Errorf("stderr pipe: %w", err)
	}
	return stdoutPipe, stderrPipe, nil
}

func stopProcessGroup(cmd *exec.Cmd, done <-chan struct{}, gracePeriod, killWait time.Duration) {
	if cmd == nil || cmd.Process == nil {
		return
	}
	pid := cmd.Process.Pid
	_ = syscall.Kill(-pid, syscall.SIGTERM)
	if waitForDone(done, gracePeriod) {
		return
	}
	_ = syscall.Kill(-pid, syscall.SIGKILL)
	waitForDone(done, killWait)
}

func waitForDone(done <-chan struct{}, timeout time.Duration) bool {
	if done == nil {
		return false
	}
	select {
	case <-done:
		return true
	default:
	}
	if timeout <= 0 {
		return false
	}

	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case <-done:
		return true
	case <-timer.C:
		return false
	}
}

// buildEnv merges base env, defaults (if missing), and request env.
// Order: base (os.Environ) -> defaults (only if not in base) -> request (wins)
func buildEnv(requestEnv map[string]string) []string {
	base := make(map[string]string)
	for _, e := range os.Environ() {
		if idx := strings.IndexByte(e, '='); idx > 0 {
			base[e[:idx]] = e[idx+1:]
		}
	}
	// Apply defaults for dev tools. Override macOS paths (/Users/...) which don't exist in the Linux VM.
	defaultKeys := map[string]string{
		"HOME":             defaultHome,
		"XDG_CACHE_HOME":   defaultHome + "/.cache",
		"NEXT_CACHE_DIR":   defaultNextCacheDir,
		"NPM_CONFIG_CACHE": defaultNpmCache,
	}
	for k, v := range defaultKeys {
		current := base[k]
		if rv, ok := requestEnv[k]; ok {
			current = rv
		}
		// Use default if unset or if value is macOS path (invalid in Linux VM)
		if current == "" || strings.HasPrefix(current, "/Users/") {
			base[k] = v
		} else {
			base[k] = current
		}
	}
	// Request env wins, except skip macOS paths for cache-related keys (would overwrite our defaults)
	for k, v := range requestEnv {
		if _, isDefaultKey := defaultKeys[k]; isDefaultKey && strings.HasPrefix(v, "/Users/") {
			continue
		}
		base[k] = v
	}
	out := make([]string, 0, len(base))
	for k, v := range base {
		out = append(out, k+"="+v)
	}
	return out
}

// mergeMiseEnv overlays mise-provided env vars onto an existing env slice.
// PATH values are prepended to the existing PATH rather than replacing it.
func mergeMiseEnv(env []string, miseEnv map[string]string) []string {
	if len(miseEnv) == 0 {
		return env
	}
	existing := make(map[string]int, len(env))
	for i, e := range env {
		if idx := strings.IndexByte(e, '='); idx > 0 {
			existing[e[:idx]] = i
		}
	}
	for k, v := range miseEnv {
		if k == "PATH" {
			if i, ok := existing["PATH"]; ok {
				oldVal := env[i][len("PATH="):]
				env[i] = "PATH=" + v + ":" + oldVal
				continue
			}
		}
		if i, ok := existing[k]; ok {
			env[i] = k + "=" + v
		} else {
			env = append(env, k+"="+v)
		}
	}
	return env
}

func markInflight(paths []string) {
	inflightMountsMu.Lock()
	for _, p := range paths {
		inflightMounts[p] = true
	}
	inflightMountsMu.Unlock()
}

func clearInflight(paths []string) {
	inflightMountsMu.Lock()
	for _, p := range paths {
		delete(inflightMounts, p)
	}
	inflightMountsMu.Unlock()
}

// reconcileMounts parses /proc/mounts and unmounts any bind mount under /workspace
// that is not owned by a running job or in-flight startup. Uses MNT_DETACH (lazy
// unmount) to avoid "device is busy" errors when zombie processes hold file handles.
func reconcileMounts() {
	data, err := os.ReadFile("/proc/mounts")
	if err != nil {
		return
	}

	// Collect mount points owned by running jobs
	ownedMounts := make(map[string]bool)
	jobsMu.RLock()
	for _, j := range jobs {
		j.mu.RLock()
		running := j.info.Status == "running"
		j.mu.RUnlock()
		if running {
			for _, m := range j.mounted {
				ownedMounts[m] = true
			}
		}
	}
	jobsMu.RUnlock()

	// Also protect mounts that are between applyMounts and job registration
	inflightMountsMu.Lock()
	for p := range inflightMounts {
		ownedMounts[p] = true
	}
	inflightMountsMu.Unlock()

	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		mountpoint := fields[1]
		if mountpoint != "/workspace" && !strings.HasPrefix(mountpoint, "/workspace/") {
			continue
		}
		if ownedMounts[mountpoint] {
			continue
		}
		// MNT_DETACH (0x2): immediately removes from namespace, waits for in-use fds to close
		if err := syscall.Unmount(mountpoint, 0x2); err != nil {
			logging.Error("reconcile: unmount failed", "path", mountpoint, "err", err)
		} else {
			logging.Info("reconcile: pruned stale mount", "path", mountpoint)
			os.Remove(mountpoint)
		}
	}
}

// reconcileLoop is the unified garbage collector. Every 30 seconds it
// reconciles four types of kernel objects: bind mounts, overlay mounts,
// cgroups, and network namespaces.
func reconcileLoop() {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for range ticker.C {
		reconcileMounts()
		reconcileOverlays()
		reconcileCgroups()
		reconcileNetns()
	}
}

func activeJobIDs() map[string]bool {
	ids := make(map[string]bool)
	jobsMu.RLock()
	for id, j := range jobs {
		j.mu.RLock()
		if j.info.Status == "running" {
			ids[id] = true
		}
		j.mu.RUnlock()
	}
	jobsMu.RUnlock()
	return ids
}

func reconcileCgroups() {
	cgroup.Reconcile(activeJobIDs())
}

func reconcileOverlays() {
	overlay.Reconcile(activeJobIDs())
}

func reconcileNetns() {
	netns.Reconcile(activeJobIDs())
}

func streamResult(w http.ResponseWriter, exitCode int, output string) {
	w.Header().Set("Transfer-Encoding", "chunked")
	w.Header().Set("Content-Type", "text/plain")
	if output != "" {
		w.Write([]byte(output))
		if !strings.HasSuffix(output, "\n") {
			w.Write([]byte("\n"))
		}
	}
	w.Write([]byte(exitCodePrefix + fmt.Sprintf("%d", exitCode) + "\n"))
}
