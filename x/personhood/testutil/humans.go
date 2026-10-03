package testutil

import (
	"sort"

	"github.com/consensys/gnark-crypto/ecc/bn254/fr"
	"github.com/cosmos/cosmos-sdk/crypto/keys/secp256k1"

	shieldedtest "github.com/earth-network/earth/x/shielded/testutil"
	"github.com/earth-network/earth/zk/privacy"
)

// Registration is one passport registration in the app tests: whose, with
// which identity secret, and the notes it is paid to. Its passport proof
// (x/personhood/testdata/passports/<Name>) is bound to Binding().
type Registration struct {
	Name     string
	Human    string // owner of the wallet (nk) the notes belong to
	Secret   uint64 // which of the human's identity secrets
	Doc      string // MRZ document number: same doc, same passport nullifier
	Date     string // the proof's current_date, YYMMDD
	Referrer string // human whose referral address is named, "" for none
	// ReferrerCode names the referrer by referral code instead (the code
	// Referrer's binding claimed); "" names it by address.
	ReferrerCode string
}

// Registrations are the passport fixtures the app tests use, by name.
var Registrations = map[string]Registration{
	"A1": {Name: "A1", Human: "A", Secret: 1, Doc: "L898902C3", Date: "250101"},
	"B":  {Name: "B", Human: "B", Secret: 1, Doc: "X12345678", Date: "250101"},
	"C1": {Name: "C1", Human: "C", Secret: 1, Doc: "Y87654321", Date: "250101"},
	// A switches to a new identity secret, same passport, two days in.
	"A2": {Name: "A2", Human: "A", Secret: 2, Doc: "L898902C3", Date: "250103"},
	// C's registration lapses and C re-enters, four days in, referred by A.
	"C2": {Name: "C2", Human: "C", Secret: 2, Doc: "Y87654321", Date: "250105", Referrer: "A"},
	// D registers four days in, naming A by referral code (A2 claims
	// "alice" with its binding).
	"D1": {Name: "D1", Human: "D", Secret: 1, Doc: "Z11223344", Date: "250105", Referrer: "A", ReferrerCode: "alice"},
}

// RegistrationNames lists Registrations in a stable order.
func RegistrationNames() []string {
	out := make([]string, 0, len(Registrations))
	for n := range Registrations {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

// WalletNK is a human's note spending key.
func WalletNK(human string) fr.Element { return Det("nk/"+human, 0) }

// IDSecret is the identity secret of r.
func (r Registration) IDSecret() fr.Element { return Det("id/"+r.Human, r.Secret) }

// IDC is its identity commitment.
func (r Registration) IDC() fr.Element { return privacy.IDC(r.IDSecret()) }

func (r Registration) note(kind, denom string, value uint64) Note {
	return Note{NK: WalletNK(r.Human), Denom: denom, Value: value,
		Rho: Det(r.Name+"/"+kind+"/rho", 0), Rcm: Det(r.Name+"/"+kind+"/rcm", 0)}
}

// AnmlNote is the registration's 1 ANML note.
func (r Registration) AnmlNote() Note { return r.note("anml", "uanml", 1_000_000) }

// ErthPC is where the registration reward goes (its value is only known once
// paid).
func (r Registration) ErthPC() fr.Element { return r.note("erth", "uerth", 0).PC() }

// ReferralKey is the secp256k1 key of the account a human binds as their
// referral address: binding it takes the key's signed consent.
func ReferralKey(human string) *secp256k1.PrivKey {
	return secp256k1.GenPrivKeyFromSecret([]byte("referral/" + human))
}

// ReferralAddress is the account a human binds as their referral address
// (raw bytes; bech32 it with the chain's codec): ReferralKey's address.
func ReferralAddress(human string) []byte {
	return ReferralKey(human).PubKey().Address()
}

// ReferrerField is the affiliate r names, as the binding carries it:
// Bytes(address bytes), AffiliateCode(code) when named by code, 0 for none
// (types.AffiliateField).
func (r Registration) ReferrerField() fr.Element {
	if r.ReferrerCode != "" {
		return privacy.AffiliateCode(r.ReferrerCode)
	}
	if r.Referrer == "" {
		return fr.Element{}
	}
	return privacy.Bytes(ReferralAddress(r.Referrer))
}

// CiphertextAnml and CiphertextErth are the registration's note ciphertexts:
// fixed stand-ins of an amount-blind ciphertext's length (nothing decrypts
// them in the tests), bound by Binding.
func (r Registration) CiphertextAnml() []byte { return shieldedtest.BlindCT("personhood-ct:" + r.Name + ":10") }
func (r Registration) CiphertextErth() []byte { return shieldedtest.BlindCT("personhood-ct:" + r.Name + ":11") }

// Binding is the passport proof's address input.
func (r Registration) Binding() fr.Element {
	return privacy.RegistrationBinding(r.IDC(), r.AnmlNote().PC(), r.CiphertextAnml(), r.ErthPC(), r.CiphertextErth(), r.ReferrerField())
}
