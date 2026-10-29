<!--
# SPDX-License-Identifier: Apache-2.0
# SPDX-FileCopyrightText: 2025 The Linux Foundation
-->

# 🔢 Supported Versions

<!-- prettier-ignore-start -->
<!-- markdownlint-disable-next-line MD013 -->
[![Linux Foundation](https://img.shields.io/badge/Linux-Foundation-blue)](https://linuxfoundation.org/) [![Source Code](https://img.shields.io/badge/GitHub-100000?logo=github&logoColor=white&color=blue)](https://github.com/lfreleng-actions/actions-template) [![License](https://img.shields.io/badge/License-Apache_2.0-blue.svg)](https://opensource.org/licenses/Apache-2.0) [![pre-commit.ci status badge]][pre-commit.ci results page] [![OpenSSF Scorecard](https://api.scorecard.dev/projects/github.com/lfreleng-actions/actions-template/badge)](https://scorecard.dev/viewer/?uri=github.com/lfreleng-actions/actions-template)
<!-- prettier-ignore-end -->

Resolves the supported release lines of a tool, such as Python or Go,
from its upstream release facts and the organisation's catalog policy.
It produces a build matrix, checks whether a project may still use one
version, or lists a tool's release lines.

The action builds the `supported-versions` command-line tool from its
own source and runs it with `--format github`; the tool writes every
output, annotation and job summary.

## Usage Example

<!-- markdownlint-disable MD046 -->

Feed the supported matrix to a later job, narrowed by the constraint the
project declares (`requires-python` in `pyproject.toml` here):

```yaml
jobs:
  versions:
    runs-on: ubuntu-latest
    outputs:
      matrix: ${{ steps.versions.outputs.matrix_json }}
    steps:
      - uses: actions/checkout@v6
      - name: "Supported Python versions"
        id: versions
        uses: lfreleng-actions/supported-versions-action@main
        with:
          tool: python

  test:
    needs: versions
    runs-on: ubuntu-latest
    strategy:
      matrix: ${{ fromJson(needs.versions.outputs.matrix) }}
    steps:
      - uses: actions/setup-python@v6
        with:
          python-version: ${{ matrix.python-version }}
```

Fail a job that pins a version that is no longer supported:

```yaml
steps:
  - name: "Check the pinned Python"
    uses: lfreleng-actions/supported-versions-action@main
    with:
      tool: python
      mode: check
      version: "3.10"
      fail_on_unsupported: true
```

Pin the action and any other action to a commit SHA in real workflows.

<!-- markdownlint-enable MD046 -->

## Inputs

<!-- markdownlint-disable MD013 -->

| Name                | Required | Default  | Description                                                                                                       |
| ------------------- | -------- | -------- | ----------------------------------------------------------------------------------------------------------------- |
| tool                | True     |          | Catalog tool id or alias, for example `python` or `go`                                                            |
| mode                | False    | `matrix` | `matrix`, `check` or `list` (the release lines of `tool`); any other value fails                                  |
| version             | False    |          | Version to check; required in `check` mode and rejected in the others                                             |
| constraint          | False    |          | Constraint applied to the matrix; wins over `path_prefix`; in `matrix` mode                                       |
| path_prefix         | False    | `.`      | Project directory to detect the constraint in when `constraint` is empty; empty disables detection; `matrix` mode |
| strict_constraint   | False    | `false`  | Fail when the constraint allows no line of the policy matrix; in `matrix` mode                                    |
| fail_on_unsupported | False    | `false`  | Fail when the checked version is not supported; in `check` mode                                                   |
| policy              | False    |          | Path to a local policy overlay file                                                                               |
| offline_mode        | False    | `false`  | Never touch the network; use the cache or the embedded snapshot                                                   |
| network_timeout     | False    | `5`      | Per-request timeout in whole seconds, 1 to 300                                                                    |
| max_retries         | False    | `2`      | Retries for a failed request, 0 to 10                                                                             |
| eol_behaviour       | False    |          | Override the policy's end-of-life behaviour: `warn`, `strip`, `fail` or `allow`; empty keeps the policy's value   |
| include_prerelease  | False    |          | Override whether the policy admits pre-release lines: `true` or `false`; empty keeps the policy's value           |
| matrix_key          | False    |          | Rename the key of `matrix_json` and of the report's matrix                                                        |
| summary             | False    | `true`   | Write a section to the job summary                                                                                |

<!-- markdownlint-enable MD013 -->

`eol_behaviour` and `include_prerelease` default to empty on purpose: a
non-empty default would override the catalog and any organisation
overlay without a word.

## Outputs

<!-- markdownlint-disable MD013 -->

| Name               | Modes | Description                                                                      |
| ------------------ | ----- | -------------------------------------------------------------------------------- |
| matrix_json        | All   | JSON object mapping the matrix key to the supported lines, for `fromJson`        |
| supported_versions | All   | Supported lines of the matrix, space separated                                   |
| latest             | All   | Newest stable line in the matrix                                                 |
| eol_versions       | All   | End-of-life lines kept in the matrix, space separated                            |
| versions_source    | All   | Where the release facts came from: `live`, `cache`, `stale` or `snapshot:<date>` |
| report_path        | All   | Path of the JSON report under `RUNNER_TEMP`                                      |
| status             | check | Status of the checked version's line, for example `eol`                          |
| supported          | check | `true` when upstream and the policy support the checked version                  |
| policy_excluded    | check | `true` when upstream supports the line but the policy keeps it out of the matrix |
| eol_date           | check | End-of-life date of the checked version's line, if known                         |

<!-- markdownlint-enable MD013 -->

## Implementation Details

## Notes

- The action runs `go build` on every run, so it needs to reach
  `proxy.golang.org` and `sum.golang.org` unless the Go module cache
  already holds the modules and the toolchain `go.mod` names.
- A `go` already on `PATH` at Go 1.21 or newer builds the tool, switching
  toolchains through `GOTOOLCHAIN` as needed, and stays on `PATH`.
  Without one, the action runs `actions/setup-go`, which puts its Go on
  `PATH` for every later step of the job.
- Exit codes: `0` success; `1` an invalid input, a usage error or any
  other failure; `2` no release facts available; `3` an unsupported
  version under `fail_on_unsupported`; `4` an end-of-life line under the
  `fail` end-of-life behaviour; `5` a constraint outside the policy
  matrix under `strict_constraint`.
- The action sets its outputs even when the step fails, so a later step
  running under `if: always()` can still read them.

[pre-commit.ci results page]: https://results.pre-commit.ci/latest/github/lfreleng-actions/actions-template/main
[pre-commit.ci status badge]: https://results.pre-commit.ci/badge/github/lfreleng-actions/actions-template/main.svg
