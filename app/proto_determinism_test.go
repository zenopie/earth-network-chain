package app_test

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// TestProtoMapsAreStablyMarshaled fails if any message in proto/earth/** declares
// a map field without (gogoproto.stable_marshaler) = true.
//
// gogoproto marshals map entries in Go's randomised iteration order unless the
// option is set. Any message written to state then serialises differently on
// every validator, so each computes a different AppHash and the network halts
// with "wrong Block.Header.AppHash". It cannot be caught by a single-node
// devnet, which only ever compares against itself.
//
// This is a real incident, not a hypothetical: caretaker's Params carries a
// verifying_keys map, and the chain halted on a 3-validator testnet the moment
// more than one register-circuit key was configured. A unit test on one message
// would not have prevented it — the next map field added anywhere would
// reintroduce it — so the check is structural.
func TestProtoMapsAreStablyMarshaled(t *testing.T) {
	root := filepath.Join("..", "proto", "earth")

	var offenders []string
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() || !strings.HasSuffix(path, ".proto") {
			return err
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		for _, m := range splitMessages(string(raw)) {
			if !strings.Contains(m.body, "map<") {
				continue
			}
			if !strings.Contains(m.body, "stable_marshaler") {
				offenders = append(offenders,
					filepath.Base(path)+": message "+m.name+" has a map field but no stable_marshaler")
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk protos: %v", err)
	}

	for _, o := range offenders {
		t.Errorf("%s", o)
	}
	if len(offenders) > 0 {
		t.Log("add `option (gogoproto.stable_marshaler) = true;` to the message and regenerate")
	}
}

type protoMessage struct {
	name string
	body string
}

// splitMessages returns each top-level `message X { ... }` block. Brace counting
// rather than a regex, so nested messages stay with their parent instead of
// truncating the body and hiding a map field.
func splitMessages(src string) []protoMessage {
	var out []protoMessage
	header := regexp.MustCompile(`(?m)^message\s+(\w+)\s*\{`)
	for _, loc := range header.FindAllStringSubmatchIndex(src, -1) {
		name := src[loc[2]:loc[3]]
		depth, i := 0, loc[1]-1
		for ; i < len(src); i++ {
			switch src[i] {
			case '{':
				depth++
			case '}':
				depth--
			}
			if depth == 0 {
				break
			}
		}
		if i >= len(src) {
			i = len(src) - 1
		}
		out = append(out, protoMessage{name: name, body: src[loc[1]:i]})
	}
	return out
}
