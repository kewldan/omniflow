# Support

Omniflow is maintained as an open-source project. Community support is best
effort and given in public, so that the next person with the same problem finds
the answer.

## Where to go

| You have | Use |
| --- | --- |
| A question about setting up, configuring, or operating an installation | [GitHub Discussions](https://github.com/kewldan/omniflow/discussions) |
| A reproducible defect | [GitHub Issues](https://github.com/kewldan/omniflow/issues) — Bug report |
| An idea for a capability | GitHub Issues — Feature proposal. Read the proposal process in [`CONTRIBUTING.md`](./CONTRIBUTING.md) first |
| A security vulnerability | Private vulnerability reporting, per [`SECURITY.md`](./SECURITY.md). Never open a public issue |

Read the documentation first. Most operational questions are answered by
[Troubleshooting](https://github.com/kewldan/omniflow/blob/main/docs/operations/troubleshooting.mdx),
[Configuration](https://github.com/kewldan/omniflow/blob/main/docs/getting-started/configuration.mdx),
and the runbooks under `docs/operations/`.

## What to include

A report nobody can reproduce cannot be fixed, and a report that arrives with
the wrong details costs a round trip before anyone can start.

- The Omniflow version, and whether you are running published images or a build
  of your own.
- How it is deployed: Compose as shipped, Compose with your own overlay, or
  something else.
- The Remnawave, PostgreSQL, and Valkey versions if the problem touches them.
- What you expected, what happened, and the exact steps that produce it.
- Sanitized logs, with the request ID if you have one.

Never publish secrets, tokens, access links, customer identifiers, payment
payloads, or anything else from a production database. If a log line has to be
edited to be safe to post, say that you edited it.

## What is supported

- The versions in the
  [compatibility matrix](https://github.com/kewldan/omniflow/blob/main/docs/operations/compatibility.mdx),
  and only those. A Remnawave outside 3.2.x, a PostgreSQL other than 18, or a
  Valkey other than 9 is unsupported rather than untested-but-probably-fine.
- The latest release. Fixes land on `main` and ship in the next version; there
  are no maintenance branches, so an installation upgrades forward.
- The single-server Compose topology in the repository. A reverse proxy of your
  own choosing is fine and two examples ship; the rest of your infrastructure is
  yours.

## What is out of scope

These are not refusals of help, only statements about what this project is
responsible for:

- **Remnawave itself**, its nodes, Xray configuration, and anything on the VPN
  side of the boundary described in `docs/architecture/data-ownership.mdx`.
- **Payment-provider accounts.** Whether a provider grants you card binding,
  fiscalization, or a particular currency is between you and them.
- **Bespoke deployment.** Kubernetes, multi-instance topologies, and managed
  database migration are post-1.0 candidates at best; nothing in the repository
  supports them today.
- **The legality of running a VPN service where you are.** That is your decision
  and your responsibility.
- **Private consulting, installation for hire, or a service-level agreement.**
  There is none, and nobody is on call.

## Response expectations

There is no guaranteed response time for questions or bug reports, and none is
implied by an issue staying open. Security reports are handled separately and
take precedence — see [`SECURITY.md`](./SECURITY.md).

An issue with no reproduction, no version, or no response to a request for
detail may be closed. Reopen it when you have the missing piece — a closed issue
is not a verdict on the problem.
