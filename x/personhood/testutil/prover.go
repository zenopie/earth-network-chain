package testutil

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"

	"github.com/consensys/gnark-crypto/ecc/bn254/fr"
)

// ProveEnv names the circuits workspace to prove with. Unset, a Prover
// replays committed proofs.
const ProveEnv = "EARTH_PROVE_CIRCUITS"

// Prover hands out proofs by name from Dir, proving them first when
// EARTH_PROVE_CIRCUITS is set.
type Prover struct {
	Dir string

	once     sync.Once
	work     string
	compiled map[string]bool
	err      error
}

// Proving reports whether this run makes proofs rather than replaying them.
func Proving() bool { return os.Getenv(ProveEnv) != "" }

func run(dir, name string, args ...string) error {
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	home, _ := os.UserHomeDir()
	cmd.Env = append(os.Environ(), "PATH="+filepath.Join(home, ".nargo/bin")+":"+filepath.Join(home, ".bb")+":"+os.Getenv("PATH"))
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s %v: %w\n%s", name, args, err, out)
	}
	return nil
}

func (p *Prover) setup() error {
	p.once.Do(func() {
		src := os.Getenv(ProveEnv)
		p.work, p.err = os.MkdirTemp("", "earth-prove-")
		if p.err != nil {
			return
		}
		p.err = run("", "cp", "-R", src, filepath.Join(p.work, "circuits"))
		if p.err == nil {
			_ = os.RemoveAll(filepath.Join(p.work, "circuits", "target"))
		}
		p.compiled = map[string]bool{}
	})
	return p.err
}

// VK returns circuit's verifying key: written to Dir/<circuit>.vk when
// proving, read from there otherwise.
func (p *Prover) VK(circuit string) ([]byte, error) {
	if Proving() {
		if err := p.compile(circuit); err != nil {
			return nil, err
		}
	}
	return os.ReadFile(filepath.Join(p.Dir, circuit+".vk"))
}

func (p *Prover) compile(circuit string) error {
	if err := p.setup(); err != nil {
		return err
	}
	if p.compiled[circuit] {
		return nil
	}
	c := filepath.Join(p.work, "circuits")
	if err := run(c, "nargo", "compile", "--package", circuit); err != nil {
		return err
	}
	vkDir := filepath.Join(p.work, "vk-"+circuit)
	if err := run(c, "bb", "write_vk", "-b", "target/"+circuit+".json", "-o", vkDir, "-t", "noir-recursive"); err != nil {
		return err
	}
	vk, err := os.ReadFile(filepath.Join(vkDir, "vk"))
	if err != nil {
		return err
	}
	if err := os.MkdirAll(p.Dir, 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(p.Dir, circuit+".vk"), vk, 0o644); err != nil {
		return err
	}
	p.compiled[circuit] = true
	return nil
}

// Proof returns the proof named name for circuit. When proving, toml is the
// witness and pub the public inputs the chain will verify against; bb's must
// equal them.
func (p *Prover) Proof(name, circuit, toml string, pub []fr.Element) ([]byte, error) {
	name = strings.ReplaceAll(name, "/", "_")
	path := filepath.Join(p.Dir, name+".proof")
	if !Proving() {
		return os.ReadFile(path)
	}
	if err := p.compile(circuit); err != nil {
		return nil, err
	}
	c := filepath.Join(p.work, "circuits")
	if err := os.WriteFile(filepath.Join(c, circuit, "Prover.toml"), []byte(toml), 0o644); err != nil {
		return nil, err
	}
	if err := run(c, "nargo", "execute", "--package", circuit); err != nil {
		return nil, fmt.Errorf("%s: %w", name, err)
	}
	out := filepath.Join(p.work, "proof-"+name)
	if err := run(c, "bb", "prove", "-b", "target/"+circuit+".json", "-w", "target/"+circuit+".gz",
		"-k", filepath.Join(p.work, "vk-"+circuit, "vk"), "-o", out, "-t", "noir-recursive"); err != nil {
		return nil, err
	}
	got, err := os.ReadFile(filepath.Join(out, "public_inputs"))
	if err != nil {
		return nil, err
	}
	var want []byte
	for _, e := range pub {
		b := e.Bytes()
		want = append(want, b[:]...)
	}
	if !bytes.Equal(got, want) {
		return nil, fmt.Errorf("%s: bb's public inputs differ from the chain's", name)
	}
	proof, err := os.ReadFile(filepath.Join(out, "proof"))
	if err != nil {
		return nil, err
	}
	return proof, os.WriteFile(path, proof, 0o644)
}
