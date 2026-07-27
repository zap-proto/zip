//go:build !linux

package zip

// readUsage reports nothing where /proc is unavailable. The zero Usage is
// meaningful: it says "not measured here", not "measured as zero".
func readUsage(int) Usage { return Usage{} }
