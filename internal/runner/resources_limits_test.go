package runner

import (
	"context"
	"strings"
	"testing"

	"github.com/containerd/containerd/v2/pkg/oci"
	"github.com/opencontainers/runtime-spec/specs-go"
)

var fullLimits = ResourceLimits{
	MemoryMB:   512,
	MemorySwap: 1024,
	CPUShares:  512,
	CPUQuota:   50000,
	CPUPeriod:  200000,
	PidsLimit:  64,
}

// TestContainerdAppliesEveryDeclaredLimit: cpu_quota, cpu_period and
// memory_swap were accepted and dropped on the containerd path while the CLI
// path honoured some of them, so the same recipe was CPU-bounded under docker
// and not under containerd.
func TestContainerdAppliesEveryDeclaredLimit(t *testing.T) {
	s := &oci.Spec{Linux: &specs.Linux{}}
	for _, o := range resourceSpecOpts(fullLimits) {
		if err := o(context.Background(), nil, nil, s); err != nil {
			t.Fatalf("apply: %v", err)
		}
	}
	res := s.Linux.Resources
	if res == nil || res.Memory == nil || res.CPU == nil || res.Pids == nil {
		t.Fatalf("resources not populated: %+v", res)
	}
	if got := *res.Memory.Limit; got != 512<<20 {
		t.Errorf("memory limit = %d", got)
	}
	if got := *res.Memory.Swap; got != 1024<<20 {
		t.Errorf("memory+swap = %d, want 1024 MB in bytes", got)
	}
	if res.CPU.Quota == nil || *res.CPU.Quota != 50000 {
		t.Errorf("cpu quota = %v, want 50000", res.CPU.Quota)
	}
	if res.CPU.Period == nil || *res.CPU.Period != 200000 {
		t.Errorf("cpu period = %v, want 200000", res.CPU.Period)
	}
	if res.Pids.Limit == nil || *res.Pids.Limit != 64 {
		t.Errorf("pids = %v", res.Pids.Limit)
	}
}

func TestContainerdQuotaDefaultsTheKernelPeriod(t *testing.T) {
	s := &oci.Spec{Linux: &specs.Linux{}}
	for _, o := range resourceSpecOpts(ResourceLimits{CPUQuota: 50000}) {
		_ = o(context.Background(), nil, nil, s)
	}
	if p := s.Linux.Resources.CPU.Period; p == nil || *p != 100000 {
		t.Errorf("period = %v, want the 100000 default a quota is measured against", p)
	}
}

func TestCLIPassesEveryDeclaredLimit(t *testing.T) {
	args := strings.Join((&CLIRunner{cli: "docker"}).buildRunArgs(Options{Name: "c", Image: "x", Resources: fullLimits}), " ")
	for _, want := range []string{"-m 512m", "--memory-swap 1024m", "--cpu-shares 512", "--cpu-quota 50000", "--cpu-period 200000", "--pids-limit 64"} {
		if !strings.Contains(args, want) {
			t.Errorf("args %q lack %q", args, want)
		}
	}
}
