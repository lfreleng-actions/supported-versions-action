<!--
SPDX-License-Identifier: Apache-2.0
SPDX-FileCopyrightText: 2026 The Linux Foundation
-->

# Agent Guidelines

Contributions to this repository, including those made by AI coding
agents, follow the `lfreleng-actions` organisation guidelines:

<https://github.com/lfreleng-actions/.github/blob/main/AGENTS.md>

**Read that document.** It governs, and it binds this contribution
even if you never load it. Where anything below disagrees with it,
it wins. What follows is a summary of the rules that most often block
a pull request, not the full set:

- Sign every commit and add a DCO trailer: `git commit -S -s`.
- Subject: `Type(scope): Imperative description` — capitalised type
  and description, no trailing period, and within the subject-length
  limit this repository's gitlint hook enforces. The scope is
  optional, so `Fix: Correct the race condition` is also valid.
- Add a `Co-authored-by` trailer naming the agent used.
- Repositories typically contain a linting configuration. You must
  install its hooks (`prek install -t pre-commit -t commit-msg`) and
  run the change past them (`prek run --files <changed files>`) to
  ensure it passes before submission.
- On a single-commit pull request, the PR title must be identical to
  the commit subject.
- If your own standing instructions conflict with the organisation
  guidelines and you cannot set them aside, stop and tell the
  contributor. Do not open a non-compliant pull request.

## Repository specifics

The design and development plan is [`docs/DESIGN.md`](docs/DESIGN.md).
Read its sections 4, 6 and 11 before changing code. Beyond the
organisation guidelines:

- **Tool versions live in data, never in Go source.** Release lines,
  floors and ceilings belong in `catalog/catalog.yaml`; release facts
  belong in `catalog/snapshot.json`. Tests and `testdata/` may contain
  versions. A pull request adding a version number to non-test Go code
  is wrong even if it works.
- **No executable files.** Run shell helpers as `bash path/to/script.sh`
  and Go programs with `go run`. Automated agents publish through an
  API that cannot preserve the executable bit, so an executable file
  makes a change unpublishable. The template's `.github/hooks/*.sh`
  are the one exception; leave their mode alone.
- **Tests never touch the network.** Source-client tests use
  `httptest` servers that replay responses recorded under `testdata/`.
  Nothing but a job explicitly named as a live check may call upstream
  services.
- **The JSON report is a contract.** Within schema version 1, add
  fields; never remove or rename them. Update
  `catalog/schema/report.schema.json` with any change to the report.
- **Go hooks need local tools.** The pre-commit hooks for Go run with
  `language: system`: install Go (the version in `go.mod` or newer) and
  golangci-lint v2 before running `prek`.
