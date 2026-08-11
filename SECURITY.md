# Security policy

Engram treats store files, Git objects, repository configuration, and imported
content as untrusted input. Security reports are welcome for both the standard
and the Go reference CLI.

## Supported versions

Before the first public release, security fixes land on `main`; development
builds carry no backport guarantee. After releases begin, the project supports
the latest stable release and, when newer, the latest published prerelease.
Older releases may receive a fix when practical, but this is not guaranteed.

The reporter should always identify the exact commit or `engram version
--format json` result used to reproduce a problem.

## Report a vulnerability privately

Do not open a public issue, discussion, or pull request containing vulnerability
details. Use GitHub's
[private vulnerability reporting form](https://github.com/ontopix/engram/security/advisories/new).
If that form is unavailable, open a public issue containing no sensitive or
exploit information and ask a maintainer to establish a private channel.

Include as much of the following as is safe:

- affected version or full commit ID;
- operating system, architecture, Git version, and Go version when relevant;
- a minimal reproduction using synthetic data and no real credentials;
- expected and actual behavior;
- plausible impact and affected authority boundary;
- whether exploitation requires a trusted hook, repository credentials, or
  network access; and
- any known mitigation or evidence that the issue is already being exploited.

Never send real access tokens, private keys, personal memory stores, or other
third-party secrets. A minimal purpose-built repository is strongly preferred.

## Response and disclosure

The maintainers aim to acknowledge a report within three business days and
provide an initial assessment within seven business days. These are response
targets, not a service-level agreement. Complex cross-platform, Git, or
filesystem issues may require more time to reproduce safely.

Please allow time for a fix and coordinated publication before disclosing the
issue publicly. The project will keep the reporter informed of material status
changes and will credit reporters who want attribution, subject to coordinated
disclosure and applicable legal constraints.

## Security-relevant scope

Examples of issues that should be reported privately include:

- path traversal, symlink, alias, or filesystem-identity bypasses;
- reading or writing outside an authorized store or controller-owned state;
- unauthorized program or preparation-hook execution;
- bypassing exact hook-set trust, candidate sealing, or managed acceptance;
- unexpected credential use, fetching, pushing, or other network access;
- malformed Git, YAML, Markdown, schema, or Unicode input causing a security
  boundary failure or practical denial of service;
- races that allow stale observations, partial publication, ref replacement,
  or loss of unrelated draft bytes; and
- compromised release artifacts, provenance, generated data, or dependency
  supply-chain controls.

Documentation ambiguities that could cause conforming implementations to cross
one of these boundaries are also security relevant.

## Trust boundary

Store content never expands the authority granted by the user or host. A
preparation hook that a controller has explicitly trusted runs with the host
authority given to that hook; malicious behavior by that exact authorized
program is not automatically a vulnerability in Engram. Executing it without
the required trust decision, substituting different bytes, or granting it
unexpected filesystem, credential, or network authority is security relevant.

The non-normative [operator security boundary](docs/operator-guide.md#security-boundary)
summarizes deployment expectations. Normative executor and trust obligations
are defined by [core §8.5](docs/spec/README.md#85-trust-and-executor-conformance).
