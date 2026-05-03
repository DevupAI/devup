package memoryctrl

import "devup/internal/api"

const (
	mib                  = int64(1024 * 1024)
	freeRatioRelaxed     = 0.20
	freeRatioConstrained = 0.05
)

type profileConfig struct {
	autoBudgetFraction  float64
	autoBudgetMinBytes  int64
	autoBudgetMaxBytes  int64
	protectRelaxed      float64
	protectConstrained  float64
	headroomRelaxed     float64
	headroomConstrained float64
	minFloorBytes       int64
	minHeadroomBytes    int64
	reclaimPriority     int
}

// Sample is a single observation used to tune a job's cgroup memory knobs.
type Sample struct {
	Profile            string
	CurrentBytes       int64
	BudgetBytes        int64
	MaxBytes           int64 // hard ceiling; 0 = unlimited
	HostAvailableBytes int64
	HostTotalBytes     int64
}

// Limits is the controller's recommended cgroup memory configuration.
type Limits struct {
	LowBytes         int64
	HighBytes        int64
	MaxBytes         int64
	ReclaimableBytes int64
}

// Controller tracks a recent working-set estimate and translates it into
// memory.low and memory.high recommendations under host pressure.
type Controller struct {
	recentPeak float64
}

// New constructs a controller for a single job.
func New() *Controller {
	return &Controller{}
}

// Observe records one sample and returns the next soft/hard memory knobs.
func (c *Controller) Observe(sample Sample) Limits {
	cfg := configFor(sample.Profile)
	budgetBytes := sample.BudgetBytes
	if budgetBytes <= 0 {
		budgetBytes = DefaultBudgetBytes(sample.Profile, sample.HostTotalBytes)
	}

	hardMaxBytes := sample.MaxBytes
	if hardMaxBytes > 0 && (budgetBytes <= 0 || budgetBytes > hardMaxBytes) {
		budgetBytes = hardMaxBytes
	}
	if budgetBytes <= 0 {
		return Limits{MaxBytes: hardMaxBytes}
	}

	current := clamp(sample.CurrentBytes, 0, maxInt64(sample.CurrentBytes, budgetBytes))
	if c.recentPeak == 0 || float64(current) > c.recentPeak {
		c.recentPeak = float64(current)
	} else {
		c.recentPeak = (c.recentPeak * 0.90) + (float64(current) * 0.10)
	}

	pressure := hostPressure(sample.HostAvailableBytes, sample.HostTotalBytes)
	protectFactor := interpolate(cfg.protectRelaxed, cfg.protectConstrained, pressure)
	headroomFactor := interpolate(cfg.headroomRelaxed, cfg.headroomConstrained, pressure)

	low := clamp(int64(c.recentPeak*protectFactor), cfg.minFloorBytes, budgetBytes)
	headroom := maxInt64(cfg.minHeadroomBytes, int64(c.recentPeak*headroomFactor))
	high := clamp(low+headroom, low, budgetBytes)

	if pressure <= 0 {
		high = budgetBytes
	}

	return Limits{
		LowBytes:         low,
		HighBytes:        high,
		MaxBytes:         hardMaxBytes,
		ReclaimableBytes: maxInt64(0, high-low),
	}
}

// DefaultBudgetBytes returns the default soft budget for a job profile.
// This is the runtime's "elastic target" when no hard memory limit is set.
func DefaultBudgetBytes(profile string, hostTotalBytes int64) int64 {
	cfg := configFor(profile)
	if hostTotalBytes <= 0 {
		return cfg.autoBudgetMinBytes
	}
	return clamp(int64(float64(hostTotalBytes)*cfg.autoBudgetFraction), cfg.autoBudgetMinBytes, cfg.autoBudgetMaxBytes)
}

// EstimatedDemandBytes returns the memory demand used for admission and
// capacity planning. Explicit hard limits win; otherwise the profile default
// budget acts as the job's elastic demand.
func EstimatedDemandBytes(profile string, explicitMaxBytes, hostTotalBytes int64) int64 {
	if explicitMaxBytes > 0 {
		return explicitMaxBytes
	}
	return DefaultBudgetBytes(profile, hostTotalBytes)
}

// MinHighHeadroomBytes returns the minimum headroom a profile should keep
// above its protected floor when reclaiming.
func MinHighHeadroomBytes(profile string) int64 {
	return configFor(profile).minHeadroomBytes
}

// ReclaimPriority returns the relative reclaim cost of a profile.
// Lower numbers are cheaper to reclaim.
func ReclaimPriority(profile string) int {
	return configFor(profile).reclaimPriority
}

// SafetyReserveBytes leaves a host-level reserve so the runtime doesn't fill
// the machine to zero available memory.
func SafetyReserveBytes(hostTotalBytes int64) int64 {
	if hostTotalBytes <= 0 {
		return 256 * mib
	}
	return maxInt64(256*mib, hostTotalBytes/20)
}

func hostPressure(availableBytes, totalBytes int64) float64 {
	if totalBytes <= 0 {
		return 0
	}
	if availableBytes <= 0 {
		return 1
	}

	ratio := float64(availableBytes) / float64(totalBytes)
	switch {
	case ratio >= freeRatioRelaxed:
		return 0
	case ratio <= freeRatioConstrained:
		return 1
	default:
		return (freeRatioRelaxed - ratio) / (freeRatioRelaxed - freeRatioConstrained)
	}
}

func configFor(profile string) profileConfig {
	switch api.NormalizeProfile(profile) {
	case api.ProfileBatch:
		return profileConfig{
			autoBudgetFraction:  0.12,
			autoBudgetMinBytes:  256 * mib,
			autoBudgetMaxBytes:  1024 * mib,
			protectRelaxed:      0.18,
			protectConstrained:  0.08,
			headroomRelaxed:     0.40,
			headroomConstrained: 0.22,
			minFloorBytes:       64 * mib,
			minHeadroomBytes:    64 * mib,
			reclaimPriority:     1,
		}
	case api.ProfileInteractive:
		return profileConfig{
			autoBudgetFraction:  0.25,
			autoBudgetMinBytes:  512 * mib,
			autoBudgetMaxBytes:  2048 * mib,
			protectRelaxed:      0.55,
			protectConstrained:  0.35,
			headroomRelaxed:     0.60,
			headroomConstrained: 0.40,
			minFloorBytes:       128 * mib,
			minHeadroomBytes:    128 * mib,
			reclaimPriority:     3,
		}
	default:
		return profileConfig{
			autoBudgetFraction:  0.18,
			autoBudgetMinBytes:  384 * mib,
			autoBudgetMaxBytes:  1536 * mib,
			protectRelaxed:      0.38,
			protectConstrained:  0.22,
			headroomRelaxed:     0.52,
			headroomConstrained: 0.30,
			minFloorBytes:       96 * mib,
			minHeadroomBytes:    96 * mib,
			reclaimPriority:     2,
		}
	}
}

func interpolate(relaxed, constrained, pressure float64) float64 {
	return relaxed + ((constrained - relaxed) * pressure)
}

func clamp(v, min, max int64) int64 {
	if max < min {
		max = min
	}
	if v < min {
		return min
	}
	if v > max {
		return max
	}
	return v
}

func maxInt64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}
