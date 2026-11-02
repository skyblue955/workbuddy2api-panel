# Project maintenance workflow

- Coordinator owns requirements, scope, architecture decisions, integration, acceptance, and final reporting.
- Delegate bounded implementation to `implementer`; use `repo_explorer`, `test_engineer`, `reviewer`, `security_reviewer`, and `docs_release` for their configured roles. Preserve project-local model settings; never modify global Codex configuration.
- Delegate read-only investigations in parallel. Assign explicit file ownership before writes; agents must not revert other agents' work. Coordinate overlapping changes serially.
- Save milestones and outstanding work in `maintenance/ROADMAP.txt`. Keep routine progress in files; notify the user only for delivery, terminal failure, or a decision that actually requires their input.
- Before acceptance run `bash scripts/verify.sh`, inspect the diff, and obtain independent review. Report actual commands and exit codes, not unexecuted expectations.
- Keep fixes in small reversible Git commits. Do not push main, create release tags, or publish images automatically; use a maintenance branch and an integration PR.
- New upstream integrations require a current official contract and an explicit opt-in path. Keep Open API OAuth tokens separate from legacy client credentials. Never invent account progress, bypass enforcement, or represent legacy/private endpoints as officially supported.
- No live account or reward mutation in automated tests. Use deterministic fixtures; never commit/log tokens, redemption codes, real account records, or personal request payloads.
- Use `uv` for Python tooling/execution, `fnm` for Node environments, and `jenv` for Java environments when needed.
