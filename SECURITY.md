# Security Policy

## Project status and supported versions

HookFlow is currently in pre-production development on the `main` branch.
Production activation is not yet approved (see [docs/operations/runbook.md](docs/operations/runbook.md)
for remaining production blockers, including KMS/Vault key custody, network egress
controls, and API rate limiting).

| Branch / Version | Supported |
| --- | --- |
| `main` (latest commit) | Yes |
| Older commits and feature branches | No |

Security fixes are applied on `main`. Please verify that a finding reproduces on the
latest commit of `main` before reporting it.

## Reporting a vulnerability

**Do not report security vulnerabilities through public GitHub issues, discussions, or pull requests.**

Please report vulnerabilities privately using GitHub Security Advisories:

- https://github.com/metamin44/hookflow/security/advisories/new

If GitHub Security Advisories are unavailable, contact the repository maintainer privately
through their GitHub profile without posting exploit details in public channels.

### What to include in a report

To help triage and reproduce the issue efficiently, please include:

- The affected component, file path, and commit SHA.
- A clear description of the vulnerability and its security impact (for example,
  SSRF bypass, authentication/scope bypass, tenant isolation break, secret leakage,
  or cryptographic flaw).
- Minimal reproduction steps or a proof-of-concept test case.
- Any known workarounds or suggested remediation.

### What not to include

Never include or publicly disclose any of the following in a report, issue, log excerpt,
or pull request:

- Real credentials, `.env` files, database passwords, or RabbitMQ URIs containing credentials.
- HookFlow API keys (`Authorization: Bearer ...` or `cmd/apikey` output).
- Endpoint signing secrets (`whsec_...`), encrypted secret envelopes (`hfse1....`),
  or File KEK keyring files (`keyring.json`).
- Real customer/tenant event payloads or production infrastructure identifiers.

Use unmistakably synthetic placeholder values in all examples and reproductions.

## Coordinated disclosure and response expectations

This repository is maintained on a best-effort basis without a formal SLA guarantee.
When a private report is submitted, the maintainer aims to:

1. Acknowledge receipt of the report as promptly as practicable.
2. Validate the finding, determine scope and severity, and investigate a fix.
3. Coordinate remediation and public disclosure timing with the reporter once a fix is
   available on `main`.

Please allow reasonable time for investigation and remediation before disclosing details
publicly, and restrict testing to local or self-hosted instances you control.
