package main

import (
	"context"
	_ "embed"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"devup/internal/api"
	"devup/internal/appfile"
	"devup/internal/client"
	"devup/internal/config"
	"devup/internal/mounts"
	"devup/internal/scheduler"
	"devup/internal/tui/dashboard"
	"devup/internal/util"
	"devup/internal/version"
	"devup/internal/vm"
)

//go:generate cp ../../scripts/vm-provision.sh provision.sh
//go:embed provision.sh
var provisionScript string

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(1)
	}
	verbose := hasFlag("--verbose") || os.Getenv("DEVUP_VERBOSE") == "1"

	switch os.Args[1] {
	case "vm":
		runVM(os.Args[2:], verbose)
	case "dev":
		runDev(os.Args[2:], verbose)
	case "app":
		runApp(os.Args[2:], verbose)
	case "dashboard":
		runDashboard(os.Args[2:], verbose)
	case "ui":
		runDashboard(os.Args[2:], verbose)
	case "run":
		runRun(os.Args[2:], verbose)
	case "start":
		runStart(os.Args[2:], verbose)
	case "ps":
		runPs(os.Args[2:], verbose)
	case "logs":
		runLogs(os.Args[2:], verbose)
	case "stop":
		runStop(os.Args[2:], verbose)
	case "down":
		runDown(verbose)
	case "version", "--version":
		fmt.Println("devup", version.Version)
	default:
		usage()
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, "Usage: devup <command> [options]")
	fmt.Fprintln(os.Stderr, "")
	fmt.Fprintln(os.Stderr, "Commands:")
	fmt.Fprintln(os.Stderr, "  vm up          Start the Lima VM and agent")
	fmt.Fprintln(os.Stderr, "  vm down        Stop the Lima VM")
	fmt.Fprintln(os.Stderr, "  vm shell       Open a shell in the VM")
	fmt.Fprintln(os.Stderr, "  vm status      Show VM and agent status")
	fmt.Fprintln(os.Stderr, "  vm logs        Show agent logs")
	fmt.Fprintln(os.Stderr, "  vm reset-token Regenerate auth token (restart VM after)")
	fmt.Fprintln(os.Stderr, "  vm provision  Install base toolchains (Node, Python, Go, C/C++)")
	fmt.Fprintln(os.Stderr, "  vm doctor     Check toolchain versions")
	fmt.Fprintln(os.Stderr, "  dashboard      Interactive TUI (alias: ui)")
	fmt.Fprintln(os.Stderr, "  dev [-f]       Start dev server (Node.js); -f to follow logs")
	fmt.Fprintln(os.Stderr, "  app up         Start services from a devup app manifest")
	fmt.Fprintln(os.Stderr, "  app down       Stop services from a devup app manifest")
	fmt.Fprintln(os.Stderr, "  app ps         Show service state from a devup app manifest")
	fmt.Fprintln(os.Stderr, "  app logs       Show logs for a manifest service")
	fmt.Fprintln(os.Stderr, "  run [options] -- <cmd>  Run command (ephemeral)")
	fmt.Fprintln(os.Stderr, "  start [options] -- <cmd>  Start background job")
	fmt.Fprintln(os.Stderr, "  ps [--cluster] List jobs (--cluster shows discovered nodes)")
	fmt.Fprintln(os.Stderr, "  logs [id] [-f] Show job logs (id optional: uses last job)")
	fmt.Fprintln(os.Stderr, "  stop [id]      Stop a job (id optional: uses last job); --all to stop all")
	fmt.Fprintln(os.Stderr, "  down           Stop all jobs (not the VM)")
	fmt.Fprintln(os.Stderr, "  version        Print version")
	fmt.Fprintln(os.Stderr, "")
	fmt.Fprintln(os.Stderr, "Examples:")
	fmt.Fprintln(os.Stderr, "  devup dev -f           Start dev server and follow logs")
	fmt.Fprintln(os.Stderr, "  devup logs -f           Follow logs of last started job")
	fmt.Fprintln(os.Stderr, "  devup stop              Stop last started job")
	fmt.Fprintln(os.Stderr, "  devup app up            Start all services from devup.app.yaml")
	fmt.Fprintln(os.Stderr, "")
	fmt.Fprintln(os.Stderr, "Run/Start Options:")
	fmt.Fprintln(os.Stderr, "  --mount host:guest  Bind mount (default .:/workspace)")
	fmt.Fprintln(os.Stderr, "  --workdir path      Working directory (default /workspace)")
	fmt.Fprintln(os.Stderr, "  --profile name     Workload profile: batch|service|interactive")
	fmt.Fprintln(os.Stderr, "  --memory MB         Memory limit (cgroups v2)")
	fmt.Fprintln(os.Stderr, "  --cpu %             CPU limit (cgroups v2)")
	fmt.Fprintln(os.Stderr, "  --pids N            PID limit (cgroups v2)")
	fmt.Fprintln(os.Stderr, "  --overlay           OverlayFS isolation (host files read-only)")
	fmt.Fprintln(os.Stderr, "  --shadow            Run from a VM-local shadow workspace (implies --overlay)")
	fmt.Fprintln(os.Stderr, "  --net-isolate       Network namespace isolation")
	fmt.Fprintln(os.Stderr, "  --isolate           Enable both --overlay and --net-isolate")
	fmt.Fprintln(os.Stderr, "  --cluster           Schedule on best available node in the cluster")
	fmt.Fprintln(os.Stderr, "")
	fmt.Fprintln(os.Stderr, "Global Options:")
	fmt.Fprintln(os.Stderr, "  --verbose           Print detailed commands")
	fmt.Fprintln(os.Stderr, "Dashboard Options:")
	fmt.Fprintln(os.Stderr, "  --bench-file path   Benchmark JSON to show in the dashboard")
}

