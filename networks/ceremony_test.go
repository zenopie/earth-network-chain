package networks

import (
	"encoding/base64"
	"fmt"
	"net"
	"net/netip"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/cosmos/cosmos-sdk/crypto/keys/secp256k1"
	"github.com/cosmos/cosmos-sdk/types/bech32"
)

// The launch ceremony (scripts/ceremony.sh). These are the decisions it
// carries out; changing one is a ceremony decision, made here and in the
// script together.
const (
	// launchOperator is the genesis validator's operator account.
	launchOperator = "earth1n6amvkgfrrgy6ulhurewnm0endkgye69fkcapr"
	// launchConsensusKey is the genesis validator's consensus key: never used
	// to sign any earlier chain.
	launchConsensusKey = "PGqvPN4CxEkxvvh3tSBX0SGeBgjMdqQwZkdHt8FRLm4="
	// launchValidatorCoins is the operator account's genesis balance.
	launchValidatorCoins = "1000000000uerth"
	// placeholderOperator signs the placeholder gentx until the ceremony.
	placeholderOperator = "earth14e6sqtf5y7mtzwykqreewe9kg3w94t0f25d54a"
	// placeholderGenesisTime is the placeholder's (past) genesis_time.
	placeholderGenesisTime = "2026-10-02T12:00:00Z"
	// placeholderMoniker is the placeholder gentx's (devnet) moniker.
	placeholderMoniker = "earth-akash-devnet"
)

// publicPeer refuses a gentx memo that is not ID@HOST:PORT with a public
// host, as scripts/ceremony.sh does.
func publicPeer(memo string) error {
	id, hostport, ok := strings.Cut(memo, "@")
	if !ok || len(id) != 40 {
		return fmt.Errorf("not <40-hex node id>@HOST:PORT")
	}
	host, port, err := net.SplitHostPort(hostport)
	if err != nil || port == "" {
		return fmt.Errorf("not HOST:PORT: %v", err)
	}
	if host == "localhost" {
		return fmt.Errorf("host %s is not public", host)
	}
	if ip, err := netip.ParseAddr(host); err == nil &&
		(ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsUnspecified() || ip.IsMulticast()) {
		return fmt.Errorf("host %s is not public", host)
	}
	return nil
}

// devnetAccounts are keys that have been on a laptop: in the placeholder set
// only, never in the launch genesis.
var devnetAccounts = []string{
	"earth1s7rgscltvw8v3kzhj46pptdqg843ngs7th9ywp", // faucet
	"earth1jtc2zjmmmyttdayz6aw8vfgt5qn4hg7rpxaar6", // ads-for-gas hot wallet
}

// usedConsensusKeys signed an earlier earth-1: a gentx with one could
// double-sign the same heights.
var usedConsensusKeys = []string{"kTMzoCBEj1g2z49K1D/jxuLGrhTsnzfTx6Gf1LnBUJw="}

type gentxDoc struct {
	Body struct {
		Memo     string `json:"memo"`
		Messages []struct {
			Type        string `json:"@type"`
			Description struct {
				Moniker string `json:"moniker"`
			} `json:"description"`
			ValidatorAddress string `json:"validator_address"`
			Pubkey           struct {
				Type string `json:"@type"`
				Key  string `json:"key"`
			} `json:"pubkey"`
			Value coin `json:"value"`
		} `json:"messages"`
	} `json:"body"`
	AuthInfo struct {
		SignerInfos []struct {
			PublicKey struct {
				Type string `json:"@type"`
				Key  string `json:"key"`
			} `json:"public_key"`
		} `json:"signer_infos"`
	} `json:"auth_info"`
}

type ceremonyGenesis struct {
	GenesisTime string `json:"genesis_time"`
	AppState    struct {
		Genutil struct {
			GenTxs []gentxDoc `json:"gen_txs"`
		} `json:"genutil"`
		Bank struct {
			Balances []struct {
				Address string `json:"address"`
			} `json:"balances"`
		} `json:"bank"`
	} `json:"app_state"`
}

