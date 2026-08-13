# Contributing to Omniflow

Thank you for improving Omniflow.

## Before opening a change

- Search existing issues and discussions.
- For large features or architectural changes, open a proposal first — see below.
- Read `AGENTS.md` and the architecture documentation.
- Never include production credentials, customer data, access links, or payment payloads.

## Proposing a feature

Open a **Feature proposal** issue before writing the code. A proposal describes
the operator or customer problem first, the outcome you want second, and the
alternatives you considered third. Whether a capability is worth carrying is a
maintenance decision rather than a code-quality one, and it is cheaper to make
before the pull request exists than after.

A proposal is answered in one of four ways: accepted for the roadmap, accepted
as a contribution you are welcome to write, deferred with the condition that
would change the answer, or declined with the reason. `ROADMAP.md` lists what is
already planned and what is explicitly a non-goal before 1.0; a proposal that
matches a non-goal is declined on that basis rather than on merit.

Changes that need an accepted architecture decision record before they can be
made at all — a message broker and a dependency-injection framework among them —
are described in `docs/architecture/decisions.mdx`.

A new payment provider, digital-goods gateway, or model-provider adapter has its
own contract in `docs/integrations/extending.mdx`. Read it before proposing one:
it is what a review will hold the adapter to.

## Development workflow

1. Fork the repository and create a focused branch.
2. Copy `.env.example` to `.env` and use non-production credentials.
3. Add or update tests and Mintlify documentation with the behavior.
4. Run the checks relevant to your change.
5. Open a pull request using the repository template.

Database changes follow this order:

```text
database/schema.sql -> Atlas diff -> review migration -> sqlc generate -> tests
```

Do not modify migrations that have been included in a release.

## Commit messages

Use Conventional Commits:

```text
feat(bot): add subscription renewal flow
fix(billing): deduplicate Stars webhook events
docs(telemetry): document feature usage counters
chore(deps): update frontend dependencies
```

Allowed common types are `feat`, `fix`, `docs`, `refactor`, `test`, `build`, `ci`, `perf`, `chore`, and `revert`.

Do not add generated-by or assistant attribution to commits or pull requests.

The commit type decides the next version, so a message is a release decision
rather than a note. `feat!:` or a `BREAKING CHANGE:` footer is required for a
change to any public surface, including removing an environment variable or
renaming a field in a `/v1/admin` response. The full process is documented in
`docs/contributing/releases.mdx`.

## Testing policy

CI runs Go unit tests with the race detector, the frontend type-check and lint,
a production frontend build, Playwright, Testcontainers integration suites
against a real PostgreSQL, every supported migration upgrade path, Compose and
reverse-proxy validation, and the documentation checks for both language trees.
All of it must be green before a merge.

Run the narrowest relevant tests while iterating and the full available checks
before handing over. Report what actually ran: a check you could not run locally
is unverified rather than passing, and saying so is part of the change.

New payment, wallet, authentication, RBAC, import, and provisioning behaviour
requires failure-path and idempotency tests. `docs/contributing/testing.mdx`
describes what each layer is responsible for proving.

## License

By contributing, you agree that your contribution is licensed under Apache License 2.0.
