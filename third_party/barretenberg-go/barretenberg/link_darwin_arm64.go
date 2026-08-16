//go:build darwin && arm64

package barretenberg

// #cgo LDFLAGS: -L${SRCDIR}/../lib/darwin_arm64 -lbarretenberg -lc++ -lm
import "C"
