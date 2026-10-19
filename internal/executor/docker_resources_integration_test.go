package executor

import (
	"strings"
	"testing"
)

func TestDockerIntegrationResourceEnforcement(t *testing.T) {
	e := integrationDocker(t, StrictCodingPolicy())
	r := integrationExecute(t, e, t.TempDir(), `if [ -f /sys/fs/cgroup/cgroup.controllers ]; then
cat /sys/fs/cgroup/memory.max /sys/fs/cgroup/memory.swap.max /sys/fs/cgroup/cpu.max /sys/fs/cgroup/pids.max
else
cat /sys/fs/cgroup/memory/memory.limit_in_bytes /sys/fs/cgroup/memory/memory.memsw.limit_in_bytes /sys/fs/cgroup/cpu/cpu.cfs_quota_us /sys/fs/cgroup/cpu/cpu.cfs_period_us /sys/fs/cgroup/pids/pids.max
fi`)
	if r.ExitCode != 0 {
		t.Fatalf("cannot inspect enforced resource controls: %+v", r)
	}
	// cgroup v2: 1GiB RAM, zero additional swap, two CPUs, 128 PIDs.
	v2 := "1073741824\n0\n200000 100000\n128"
	// cgroup v1: memory+swap total equals RAM, same CPU quota/period.
	v1 := "1073741824\n1073741824\n200000\n100000\n128"
	if got := strings.TrimSpace(r.Stdout); got != v1 && got != v2 {
		t.Fatalf("unexpected resource limits: %q", got)
	}
}
