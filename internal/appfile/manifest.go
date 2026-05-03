package appfile

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"devup/internal/api"
	"devup/internal/mounts"
	"gopkg.in/yaml.v3"
)

var defaultManifestNames = []string{
	"devup.app.yaml",
	"devup.app.yml",
	"devup.yaml",
	"devup.yml",
}

type File struct {
	Name     string             `yaml:"name"`
	Services map[string]Service `yaml:"services"`
}

type Service struct {
	Command    Command           `yaml:"command"`
	Cmd        Command           `yaml:"cmd"`
	Workdir    string            `yaml:"workdir"`
	Profile    string            `yaml:"profile"`
	Mounts     []string          `yaml:"mounts"`
	Env        map[string]string `yaml:"env"`
	MemoryMB   int               `yaml:"memory_mb"`
	CPUPercent int               `yaml:"cpu_percent"`
	PidsMax    int               `yaml:"pids_max"`
	Overlay    bool              `yaml:"overlay"`
	Shadow     bool              `yaml:"shadow"`
	NetIsolate bool              `yaml:"net_isolate"`
	Isolate    bool              `yaml:"isolate"`
	DependsOn  []string          `yaml:"depends_on"`
}

type Command []string

type ResolvedFile struct {
	Name         string
	ManifestPath string
	Services     map[string]ResolvedService
	order        []string
}

type ResolvedService struct {
	Name       string
	Cmd        []string
	Workdir    string
	Profile    string
	Mounts     []api.Mount
	Env        map[string]string
	Limits     *api.ResourceLimits
	Overlay    bool
	Shadow     bool
	NetIsolate bool
	DependsOn  []string
}

func (c *Command) UnmarshalYAML(value *yaml.Node) error {
	switch value.Kind {
	case yaml.ScalarNode:
		s := strings.TrimSpace(value.Value)
		if s == "" {
			*c = nil
			return nil
		}
		*c = []string{"sh", "-lc", s}
		return nil
	case yaml.SequenceNode:
		var parts []string
		if err := value.Decode(&parts); err != nil {
			return err
		}
		*c = slices.Clone(parts)
		return nil
	default:
		return fmt.Errorf("command must be a string or list of strings")
	}
}

func DefaultPath(cwd string) (string, error) {
	for _, name := range defaultManifestNames {
		path := filepath.Join(cwd, name)
		if _, err := os.Stat(path); err == nil {
			return path, nil
		}
	}
	return "", fmt.Errorf("no manifest found; tried %s", strings.Join(defaultManifestNames, ", "))
}

func Load(path string) (*File, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var f File
	if err := yaml.Unmarshal(data, &f); err != nil {
		return nil, err
	}
	if strings.TrimSpace(f.Name) == "" {
		f.Name = strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
	}
	if len(f.Services) == 0 {
		return nil, fmt.Errorf("manifest %s defines no services", path)
	}
	for name, svc := range f.Services {
		if strings.TrimSpace(name) == "" {
			return nil, fmt.Errorf("service name cannot be empty")
		}
		cmd := serviceCommand(svc)
		if len(cmd) == 0 {
			return nil, fmt.Errorf("service %s missing command", name)
		}
	}
	return &f, nil
}

func Resolve(path string) (*ResolvedFile, error) {
	absPath, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	f, err := Load(absPath)
	if err != nil {
		return nil, err
	}
	manifestDir := filepath.Dir(absPath)
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, fmt.Errorf("home dir: %w", err)
	}
	rf := &ResolvedFile{
		Name:         f.Name,
		ManifestPath: absPath,
		Services:     make(map[string]ResolvedService, len(f.Services)),
	}
	for name, svc := range f.Services {
		resolvedMounts, err := resolveMounts(svc.Mounts, manifestDir, home)
		if err != nil {
			return nil, fmt.Errorf("service %s: %w", name, err)
		}
		rs := ResolvedService{
			Name:       name,
			Cmd:        serviceCommand(svc),
			Workdir:    normalizeWorkdir(svc.Workdir),
			Profile:    api.NormalizeProfile(svc.Profile),
			Mounts:     resolvedMounts,
			Env:        cloneEnv(svc.Env),
			Limits:     buildLimits(svc),
			Overlay:    svc.Overlay || svc.Isolate || svc.Shadow,
			Shadow:     svc.Shadow,
			NetIsolate: svc.NetIsolate || svc.Isolate,
			DependsOn:  slices.Clone(svc.DependsOn),
		}
		if rs.Profile == "" {
			rs.Profile = api.ProfileService
		}
		rf.Services[name] = rs
	}
	order, err := topoOrder(rf.Services)
	if err != nil {
		return nil, err
	}
	rf.order = order
	return rf, nil
}

func (f *ResolvedFile) StartOrder(targets []string) ([]ResolvedService, error) {
	selected, err := f.selectedTargets(targets, true)
	if err != nil {
		return nil, err
	}
	return orderedServices(f.Services, f.order, selected), nil
}

