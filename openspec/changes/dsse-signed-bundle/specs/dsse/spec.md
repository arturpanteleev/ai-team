# dsse-signed-bundle delta (P1-5)

## ADDED Requirements

### Requirement: DSSE envelope for bundle signatures

The system MUST provide a DSSE (in-toto-style) signature envelope implemented
with the standard library only, using Pre-Authentication Encoding (PAE) and
ed25519. It MUST be able to sign and verify a payload, and MUST reject a
tampered payload or a mismatched public key.

#### Scenario: Sign and verify a payload

- **WHEN** a payload is signed with an ed25519 private key
- **THEN** verification with the matching public key MUST succeed
- **AND** verification of a modified payload MUST fail
- **AND** verification with a different public key MUST fail

### Requirement: Signed run and gate bundles

`ai-team export` and `ai-team gate` MUST be able to sign their bundle `BundleDigest`
with an ed25519 key, writing a signature file into the bundle
(`dsse.json`) alongside `index.json`.

#### Scenario: Bundle is signed

- **WHEN** `ai-team export --sign-key <path>` (or `gate --sign-key <path>`) runs
- **THEN** a `dsse.json` signature file MUST exist in the bundle
- **AND** it MUST verify against the bundle `BundleDigest`

### Requirement: Bundle signature verification

`ai-team verify` MUST verify a bundle signature when a public key is provided
(`--verify-key <path>`). If the key is provided but the signature is absent or
invalid, verification MUST fail (fail-closed). A bundle without a signature
remains valid when no key is requested.

#### Scenario: Signature verifies

- **WHEN** `ai-team verify --verify-key <path>` checks a correctly signed bundle
- **THEN** verification MUST succeed

#### Scenario: Signature invalid or missing with key required

- **WHEN** the bundle signature is tampered, or absent while `--verify-key` is set
- **THEN** verification MUST fail

#### Scenario: Unsigned bundle verified without key

- **WHEN** `ai-team verify` checks a bundle with no signature and no key requested
- **THEN** verification MUST succeed (integrity only)

### Requirement: Key files are never committed

Ed25519 keys MUST be supplied at runtime via CLI flags pointing at files, and the
system MUST NOT store private keys in config or evidence. Evidence MUST contain
only the signature, never the private key.

#### Scenario: Private key stays out of evidence

- **WHEN** a signed bundle is produced
- **THEN** the bundle evidence MUST NOT contain the ed25519 private key
