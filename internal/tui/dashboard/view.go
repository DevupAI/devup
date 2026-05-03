package dashboard

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

func (m *Model) View() string {
	switch m.view {
	case ViewJobsList:
		return m.viewJobsList()
	case ViewBenchmarks:
		return m.viewBenchmarks()
	case ViewLogs:
		return m.viewLogs()
	case ViewStartModal:
		return m.viewStartModal()
	}
	return ""
}

func (m *Model) viewJobsList() string {
	var b strings.Builder

	b.WriteString(headerStyle.Render("Devup Runtime"))
	b.WriteString("\n\n")
	b.WriteString(renderJobsCards(m))
	b.WriteString("\n\n")

	b.WriteString(m.tableModel.View())
	b.WriteString("\n\n")

	// Status line
	if m.lastError != nil {
		b.WriteString(statusErrStyle.Render("Error: " + m.lastError.Error()))
		b.WriteString("\n")
	}

	footer := "r: refresh | enter: logs | s: stop | a: start | b: benchmarks | d: down | v: debug | q: quit"
	b.WriteString(footerStyle.Render(footer))
	b.WriteString("\n")

	if m.showDebug {
		b.WriteString("\n--- debug ---\n")
		b.WriteString(fmt.Sprintf("jobs=%d vm=%v health=%v\n", len(m.jobs), m.vmRunning, m.health.OK))
	}

	return b.String()
}

func (m *Model) viewBenchmarks() string {
	var b strings.Builder
	b.WriteString(headerStyle.Render("Devup Benchmarks"))
	b.WriteString("\n\n")
	if m.benchSummary == nil {
		b.WriteString(mutedStyle.Render("No benchmark file found. Run `make bench` to populate .devup-bench/latest.json."))
		b.WriteString("\n\n")
		b.WriteString(footerStyle.Render("j/esc/b: back | r: refresh | q: quit"))
		return b.String()
	}
	hero := renderBenchHero(m.benchSummary)
	b.WriteString(hero)
	b.WriteString("\n\n")
	for _, workload := range m.benchSummary.Workloads {
		card := cardStyle.Width(34).Render(strings.Join([]string{
			cardTitleStyle.Render(workload.Name),
			"",
			"Ephemeral winner",
			cardValueStyle.Render(fmt.Sprintf("%s  %.1fms", workload.BestEphemeral, workload.BestEphemeralMS)),
			"",
			"Service ready winner",
			cardValueStyle.Render(fmt.Sprintf("%s  %.1fms", workload.BestReady, workload.BestReadyMS)),
			"",
			"Idle memory winner",
			cardValueStyle.Render(fmt.Sprintf("%s  %.1fMiB", workload.LowestMemory, workload.LowestMemoryMB)),
		}, "\n"))
		b.WriteString(card)
		b.WriteString("\n")
	}
	b.WriteString("\n")
	b.WriteString(footerStyle.Render("j/esc/b: back | r: refresh | q: quit"))
	return b.String()
}

func (m *Model) viewLogs() string {
	var b strings.Builder

	title := fmt.Sprintf("Logs: %s", m.logsJobID)
	if m.logsFollow {
		title += " (following)"
	}
	b.WriteString(headerStyle.Render(title))
	b.WriteString("\n\n")

	content := m.logsContent
	if content == "" && !m.logsFollow {
		content = "(no logs yet)"
	}
	// Truncate if very long for display
	lines := strings.Split(content, "\n")
	maxLines := 30
	if len(lines) > maxLines && !m.logsFollow {
		lines = lines[len(lines)-maxLines:]
		content = strings.Join(lines, "\n")
	}
	b.WriteString(lipgloss.NewStyle().Width(80).Render(content))
	b.WriteString("\n\n")

	footer := "f: toggle follow | esc: back"
	b.WriteString(footerStyle.Render(footer))

	return b.String()
}

func (m *Model) viewStartModal() string {
	var b strings.Builder

	b.WriteString(headerStyle.Render("Start new job"))
	b.WriteString("\n\n")

	b.WriteString("Command: ")
	b.WriteString(m.cmdInput.View())
	b.WriteString("\n")

	b.WriteString("Mount (default .:/workspace): ")
	b.WriteString(m.mountInput.View())
	b.WriteString("\n\n")
	b.WriteString(fmt.Sprintf("Profile: %s\n", accentStyle.Render(m.startProfile)))
	shadow := "off"
	if m.startShadow {
		shadow = "on"
	}
	b.WriteString(fmt.Sprintf("Shadow workspace: %s\n\n", accentStyle.Render(shadow)))

	if m.lastError != nil {
		b.WriteString(statusErrStyle.Render("Error: " + m.lastError.Error()))
		b.WriteString("\n")
	}

	b.WriteString(footerStyle.Render("enter: next/submit | tab: switch field | p: cycle profile | x: toggle shadow | esc: cancel"))

	return b.String()
}

func renderJobsCards(m *Model) string {
	vmState := "stopped"
	if m.vmRunning {
		vmState = "running"
	}
	agentState := "down"
	if m.health.OK {
		agentState = "healthy"
	}
	latencyStr := "-"
	if m.health.OK {
		latencyStr = fmt.Sprintf("%dms", m.health.Latency.Milliseconds())
	}
	running := 0
	adaptive := 0
	for _, job := range m.jobs {
		if job.Status == "running" {
			running++
		}
		if job.Memory != nil && job.Memory.Adaptive {
			adaptive++
		}
	}
	cards := []string{
		cardStyle.Width(22).Render(cardTitleStyle.Render("VM") + "\n" + cardValueStyle.Render(vmState)),
		cardStyle.Width(22).Render(cardTitleStyle.Render("Agent") + "\n" + cardValueStyle.Render(agentState) + "\n" + mutedStyle.Render("latency "+latencyStr)),
		cardStyle.Width(22).Render(cardTitleStyle.Render("Jobs") + "\n" + cardValueStyle.Render(fmt.Sprintf("%d running", running)) + "\n" + mutedStyle.Render(fmt.Sprintf("%d total", len(m.jobs)))),
		cardStyle.Width(22).Render(cardTitleStyle.Render("Adaptive Memory") + "\n" + cardValueStyle.Render(fmt.Sprintf("%d jobs", adaptive))),
	}
	return lipgloss.JoinHorizontal(lipgloss.Top, cards...)
}

func renderBenchHero(summary *benchSummary) string {
	left := cardStyle.Width(32).Render(strings.Join([]string{
		cardTitleStyle.Render("Latest file"),
		cardValueStyle.Render(filepathBase(summary.Path)),
		"",
		mutedStyle.Render(summary.Timestamp),
	}, "\n"))
	right := cardStyle.Width(32).Render(strings.Join([]string{
		cardTitleStyle.Render("Workloads"),
		cardValueStyle.Render(fmt.Sprintf("%d languages", len(summary.Workloads))),
		"",
		mutedStyle.Render(summary.Platform),
	}, "\n"))
	return lipgloss.JoinHorizontal(lipgloss.Top, left, right)
}

func filepathBase(path string) string {
	parts := strings.Split(path, "/")
	if len(parts) == 0 {
		return path
	}
	return parts[len(parts)-1]
}
