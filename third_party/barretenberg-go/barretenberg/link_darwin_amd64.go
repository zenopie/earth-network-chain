//go:build darwin && amd64

package barretenberg

// #cgo LDFLAGS: -L${SRCDIR}/../lib/darwin_amd64 -lbarretenberg -lc++ -lm
import "C"
