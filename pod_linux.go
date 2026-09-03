//go:build linux

package zip

import (
	"os"
	"regexp"
	"strconv"
	"strings"
)

// podRE matches the pod segment the kubelet writes into a container's cgroup
// path under either cgroup driver: `kubepods-burstable-pod<uid>.slice` for
// systemd, where the uid's dashes become underscores, and
// `/kubepods/burstable/pod<uid>` for cgroupfs.
var podRE = regexp.MustCompile(`pod([0-9a-f]{8})[-_]([0-9a-f]{4})[-_]([0-9a-f]{4})[-_]([0-9a-f]{4})[-_]([0-9a-f]{12})`)

// PodUID returns the UID of the Kubernetes pod the peer process runs in, read
// from the cgroup the kubelet placed it in, or "" for a process outside any
// pod. The kernel wrote the cgroup and the kubelet named it, so like the peer
// itself it is a fact about the caller and not a value the caller states.
func (p *Peer) PodUID() string {
	if p == nil || p.PID <= 0 {
		return ""
	}
	b, err := os.ReadFile("/proc/" + strconv.Itoa(p.PID) + "/cgroup")
	if err != nil {
		return ""
	}
	return podUID(string(b))
}

func podUID(cgroup string) string {
	m := podRE.FindStringSubmatch(cgroup)
	if m == nil {
		return ""
	}
	return strings.Join(m[1:], "-")
}