func hasFlag(name string) bool {
	for _, a := range os.Args {
		if a == name {
			return true
		}
	}
	return false
}

// ensureReady is the single setup path shared by all commands that talk to the agent.
// It checks platform, loads config, ensures limactl, starts the VM, and returns a client.
func ensureReady(verbose bool, quiet bool) (context.Context, *config.Config, *client.Client, error) {
	if !vm.IsDarwin() {
		vm.LinuxHint()
	}
	cfg, err := config.Load()
	if err != nil {
		cfg, err = config.Create()
		if err != nil {
			return nil, nil, nil, fmt.Errorf("config: %w", err)
		}
	}
	vm.EnsureLimactl(verbose)
	configPath := vm.FindLimaConfig()
	ctx := context.Background()
	if err := vm.Up(ctx, configPath, cfg.Token, verbose, quiet); err != nil {
		return nil, nil, nil, err
	}
	return ctx, cfg, client.New(cfg.Token), nil
}

func runVM(args []string, verbose bool) {
	if len(args) < 1 {
		fmt.Fprintln(os.Stderr, "Usage: devup vm {up|down|shell|status|logs|reset-token|provision|doctor}")
		os.Exit(1)
	}
	if !vm.IsDarwin() {
		vm.LinuxHint()
	}
	switch args[0] {
	case "up":
		if _, _, _, err := ensureReady(verbose, false); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		fmt.Println("devup VM is up. Run: devup run -- echo hello")
	case "down":
		vm.EnsureLimactl(verbose)
		if err := vm.Down(context.Background(), verbose); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
	case "shell":
		vm.EnsureLimactl(verbose)
		if err := vm.Shell(context.Background()); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
	case "status":
		vm.EnsureLimactl(verbose)
		cfg, err := config.Load()
		if err != nil {
			cfg, err = config.Create()
			if err != nil {
				fmt.Fprintln(os.Stderr, "config:", err)
				os.Exit(1)
			}
		}
		if err := vm.Status(context.Background(), cfg.Token); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
	case "logs":
		vm.EnsureLimactl(verbose)
		if err := vm.Logs(context.Background()); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
	case "reset-token":
		cfg, err := config.Load()
		if err != nil {
			cfg, err = config.Create()
			if err != nil {
				fmt.Fprintln(os.Stderr, "config:", err)
				os.Exit(1)
			}
		}
		if err := cfg.ResetToken(); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		fmt.Println("Token regenerated. Restart the VM: devup vm down && devup vm up")
	case "provision":
		runVMProvision(verbose)
	case "doctor":
		runVMDoctor(verbose)
	default:
		fmt.Fprintln(os.Stderr, "Unknown vm command:", args[0])
		os.Exit(1)
	}
}

func runRun(args []string, verbose bool) {
	idx := findDoubleDash(args)
	if idx < 0 || idx+1 >= len(args) {
		fmt.Fprintln(os.Stderr, "Usage: devup run [--mount host:guest]... [--workdir path] [--profile name] [--memory MB] [--cpu %] [--pids N] [--cluster] -- <cmd> [args...]")
		fmt.Fprintln(os.Stderr, "Example: devup run -- echo hello")
		fmt.Fprintln(os.Stderr, "Example: devup run --cluster -- make build")
		os.Exit(1)
	}
	flags := args[:idx]
	cmd := args[idx+1:]

	opts, err := parseRunStartFlags(flags)
	if err != nil {
		fmt.Fprintln(os.Stderr, "For V1, mounts must be within your home directory so Lima can share them.")
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if opts.Profile == "" {
		opts.Profile = api.ProfileBatch
	}

	ctx, cfg, c, err := ensureReady(verbose, false)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}

	if opts.Cluster {
		exitCode := runRunCluster(ctx, cfg, c, cmd, opts, verbose)
		os.Exit(exitCode)
	}

	req := &api.RunRequest{
		RequestID:  util.GenerateRequestID(),
		Cmd:        cmd,
		Env:        util.EnvMap(),
		Cwd:        opts.Workdir,
		Profile:    opts.Profile,
		Mounts:     opts.Mounts,
		Limits:     opts.Limits,
		Overlay:    opts.Overlay,
		Shadow:     opts.Shadow,
		NetIsolate: opts.NetIsolate,
	}
	exitCode, err := c.Run(ctx, req, os.Stdout)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	os.Exit(exitCode)
}

