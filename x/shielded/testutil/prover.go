package testutil

import (
	"bytes"
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
)

// Prover returns action proofs for the tests: from a cache of committed
// proofs keyed by their public inputs, or, when EARTH_CIRCUITS points at
// earth-network-mobile/circuits, by proving the missing ones with nargo + bb
// (v5.0.0) and writing them to the cache. Every proof it writes was checked:
// bb's public inputs must equal the chain's, byte for byte (the Go <-> Noir
// parity check of the bases, R, cv and the sighash).
//
//	scripts/shielded-fixtures.sh [path-to-earth-network-mobile/circuits]
//
// re-runs the tests with EARTH_CIRCUITS set.
type Prover struct {
	// Dir is the proof cache, x/shielded/testdata/proofs.
	Dir string
	// VK is the action verifying key file the proofs are made against.
	VK string
	// Script names what re-records Dir, for the error a missing proof
	// reports (default scripts/shielded-fixtures.sh).
	Script string
}

// ForDir is a prover caching under dir (relative to the test's package)
// against this module's action key, re-recorded by script.
func ForDir(tb testing.TB, dir, script string) *Prover {
	p := DefaultProver(tb)
	p.Dir, p.Script = dir, script
	return p
}

// DefaultProver is the cache under this module's testdata, for a test
// running in a package directory one or two levels below the repo root
// (x/shielded/keeper, app).
func DefaultProver(tb testing.TB) *Prover {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		tb.Fatal("no caller")
	}
	td := filepath.Join(filepath.Dir(file), "..", "testdata")
	return &Prover{Dir: filepath.Join(td, "proofs"), VK: filepath.Join(td, "action.vk")}
}

// VerifyingKey is the action key the cached proofs verify against.
func (p *Prover) VerifyingKey(tb testing.TB) []byte {
	tb.Helper()
	bz, err := os.ReadFile(p.VK)
	if err != nil {
		tb.Fatalf("action verifying key: %v (run scripts/privacy-vks.sh)", err)
	}
	return bz
}

// File is the cache path of the action proof with public inputs pub.
func (p *Prover) File(pub [][]byte) string {
	h := sha256.New()
	h.Write([]byte("action"))
	for _, x := range pub {
		h.Write(x)
	}
	return filepath.Join(p.Dir, fmt.Sprintf("action-%x.proof", h.Sum(nil)[:10]))
}

// Prove returns the action proof for (toml, pub), failing the test when it
// is neither cached nor provable.
func (p *Prover) Prove(tb testing.TB, toml string, pub [][]byte) []byte {
	tb.Helper()
	proof, err := p.TryProve(toml, pub)
	if err != nil {
		tb.Fatal(err)
	}
	return proof
}

// ErrNoCircuits is TryProve's error for a proof that is not cached when
// EARTH_CIRCUITS is unset.
var ErrNoCircuits = errors.New("proof not cached and EARTH_CIRCUITS unset")

// ErrWitnessRefused is TryProve's error when the circuit refuses the witness
// (nargo execute fails): no proof of it exists.
var ErrWitnessRefused = errors.New("the action circuit refuses this witness")

var (
	compileOnce sync.Once
	compiled    string
	compileErr  error
	proveMu     sync.Mutex
)

// Circuits is EARTH_CIRCUITS, "" when unset.
func Circuits() string { return os.Getenv("EARTH_CIRCUITS") }

// TryProve is Prove returning errors: ErrNoCircuits, ErrWitnessRefused, or a
// tool failure.
func (p *Prover) TryProve(toml string, pub [][]byte) ([]byte, error) {
	file := p.File(pub)
	if bz, err := os.ReadFile(file); err == nil {
		return bz, nil
	}
	src := Circuits()
	if src == "" {
		script := p.Script
		if script == "" {
			script = "scripts/shielded-fixtures.sh"
		}
		return nil, fmt.Errorf("%w: no proof fixture %s for this action: run %s", ErrNoCircuits, file, script)
	}
	proveMu.Lock()
	defer proveMu.Unlock()
	compileOnce.Do(func() {
		dir, err := os.MkdirTemp("", "action-circuits")
		if err != nil {
			compileErr = err
			return
		}
		if _, err := run(".", "cp", "-R", src, filepath.Join(dir, "circuits")); err != nil {
			compileErr = err
			return
		}
		compiled = filepath.Join(dir, "circuits")
		_ = os.RemoveAll(filepath.Join(compiled, "target"))
		_, compileErr = run(compiled, "nargo", "compile", "--package", "action", "--silence-warnings")
	})
	if compileErr != nil {
		return nil, compileErr
	}
	if err := os.WriteFile(filepath.Join(compiled, "action", "Prover.toml"), []byte(toml), 0o644); err != nil {
		return nil, err
	}
	if out, err := run(compiled, "nargo", "execute", "--package", "action", "--silence-warnings"); err != nil {
		return nil, fmt.Errorf("%w: %s", ErrWitnessRefused, out)
	}
	vk, err := filepath.Abs(p.VK)
	if err != nil {
		return nil, err
	}
	out, err := os.MkdirTemp("", "action-proof")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(out)
	if _, err := run(compiled, "bb", "prove", "-b", "target/action.json", "-w", "target/action.gz", "-k", vk, "-o", out, "-t", "noir-recursive"); err != nil {
		return nil, err
	}
	gotPub, err := os.ReadFile(filepath.Join(out, "public_inputs"))
	if err != nil {
		return nil, err
	}
	if !bytes.Equal(gotPub, bytes.Join(pub, nil)) {
		return nil, errors.New("bb's public inputs differ from the chain's")
	}
	proof, err := os.ReadFile(filepath.Join(out, "proof"))
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(p.Dir, 0o755); err != nil {
		return nil, err
	}
	return proof, os.WriteFile(file, proof, 0o644)
}

func run(dir, name string, args ...string) (string, error) {
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	home := os.Getenv("HOME")
	cmd.Env = append(os.Environ(), "PATH="+home+"/.nargo/bin:"+home+"/.bb:"+os.Getenv("PATH"))
	out, err := cmd.CombinedOutput()
	if err != nil {
		return string(out), fmt.Errorf("%s %v: %w: %s", name, args, err, out)
	}
	return string(out), nil
}
