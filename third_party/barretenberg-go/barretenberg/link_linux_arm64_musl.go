//go:build linux && arm64 && muslc

package barretenberg

// #cgo LDFLAGS: -L${SRCDIR}/../lib/linux_arm64_musl -lbarretenberg -lc++ -lm -lpthread
import "C"