func runStart(args []string, verbose bool) {
	idx := findDoubleDash(args)
	if idx < 0 || idx+1 >= len(args) {
		fmt.Fprintln(os.Stderr, "Usage: devup start [--mount host:guest]... [--workdir path] [--profile name] [--memory MB] [--cpu %] [--pids N] [--cluster] -- <cmd> [args...]")
		fmt.Fprintln(os.Stderr, "Example: devup start --cluster -- npm run dev")
		os.Exit(1)
	}
	flags := args[:idx]
	cmd := args[idx+1:]

	opts, err := parseRunStartFlags(flags)
	if err != nil {
		fmt.Fprintln(os.Stderr, "For V1, mounts must be within your home directory so Lima can share them.")
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if opts.Profile == "" {
		opts.Profile = api.ProfileService
	}

	ctx, cfg, c, err := ensureReady(verbose, false)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}

	if opts.Cluster {
		runStartCluster(ctx, cfg, c, cmd, opts, verbose)
		return
	}

	req := &api.StartRequest{
		RequestID:  util.GenerateRequestID(),
		Cmd:        cmd,
		Env:        util.EnvMap(),
		Cwd:        opts.Workdir,
		Profile:    opts.Profile,
		Mounts:     opts.Mounts,
		Limits:     opts.Limits,
		Overlay:    opts.Overlay,
		Shadow:     opts.Shadow,
		NetIsolate: opts.NetIsolate,
	}
	jobID, err := c.Start(ctx, req)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if err := config.WriteLastJob(jobID); err != nil {
		fmt.Fprintln(os.Stderr, "warning: could not save last job:", err)
	}
	fmt.Println(jobID)
}

// clusterPeers fetches the cluster and returns ranked peers, or exits on error.
func clusterPeers(ctx context.Context, c *client.Client) []api.PeerInfo {
	peers, err := c.Cluster(ctx)
	if err != nil {
		fmt.Fprintln(os.Stderr, "cluster:", err)
		os.Exit(1)
	}
	localID := ""
	for _, p := range peers {
		if p.Status == "local" {
			localID = p.NodeID
			break
		}
	}
	ranked := scheduler.Rank(peers, localID)
	if len(ranked) == 0 {
		fmt.Fprintln(os.Stderr, "cluster: no nodes with free slots")
		os.Exit(1)
	}
	return ranked
}

func isRetryable(err error) bool {
	if err == nil {
		return false
	}
	s := err.Error()
	return strings.Contains(s, "too many concurrent jobs") ||
		strings.Contains(s, "429") ||
		strings.Contains(s, "connection refused") ||
		strings.Contains(s, "connect: connection refused") ||
		strings.Contains(s, "i/o timeout")
}

func clientForPeer(p api.PeerInfo, token string) *client.Client {
	if p.Status == "local" {
		return client.New(token)
	}
	return client.NewWithAddr(p.Addr, p.Port, token)
}

