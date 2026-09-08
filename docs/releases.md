# Builds and releases

CI validates changes without publishing release assets. Separate manual workflows publish native executable archives and container images for a matching release tag. Container publication additionally requires verified corresponding-source archives.

The source repository is [Ldogg123/rss-workshop](https://github.com/Ldogg123/rss-workshop). Check [GitHub Releases](https://github.com/Ldogg123/rss-workshop/releases) for published versions and prebuilt image availability. The examples below use `v0.1.2`.

## Images and architectures

| Variant | Example image tag | Runtime |
| --- | --- | --- |
| Browser (default) | `ghcr.io/ldogg123/rss-workshop:v0.1.2-browser` | App with the pinned Debian slim Chromium runtime |
| Static | `ghcr.io/ldogg123/rss-workshop:v0.1.2-static` | Statically linked Go executable, CA certificates and license notices; no shell or local browser |

Both variants support the configured external FlareSolverr service. The browser variant additionally supports local Chromium and Auto rendering. The publishing workflow builds Linux `amd64` and `arm64` manifests for both tags. The lowercased GitHub repository determines the GHCR namespace: `Ldogg123/rss-workshop` publishes under `ghcr.io/ldogg123/rss-workshop`.

Tags include the full version and variant. There is no implicit `latest` tag. Record the digest shown by the publishing job and prefer that digest for a repeatable deployment. Compose defaults to the published browser image; [compose.static.yaml](../compose.static.yaml) selects the published static image. The optional `RSS_IMAGE` accepts a tag or digest matching the selected runtime; see [image selection](deployment.md#registry-images).

The Dockerfiles support cross-compiling the Go app on the builder's native CPU. The publishing workflow uses native amd64 and arm64 runners for the whole image, checks each locally built image, and combines its verified platform digests into a multi-platform manifest. Race-enabled browser test images also require native builders. This follows Docker's [multi-platform build guidance](https://docs.docker.com/build/building/multi-platform/).

## Native executable archives

Linux users can download `rss-workshop-v0.1.2-linux-amd64.tar.gz` or `rss-workshop-v0.1.2-linux-arm64.tar.gz`, plus the archive's `.sha256` file. Each extracts into a directory of the same name without `.tar.gz`, containing the executable, `LICENSE`, dependency notices in `licenses/`, and build information. See [native installation](deployment.md#run-a-prebuilt-executable) for checksum verification, environment variables, and persistent storage.

The executable embeds the UI and SQLite support. It uses the host's CA certificate store and optionally an installed Chromium browser or external FlareSolverr. Native archives do not contain the container's Debian packages or Chromium and do not require the Debian source bundles to run. Prebuilt support is limited to Linux `amd64` and `arm64`, tested on native runners.

To publish these archives:

1. Push the reviewed version tag and publish its GitHub release. Existing container releases can receive native archives for the same immutable tag without rebuilding their images.
2. Open **Actions → Publish Linux executables → Run workflow**, select the reviewed default branch, and enter the version. The workflow resolves the exact tag commit and rejects a draft/missing release or colliding native asset names.
3. Review both native jobs. They verify modules and reachable vulnerabilities, run the race suite and native fixture workflow, then package the same tested CGo-free executable with its version, commit, license notices, and checksums. Archive extraction and version checks run on each architecture before upload.
4. Confirm both archives and checksum files are attached to the release. The upload job rechecks the release and tag before writing; existing assets are never overwritten. It does not change release tags, publish images, or rebuild after testing.

If publication is interrupted, inspect the release and workflow before retrying. Preserve valid published assets; do not overwrite an existing executable under the same version.

## Source publication

Publishing source is separate from publishing container images. A source push does not require a release tag, GHCR package, or Debian source archives; those belong to the manual image-release process below.

1. Review the exact candidate files with `git status --short --untracked-files=all`, then inspect the staged diff before committing. Include the source, tests, build/deployment files, documentation, `LICENSE`, and `docs/licenses/`. Keep `.env`, runtime databases, backups, generated binaries, source bundles, and browser artifacts ignored. Do not force-add ignored files; check that examples contain placeholders rather than local credentials or private service addresses.
2. Commit the reviewed files and push the default branch to [Ldogg123/rss-workshop](https://github.com/Ldogg123/rss-workshop). Keep this project's existing license and documentation.
3. Enable GitHub Actions and private vulnerability reporting, as described in [SECURITY.md](../SECURITY.md). The ordinary CI workflow needs read-only repository access; registry write permissions are confined to the separate manual publishing workflow.
4. Let hosted CI finish on both `amd64` and `arm64`, including the container/browser jobs and PostgreSQL checks. Local validation does not establish that the hosted runners passed. Resolve failures before advertising a tested release.

The public source can then be built using the [local container build instructions](deployment.md#build-container-images-from-source). Publish image tags only after completing the corresponding-source preparation, release/tag review, and manual GHCR steps below.

## Validation

`.github/workflows/ci.yml` runs for pull requests and pushes to `main`. Feature-branch pushes and tag pushes do not start a second run. Changes limited to root Markdown files, Markdown guides directly under `docs/`, and `docs/screenshots/` skip automatic CI. Code, workflows, deployment files, recipe examples, and shipped license notices still trigger it. GitHub evaluates the full pull-request diff, so a documentation edit in a PR that also changes code can still trigger CI. See GitHub's [path-filter rules](https://docs.github.com/en/actions/reference/workflows-and-actions/workflow-syntax#onpushpull_requestpull_request_targetpathspaths-ignore).

A newer automatic run cancels an unfinished run for the same PR or `main` branch. Manual dispatches and the container release prerequisite always run the full suite and are isolated from automatic cancellation. CI has read-only repository permissions and publishes no images. Separate native `ubuntu-24.04` and `ubuntu-24.04-arm` jobs run:

1. Module verification, reachable Go vulnerability checking, formatting, the full race suite, `go vet`, backup and release-source verification tests, a CGo-free build, and the native fixture workflow.
2. Static and browser runtime builds, plus the dedicated browser test target.
3. Real Chromium tests with the deployed seccomp profile, non-root user, read-only filesystem, dropped capabilities, and memory/process limits. These check Chromium's sandbox, network policy, recovery, and the visual editor.
4. Compose configuration checks for published defaults, image selection, and local build overrides, plus browser/static smoke tests covering health, extraction, RSS/Atom output, recipe portability, and persistence through restart.
5. Isolated host-directory and legacy named-volume backup/restore checks on the amd64 runner.
6. On amd64, PostgreSQL persistence and native workflow checks against a disposable server, plus an isolated PostgreSQL Compose workflow covering outage recovery and logical backup/restore.

The tests use local fixtures and a fake FlareSolverr service. CI does not need the operator's LAN instance, private feeds, or production credentials. Browser screenshots are retained as short-lived Actions artifacts. Required checks use deterministic fixtures rather than live sites or capacity benchmarks.

The workflow uses GitHub's [native runner labels](https://docs.github.com/en/actions/reference/runners/github-hosted-runners) for each architecture. Both hosted architecture checks must pass; Chromium requires a runner kernel that supports its sandbox.

Equivalent local commands, from the repository root:

```sh
make check
go run golang.org/x/vuln/cmd/govulncheck@v1.7.0 ./cmd/... ./internal/...
make compose-test
make check-containers
```

The container checks create isolated smoke-test projects and private temporary host directories, and preserve operator storage. `make browser-test` writes synthetic screenshots to ignored `artifacts/browser/`. Go, Python 3.9+, Docker Engine, and Docker Compose are required. No Node installation is needed. `GO`, `PYTHON`, `DOCKER`, and the three image names can be overridden for another local toolchain; use `DOCKER='sudo docker'` only when your Docker installation requires it. CI also checks workflow syntax with a pinned actionlint version.

## Version and image metadata

```sh
make build VERSION=v0.1.2 COMMIT=COMMIT_SHA
./bin/rss-workshop -version
make docker-static docker-browser VERSION=v0.1.2 COMMIT=COMMIT_SHA \
  SOURCE_URL=https://github.com/Ldogg123/rss-workshop
```

Builds inject `main.version`, `main.commit`, and `main.buildDate`; native and Docker builds use `VERSION`, `COMMIT`, and `BUILD_DATE`. Defaults remain suitable for local development (`dev`, an available Git revision or `unknown`, and the build time for Make). Direct Docker builds default the date to `unknown`. The application reports its version without requiring a password or database.

OCI labels carry the application version, source URL, revision, and MIT application license. `SOURCE_URL` defaults to `https://github.com/Ldogg123/rss-workshop` for Make and direct Docker builds; CI sets it from the repository being built. Third-party components retain their own licenses and notices; the image label does not relicense them. Release builds add a creation timestamp and record the verified image IDs, published platform digests, and source-archive checksums in the workflow. Build arguments contain only public build metadata, never service URLs or credentials.

This release path deliberately loads final images for exact inventory checks and pushes those same immutable image IDs. It does not attach an SBOM or BuildKit provenance: Docker's normal local image loading path does not preserve those attestations. See Docker's [attestation export limits](https://docs.docker.com/build/ci/github-actions/attestations/). The recorded identities and source inventories are useful build records, not signed supply-chain attestations.

The production browser target excludes the race test binaries. Both runtime images include the application license and copied Go dependency notices. The static image also retains CA certificate package copyright, common license texts, and its exact Debian package/source version inventory. The browser image retains Debian package copyright and common license files.

## Preparing corresponding source

Before distributing a release, collect the exact Debian source archives for each final platform image, including the static image's CA package. See [licensing and corresponding source](licensing.md). An SBOM, a package list, a source URL, or an expiring Actions artifact is not a replacement for the actual matching source files.

### Prepare sources with GitHub Actions

The manual **Prepare release source archives** workflow builds and verifies both architectures on native GitHub runners. Run the reviewed workflow from the default branch; it resolves the release tag once and checks out that exact commit for both image builds. Archive collection and validation use the scripts from the reviewed workflow revision, recorded alongside the image commit in the reports. This lets preparation tooling be fixed without moving an existing release tag. It uploads dependency source bundles to an existing draft release; it does not publish images or make the release public.

1. Push the reviewed commit and its version tag, then create a draft GitHub release for that exact tag. The workflow must already be on the default branch.
2. Open **Actions → Prepare release source archives → Run workflow**, select the reviewed default branch, and enter the version and the draft's numeric release ID. The ID is available through `gh api repos/Ldogg123/rss-workshop/releases`; use the entry matching the draft's tag.
3. Review the completed run's verification reports and package inventories for both architectures. Each job collects exact Debian sources, verifies checksums and descriptors, and compares the archives with its locally built image IDs. Large archives are streamed into numbered parts to limit disk use.
4. Confirm that both source bundles and their checksums are attached to the draft. Include the source download/reassembly instructions below in the release notes, then publish the release before running the image publication workflow.

Source preparation refuses to replace existing release assets. If an upload is interrupted, inspect the draft and remove only incomplete assets from that preparation attempt before retrying. Preserve published archives. The separate image workflow verifies the durable downloads again against the final images before pushing them.

### Prepare sources locally

On each native architecture, build the intended release images and collect their sources. Example for `amd64`:

```sh
python3 scripts/collect-debian-sources.py --image rss-workshop:static-check \
  --output dist/debian-sources/linux-amd64/static --download
python3 scripts/collect-debian-sources.py --image rss-workshop:browser-check \
  --output dist/debian-sources/linux-amd64/browser --download
tar -C dist/debian-sources/linux-amd64 -czf dist/debian-sources-v0.1.2-linux-amd64.tar.gz static browser
(cd dist && sha256sum debian-sources-v0.1.2-linux-amd64.tar.gz > debian-sources-v0.1.2-linux-amd64.tar.gz.sha256)
```

Use `linux-arm64` and the native ARM images for the other archive. Add `--docker 'sudo docker'` where Docker requires sudo. Start with empty output directories; an interrupted collection can resume in the same directory only while the image's installed package inventory is unchanged. If packages change, collect into a new directory so the archive cannot include old versions left by an earlier build. Each archive must contain `static/` and `browser/` at its root, including their `index.json`, `SHA256SUMS`, and actual source files. The collector records image identity, installed package/source versions, checksum manifest, and completion status. All downloads must finish and the inventories must match the released images. Chromium's source archive is large; allow sufficient disk space and download time. Keep these generated archives outside Git.

Attach the two archives to the durable GitHub release for the same version, using exactly `debian-sources-VERSION-linux-amd64.tar.gz` and `debian-sources-VERSION-linux-arm64.tar.gz`. GitHub requires [each release asset to be under 2 GiB](https://docs.github.com/en/repositories/releasing-projects-on-github/about-releases). Split a larger archive into 1900 MiB chunks:

```sh
split -b 1900M -d -a 2 dist/debian-sources-v0.1.2-linux-amd64.tar.gz \
  dist/debian-sources-v0.1.2-linux-amd64.tar.gz.part-
```

Upload either the single archive or its complete contiguous `.part-00`, `.part-01`, and subsequent parts, never both. Include the original archive's `.sha256` file in either case. The verifier streams parts in order without extracting or creating another combined copy. Anyone downloading split sources can reconstruct the archive with `cat debian-sources-v0.1.2-linux-amd64.tar.gz.part-* > debian-sources-v0.1.2-linux-amd64.tar.gz`, then run `sha256sum -c debian-sources-v0.1.2-linux-amd64.tar.gz.sha256`; publish those instructions with the assets.

Make the release and archives accessible to recipients before publishing the images. Preserve them alongside the release; do not rely on an upstream package mirror retaining an old version indefinitely. Refresh the source archives if a rebuild changes the installed packages.

A patch release with identical Debian package inventories can reuse verified dependency source bundles from an earlier release. Copy the complete bundles to the new release, update their outer download filenames and checksum sidecars, and retain the original inventories and source files. Record their provenance in the release notes. The image publication workflow must still verify the downloaded sources against both exact new platform images; unchanged Dockerfiles alone do not replace that check.

## Manual GHCR publication

1. Push the reviewed source and release tag, enable Actions, and allow the repository to publish its GHCR package. No personal registry token is needed: the publishing job uses the repository's `GITHUB_TOKEN` with `packages: write`, following [GitHub's container registry guidance](https://docs.github.com/en/packages/working-with-a-github-packages-registry/working-with-the-container-registry).
2. Finish the source preparation above and publish the GitHub release with its actual corresponding-source archives. Review the intended code revision and keep release version numbers unique.
3. Open **Actions → Publish container images → Run workflow**, select the reviewed default branch, enter `vMAJOR.MINOR.PATCH` or `vMAJOR.MINOR.PATCH-rc.N`, and confirm that the corresponding-source inventories were reviewed.
4. The workflow rejects an unchecked confirmation, an invalid version, a draft/missing release, an invalid release tag, or incomplete source asset names. It resolves the tag to one immutable commit, runs the full native CI matrix for that commit, builds its final versioned images on each architecture, and smoke-tests them. Source verification uses maintained scripts from the reviewed workflow revision; reports record both application and tooling commits. Before any image upload, `check-release-sources.py` streams the actual downloaded archives, validates completion, filenames, checksums and Debian source descriptors, and compares both exact package inventories and architectures against those final images. An older source bundle fails if a package changed during the build, and a tag changed during publication is rejected.
5. Only the verified immutable image IDs are uploaded, with unique workflow staging tags. The final version tags combine their recorded registry digests; there is no rebuild after verification. The source check verifies release completeness and package correspondence, while the maintainer still reviews the applicable license terms.
6. Record both final image digests from the manifest-job summaries. Check the package visibility and repository linkage; a newly published GHCR package is normally private until its visibility is changed. Verify that image recipients can also download its source archives before making a package public.

The workflow grants write access only to its platform-upload and manifest jobs. A push, pull request, or Git tag alone cannot trigger publication. It refuses to overwrite an existing version tag and stops on registry lookup errors other than a missing manifest. It does not create or mutate a Git tag, GitHub release, repository, or package visibility. A failure can leave verified staging images or one final variant uploaded; inspect both final digests before deploying or announcing the release, and rerun only failed jobs when recovering a partial release. Staging tags begin with `build-` and are not deployment aliases; retain the final digests when cleaning old staging tags manually.

Action references are pinned to official release commit hashes in the workflow files. Update them through reviewed changes and rerun CI. Review the Go toolchain, Debian base digest, and Chromium package pins for security updates as well. If a pinned package becomes unavailable, update it deliberately and recollect matching sources; do not silently substitute another version.
