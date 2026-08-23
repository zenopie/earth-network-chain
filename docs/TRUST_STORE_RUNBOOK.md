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

What is revoked is the DSC's **signing key**, not the certificate — revoking the
key covers every certificate carrying it. But you hand in a *certificate*, and
the chain derives the key from it, so the authority never has to encode a
canonical public key by hand.

```bash
# Check it parses and is the certificate you think it is
openssl x509 -in dsc.cer -inform DER -noout -subject -issuer -dates

# Who registered under it (x/pki has no DSC query; x/personhood has this one)
earthd query personhood registrations-by-dsc <dsc-key> --node https://rpc.erth.network
```

### 2. Write the proposal

`revoke-dsc.json`:

```json
{
  "messages": [
    {
      "@type": "/earth.pki.v1.MsgRevokeDsc",
      "authority": "GOV_MODULE_ADDRESS",
      "certificate_der": "BASE64_DER_CERTIFICATE"
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

Revocation **is retroactive, and starts on its own.** Passing the proposal does
two things: no further registration can be made with that signer, and the
registrations it already produced are queued for retirement. No second vote.

The retirement is paced — `purgeRevokedDscs` retires a bounded batch per block
out of the same budget as the expiry sweep, taking its share first — so a signer
behind many registrations drains over several blocks rather than in one. Watch
it finish:

```bash
# queued, then per-batch, then done
#   dsc_purge_started / registration_sweep_capped (reason=dsc_revoked) / dsc_purge_complete
earthd query personhood registrations-by-dsc <dsc-key> --node https://rpc.erth.network
```

ANML claims stop immediately regardless of the queue, because the claim path
re-reads the registration every time. Governance weight is what the purge is
for: a stream stores its total weight and only moves it when a voter is
explicitly cleared, so weight bought with a revoked signer stays counted until
its registrations are retired.

Say publicly what was retired and why. The people behind those registrations did
nothing wrong in the ordinary case — a compromised signer is the state's
failure, not theirs — and they can re-register once their country has a signer
the chain trusts again.

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

## Revoking a CSCA

A country's root, not one of its signers. Use when the state itself should no
longer be able to mint identities this chain accepts — its root key is
compromised, or governance has decided not to trust it.

Understand what this is before reaching for it. A CSCA in the trust store can
sign as many Document Signers as it likes, and each of those can sign passports
the chain counts as distinct humans. That is the sybil surface, and it is why
this exists. It is also why revoking one shuts out every honest passport holder
of that country, who did nothing.

**Track:** expedited if the root is compromised, normal if it is a policy
decision. A policy decision that cannot wait a week is worth a second look.

`revoke-csca.json`:

```json
{
  "messages": [
    {
      "@type": "/earth.pki.v1.MsgRevokeCsca",
      "authority": "GOV_MODULE_ADDRESS",
      "certificate_der": "BASE64_DER_CERTIFICATE"
    }
  ],
  "metadata": "",
  "deposit": "5000000uerth",
  "title": "Revoke CSCA <country>",
  "summary": "Reason. What is known about Document Signers already issued under it, and what will be done about registrations already made.",
  "expedited": true
}
```

Three things to know:

**One message covers the whole key.** Countries carry several CSCA certificates
— renewals, link certificates — that share one signing key, and any of them
verifies a child signature. The chain revokes the *key* behind the certificate
you hand in, so siblings go with it. You do not need one proposal per
certificate. You *do* need one per key, if the country has rotated to a genuinely
new one.

**It is prospective only.** Nothing already registered is touched, and no purge
starts. Left alone those registrations lapse within one
`registration_validity_seconds` — one year at genesis — because renewing
re-verifies the Document Signer and that check now fails. To retire them sooner,
revoke the individual DSCs with `MsgRevokeDsc` above, which does carry the purge.

**Re-adding the certificate undoes it.** There is no un-revoke message;
`MsgAddCsca` with the same certificate is the way back, and it clears the
revocation as a side effect. Worth knowing before someone proposes "re-add the
ICAO master list" as routine maintenance and quietly restores a revoked root.

Then update `csca/` in the repo to match, the same as for adding one — the
trust store on disk and the one on chain disagreeing is how a genesis export
starts trusting something nobody voted for.

---

## Timing

| | deposit | voting | total |
| --- | --- | --- | --- |
| Expedited — revocation (DSC or CSCA) | 5 ERTH | 1 day | ~1 day |
| Normal — adding a CSCA | 1 ERTH | 7 days | ~7 days |

Quorum 33.4% either way. Two thirds yes to pass a normal proposal, three quarters
for an expedited one. While there is one validator, every vote passes —
the delay is the voting period itself, not the outcome.

---

## What is not covered here

**Retiring a country's existing registrations in one step.** Revoking a CSCA
closes the door; it does not walk back through it. There is no country-wide
purge, and adding one was considered and deliberately left out — it is the
operation that strips personhood from people who did nothing, so it wants its
own decision rather than riding along with a root revocation. Today the paths
are per-DSC revocation, or waiting out the validity window.

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
