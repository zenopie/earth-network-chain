---
sidebar_position: 2
---

# Registering

You register once, from your phone, with your passport.

1. The app reads the chip in your passport.
2. Your phone builds a zero-knowledge proof that the passport is genuine and
   signed by a government the chain trusts.
3. Only the proof is sent.

Your name, date of birth, passport number and photo never leave the device. This
is a property of where the proof is generated, not a promise about how data is
handled after it is received — the network never receives it.

## The nullifier

The chain records a **nullifier**: a one-way identifier derived from the passport
itself.

It cannot be reversed into your passport, and it is what stops the same passport
registering twice. Renewing your passport produces the same nullifier, so a
renewal is not a second person.

This is the whole anti-Sybil mechanism. It is why an account cannot be duplicated
by making another wallet.

## What it gets you

- **1 ANML per day**, claimable in the app.
- **A vote in the Caretaker fund** — one human, one vote, regardless of holdings.
- **A share of the registration reward pool**, paid in ERTH when you register.

## Renewing

Registration lasts a year. Renew by registering again with the same passport.

## Which passports work

The chain trusts the Country Signing Certificate Authorities published by ICAO,
plus a few countries whose certificates ICAO does not distribute. Adding a
country is a governance decision.

If your passport has no readable chip, it cannot be used. That is a limitation of
the document, not of the network.
