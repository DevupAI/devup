package dashboard

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
)

type benchFile struct {
	Timestamp string                          `json:"timestamp"`
	Host      benchHost                       `json:"host"`
	Ephemeral map[string]map[string]benchStat `json:"ephemeral"`
	Service   map[string]map[string]benchSvc  `json:"service"`
}

type benchHost struct {
	Platform string `json:"platform"`
}

type benchStat struct {
	MeanMS float64 `json:"mean_ms"`
	P50MS  float64 `json:"p50_ms"`
	P95MS  float64 `json:"p95_ms"`
}

type benchSvc struct {
	ReadyMS      float64 `json:"ready_ms"`
	IdleMemoryMB float64 `json:"idle_memory_mb"`
}

type benchWorkloadSummary struct {
	Name            string
	BestEphemeral   string
	BestEphemeralMS float64
	BestReady       string
	BestReadyMS     float64
	LowestMemory    string
	LowestMemoryMB  float64
}

type benchSummary struct {
	Path      string
	Timestamp string
	Platform  string
	Workloads []benchWorkloadSummary
}

func loadBenchSummary(path string) (*benchSummary, error) {
	if path == "" {
		path = defaultBenchPath()
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var raw benchFile
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, err
	}
	summary := &benchSummary{
		Path:      path,
		Timestamp: raw.Timestamp,
		Platform:  raw.Host.Platform,
	}
	names := make([]string, 0, len(raw.Ephemeral))
	for name := range raw.Ephemeral {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		workload := benchWorkloadSummary{Name: name}
		if stats := raw.Ephemeral[name]; len(stats) > 0 {
			workload.BestEphemeral, workload.BestEphemeralMS = bestEphemeral(stats)
		}
		if stats := raw.Service[name]; len(stats) > 0 {
			workload.BestReady, workload.BestReadyMS, workload.LowestMemory, workload.LowestMemoryMB = bestService(stats)
		}
		summary.Workloads = append(summary.Workloads, workload)
	}
	return summary, nil
}

func bestEphemeral(stats map[string]benchStat) (string, float64) {
	bestName := ""
	bestValue := 0.0
	for name, stat := range stats {
		if bestName == "" || stat.MeanMS < bestValue {
			bestName = name
			bestValue = stat.MeanMS
		}
	}
	return bestName, bestValue
}

func bestService(stats map[string]benchSvc) (string, float64, string, float64) {
	bestReadyName := ""
	bestReady := 0.0
	bestMemName := ""
	bestMem := 0.0
	for name, stat := range stats {
		if bestReadyName == "" || stat.ReadyMS < bestReady {
			bestReadyName = name
			bestReady = stat.ReadyMS
		}
		if bestMemName == "" || stat.IdleMemoryMB < bestMem {
			bestMemName = name
			bestMem = stat.IdleMemoryMB
		}
	}
	return bestReadyName, bestReady, bestMemName, bestMem
}

func defaultBenchPath() string {
	cwd, err := os.Getwd()
	if err != nil {
		return ".devup-bench/latest.json"
	}
	return filepath.Join(cwd, ".devup-bench", "latest.json")
}
