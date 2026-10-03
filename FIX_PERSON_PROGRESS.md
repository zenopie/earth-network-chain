# fix/person progress

Audit fixes for personhood/assembly + removal of the transparent gas grant.

- [x] 1. Assembly: revocation mixed with other messages, or nested (authz MsgExec / any wrapper) -> Refusal
- [ ] 2. Registration replay: refuse no-op switch (same idc); bind ciphertexts into RegistrationBinding
- [ ] 3. Remove transparent gas grant (gas-check membership, CheckGasMembership, GasScope/GasTransparentSignal, fixtures)
- [ ] 4. Sweep starvation: guaranteed share for expiry/caretaker/referrer sweeps
- [ ] 5. Removal-ballot cooldown param per groundworks option (default 30d)
- [x] 6. Remove dead PrivateAnchorAcceptor hook
