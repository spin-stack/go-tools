---
title: Provenance and SBOMs
description: SLSA provenance and CycloneDX SBOMs from what a build read, a gate that fails on a workflow that publishes without attesting, and Sigstore's trusted root embedded for offline verification.
---

Four pieces, used together by the spin-stack release workflows: one says what a release was built
from, one says what is inside it, one makes sure nothing is published without both, and one lets a
verifier check the signatures without reaching the network.

## provenance: what it was built from

```sh
go tool provenance -version <release> -out provenance.json
cosign attest-blob --type slsaprovenance1 --predicate provenance.json <files>
```

Run in a GitHub Actions workflow, it writes the [SLSA v1](https://slsa.dev/spec/v1.0/provenance)
provenance predicate of that run: the workflow at its ref is the builder, and the inputs are the
repository at its commit plus every pin in [versions.yaml](../versions/). There is no timestamp in
it, so the same run gives the same bytes. Package `provenance` does the same for a build that adds
inputs of its own (`Build`'s extra).

## sbom: what is inside

```sh
go tool sbom <kind> -name <subject> -version <v> -out <file> ...
```

Writes a CycloneDX 1.6 SBOM from what a build actually read:

| kind | from |
|---|---|
| `go` | the build info of Go binaries |
| `apko` | an `apko.lock.json` |
| `dpkg` | a `<package> <version> <arch>` listing |
| `apk` | an apk database; `-only` what is shipped, the rest is formulation |
| `pnpm` | a lockfile's runtime dependencies |
| `pins` | `versions.yaml` entries |
| `file` | one file, by its hash |

`sbom merge -subject <file>` nests SBOMs under the file they describe. `sbom check` fails on a
component with no version, Go's `(devel)`, or neither a package URL nor a hash. The same inputs
give the same bytes: components are sorted, the serial number is the subject's, and the time is
`SOURCE_DATE_EPOCH`.

## attestgate: nothing published unattested

A test, like the [versions gate](../versions/#the-gate), that reads the repository's workflows and
fails on a step that publishes without a step before it, in the same job, that attests:

- **publishing** is a release action, `oras push`, `gh release upload`, `docker push`,
  `aws s3 cp`, or a task named in `UploadTasks`;
- **attesting** is `cosign attest-blob --type slsaprovenance1`, or a task in `AttestTasks`, run
  unconditionally and with `id-token: write`.

`RequireSBOM` also demands a `cosign attest-blob --type cyclonedx`. `NotPublished` excuses a step by
`<file>/<job>/<step>` with its reason, and an excuse that matches nothing fails. `MainOnly` holds
release workflows to no push trigger and every job on `refs/heads/main`.

A workflow can't say which files a glob matched, so the step right before an upload checks the
directory itself:

```sh
go tool attestgate files -require provenance -except checksums.txt dist/
```

It fails on a file with no `<file>.provenance.sigstore.json` beside it.

## trustroot: verifying offline

`trustroot.Load()` returns Sigstore's trusted root - the Fulcio authorities, Rekor logs, CT logs and
timestamp authorities a bundle is checked against - embedded exactly as TUF served it. A signature
is verified with nothing reached over the network.

Every repository gets the same root through its go-tools version. Renewing it is
`go run ./cmd/trustroot -write` here, a commit, and a go-tools bump there. `go tool trustroot -check`
fails when TUF serves anything else, byte for byte; it needs the network, and this repository's
`task lint` runs it.