func runRunCluster(ctx context.Context, cfg *config.Config, localClient *client.Client, cmd []string, opts *runStartOpts, verbose bool) int {
	ranked := clusterPeers(ctx, localClient)

	cwd, _ := os.Getwd()

	for i, peer := range ranked {
		if verbose {
			fmt.Fprintf(os.Stderr, "[cluster] trying %s (%s:%d, slots=%d, mem=%dMB)\n",
				peer.NodeID, peer.Addr, peer.Port, peer.SlotsFree, peer.MemFreeMB)
		}

		c := clientForPeer(peer, cfg.Token)
		var workdir string
		var reqMounts []api.Mount

		if peer.Status == "local" {
			workdir = opts.Workdir
			reqMounts = opts.Mounts
		} else {
			wsPath, err := c.Upload(ctx, cwd)
			if err != nil {
				fmt.Fprintf(os.Stderr, "[cluster] upload to %s failed: %v\n", peer.NodeID, err)
				if i < len(ranked)-1 {
					continue
				}
				return 1
			}
			workdir = wsPath
			reqMounts = nil
		}

		req := &api.RunRequest{
			RequestID:  util.GenerateRequestID(),
			Cmd:        cmd,
			Env:        util.EnvMap(),
			Cwd:        workdir,
			Profile:    opts.Profile,
			Mounts:     reqMounts,
			Limits:     opts.Limits,
			Overlay:    opts.Overlay,
			Shadow:     opts.Shadow && peer.Status == "local",
			NetIsolate: opts.NetIsolate,
		}

		exitCode, err := c.Run(ctx, req, os.Stdout)
		if err != nil {
			if isRetryable(err) && i < len(ranked)-1 {
				fmt.Fprintf(os.Stderr, "[cluster] %s rejected, trying next node\n", peer.NodeID)
				continue
			}
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		if verbose {
			fmt.Fprintf(os.Stderr, "[cluster] ran on %s\n", peer.NodeID)
		}
		return exitCode
	}

	fmt.Fprintln(os.Stderr, "cluster: all nodes exhausted")
	return 1
}

func runStartCluster(ctx context.Context, cfg *config.Config, localClient *client.Client, cmd []string, opts *runStartOpts, verbose bool) {
	ranked := clusterPeers(ctx, localClient)

	cwd, _ := os.Getwd()

	for i, peer := range ranked {
		if verbose {
			fmt.Fprintf(os.Stderr, "[cluster] trying %s (%s:%d, slots=%d, mem=%dMB)\n",
				peer.NodeID, peer.Addr, peer.Port, peer.SlotsFree, peer.MemFreeMB)
		}

		c := clientForPeer(peer, cfg.Token)
		var workdir string
		var reqMounts []api.Mount

		if peer.Status == "local" {
			workdir = opts.Workdir
			reqMounts = opts.Mounts
		} else {
			wsPath, err := c.Upload(ctx, cwd)
			if err != nil {
				fmt.Fprintf(os.Stderr, "[cluster] upload to %s failed: %v\n", peer.NodeID, err)
				if i < len(ranked)-1 {
					continue
				}
				fmt.Fprintln(os.Stderr, "cluster: all nodes exhausted")
				os.Exit(1)
			}
			workdir = wsPath
			reqMounts = nil
		}

		req := &api.StartRequest{
			RequestID:  util.GenerateRequestID(),
			Cmd:        cmd,
			Env:        util.EnvMap(),
			Cwd:        workdir,
			Profile:    opts.Profile,
			Mounts:     reqMounts,
			Limits:     opts.Limits,
			Overlay:    opts.Overlay,
			Shadow:     opts.Shadow && peer.Status == "local",
			NetIsolate: opts.NetIsolate,
		}

		jobID, err := c.Start(ctx, req)
		if err != nil {
			if isRetryable(err) && i < len(ranked)-1 {
				fmt.Fprintf(os.Stderr, "[cluster] %s rejected, trying next node\n", peer.NodeID)
				continue
			}
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		if err := config.WriteLastJob(jobID); err != nil {
			fmt.Fprintln(os.Stderr, "warning: could not save last job:", err)
		}
		if verbose {
			fmt.Fprintf(os.Stderr, "[cluster] started on %s\n", peer.NodeID)
		}
		fmt.Println(jobID)
		return
	}

	fmt.Fprintln(os.Stderr, "cluster: all nodes exhausted")
	os.Exit(1)
}

func findDoubleDash(args []string) int {
	for i, a := range args {
		if a == "--" {
			return i
		}
	}
	return -1
}

type runStartOpts struct {
	Mounts     []api.Mount
	Workdir    string
	Profile    string
	Limits     *api.ResourceLimits
	Overlay    bool
	Shadow     bool
	NetIsolate bool
	Cluster    bool
}

func parseRunStartFlags(flags []string) (*runStartOpts, error) {
	mountList, err := mounts.ParseMountsFromFlags(flags)
	if err != nil {
		return nil, err
	}
	if len(mountList) == 0 {
		mountList, err = mounts.DefaultMounts()
		if err != nil {
			return nil, err
		}
	}
	opts := &runStartOpts{
		Mounts:  mountList,
		Workdir: "/workspace",
	}
	var limits api.ResourceLimits
	hasLimits := false
	for i := 0; i < len(flags); i++ {
		switch flags[i] {
		case "--workdir":
			if i+1 < len(flags) {
				opts.Workdir = flags[i+1]
				i++
			}
		case "--profile":
			if i+1 < len(flags) {
				opts.Profile = api.NormalizeProfile(flags[i+1])
				i++
			}
		case "--memory":
			if i+1 < len(flags) {
				if n, err := strconv.Atoi(flags[i+1]); err == nil && n > 0 {
					limits.MemoryMB = n
					hasLimits = true
				}
				i++
			}
		case "--cpu":
			if i+1 < len(flags) {
				if n, err := strconv.Atoi(flags[i+1]); err == nil && n > 0 {
					limits.CPUPercent = n
					hasLimits = true
				}
				i++
			}
		case "--pids":
			if i+1 < len(flags) {
				if n, err := strconv.Atoi(flags[i+1]); err == nil && n > 0 {
					limits.PidsMax = n
					hasLimits = true
				}
				i++
			}
		case "--overlay":
			opts.Overlay = true
		case "--shadow":
			opts.Shadow = true
			opts.Overlay = true
		case "--net-isolate":
			opts.NetIsolate = true
		case "--isolate":
			opts.Overlay = true
			opts.NetIsolate = true
		case "--cluster":
			opts.Cluster = true
		}
	}
	if hasLimits {
		opts.Limits = &limits
	}
	return opts, nil
}

func runPs(args []string, verbose bool) {
	cluster := false
	for _, a := range args {
		if a == "--cluster" {
			cluster = true
		}
	}

	ctx, _, c, err := ensureReady(verbose, false)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}

	if cluster {
		runPsCluster(ctx, c)
		return
	}

	jobs, err := c.Ps(ctx)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}

	now := time.Now().Unix()
	if verbose {
		fmt.Printf("%-10s %-10s %-12s %-10s %-22s %-12s %s\n", "JOB_ID", "STATUS", "PROFILE", "UPTIME", "MEMORY", "LIMITS", "CMD")
	} else {
		fmt.Printf("%-10s %-10s %-10s %-12s %s\n", "JOB_ID", "STATUS", "UPTIME", "MEM/CPU", "CMD")
	}
	for _, j := range jobs {
		uptime := formatUptime(j.StartedAt, j.FinishedAt, now)
		cmdStr := strings.Join(j.Cmd, " ")
		if len(cmdStr) > 50 {
			cmdStr = cmdStr[:47] + "..."
		}
		limitsStr := formatJobLimits(j.Limits)
		if verbose {
			fmt.Printf("%-10s %-10s %-12s %-10s %-22s %-12s %s\n", j.JobID, j.Status, formatJobProfile(j.Profile), uptime, formatJobMemory(j.Memory), limitsStr, cmdStr)
			continue
		}
		fmt.Printf("%-10s %-10s %-10s %-12s %s\n", j.JobID, j.Status, uptime, limitsStr, cmdStr)
	}
}

