//go:build !linux

package zip

// PodUID is "" where there is no cgroup to read.
func (p *Peer) PodUID() string { return "" }
