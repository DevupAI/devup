package dashboard

import (
	"context"
	"fmt"
	"os"

	"devup/internal/config"
	"devup/internal/vm"

	tea "github.com/charmbracelet/bubbletea"
)

// RunDashboard runs the TUI dashboard. Returns error only for setup failures.
// The dashboard runs until user quits.
func RunDashboard(noVMUp bool, benchPath string, refreshMs int, verbose bool) error {
	if !vm.IsDarwin() {
		fmt.Println("Dashboard is macOS+Lima only (for now).")
		os.Exit(0)
	}

	cfg, err := config.Load()
	if err != nil {
		cfg, err = config.Create()
		if err != nil {
			return fmt.Errorf("config: %w", err)
		}
	}

	configPath := vm.FindLimaConfig()
	if !noVMUp {
		vm.EnsureLimactl(verbose)
		ctx := context.Background()
		if err := vm.Up(ctx, configPath, cfg.Token, false, true); err != nil {
			return fmt.Errorf("vm up: %w", err)
		}
	}

	if refreshMs <= 0 {
		refreshMs = 2000
	}

	m := NewModel(cfg, configPath, benchPath, refreshMs, verbose)
	p := tea.NewProgram(m, tea.WithAltScreen())
	m.SetProgram(p)

	if _, err := p.Run(); err != nil {
		return err
	}
	return nil
}