func runPsCluster(ctx context.Context, c *client.Client) {
	peers, err := c.Cluster(ctx)
	if err != nil {
		fmt.Fprintln(os.Stderr, "cluster:", err)
		os.Exit(1)
	}

	fmt.Printf("%-20s %-10s %-10s %-12s %-12s %-8s %s\n",
		"NODE", "STATUS", "VERSION", "SLOTS_FREE", "RAM_FREE", "LOAD", "ACTIVE_JOBS")
	for _, p := range peers {
		name := p.NodeID
		if p.Status == "local" {
			name += " (local)"
		}
		ramStr := "-"
		if p.MemTotalMB > 0 {
			ramStr = fmt.Sprintf("%dM/%dM", p.MemFreeMB, p.MemTotalMB)
		}
		loadStr := "-"
		if p.LoadAvg1 > 0 {
			loadStr = fmt.Sprintf("%.1f", p.LoadAvg1)
		}
		fmt.Printf("%-20s %-10s %-10s %-12d %-12s %-8s %d\n",
			name, p.Status, p.Version, p.SlotsFree, ramStr, loadStr, p.ActiveJobs)
	}
}

func formatUptime(started, finished, now int64) string {
	end := now
	if finished > 0 {
		end = finished
	}
	sec := end - started
	if sec < 0 {
		return "0s"
	}
	if sec < 60 {
		return fmt.Sprintf("%ds", sec)
	}
	if sec < 3600 {
		return fmt.Sprintf("%dm%ds", sec/60, sec%60)
	}
	return fmt.Sprintf("%dh%dm", sec/3600, (sec%3600)/60)
}

func formatJobLimits(limits *api.ResourceLimits) string {
	if limits == nil {
		return "-"
	}

	parts := make([]string, 0, 3)
	if limits.MemoryMB > 0 {
		parts = append(parts, fmt.Sprintf("%dM", limits.MemoryMB))
	}
	if limits.CPUPercent > 0 {
		parts = append(parts, fmt.Sprintf("%d%%", limits.CPUPercent))
	}
	if limits.PidsMax > 0 {
		parts = append(parts, fmt.Sprintf("%dp", limits.PidsMax))
	}
	if len(parts) == 0 {
		return "-"
	}
	return strings.Join(parts, "/")
}

func formatJobProfile(profile string) string {
	if profile == "" {
		return api.ProfileService
	}
	return profile
}

func formatJobMemory(memory *api.MemoryStatus) string {
	if memory == nil {
		return "-"
	}

	if memory.CurrentMB > 0 || memory.LowMB > 0 || memory.HighMB > 0 || memory.MaxMB > 0 {
		maxPart := "max"
		if memory.MaxMB > 0 {
			maxPart = fmt.Sprintf("%dM", memory.MaxMB)
		}
		return fmt.Sprintf("%d/%d/%d/%s", memory.CurrentMB, memory.LowMB, memory.HighMB, maxPart)
	}
	if memory.Adaptive {
		return "adaptive"
	}
	return "-"
}

