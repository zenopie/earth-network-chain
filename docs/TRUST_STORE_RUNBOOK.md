# Trust store runbook

What to do when a passport certificate has to be revoked or added, and how long
each path takes.

`x/pki` decides which passports can register. A Country Signing Certificate
Authority (CSCA) signs a country's Document Signers (DSCs), and a DSC signs
passports. The chain accepts a registration only if its DSC chains to a CSCA it
trusts and is not revoked.

Both changes go through governance. Nobody holds a key that can do it directly —
the authority is the gov module account, which has no private key.

---

## Emergency: a Document Signer is compromised

**Decided in advance: this goes on the expedited track.** One day, not seven.

Everything below should be ready before it's needed. Drafting under pressure is
how the one-day path becomes four.

### 1. Identify the key

Revocation is by the DSC's **public key**, not by certificate. That's deliberate:
revoking the key covers every certificate carrying it.

```bash
# From the certificate
openssl x509 -in dsc.cer -inform DER -pubkey -noout > pub.pem

# Confirm the chain knows it
earthd query pki dsc <identifier> --node https://rpc.erth.network
```

### 2. Write the proposal

`revoke-dsc.json`:

```json
{
  "messages": [
    {
      "@type": "/earth.pki.v1.MsgRevokeDsc",
      "authority": "GOV_MODULE_ADDRESS",
      "pubkey": "BASE64_DER_PUBLIC_KEY"
    }
  ],
  "metadata": "",
  "deposit": "5000000uerth",
  "title": "Revoke compromised DSC <country> <identifier>",
  "summary": "Reason. When it was discovered. Who reported it. What is known about registrations already made with it.",
  "expedited": true
}
```

Get the authority address:

```bash
earthd query auth module-account gov --node https://rpc.erth.network
```

### 3. Submit

```bash
earthd tx gov submit-proposal revoke-dsc.json \
  --from <key> --chain-id earth-1 --gas auto --gas-adjustment 1.5
```

**The deposit is 5 ERTH and must be there in full**, or the proposal sits in
deposit period and the clock never starts.

### 4. Vote and watch

```bash
earthd tx gov vote <id> yes --from <key> --chain-id earth-1
earthd query gov proposal <id>
```

Passes at 33.4% quorum and three quarters yes — the expedited bar is higher than
the normal two thirds, because it buys a one-day vote instead of a week.

### 5. After it passes

Revocation is **not retroactive**. Registrations already made with that DSC stay
valid.

That's a deliberate design choice, not an oversight: every registration records
its DSC publicly, so they can be listed and handled separately rather than being
wiped by a single vote.

```bash
earthd query personhood registrations-by-dsc <dsc-key> --node https://rpc.erth.network
```

Decide separately whether those registrations need action, and say publicly what
you decided.

---

## Routine: adding a CSCA

A new country, or a country rotating its root. No urgency — use the normal
7-day track.

CSCAs come from the ICAO master list. Update the trust store rather than adding
by hand, so the repo and the chain agree:

```bash
# add the certificate, then regenerate
go run ./tools/pki-genesis csca/masterlist/allowlist.ml csca/additional/*.cer
```

For a chain already running, the certificate goes on-chain via `MsgAddCsca`,
same proposal shape as above with `"expedited": false` and a 1 ERTH deposit.

Keep `csca/` in sync either way. If the repo and the chain disagree about which
passports are accepted, the repo is wrong and nobody finds out until a genesis
export.

---

## Timing

| | deposit | voting | total |
| --- | --- | --- | --- |
| Expedited — revocation | 5 ERTH | 1 day | ~1 day |
| Normal — adding a CSCA | 1 ERTH | 7 days | ~7 days |

Quorum 33.4% either way. Two thirds yes to pass a normal proposal, three quarters
for an expedited one. While there is one validator, every vote passes —
the delay is the voting period itself, not the outcome.

---

## What is not covered here

**A compromised CSCA**, as opposed to a DSC. There is no `MsgRevokeCsca`. A
country's root going bad would need a parameter change or a code change, and
neither is a one-day path. Worth deciding on before it happens.

**A DSC whose key was never valid** — i.e. a CSCA issuing certificates it should
not. Same problem one level up.

---

## Before launch

- [ ] Confirm the expedited track is right for revocation, or say why not.
- [ ] Fill in `GOV_MODULE_ADDRESS` above with the real value.
- [ ] Check the query commands against the shipped binary — they are written from
      the module's messages, not from a live run.
- [ ] Decide who can submit a revocation, and how they are reached out of hours.
      At launch that is the genesis validator,
      `earth14e6sqtf5y7mtzwykqreewe9kg3w94t0f25d54a`, whose key is the
      `VALIDATOR_MNEMONIC` in the gitignored `.env`. One person with one key is
      fine to launch with; not knowing who they are is not — and a mnemonic in a
      `.env` is not where this key should live once the chain carries value.
