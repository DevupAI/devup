package vm

import (
	"context"
	"fmt"
	"math/rand"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"devup/internal/logging"
)

const (
	InstanceName = "devup"
	AgentBinary  = "devup-agent"
	AgentPath    = "/usr/local/bin/devup-agent"
	TokenPath    = "/etc/devup/token"
	HealthURL    = "http://127.0.0.1:7777/health"
	HealthWait   = 10 * time.Second
)

// EnsureLimactl checks limactl is in PATH; exits with helpful message if not
func EnsureLimactl(verbose bool) error {
	if _, err := exec.LookPath("limactl"); err != nil {
		fmt.Fprintln(os.Stderr, "limactl not found. Install Lima:")
		fmt.Fprintln(os.Stderr, "  brew install lima")
		fmt.Fprintln(os.Stderr, "")
		fmt.Fprintln(os.Stderr, "Lima provides a lightweight Linux VM on macOS. devup uses it to run containers.")
		os.Exit(1)
	}
	return nil
}

// IsDarwin returns true if we should use Lima (macOS)
func IsDarwin() bool {
	return runtime.GOOS == "darwin"
}

// LinuxHint prints message for Linux users
func LinuxHint() {
	fmt.Fprintln(os.Stderr, "Linux: run the agent directly. VM support is not implemented yet.")
	os.Exit(0)
}

// instanceExists returns true if ~/.lima/devup exists (Lima instance dir)
func instanceExists() bool {
	home, err := os.UserHomeDir()
	if err != nil {
		return false
	}
	instDir := filepath.Join(home, ".lima", InstanceName)
	_, err = os.Stat(instDir)
	return err == nil
}

// IsRunning returns true if the instance exists and is running
func IsRunning() bool {
	out, err := exec.Command("limactl", "list", "--tty=false", InstanceName).CombinedOutput()
	if err != nil {
		return false
	}
	return strings.Contains(string(out), "Running")
}

// Exists returns true if the devup instance already exists (~/.lima/devup)
func Exists() bool {
	return instanceExists()
}

// Up starts the Lima instance and deploys the agent. Idempotent.
func Up(ctx context.Context, configPath, token string, verbose bool) error {
	home, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("home dir: %w", err)
	}
	instDir := filepath.Join(home, ".lima", InstanceName)

	_, statErr := os.Stat(instDir)
	if statErr == nil {
		// Existing instance: start only (no YAML, no creation prompt)
		if verbose {
			logging.Info("limactl start (existing)", "name", InstanceName)
		}
		cmd := exec.CommandContext(ctx, "limactl", "start", "--tty=false", InstanceName)
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		if err := cmd.Run(); err != nil {
			if IsRunning() {
				return ensureAgentRunning(ctx, configPath, token, verbose)
			}
			return fmt.Errorf("limactl start: %w", err)
		}
		return ensureAgentRunning(ctx, configPath, token, verbose)
	}
	if os.IsNotExist(statErr) {
		// No instance: create from YAML
		if verbose {
			logging.Info("limactl start (create)", "name", InstanceName, "config", configPath)
		}
		cmd := exec.CommandContext(ctx, "limactl", "start", "--tty=false", "--name", InstanceName, configPath)
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("limactl start: %w", err)
		}
		return ensureAgentRunning(ctx, configPath, token, verbose)
	}
	return fmt.Errorf("check instance dir: %w", statErr)
}

func isAlreadyRunning() bool {
	return IsRunning()
}

// ensureAgentRunning builds agent if needed, copies to VM, writes token, starts service
func ensureAgentRunning(ctx context.Context, configPath, token string, verbose bool) error {
	// Check if agent is already healthy
	if err := WaitForAgent(ctx, token, 2*time.Second); err == nil {
		if verbose {
			logging.Info("agent already healthy")
		}
		return nil
	}
	// Build and deploy
	binaryPath, err := buildAgent()
	if err != nil {
		return err
	}
	defer os.Remove(binaryPath)
	if err := copyAgentAndStart(ctx, binaryPath, token, verbose); err != nil {
		return err
	}
	return WaitForAgent(ctx, token, HealthWait)
}

func buildAgent() (string, error) {
	goarch := runtime.GOARCH
	if goarch != "arm64" && goarch != "amd64" {
		goarch = "amd64"
	}
	tmp, err := os.CreateTemp("", "devup-agent-*")
	if err != nil {
		return "", err
	}
	tmp.Close()
	path := tmp.Name()
	cmd := exec.Command("go", "build", "-o", path, "./cmd/devup-agent")
	cmd.Env = append(os.Environ(), "GOOS=linux", "GOARCH="+goarch)
	if out, err := cmd.CombinedOutput(); err != nil {
		return "", fmt.Errorf("build devup-agent: %w\n%s", err, out)
	}
	return path, nil
}

