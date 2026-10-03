# fix/person progress

Audit fixes for personhood/assembly + removal of the transparent gas grant.

- [x] 1. Assembly: revocation mixed with other messages, or nested (authz MsgExec / any wrapper) -> Refusal
- [ ] 2. Registration replay: refuse no-op switch (same idc); bind ciphertexts into RegistrationBinding
- [x] 3. Remove transparent gas grant (gas-check membership, CheckGasMembership, GasScope/GasTransparentSignal, fixtures)
- [x] 4. Sweep starvation: guaranteed share for expiry/caretaker/referrer sweeps
- [x] 5. Removal-ballot cooldown param per groundworks option (default 30d)
- [x] 6. Remove dead PrivateAnchorAcceptor hook
  - 5: constant types.RemovalCooldown (30d), not a param: x/assembly deliberately has no params (gov must not tune the chamber). Genesis field removal_cooldowns (5).
