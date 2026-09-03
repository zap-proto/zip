//go:build linux

package zip

import (
	"os"
	"testing"
)

func TestPodUIDReadsTheKubeletsCgroupUnderEitherDriver(t *testing.T) {
	const uid = "e03401ab-1ff2-44e3-8260-3d175888e4a7"
	cases := map[string]string{
		"systemd besteffort": "0::/kubepods.slice/kubepods-besteffort.slice/kubepods-besteffort-pode03401ab_1ff2_44e3_8260_3d175888e4a7.slice/cri-containerd-2acff90d.scope\n",
		"systemd burstable":  "0::/kubepods.slice/kubepods-burstable.slice/kubepods-burstable-pode03401ab_1ff2_44e3_8260_3d175888e4a7.slice/cri-containerd-2acff90d.scope\n",
		"systemd guaranteed": "0::/kubepods.slice/kubepods-pode03401ab_1ff2_44e3_8260_3d175888e4a7.slice/cri-containerd-2acff90d.scope\n",
		"cgroupfs":           "0::/kubepods/burstable/pode03401ab-1ff2-44e3-8260-3d175888e4a7/2acff90d\n",
		"cgroup v1":          "12:pids:/kubepods/besteffort/pode03401ab-1ff2-44e3-8260-3d175888e4a7/2acff90d\n11:memory:/kubepods/besteffort/pode03401ab-1ff2-44e3-8260-3d175888e4a7/2acff90d\n",
	}
	for name, cg := range cases {
		if got := podUID(cg); got != uid {
			t.Errorf("%s: got %q, want %q", name, got, uid)
		}
	}
	for name, cg := range map[string]string{
		"host process":   "0::/user.slice/user-1000.slice/session-3.scope\n",
		"docker, no pod": "0::/system.slice/docker-2acff90d.scope\n",
		"uid too short":  "0::/kubepods.slice/kubepods-pode03401ab_1ff2.slice\n",
		"empty":          "",
	} {
		if got := podUID(cg); got != "" {
			t.Errorf("%s: got %q, want no pod", name, got)
		}
	}
}

// A peer outside Kubernetes is not a pod, and a peer the kernel never attested
// is not one either.
func TestPodUIDIsEmptyOffCluster(t *testing.T) {
	if os.Getenv("KUBERNETES_SERVICE_HOST") != "" {
		t.Skip("running inside a pod")
	}
	if got := (&Peer{PID: os.Getpid()}).PodUID(); got != "" {
		t.Fatalf("this process reads as pod %q", got)
	}
	var none *Peer
	if got := none.PodUID(); got != "" {
		t.Fatalf("nil peer reads as pod %q", got)
	}
}
