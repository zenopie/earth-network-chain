package testutil

import (
	"sort"

	"github.com/consensys/gnark-crypto/ecc/bn254/fr"

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
	Referrer string // human whose handle is named, "" for none
	// ReferrerHandle is the handle named (Referrer's).
	ReferrerHandle string
	// ProverHuman, ProverSecret: the identity secret the passport proof was
	// made with, when it is not this registration's own (a registration
	// attempt by someone who lacks the idc's secret). "" for its own.
	ProverHuman  string
	ProverSecret uint64
}

// Registrations are the passport fixtures the app tests use, by name.
var Registrations = map[string]Registration{
	"A1": {Name: "A1", Human: "A", Secret: 1, Doc: "L898902C3", Date: "250101"},
	"B":  {Name: "B", Human: "B", Secret: 1, Doc: "X12345678", Date: "250101"},
	"C1": {Name: "C1", Human: "C", Secret: 1, Doc: "Y87654321", Date: "250101"},
	// A switches to a new identity secret, same passport, two days in.
	"A2": {Name: "A2", Human: "A", Secret: 2, Doc: "L898902C3", Date: "250103"},
	// C's registration lapses and C re-enters, four days in, referred by A
	// (A2's handle "amy").
	"C2": {Name: "C2", Human: "C", Secret: 2, Doc: "Y87654321", Date: "250105", Referrer: "A", ReferrerHandle: "amy"},
	// D registers four days in, also naming A's handle.
	"D1": {Name: "D1", Human: "D", Secret: 1, Doc: "Z11223344", Date: "250105", Referrer: "A", ReferrerHandle: "amy"},
	// Refused (audit R2-B1, R2-B2). A3: A, after the switch to A2, switches
	// back to A1's identity (fresh notes, so a fresh binding): an idc
	// registered before. SALE: A switches her passport to buyer K's idc,
	// which she cannot prove: the circuit outputs the idc of the secret she
	// proves with (her own), not K's.
	"A3":   {Name: "A3", Human: "A", Secret: 1, Doc: "L898902C3", Date: "250103"},
	"SALE": {Name: "SALE", Human: "K", Secret: 1, Doc: "L898902C3", Date: "250103", ProverHuman: "A", ProverSecret: 3},
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

// ProofSecret is the identity secret its passport proof was made with:
// IDSecret, unless ProverHuman says otherwise.
func (r Registration) ProofSecret() fr.Element {
	if r.ProverHuman != "" {
		return Det("id/"+r.ProverHuman, r.ProverSecret)
	}
	return r.IDSecret()
}

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

// ShieldedAddress is a human's wallet address (its handle resolves to it):
// the wallet's owner key and a fixed stand-in encryption key.
func ShieldedAddress(human string) privacy.ShieldedAddress {
	a := privacy.ShieldedAddress{OwnerPK: privacy.OwnerPK(WalletNK(human))}
	copy(a.EKPub[:], privacy.FieldBytes(Det("ek/"+human, 0)))
	return a
}

// ReferralNote is the referrer's half of r's reward as the chain mints it:
// to the referrer's address, with the opening derived from r's passport
// nullifier and the leaf index it was given (privacy.ReferralOpening). Its
// value is only known once paid.
func (r Registration) ReferralNote(passportNullifier fr.Element, leafIndex uint64) Note {
	rho, rcm := privacy.ReferralOpening(passportNullifier, leafIndex)
	return Note{NK: WalletNK(r.Referrer), Denom: "uerth", Rho: rho, Rcm: rcm}
}

// ReferrerField is the affiliate r names, as the binding carries it:
// privacy.AffiliateField(handle), 0 for none
// (types.MsgRegister.AffiliateField).
func (r Registration) ReferrerField() fr.Element {
	if r.ReferrerHandle == "" {
		return fr.Element{}
	}
	return privacy.AffiliateField(r.ReferrerHandle)
}

// CiphertextAnml and CiphertextErth are the registration's note ciphertexts:
// fixed stand-ins of an amount-blind ciphertext's length (nothing decrypts
// them in the tests), bound by Binding.
func (r Registration) CiphertextAnml() []byte {
	return shieldedtest.BlindCT("personhood-ct:" + r.Name + ":10")
}
func (r Registration) CiphertextErth() []byte {
	return shieldedtest.BlindCT("personhood-ct:" + r.Name + ":11")
}

// Binding is the passport proof's address input.
func (r Registration) Binding() fr.Element {
	return privacy.RegistrationBinding(shieldedtest.ChainID, r.IDC(), r.AnmlNote().PC(), r.CiphertextAnml(), r.ErthPC(), r.CiphertextErth(), r.ReferrerField())
}
