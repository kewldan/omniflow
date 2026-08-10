# Contributing to Omniflow

Thank you for improving Omniflow.

## Before opening a change

- Search existing issues and discussions.
- For large features or architectural changes, open a proposal first.
- Read `AGENTS.md` and the architecture documentation.
- Never include production credentials, customer data, access links, or payment payloads.

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

## Testing policy

The initial foundation runs unit, static, build, migration, documentation, and security checks. PostgreSQL/Valkey Testcontainers and full Playwright suites are intentionally scheduled for later milestones; changes should not make their adoption harder.

## License

By contributing, you agree that your contribution is licensed under Apache License 2.0.