func (f *ResolvedFile) ExactOrder(targets []string, reverse bool) ([]ResolvedService, error) {
	selected, err := f.selectedTargets(targets, false)
	if err != nil {
		return nil, err
	}
	services := orderedServices(f.Services, f.order, selected)
	if reverse {
		slices.Reverse(services)
	}
	return services, nil
}

func (f *ResolvedFile) selectedTargets(targets []string, includeDeps bool) (map[string]struct{}, error) {
	if len(targets) == 0 {
		selected := make(map[string]struct{}, len(f.Services))
		for name := range f.Services {
			selected[name] = struct{}{}
		}
		return selected, nil
	}
	selected := make(map[string]struct{}, len(targets))
	for _, name := range targets {
		if _, ok := f.Services[name]; !ok {
			return nil, fmt.Errorf("unknown service %q", name)
		}
		selected[name] = struct{}{}
	}
	if !includeDeps {
		return selected, nil
	}
	var visit func(string) error
	visit = func(name string) error {
		svc := f.Services[name]
		for _, dep := range svc.DependsOn {
			if _, ok := f.Services[dep]; !ok {
				return fmt.Errorf("service %s depends on unknown service %s", name, dep)
			}
			if _, ok := selected[dep]; ok {
				if err := visit(dep); err != nil {
					return err
				}
				continue
			}
			selected[dep] = struct{}{}
			if err := visit(dep); err != nil {
				return err
			}
		}
		return nil
	}
	names := make([]string, 0, len(selected))
	for name := range selected {
		names = append(names, name)
	}
	slices.Sort(names)
	for _, name := range names {
		if err := visit(name); err != nil {
			return nil, err
		}
	}
	return selected, nil
}

func orderedServices(all map[string]ResolvedService, order []string, selected map[string]struct{}) []ResolvedService {
	services := make([]ResolvedService, 0, len(selected))
	for _, name := range order {
		if _, ok := selected[name]; !ok {
			continue
		}
		services = append(services, all[name])
	}
	return services
}

func topoOrder(services map[string]ResolvedService) ([]string, error) {
	order := make([]string, 0, len(services))
	visiting := make(map[string]bool, len(services))
	visited := make(map[string]bool, len(services))
	var visit func(string) error
	visit = func(name string) error {
		if visited[name] {
			return nil
		}
		if visiting[name] {
			return fmt.Errorf("service dependency cycle detected at %s", name)
		}
		svc, ok := services[name]
		if !ok {
			return fmt.Errorf("unknown service %s", name)
		}
		visiting[name] = true
		for _, dep := range svc.DependsOn {
			if _, ok := services[dep]; !ok {
				return fmt.Errorf("service %s depends on unknown service %s", name, dep)
			}
			if err := visit(dep); err != nil {
				return err
			}
		}
		visiting[name] = false
		visited[name] = true
		order = append(order, name)
		return nil
	}
	names := make([]string, 0, len(services))
	for name := range services {
		names = append(names, name)
	}
	slices.Sort(names)
	for _, name := range names {
		if err := visit(name); err != nil {
			return nil, err
		}
	}
	return order, nil
}

func resolveMounts(specs []string, cwd string, home string) ([]api.Mount, error) {
	if len(specs) == 0 {
		m, err := mounts.ParseMountFromString(".:/workspace", cwd, home)
		if err != nil {
			return nil, err
		}
		return []api.Mount{m}, nil
	}
	resolved := make([]api.Mount, 0, len(specs))
	for _, spec := range specs {
		m, err := mounts.ParseMountFromString(spec, cwd, home)
		if err != nil {
			return nil, err
		}
		resolved = append(resolved, m)
	}
	return resolved, nil
}

func normalizeWorkdir(workdir string) string {
	workdir = strings.TrimSpace(workdir)
	if workdir == "" {
		return "/workspace"
	}
	if strings.HasPrefix(workdir, "/") {
		return filepath.ToSlash(workdir)
	}
	return filepath.ToSlash(filepath.Join("/workspace", workdir))
}

func serviceCommand(svc Service) []string {
	if len(svc.Command) > 0 {
		return slices.Clone(svc.Command)
	}
	return slices.Clone(svc.Cmd)
}

func buildLimits(svc Service) *api.ResourceLimits {
	limits := &api.ResourceLimits{
		MemoryMB:   svc.MemoryMB,
		CPUPercent: svc.CPUPercent,
		PidsMax:    svc.PidsMax,
	}
	if limits.MemoryMB == 0 && limits.CPUPercent == 0 && limits.PidsMax == 0 {
		return nil
	}
	return limits
}

func cloneEnv(env map[string]string) map[string]string {
	if len(env) == 0 {
		return nil
	}
	cloned := make(map[string]string, len(env))
	for k, v := range env {
		cloned[k] = v
	}
	return cloned
}
