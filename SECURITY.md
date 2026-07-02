# Security Policy

## Reporting a vulnerability

Email security@hanzo.ai with details. Encrypt with our PGP key (fingerprint TBD).

We respond within 48 hours. Critical issues receive same-day acknowledgment.

## Scope

This policy covers code in this repository. For the broader Hanzo platform threat model, see [hanzoai/HIPs](https://github.com/hanzoai/HIPs).

## Sandbox boundary

`zip` is a Go web framework with no privileged surface of its own — it executes only handler code registered by its consumer. Trust is delegated to the calling service: middlewares for JWT validation, identity-header strip+mint, rate limiting, and CORS must be configured by the consumer per the Hanzo platform contract.

For runtime sandbox guarantees, see HIP-0105 (in-process extension runtimes).