func copyAgentAndStart(ctx context.Context, binaryPath, token string, verbose bool) error {
	// Copy to /tmp first (limactl copy uses SSH user; /usr/local/bin needs root)
	if verbose {
		logging.Info("limactl copy", "src", binaryPath, "dst", InstanceName+":/tmp/devup-agent")
	}
	cmd := exec.CommandContext(ctx, "limactl", "copy", "--tty=false", binaryPath, InstanceName+":/tmp/devup-agent")
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("copy agent: %w\n%s", err, out)
	}
	// Move to /usr/local/bin and chmod
	cmd = exec.CommandContext(ctx, "limactl", "shell", "--tty=false", InstanceName, "sudo", "sh", "-c", "mv /tmp/devup-agent "+AgentPath+" && chmod 755 "+AgentPath)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("install agent: %w", err)
	}
	// Create /etc/devup and write token via stdin (avoids shell escaping)
	cmd = exec.CommandContext(ctx, "limactl", "shell", "--tty=false", InstanceName, "sudo", "sh", "-c", "mkdir -p /etc/devup && cat > "+TokenPath)
	cmd.Stdin = strings.NewReader(token)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("write token: %w\n%s", err, out)
	}
	// Try systemd first
	cmd = exec.CommandContext(ctx, "limactl", "shell", "--tty=false", InstanceName, "sudo", "systemctl", "start", "devup-agent")
	if err := cmd.Run(); err != nil {
		// Fallback: nohup
		if verbose {
			logging.Info("systemctl start failed, using nohup fallback")
		}
		fallback := fmt.Sprintf("nohup %s > /var/log/devup-agent.log 2>&1 &", AgentPath)
		cmd = exec.CommandContext(ctx, "limactl", "shell", "--tty=false", InstanceName, "sudo", "sh", "-c", fallback)
		if out, err := cmd.CombinedOutput(); err != nil {
			return fmt.Errorf("start agent: %w\n%s", err, out)
		}
		time.Sleep(500 * time.Millisecond)
	}
	return nil
}

// WaitForAgent polls /health with exponential backoff + jitter
func WaitForAgent(ctx context.Context, token string, timeout time.Duration) error {
	client := &httpHealthClient{token: token}
	deadline := time.Now().Add(timeout)
	base := 200 * time.Millisecond
	for time.Now().Before(deadline) {
		if err := ctx.Err(); err != nil {
			return err
		}
		if client.check() == nil {
			return nil
		}
		jitter := time.Duration(rand.Int63n(int64(base)))
		time.Sleep(base + jitter)
		base *= 2
		if base > 2*time.Second {
			base = 2 * time.Second
		}
	}
	return fmt.Errorf("agent not reachable after %v; check 'devup vm logs'", timeout)
}

type httpHealthClient struct {
	token string
}

func (c *httpHealthClient) check() error {
	req, _ := http.NewRequest("GET", HealthURL, nil)
	req.Header.Set("X-Devup-Token", c.token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	resp.Body.Close()
	if resp.StatusCode != 200 {
		return fmt.Errorf("status %d", resp.StatusCode)
	}
	return nil
}

// Down stops the Lima instance. Idempotent: "already stopped" is success.
func Down(ctx context.Context, verbose bool) error {
	if verbose {
		logging.Info("limactl stop -f", "name", InstanceName)
	}
	cmd := exec.CommandContext(ctx, "limactl", "stop", "--tty=false", "-f", InstanceName)
	out, err := cmd.CombinedOutput()
	if err != nil {
		// Already stopped or "expected status Running, got Stopped": treat as success (quiet)
		if !IsRunning() || strings.Contains(string(out), "Stopped") {
			return nil
		}
		os.Stderr.Write(out)
		return fmt.Errorf("limactl stop: %w", err)
	}
	return nil
}

// Shell opens a shell in the VM
func Shell(ctx context.Context) error {
	cmd := exec.CommandContext(ctx, "limactl", "shell", InstanceName)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

// Status prints VM status and agent health
func Status(ctx context.Context, token string) error {
	cmd := exec.CommandContext(ctx, "limactl", "list", "--tty=false", InstanceName)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return err
	}
	if err := WaitForAgent(ctx, token, 2*time.Second); err != nil {
		fmt.Println("Agent: not reachable")
		return nil
	}
	fmt.Println("Agent: healthy")
	return nil
}

// Logs shows agent logs (journalctl first, else /var/log/devup-agent.log)
func Logs(ctx context.Context) error {
	cmd := exec.CommandContext(ctx, "limactl", "shell", "--tty=false", InstanceName, "sudo", "journalctl", "-u", "devup-agent", "--no-pager", "-n", "200")
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		cmd = exec.CommandContext(ctx, "limactl", "shell", "--tty=false", InstanceName, "sudo", "cat", "/var/log/devup-agent.log")
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		return cmd.Run()
	}
	return nil
}
