package cmd

import (
	"reflect"
	"strings"
	"testing"
)

// Private txs have no signer; any app-side mempool but the no-op one refuses
// them. The template every node writes must pin max-txs = -1, and say why.
func TestAppConfigPinsNoOpMempool(t *testing.T) {
	tmpl, cfg := initAppConfig()
	if !strings.Contains(tmpl, "EARTH: keep -1") || !strings.Contains(tmpl, "max-txs = {{ .Mempool.MaxTxs }}") {
		t.Fatal("app.toml template lost the mempool note")
	}
	got := reflect.ValueOf(cfg).FieldByName("Mempool").FieldByName("MaxTxs").Int()
	if got != -1 {
		t.Fatalf("mempool.max-txs = %d, want -1", got)
	}
}