func runLogs(args []string, verbose bool) {
	jobID, follow := parseLogsArgs(args)
	if jobID == "" {
		var err error
		jobID, err = config.ReadLastJob()
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
	}

	ctx, _, c, err := ensureReady(verbose, false)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if err := c.Logs(ctx, jobID, follow, os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func parseLogsArgs(args []string) (jobID string, follow bool) {
	for _, a := range args {
		switch {
		case a == "--follow" || a == "-f":
			follow = true
		case a != "" && !strings.HasPrefix(a, "-") && jobID == "":
			jobID = a
		}
	}
	return jobID, follow
}

func runStop(args []string, verbose bool) {
	for _, a := range args {
		if a == "--all" {
			runDown(verbose)
			return
		}
	}
	var jobID string
	for _, a := range args {
		if a != "" && a != "--all" && !strings.HasPrefix(a, "-") {
			jobID = a
			break
		}
	}
	if jobID == "" {
		var err error
		jobID, err = config.ReadLastJob()
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
	}

	ctx, _, c, err := ensureReady(verbose, false)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if err := c.Stop(ctx, jobID); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fmt.Printf("Stopped job %s\n", jobID)
}

func runApp(args []string, verbose bool) {
	if len(args) < 1 {
		fmt.Fprintln(os.Stderr, "Usage: devup app {up|down|ps|logs} [options]")
		os.Exit(1)
	}
	switch args[0] {
	case "up":
		runAppUp(args[1:], verbose)
	case "down":
		runAppDown(args[1:], verbose)
	case "ps":
		runAppPs(args[1:], verbose)
	case "logs":
		runAppLogs(args[1:], verbose)
	default:
		fmt.Fprintln(os.Stderr, "Unknown app command:", args[0])
		os.Exit(1)
	}
}

func runAppUp(args []string, verbose bool) {
	manifestPath, targets, err := parseAppFlags(args)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	app, err := loadResolvedApp(manifestPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	services, err := app.StartOrder(targets)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	ctx, _, c, err := ensureReady(verbose, false)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	jobs, err := c.Ps(ctx)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	jobIndex := jobsByID(jobs)
	state, _ := config.ReadAppState(app.ManifestPath)
	if state == nil {
		state = &config.AppState{
			Name:         app.Name,
			ManifestPath: app.ManifestPath,
			Services:     make(map[string]config.AppServiceState),
		}
	}
	started := make([]config.AppServiceState, 0, len(services))
	lastStarted := ""
	for _, svc := range services {
		if entry, ok := state.Services[svc.Name]; ok {
			if job, ok := jobIndex[entry.JobID]; ok && job.Status == "running" {
				fmt.Printf("%s already running as %s\n", svc.Name, entry.JobID)
				continue
			}
		}
		req := &api.StartRequest{
			RequestID:  util.GenerateRequestID(),
			Cmd:        svc.Cmd,
			Env:        mergedEnv(svc.Env),
			Cwd:        svc.Workdir,
			Profile:    svc.Profile,
			Mounts:     svc.Mounts,
			Limits:     svc.Limits,
			Overlay:    svc.Overlay,
			Shadow:     svc.Shadow,
			NetIsolate: svc.NetIsolate,
		}
		jobID, err := c.Start(ctx, req)
		if err != nil {
			rollbackAppStarts(ctx, c, started)
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		fmt.Printf("%s %s\n", svc.Name, jobID)
		lastStarted = jobID
		state.Services[svc.Name] = config.AppServiceState{
			JobID:     jobID,
			Profile:   svc.Profile,
			Cmd:       append([]string(nil), svc.Cmd...),
			StartedAt: time.Now().Unix(),
		}
		started = append(started, state.Services[svc.Name])
	}
	state.Name = app.Name
	state.ManifestPath = app.ManifestPath
	state.UpdatedAt = time.Now().Unix()
	if len(state.Services) == 0 {
		if err := config.DeleteAppState(app.ManifestPath); err != nil {
			fmt.Fprintln(os.Stderr, "warning: could not remove app state:", err)
		}
	} else if err := config.WriteAppState(app.ManifestPath, state); err != nil {
		fmt.Fprintln(os.Stderr, "warning: could not save app state:", err)
	}
	if lastStarted != "" {
		if err := config.WriteLastJob(lastStarted); err != nil {
			fmt.Fprintln(os.Stderr, "warning: could not save last job:", err)
		}
	}
}

func runAppDown(args []string, verbose bool) {
	manifestPath, targets, err := parseAppFlags(args)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	app, err := loadResolvedApp(manifestPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	state, err := config.ReadAppState(app.ManifestPath)
	if err != nil {
		fmt.Println("No tracked app services.")
		return
	}
	services, err := app.ExactOrder(targets, true)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	ctx, _, c, err := ensureReady(verbose, false)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	for _, svc := range services {
		entry, ok := state.Services[svc.Name]
		if !ok || entry.JobID == "" {
			continue
		}
		if err := c.Stop(ctx, entry.JobID); err != nil && !strings.Contains(err.Error(), "job not found") {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		fmt.Printf("Stopped %s (%s)\n", svc.Name, entry.JobID)
		delete(state.Services, svc.Name)
	}
	if len(targets) == 0 {
		for name, entry := range state.Services {
			if entry.JobID == "" {
				delete(state.Services, name)
				continue
			}
			if err := c.Stop(ctx, entry.JobID); err != nil && !strings.Contains(err.Error(), "job not found") {
				fmt.Fprintln(os.Stderr, err)
				os.Exit(1)
			}
			fmt.Printf("Stopped %s (%s)\n", name, entry.JobID)
			delete(state.Services, name)
		}
	}
	if len(state.Services) == 0 {
		if err := config.DeleteAppState(app.ManifestPath); err != nil {
			fmt.Fprintln(os.Stderr, "warning: could not remove app state:", err)
		}
		return
	}
	state.UpdatedAt = time.Now().Unix()
	if err := config.WriteAppState(app.ManifestPath, state); err != nil {
		fmt.Fprintln(os.Stderr, "warning: could not save app state:", err)
	}
}

func runAppPs(args []string, verbose bool) {
	manifestPath, targets, err := parseAppFlags(args)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	app, err := loadResolvedApp(manifestPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	services, err := app.ExactOrder(targets, false)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	state, _ := config.ReadAppState(app.ManifestPath)
	if state == nil {
		state = &config.AppState{Services: make(map[string]config.AppServiceState)}
	}
	ctx, _, c, err := ensureReady(verbose, false)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	jobs, err := c.Ps(ctx)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	jobIndex := jobsByID(jobs)
	fmt.Printf("%-14s %-10s %-10s %-12s %s\n", "SERVICE", "JOB_ID", "STATUS", "PROFILE", "CMD")
	for _, svc := range services {
		entry, ok := state.Services[svc.Name]
		jobID := "-"
		status := "stopped"
		profile := svc.Profile
		cmdStr := strings.Join(svc.Cmd, " ")
		if ok && entry.JobID != "" {
			jobID = entry.JobID
			if job, ok := jobIndex[entry.JobID]; ok {
				status = job.Status
				if job.Profile != "" {
					profile = job.Profile
				}
				if len(job.Cmd) > 0 {
					cmdStr = strings.Join(job.Cmd, " ")
				}
			}
		}
		if len(cmdStr) > 50 {
			cmdStr = cmdStr[:47] + "..."
		}
		fmt.Printf("%-14s %-10s %-10s %-12s %s\n", svc.Name, jobID, status, profile, cmdStr)
	}
}

func runAppLogs(args []string, verbose bool) {
	manifestPath, serviceName, follow, err := parseAppLogsFlags(args)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	app, err := loadResolvedApp(manifestPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if serviceName == "" && len(app.Services) == 1 {
		for name := range app.Services {
			serviceName = name
		}
	}
	if serviceName == "" {
		fmt.Fprintln(os.Stderr, "app logs requires a service name when the manifest has multiple services")
		os.Exit(1)
	}
	state, err := config.ReadAppState(app.ManifestPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	entry, ok := state.Services[serviceName]
	if !ok || entry.JobID == "" {
		fmt.Fprintln(os.Stderr, "service is not running:", serviceName)
		os.Exit(1)
	}
	ctx, _, c, err := ensureReady(verbose, false)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if err := c.Logs(ctx, entry.JobID, follow, os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func runDashboard(args []string, verbose bool) {
	noVMUp := false
	benchPath := ""
	refreshMs := 2000
	for i := 0; i < len(args); i++ {
		if args[i] == "--no-vm-up" {
			noVMUp = true
		} else if args[i] == "--bench-file" && i+1 < len(args) {
			i++
			benchPath = args[i]
		} else if args[i] == "--refresh-ms" && i+1 < len(args) {
			i++
			if n, err := parseRefreshMs(args[i]); err == nil {
				refreshMs = n
			}
		}
	}
	if err := dashboard.RunDashboard(noVMUp, benchPath, refreshMs, verbose); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func runDev(args []string, verbose bool) {
	follow := false
	port := 3000
	host := "0.0.0.0"
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "-f", "--follow":
			follow = true
		case "--port":
			if i+1 < len(args) {
				i++
				if n, err := strconv.Atoi(args[i]); err == nil && n > 0 {
					port = n
				}
			}
		case "--host":
			if i+1 < len(args) {
				i++
				host = args[i]
			}
		}
	}

	ctx, _, c, err := ensureReady(verbose, false)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}

	mountList, err := mounts.DefaultMounts()
	if err != nil {
		fmt.Fprintln(os.Stderr, "mount:", err)
		os.Exit(1)
	}

	cwd, _ := os.Getwd()
	packageJSON := filepath.Join(cwd, "package.json")
	nodeModules := filepath.Join(cwd, "node_modules")
	if _, err := os.Stat(packageJSON); err == nil {
		if _, err := os.Stat(nodeModules); os.IsNotExist(err) {
			req := &api.RunRequest{
				RequestID: util.GenerateRequestID(),
				Cmd:       []string{"npm", "install"},
				Env:       util.EnvMap(),
				Cwd:       "/workspace",
				Profile:   api.ProfileBatch,
				Mounts:    mountList,
			}
			if _, err := c.Run(ctx, req, os.Stdout); err != nil {
				fmt.Fprintln(os.Stderr, "npm install:", err)
				os.Exit(1)
			}
		}
		devCmd := []string{"npm", "run", "dev", "--", "-H", host, "-p", strconv.Itoa(port)}
		req := &api.StartRequest{
			RequestID: util.GenerateRequestID(),
			Cmd:       devCmd,
			Env:       util.EnvMap(),
			Cwd:       "/workspace",
			Profile:   api.ProfileInteractive,
			Mounts:    mountList,
		}
		jobID, err := c.Start(ctx, req)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		if err := config.WriteLastJob(jobID); err != nil {
			fmt.Fprintln(os.Stderr, "warning: could not save last job:", err)
		}
		fmt.Println(jobID)
		if follow {
			c.Logs(ctx, jobID, true, os.Stdout)
		}
		return
	}
	fmt.Fprintln(os.Stderr, "devup dev: no package.json found in current directory")
	fmt.Fprintln(os.Stderr, "Run from a Node.js project root, or use: devup start -- <cmd>")
	os.Exit(1)
}

func parseRefreshMs(s string) (int, error) {
	n, err := strconv.Atoi(s)
	if err != nil || n <= 0 {
		return 0, err
	}
	return n, nil
}

func parseAppFlags(args []string) (manifestPath string, services []string, err error) {
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--file":
			if i+1 >= len(args) {
				return "", nil, fmt.Errorf("--file requires a path")
			}
			manifestPath = args[i+1]
			i++
		default:
			if strings.HasPrefix(args[i], "-") {
				return "", nil, fmt.Errorf("unknown app option %s", args[i])
			}
			services = append(services, args[i])
		}
	}
	return manifestPath, services, nil
}

func parseAppLogsFlags(args []string) (manifestPath string, serviceName string, follow bool, err error) {
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--file":
			if i+1 >= len(args) {
				return "", "", false, fmt.Errorf("--file requires a path")
			}
			manifestPath = args[i+1]
			i++
		case "-f", "--follow":
			follow = true
		default:
			if strings.HasPrefix(args[i], "-") {
				return "", "", false, fmt.Errorf("unknown app logs option %s", args[i])
			}
			if serviceName == "" {
				serviceName = args[i]
				continue
			}
			return "", "", false, fmt.Errorf("unexpected argument %s", args[i])
		}
	}
	return manifestPath, serviceName, follow, nil
}

func loadResolvedApp(manifestPath string) (*appfile.ResolvedFile, error) {
	path := strings.TrimSpace(manifestPath)
	if path == "" {
		cwd, err := os.Getwd()
		if err != nil {
			return nil, err
		}
		path, err = appfile.DefaultPath(cwd)
		if err != nil {
			return nil, err
		}
	}
	return appfile.Resolve(path)
}

func jobsByID(jobs []api.JobInfo) map[string]api.JobInfo {
	index := make(map[string]api.JobInfo, len(jobs))
	for _, job := range jobs {
		index[job.JobID] = job
	}
	return index
}

func mergedEnv(overrides map[string]string) map[string]string {
	env := util.EnvMap()
	for k, v := range overrides {
		env[k] = v
	}
	return env
}

func rollbackAppStarts(ctx context.Context, c *client.Client, started []config.AppServiceState) {
	for i := len(started) - 1; i >= 0; i-- {
		if started[i].JobID == "" {
			continue
		}
		_ = c.Stop(ctx, started[i].JobID)
	}
}

func runDown(verbose bool) {
	ctx, _, c, err := ensureReady(verbose, false)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}

	n, err := c.Down(ctx)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fmt.Printf("Stopped %d job(s)\n", n)
}

func runVMProvision(verbose bool) {
	ctx, _, _, err := ensureReady(verbose, true)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	tmp, err := os.CreateTemp("", "devup-provision-*.sh")
	if err != nil {
		fmt.Fprintln(os.Stderr, "temp file:", err)
		os.Exit(1)
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.WriteString(provisionScript); err != nil {
		tmp.Close()
		fmt.Fprintln(os.Stderr, "write provision script:", err)
		os.Exit(1)
	}
	tmp.Close()
	if err := vm.CopyToVM(ctx, tmp.Name(), "/tmp/devup-provision.sh"); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if err := vm.ShellCmdStreaming(ctx, "sudo bash /tmp/devup-provision.sh"); err != nil {
		fmt.Fprintln(os.Stderr, "provision failed:", err)
		os.Exit(1)
	}
}

func runVMDoctor(verbose bool) {
	vm.EnsureLimactl(verbose)
	if !vm.IsRunning() {
		fmt.Fprintln(os.Stderr, "VM is stopped. Run: devup vm up")
		os.Exit(1)
	}
	cfg, err := config.Load()
	if err != nil {
		cfg, err = config.Create()
		if err != nil {
			fmt.Fprintln(os.Stderr, "config:", err)
			os.Exit(1)
		}
	}
	c := client.New(cfg.Token)
	ctx := context.Background()
	info, err := c.SystemInfo(ctx)
	if err != nil {
		fmt.Fprintln(os.Stderr, "doctor:", err)
		os.Exit(1)
	}
	toolOrder := []string{"node", "npm", "python3", "pip", "go", "ruby", "java", "cargo", "rustc", "php", "composer", "gcc", "g++", "make", "cmake", "pnpm", "mise"}
	fmt.Printf("%-10s %-10s %s\n", "TOOL", "STATUS", "VERSION")
	for _, name := range toolOrder {
		t, ok := info.Tools[name]
		if !ok {
			fmt.Printf("%-10s %-10s %s\n", name, "unknown", "-")
			continue
		}
		ver := t.Version
		if len(ver) > 50 {
			ver = ver[:47] + "..."
		}
		fmt.Printf("%-10s %-10s %s\n", name, t.Status, ver)
	}
}
