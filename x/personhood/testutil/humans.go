package testutil

import (
	"sort"

	"github.com/consensys/gnark-crypto/ecc/bn254/fr"

	"github.com/earth-network/earth/zk/privacy"
)

// Registration is one passport registration in the app tests: whose, with
// which identity secret, and the notes it is paid to. Its passport proof
// (x/personhood/testdata/passports/<Name>) is bound to Binding().
type Registration struct {
	Name     string
	Human    string // owner of the wallet (nk) the notes and affiliate pc belong to
	Secret   uint64 // which of the human's identity secrets
	Doc      string // MRZ document number: same doc, same passport nullifier
	Date     string // the proof's current_date, YYMMDD
	Referrer string // human whose affiliate pc is named, "" for none
}

// Registrations are the passport fixtures the app tests use, by name.
var Registrations = map[string]Registration{
	"A1": {Name: "A1", Human: "A", Secret: 1, Doc: "L898902C3", Date: "250101"},
	"B":  {Name: "B", Human: "B", Secret: 1, Doc: "X12345678", Date: "250101", Referrer: "A"},
	"C1": {Name: "C1", Human: "C", Secret: 1, Doc: "Y87654321", Date: "250101"},
	// A switches to a new identity secret, same passport, two days in.
	"A2": {Name: "A2", Human: "A", Secret: 2, Doc: "L898902C3", Date: "250103"},
	// C's registration lapses and C re-enters, four days in.
	"C2": {Name: "C2", Human: "C", Secret: 2, Doc: "Y87654321", Date: "250105"},
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

// AffiliatePC is a human's published referral pc.
func AffiliatePC(human string) fr.Element {
	return privacy.PC(privacy.OwnerPK(WalletNK(human)), Det("aff/"+human+"/rho", 0), Det("aff/"+human+"/rcm", 0))
}

// ReferrerPC is the affiliate pc r names, 0 for none.
func (r Registration) ReferrerPC() fr.Element {
	if r.Referrer == "" {
		return fr.Element{}
	}
	return AffiliatePC(r.Referrer)
}

// Binding is the passport proof's address input.
func (r Registration) Binding() fr.Element {
	return privacy.RegistrationBinding(r.IDC(), r.AnmlNote().PC(), r.ErthPC(), r.ReferrerPC())
}