// TestLaunchCeremony: the genesis is exactly one of two states, the
// placeholder (ceremony pending) or the launch genesis, never a mix. What
// holds in both is checked in both; what only the ceremony makes true is
// checked once it has run, and until then the test reports PENDING CEREMONY
// (a skip, or a failure with EARTH_REQUIRE_CEREMONY set, as scripts/ceremony.sh
// and a release build run it).
func TestLaunchCeremony(t *testing.T) {
	g := readJSON[ceremonyGenesis](t, "genesis.json")
	accts := readJSON[accountsDoc](t, "genesis/accounts.json")

	// Both states: one gentx, the launch consensus key, signed by its
	// operator's own account, which genesis funds for the self-delegation.
	if len(g.AppState.Genutil.GenTxs) != 1 {
		t.Fatalf("%d gentxs; the launch genesis has exactly one", len(g.AppState.Genutil.GenTxs))
	}
	tx := g.AppState.Genutil.GenTxs[0]
	if len(tx.Body.Messages) != 1 || tx.Body.Messages[0].Type != "/cosmos.staking.v1beta1.MsgCreateValidator" {
		t.Fatalf("the gentx is not one MsgCreateValidator")
	}
	m := tx.Body.Messages[0]
	if m.Pubkey.Type != "/cosmos.crypto.ed25519.PubKey" || m.Pubkey.Key != launchConsensusKey {
		t.Errorf("gentx consensus key %s %s, want the launch key %s", m.Pubkey.Type, m.Pubkey.Key, launchConsensusKey)
	}
	for _, used := range usedConsensusKeys {
		if m.Pubkey.Key == used {
			t.Errorf("gentx consensus key %s signed an earlier earth-1", used)
		}
	}
	_, valBz, err := bech32.DecodeAndConvert(m.ValidatorAddress)
	if err != nil {
		t.Fatalf("validator_address %q: %v", m.ValidatorAddress, err)
	}
	operator, err := bech32.ConvertAndEncode("earth", valBz)
	if err != nil {
		t.Fatal(err)
	}
	if len(tx.AuthInfo.SignerInfos) != 1 || tx.AuthInfo.SignerInfos[0].PublicKey.Type != "/cosmos.crypto.secp256k1.PubKey" {
		t.Fatalf("the gentx has no single secp256k1 signer")
	}
	pkBz, err := base64.StdEncoding.DecodeString(tx.AuthInfo.SignerInfos[0].PublicKey.Key)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := bech32.ConvertAndEncode("earth", (&secp256k1.PubKey{Key: pkBz}).Address())
	if err != nil {
		t.Fatal(err)
	}
	if signer != operator {
		t.Errorf("the gentx is signed by %s, not its operator %s", signer, operator)
	}

	keyed := map[string]string{}
	for _, a := range accts.Keyed {
		keyed[a.Address] = a.Coins
	}
	if _, ok := keyed[operator]; !ok {
		t.Errorf("the gentx operator %s is not a keyed genesis account", operator)
	}
	inGenesis := map[string]bool{}
	for _, b := range g.AppState.Bank.Balances {
		inGenesis[b.Address] = true
	}

	switch operator {
	case placeholderOperator:
		// Pending: exactly the placeholder set, so a half-run ceremony fails.
		want := append([]string{placeholderOperator}, devnetAccounts...)
		if len(accts.Keyed) != len(want) {
			t.Errorf("placeholder gentx with %d keyed accounts, want the placeholder set %v", len(accts.Keyed), want)
		}
		for _, a := range want {
			if _, ok := keyed[a]; !ok {
				t.Errorf("placeholder gentx but %s is missing from accounts.json: a partial ceremony", a)
			}
		}
		if _, ok := keyed[launchOperator]; ok {
			t.Errorf("placeholder gentx but the launch operator is in accounts.json: a partial ceremony")
		}
		if g.GenesisTime != placeholderGenesisTime {
			t.Errorf("placeholder gentx but genesis_time %s: a partial ceremony", g.GenesisTime)
		}
		msg := "PENDING CEREMONY: networks/genesis.json is the placeholder (gentx operator " + placeholderOperator +
			", devnet accounts " + strings.Join(devnetAccounts, ", ") + "). Run\n" +
			"  scripts/ceremony.sh --genesis-time <RFC3339> --pubkey '{\"@type\":\"/cosmos.crypto.ed25519.PubKey\",\"key\":\"" + launchConsensusKey + "\"}' --memo-peer ID@HOST:PORT --moniker NAME"
		if os.Getenv("EARTH_REQUIRE_CEREMONY") != "" {
			t.Fatal(msg)
		}
		t.Skip(msg)

	case launchOperator:
		for _, a := range append([]string{placeholderOperator}, devnetAccounts...) {
			if _, ok := keyed[a]; ok {
				t.Errorf("%s is in accounts.json of the launch genesis", a)
			}
			if inGenesis[a] {
				t.Errorf("%s holds a balance in the launch genesis", a)
			}
		}
		// The memo is the launch genesis's only advertised peer, and the
		// moniker its validator's name: neither the placeholder's LAN peer
		// nor its devnet name (audit D-8).
		if m.Description.Moniker == placeholderMoniker {
			t.Errorf("the launch gentx keeps the placeholder moniker %q", placeholderMoniker)
		}
		if err := publicPeer(tx.Body.Memo); err != nil {
			t.Errorf("gentx memo %q: %v", tx.Body.Memo, err)
		}
		if keyed[launchOperator] != launchValidatorCoins {
			t.Errorf("launch operator holds %s, want %s", keyed[launchOperator], launchValidatorCoins)
		}
		if !inGenesis[launchOperator] {
			t.Errorf("the launch operator holds nothing in genesis.json")
		}
		gt, err := time.Parse(time.RFC3339, g.GenesisTime)
		if err != nil {
			t.Fatalf("genesis_time %q: %v", g.GenesisTime, err)
		}
		pt, _ := time.Parse(time.RFC3339, placeholderGenesisTime)
		if !gt.After(pt) {
			t.Errorf("genesis_time %s is not after the placeholder's %s: the ceremony sets the launch instant", g.GenesisTime, placeholderGenesisTime)
		}

	default:
		t.Errorf("gentx operator %s is neither the placeholder %s nor the launch operator %s", operator, placeholderOperator, launchOperator)
	}
}
