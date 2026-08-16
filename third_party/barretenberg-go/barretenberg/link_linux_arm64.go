//go:build linux && arm64 && !muslc

package barretenberg

// #cgo LDFLAGS: -L${SRCDIR}/../lib/linux_arm64 -lbarretenberg -lc++ -lm -lpthread
import "C"
