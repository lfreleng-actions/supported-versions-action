<!--
# SPDX-License-Identifier: Apache-2.0
# SPDX-FileCopyrightText: 2025 The Linux Foundation
-->

# 🔢 Supported Versions

<!-- prettier-ignore-start -->
<!-- markdownlint-disable-next-line MD013 -->
[![Linux Foundation](https://img.shields.io/badge/Linux-Foundation-blue)](https://linuxfoundation.org/) [![Source Code](https://img.shields.io/badge/GitHub-100000?logo=github&logoColor=white&color=blue)](https://github.com/lfreleng-actions/actions-template) [![License](https://img.shields.io/badge/License-Apache_2.0-blue.svg)](https://opensource.org/licenses/Apache-2.0) [![pre-commit.ci status badge]][pre-commit.ci results page] [![OpenSSF Scorecard](https://api.scorecard.dev/projects/github.com/lfreleng-actions/actions-template/badge)](https://scorecard.dev/viewer/?uri=github.com/lfreleng-actions/actions-template)
<!-- prettier-ignore-end -->

Resolves the supported release lines of a tool, such as Python, Go, Node.js,
Java or Maven, from its upstream release facts and the organisation's
catalog policy. It produces a build matrix, checks whether a project may
still use one version, or lists a tool's release lines.

The action builds the `supported-versions` command-line tool from its
own source and runs it with `--format github`; the tool writes every
output, annotation and job summary.

## Supported Tools

<!-- markdownlint-disable MD013 -->

| Tool                   | ID       | Aliases                     | Matrix key       | Lines in the matrix         | Constraint detected from                                                                 |
| ---------------------- | -------- | --------------------------- | ---------------- | --------------------------- | ---------------------------------------------------------------------------------------- |
| Python                 | `python` | `cpython`, `python3`, `py`  | `python-version` | Supported from 3.11         | `pyproject.toml`, `setup.cfg`, `setup.py`, `.python-version`                             |
| Go                     | `go`     | `golang`                    | `go-version`     | Supported                   | `go.mod` (`go` and `toolchain` directives)                                               |
| Node.js                | `nodejs` | `node`                      | `node-version`   | Supported LTS lines         | `package.json` (`engines.node`), `.nvmrc`, `.node-version`                               |
| Java (Eclipse Temurin) | `java`   | `jdk`, `temurin`, `openjdk` | `java-version`   | Supported LTS lines from 17 | `pom.xml` (`maven.compiler.release`), `.java-version`, `.sdkmanrc` (`java`)              |
| Apache Maven           | `maven`  | `mvn`                       | `maven-version`  | Supported, no pre-releases  | `.mvn/wrapper/maven-wrapper.properties` (`distributionUrl`), `pom.xml` (`prerequisites`) |
| Gradle                 | `gradle` |                             | `gradle-version` | Supported                   | `gradle/wrapper/gradle-wrapper.properties` (`distributionUrl`)                           |
| Rust                   | `rust`   |                             | `rust-version`   | Six newest, then `stable`   | `Cargo.toml` (`package.rust-version`), `rust-toolchain.toml` (`toolchain.channel`)       |

<!-- markdownlint-enable MD013 -->

The catalog in [`catalog/catalog.yaml`](catalog/catalog.yaml) defines
each tool; a matrix holds the lines GitHub runners can install. Node.js
constraints use the npm semver range syntax, such as
`^20.19.0 || >=22.12.0`. An `.nvmrc` or `.node-version` may name the
nvm aliases `node`, `stable`, `lts/*` or `lts/-N`, which resolve to a
line from the release facts. A codename alias such as `lts/jod` stays
unresolved: the action warns and leaves the matrix unconstrained.

Java lines are feature releases such as `21`, and a line enters the
matrix once Adoptium publishes Temurin builds of it. A project's
`maven.compiler.release`, `.java-version` or `.sdkmanrc` names one
release, which the matrix holds as a floor: `21`, `21.0.5+11` or the
SDKMAN identifier `21.0.5-tem` keep 21 and newer, and the legacy `1.8`
names 8. A `.java-version` pins its one line, as `.python-version`
does. `maven.compiler.release` may refer to another property of the
same `pom.xml`, such as `${java.version}`; the action warns about and
skips a property the file does not define, such as one from a parent
POM.

Maven lines are minor releases such as `3.9`, and a line enters the
matrix once Maven Central publishes a release of it. A line whose
builds on Maven Central are all alpha, beta or release candidates stays out
unless `include_prerelease` is `true`. Maven constraints use
Maven's version range syntax, such as `[3.8,4.0)` or `[3.6.3,)`; a bare
version such as `3.9.6` keeps that release and newer. The Maven wrapper's
`distributionUrl` names such a version, and `<prerequisites><maven>` in
`pom.xml` names a version or a range. endoflife.date also lists Maven 1
and 2, which are not minor lines; the action leaves them out. Each line
of the JSON report at `report_path` names its newest release, such as
`3.9.16`, for an installer that needs a full version.

Gradle lines are major releases such as `8`, and a line enters the
matrix once `services.gradle.org` lists a final release of it; release
candidates, milestones, nightlies and broken releases stay out. The
Gradle wrapper's `distributionUrl` names the one release a build uses,
such as `8.14.3`, which the matrix holds as a floor: `8.14.3` keeps 8
and newer. Each line of the JSON report names its newest release, the
`checksum_url` of that release's SHA-256 checksum, and under
`compatibility.java` the newest Java line it runs on, from
[Gradle's compatibility matrix](https://docs.gradle.org/current/userguide/compatibility.html).

Rust lines are minor releases such as `1.94`. Upstream supports the
newest stable release and no other, so the matrix holds the six newest
lines, the older five end of life without warnings, and then the label
`stable` for a job on whatever stable is when it runs. `latest` and
`supported_versions` hold numeric lines alone. A line is installable up
to the release `static.rust-lang.org` serves on its stable channel,
which also names each line's newest release in the JSON report.
`package.rust-version` in `Cargo.toml` and `toolchain.channel` in
`rust-toolchain.toml` name the oldest release, such as `1.80` or
`1.80.1`, which the matrix holds as a floor; a channel name such as
`stable` or `nightly-2026-01-01` is not a version, so the action
warns and tries the next file. A workspace member's
`rust-version.workspace = true` reads `workspace.package.rust-version`
from the same `Cargo.toml`.

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

The same works for Node.js, narrowed by `engines.node` in
`package.json`, or by `.nvmrc` or `.node-version`:

```yaml
- name: "Supported Node.js versions"
  id: node-versions
  uses: lfreleng-actions/supported-versions-action@main
  with:
    tool: node
```

The test job then sets up each line of the matrix:

```yaml
- uses: actions/setup-node@v6
  with:
    node-version: ${{ matrix.node-version }}
```

For Java, set up Temurin builds of each line:

```yaml
- uses: actions/setup-java@v5
  with:
    distribution: temurin
    java-version: ${{ matrix.java-version }}
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
| tool                | True     |          | Catalog tool id or alias, such as `python`, `go`, `node`, `java`, `maven`, `gradle` or `rust`                     |
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
`eol_behaviour` skips a tool whose catalog entry allows end-of-life
lines, as Rust's does, unless the overlay sets that tool's
`eol_behaviour`.

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
