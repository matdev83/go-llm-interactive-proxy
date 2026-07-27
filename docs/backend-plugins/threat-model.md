# Executable backend plugin threat model

Dedicated threat model for hybrid optional connectors ([ADR 0008](../adr/0008-hybrid-backend-connector-plugins.md); spec task 9.3). Operator install guidance remains in [`operator.md`](operator.md).

## Trust equivalence

**Installed executable plugins are trust-equivalent to native code run as the proxy service account.**

Process isolation provides dependency, crash, lifecycle, and language-runtime separation. It is **not a malicious-code sandbox**. An attacker who can place or replace a digest-accepted artifact under a trusted root, or who already controls the proxy account, is outside this model’s residual-risk budget. Operators must treat plugin trees as privileged install surfaces (admin-owned, proxy read/execute).

## Assets

| Asset | Why it matters |
|---|---|
| Upstream/provider credentials and OAuth material | Direct account takeover / spend |
| Opaque connector configuration | Often embeds secrets or private endpoints |
| Canonical event stream integrity | Routing, failover, billing, and client protocol correctness |
| Proxy process identity and local IPC endpoints | Lateral move into configure/invoke |
| Digest-bound launch artifacts and staging trees | Exact-byte identity of launched code |
| Operator diagnostics | Secret/path leakage via logs and doctor output |

## Adversaries and entry points

1. **Local unprivileged process** on the same host (same or different UID/SID) attempting to dial plugin IPC, steal configure payloads, or inject frames.
2. **Filesystem adversary** with write access near plugin roots (symlink/junction escape, TOCTOU replacement after digest, staging race).
3. **Misconfigured operator** enabling `development_mode`, widening pipe DACLs, or placing plugins under mutable user-writable trees.
4. **Compromised or buggy plugin** (trusted code) emitting oversized frames, unknown event kinds, secret-bearing stderr/errors, or resource exhaustion.
5. **Stale generation / PID-reuse peer** after crash or restart trying to continue on a previous channel.

Out of scope as “sandbox escape from malicious plugin bytes”: once digest + trusted-root accept an artifact, that code runs with proxy privileges. Controls below reduce blast radius and stop weaker local peers; they do not sandbox hostile plugins.

## Accepted controls (must have executable tests)

| ID | Control | Failure mode |
|---|---|---|
| TM-01 | Trusted-directory containment; closed manifest v1 (unknown fields fail closed); no install hooks/downloads | Reject discovery/launch |
| TM-02 | Digest-bound exact-executable launch (descriptor or protected staging); pathname rehash alone insufficient; symlink/junction escape rejected; staging cleanup | Reject / cleanup |
| TM-03 | Approved OS local IPC only: Linux private AF_UNIX + `SO_PEERCRED` generation bind; Windows named pipe DACL + PID/SID/job; Darwin fail-closed until approved peer profile; no cookie-only/plaintext loopback; mTLS loopback only with private handle bootstrap | Fail before configure/secrets |
| TM-04 | Unauthorized, same-UID wrong-PID, stale-generation, and PID-reuse peers cannot negotiate/configure/invoke or receive credential responses | Peer rejected; zero secrets |
| TM-05 | Minimal child environment (no full inheritance); forbidden bootstrap keys; secrets only post peer+compat over confidential channel; argv/title/env secret absence | Env/bootstrap rejected |
| TM-06 | Inherited FD/handle minimization (channel ends only); descendant cleanup via cgroup/job where supported | Bounded inheritance |
| TM-07 | Bounded frames/messages/logs/streams; stderr drain with ceiling; stderr never client-visible | Protocol / resource reject |
| TM-08 | Plugin-originated canonical events validated (known kinds + envelope); diagnostics redacted via `internal/infra/diagredact` (`[redacted]`, before truncate) | Protocol / redacted |
| TM-09 | Development mode requires explicit paths; forbidden under `multi_user`; production posture keeps digest+IPC baseline | Config reject |
| TM-10 | Local-only / credential-mode startup enforcement unchanged (`runtimebundle` security_policy tests in security target) | Startup fail closed |

## Platform evidence posture

| Platform | Local channel | Launch binding | Notes |
|---|---|---|---|
| Linux | Socketpair + `SO_PEERCRED` | Descriptor-bound | Full profile intended; Linux race evidence tracked separately (task 8.2 blocker) |
| Windows | Named pipe + DACL + PID | Protected staging | Production tests exercise pipe peer rejection |
| Darwin | **Fail closed** (`unsupported_channel`) | Protected staging when launch path exists | No configure/secrets until approved peer-cred profile lands; not an mTLS weaken |

## Residual risks (accepted)

- Operator installs a malicious-but-digest-matching artifact under a trusted root (trust equivalence).
- Plugin bugs that stay within validated event kinds can still emit harmful *content* (prompt injection to clients); content policy is outside this host boundary.
- Darwin connectors remain unavailable until an approved peer-authenticated local profile is implemented and natively evidenced.
- Full `-race` on this host is Windows-skipped; Linux race/fuzz jobs are CI-owned evidence.

## Verification

```bash
make backend-plugin-security-checks
# practical Windows companions (do not claim race green on Windows):
make test-fuzz
make test-race   # skips race detector on Windows; CI Linux is authoritative
```

Adversarial coverage lives under `internal/infra/backendplugins/` (trust, processhost, adapter, discovery, security), `internal/infra/diagredact/`, `internal/infra/runtimebundle/` (TM-10 security_policy), and `pkg/lipsdk/backendplugin/` (frame/event validation + fuzz).
