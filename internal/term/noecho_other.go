//go:build !linux

package term

// NoEcho is a no-op outside Linux.
func NoEcho(fd int) func() { return func() {} }
