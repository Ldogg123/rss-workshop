# Licensing and redistribution

RSS Workshop's original code is licensed under [MIT](../LICENSE), with copyright attributed to Ldogg123. MIT permits modification, redistribution, commercial use, and closed-source forks. Dependencies retain their own licenses; the application's license does not replace their terms.

## Go executable and source repository

Runtime Go modules have BSD, MIT, or Apache-2.0 top-level licenses; `github.com/golang/groupcache/lru` uses Apache-2.0. The [dependency table](dependencies.md) distinguishes runtime modules from tools and the wider module graph. Recheck the compiled dependency graph and compare upstream notices when changing dependencies or build targets.

Preserve `docs/licenses/` in source and binary releases. Both application images include it at `/licenses`. Keep the Go license, modernc's third-party notices, and SQLite's separate notices. `Go-time-zone-data-README` records the embedded IANA time-zone database's public-domain provenance.

Native Linux archives include the executable, `LICENSE`, and these dependency notices in `licenses/`. They use the host's CA certificate store and optional host Chromium; neither Debian packages nor Chromium are bundled in the executable archives. The Debian source bundles described below accompany the container images.

Modernc's generated Linux/amd64 and Linux/arm64 header declarations include glibc LGPL-2.1-or-later notices, GCC GPL-3.0-or-later with the GCC Runtime Library Exception 3.1, and Theodore Ts'o's BSD UUID notice. These are preserved in `modernc.org_libc@v1.75.6_GENERATED-HEADER-NOTICES`, alongside their license texts. The pinned `sys/types/types_linux_{amd64,arm64}.go` and `uuid/uuid/uuid_linux_{amd64,arm64}.go` files contain constants and type/layout declarations, with no function implementations. Their compatibility assessment relies on [LGPL-2.1 section 5](licenses/LGPL-2.1) and the [GCC Runtime Library Exception](https://gcc.gnu.org/onlinedocs/libstdc++/manual/license.html). This interpretation applies to those files, not every modernc source file; reassess it for other targets or versions.

HashiCorp `golang-lru/v2` is an MPL-2.0 module-graph dependency, outside the supported runtime builds. If a build links it, retain its notices and provide the covered source and changes. Separately authored files can retain another license. [Mozilla's MPL FAQ](https://www.mozilla.org/en-US/MPL/2.0/FAQ/)

The Moby-derived `deploy/chromium-seccomp.json` remains Apache-2.0. Preserve its in-file provenance/modification notice and `docs/licenses/Moby-profiles-LICENSE`. [Apache License 2.0, section 4](https://www.apache.org/licenses/LICENSE-2.0)

The PostgreSQL client driver pgx and its pgpassfile, pgservicefile, and puddle dependencies use MIT licenses; their pinned notices are included in `docs/licenses/`. The PostgreSQL server runs separately and is not bundled into application images. Distributing a separate PostgreSQL image requires preserving that image's own notices and covered sources.

FlareSolverr and the optional Gluetun VPN container run as external services. Their executables, browsers, and operating-system packages are not bundled in RSS Workshop's application images. The Debian source collector covers the application images, not these separately pulled services; distributing service images requires preserving their own notices and covered sources.

## Container notices

The scratch image copies CA certificates from the Go builder. Preserve their Mozilla/Debian copyright notice and referenced license texts. `Dockerfile` records the exact CA binary/source package versions at `/licenses/debian-packages.tsv`.

The browser image also redistributes Debian packages, Chromium's third-party components, and Liberation fonts. Preserve `/usr/share/doc/<package>/copyright` and `/usr/share/common-licenses`, following Debian's [copyright-file policy](https://www.debian.org/doc/debian-policy/ch-docs.html#copyright-information). A Chromium-only BSD notice does not cover the full image. Extract package inventories from each final release image and architecture.

## Corresponding sources for public images

Public prebuilt images must provide matching covered sources under the included [GPL-3.0](licenses/GPL-3) and [LGPL-2.1](licenses/LGPL-2.1) terms. A Dockerfile and license texts alone do not supply those sources. The release procedure collects **every installed Debian source package**, avoiding assumptions about which packages carry source obligations.

Before making images public:

1. Collect exact sources for every static/browser image and architecture. Confirm each index has `complete: true` and matches the image's package versions.
2. Publish indices, checksums, and **actual source archives** as durable release assets. Include reassembly instructions and the original checksum for split archives. Temporary CI artifacts or upstream links alone do not complete this procedure.
3. Link source downloads from the image release notes and retain them for the applicable license obligations, together with package notices and application source/build instructions.
4. Repeat verification whenever images, packages, architectures, or dependencies change. Never substitute newer source for an unavailable pinned version.

`scripts/collect-debian-sources.py` retrieves exact versions from [Debian Snapshot](https://snapshot.debian.org/), checks file identities/sizes and `.dsc` SHA-256 entries, and writes source files, `index.json`, and `SHA256SUMS`. Inventory-only and failed runs remain incomplete. The collector does not execute sources or verify uploader OpenPGP signatures.

`scripts/check-release-sources.py` streams release archives without extracting them, rejects unsafe paths/links, verifies source bytes and descriptors, and compares exact package inventories and architectures against the images to be pushed. The publish workflow pushes those same verified images. See [release preparation](releases.md) for commands, archive layout, split-file handling, and publication gates.
