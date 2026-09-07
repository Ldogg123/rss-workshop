# Dependency provenance

Go dependencies are pinned in `go.mod` and `go.sum`; Dockerfiles pin the build toolchain and browser runtime. Verify manually downloaded Go toolchains against the [official release checksums](https://go.dev/dl/?mode=json), use the Go module checksum database, and run `go mod verify` after dependency updates.

Official package references: [goquery](https://github.com/PuerkitoBio/goquery), [htmlquery](https://github.com/antchfx/htmlquery), [bluemonday](https://github.com/microcosm-cc/bluemonday), [SQLite](https://pkg.go.dev/modernc.org/sqlite), [pgx](https://github.com/jackc/pgx), and [bcrypt](https://pkg.go.dev/golang.org/x/crypto/bcrypt). See [browser deployment](browser.md) for Chromium package and sandbox provenance.

Upstream notices are preserved under `docs/licenses/` and included in containers at `/licenses`. Original application code uses MIT; components retain their own terms, including modernc's additional notices and generated-header exceptions. See [licensing and redistribution](licensing.md).

The table distinguishes runtime modules from tools and the wider module graph. Runtime status reflects `go list -deps ./cmd/server` on Linux/amd64 and Linux/arm64. Dependency test fixtures and source-only assets are not included in application images. Refresh the table and notices when changing targets or dependencies.

| Module | Pinned version | Copied license notice | Runtime |
| --- | --- | --- | --- |
| github.com/PuerkitoBio/goquery | v1.13.0 | [LICENSE](licenses/github.com_PuerkitoBio_goquery@v1.13.0_LICENSE) | Yes |
| github.com/andybalholm/cascadia | v1.3.4 | [LICENSE](licenses/github.com_andybalholm_cascadia@v1.3.4_LICENSE) | Yes |
| github.com/antchfx/htmlquery | v1.3.6 | [LICENSE](licenses/github.com_antchfx_htmlquery@v1.3.6_LICENSE) | Yes |
| github.com/antchfx/xpath | v1.3.6 | [LICENSE](licenses/github.com_antchfx_xpath@v1.3.6_LICENSE) | Yes |
| github.com/aymerick/douceur | v0.2.0 | [LICENSE](licenses/github.com_aymerick_douceur@v0.2.0_LICENSE) | Yes |
| github.com/dustin/go-humanize | v1.0.1 | [LICENSE](licenses/github.com_dustin_go-humanize@v1.0.1_LICENSE) | Yes |
| github.com/golang/groupcache | v0.0.0-20210331224755-41bb18bfe9da | [LICENSE](licenses/github.com_golang_groupcache@v0.0.0-20210331224755-41bb18bfe9da_LICENSE) | Yes |
| github.com/google/pprof | v0.0.0-20260802141513-ef3492d7dac3 | [LICENSE](licenses/github.com_google_pprof@v0.0.0-20260802141513-ef3492d7dac3_LICENSE) | No |
| github.com/google/uuid | v1.6.0 | [LICENSE](licenses/github.com_google_uuid@v1.6.0_LICENSE) | Yes |
| github.com/gorilla/css | v1.0.1 | [LICENSE](licenses/github.com_gorilla_css@v1.0.1_LICENSE) | Yes |
| github.com/hashicorp/golang-lru/v2 | v2.0.7 | [LICENSE](licenses/github.com_hashicorp_golang-lru_v2@v2.0.7_LICENSE) | No |
| github.com/jackc/pgpassfile | v1.0.0 | [LICENSE](licenses/github.com_jackc_pgpassfile@v1.0.0_LICENSE) | Yes |
| github.com/jackc/pgservicefile | v0.0.0-20240606120523-5a60cdf6a761 | [LICENSE](licenses/github.com_jackc_pgservicefile@v0.0.0-20240606120523-5a60cdf6a761_LICENSE) | Yes |
| github.com/jackc/pgx/v5 | v5.10.0 | [LICENSE](licenses/github.com_jackc_pgx_v5@v5.10.0_LICENSE) | Yes |
| github.com/jackc/puddle/v2 | v2.2.2 | [LICENSE](licenses/github.com_jackc_puddle_v2@v2.2.2_LICENSE) | Yes |
| github.com/mattn/go-isatty | v0.0.24 | [LICENSE](licenses/github.com_mattn_go-isatty@v0.0.24_LICENSE) | No |
| github.com/microcosm-cc/bluemonday | v1.0.27 | [LICENSE.md](licenses/github.com_microcosm-cc_bluemonday@v1.0.27_LICENSE.md) | Yes |
| github.com/ncruces/go-strftime | v1.0.0 | [LICENSE](licenses/github.com_ncruces_go-strftime@v1.0.0_LICENSE) | No |
| github.com/remyoudompheng/bigfft | v0.0.0-20230129092748-24d4a6f8daec | [LICENSE](licenses/github.com_remyoudompheng_bigfft@v0.0.0-20230129092748-24d4a6f8daec_LICENSE) | Yes |
| golang.org/x/crypto | v0.56.0 | [LICENSE](licenses/golang.org_x_crypto@v0.56.0_LICENSE) | Yes |
| golang.org/x/mod | v0.38.0 | [LICENSE](licenses/golang.org_x_mod@v0.38.0_LICENSE) | No |
| golang.org/x/net | v0.58.0 | [LICENSE](licenses/golang.org_x_net@v0.58.0_LICENSE) | Yes |
| golang.org/x/sync | v0.22.0 | [LICENSE](licenses/golang.org_x_sync@v0.22.0_LICENSE) | Yes |
| golang.org/x/sys | v0.47.0 | [LICENSE](licenses/golang.org_x_sys@v0.47.0_LICENSE) | Yes |
| golang.org/x/text | v0.41.0 | [LICENSE](licenses/golang.org_x_text@v0.41.0_LICENSE) | Yes |
| golang.org/x/tools | v0.48.0 | [LICENSE](licenses/golang.org_x_tools@v0.48.0_LICENSE) | No |
| modernc.org/cc/v4 | v4.29.2 | [LICENSE](licenses/modernc.org_cc_v4@v4.29.2_LICENSE) | No |
| modernc.org/ccgo/v4 | v4.35.0 | [LICENSE](licenses/modernc.org_ccgo_v4@v4.35.0_LICENSE) | No |
| modernc.org/fileutil | v1.4.0 | [LICENSE](licenses/modernc.org_fileutil@v1.4.0_LICENSE) | No |
| modernc.org/gc/v2 | v2.6.5 | [LICENSE](licenses/modernc.org_gc_v2@v2.6.5_LICENSE) | No |
| modernc.org/gc/v3 | v3.1.5 | [LICENSE](licenses/modernc.org_gc_v3@v3.1.5_LICENSE) | No |
| modernc.org/goabi0 | v0.2.0 | [LICENSE](licenses/modernc.org_goabi0@v0.2.0_LICENSE) | No |
| modernc.org/libc | v1.75.6 | [LICENSE-3RD-PARTY.md](licenses/modernc.org_libc@v1.75.6_LICENSE-3RD-PARTY.md), [LICENSE](licenses/modernc.org_libc@v1.75.6_LICENSE) | Yes |
| modernc.org/mathutil | v1.7.1 | [LICENSE](licenses/modernc.org_mathutil@v1.7.1_LICENSE) | Yes |
| modernc.org/memory | v1.12.1 | [LICENSE-MMAP-GO](licenses/modernc.org_memory@v1.12.1_LICENSE-MMAP-GO), [LICENSE-LOGO](licenses/modernc.org_memory@v1.12.1_LICENSE-LOGO), [LICENSE-GO](licenses/modernc.org_memory@v1.12.1_LICENSE-GO), [LICENSE](licenses/modernc.org_memory@v1.12.1_LICENSE) | Yes |
| modernc.org/opt | v0.2.0 | [LICENSE](licenses/modernc.org_opt@v0.2.0_LICENSE) | No |
| modernc.org/sortutil | v1.2.1 | [LICENSE](licenses/modernc.org_sortutil@v1.2.1_LICENSE) | No |
| modernc.org/sqlite | v1.58.0 | [LICENSE-SQLITE_VEC](licenses/modernc.org_sqlite@v1.58.0_LICENSE-SQLITE_VEC), [LICENSE-SQLITE](licenses/modernc.org_sqlite@v1.58.0_LICENSE-SQLITE), [LICENSE](licenses/modernc.org_sqlite@v1.58.0_LICENSE) | Yes |
| modernc.org/strutil | v1.2.1 | [LICENSE](licenses/modernc.org_strutil@v1.2.1_LICENSE) | No |
| modernc.org/token | v1.1.0 | [LICENSE](licenses/modernc.org_token@v1.1.0_LICENSE) | No |
| github.com/chromedp/chromedp | v0.16.0 | [LICENSE](licenses/github.com_chromedp_chromedp@v0.16.0_LICENSE) | Yes |
| github.com/chromedp/cdproto | v0.0.0-20260714215040-dc233986426f | [LICENSE](licenses/github.com_chromedp_cdproto@v0.0.0-20260714215040-dc233986426f_LICENSE) | Yes |
| github.com/chromedp/sysutil | v1.1.0 | [LICENSE](licenses/github.com_chromedp_sysutil@v1.1.0_LICENSE) | Yes |
| github.com/go-json-experiment/json | v0.0.0-20260623181947-01eb4420fa68 | [LICENSE](licenses/github.com_go-json-experiment_json@v0.0.0-20260623181947-01eb4420fa68_LICENSE) | Yes |
| github.com/gobwas/ws | v1.4.0 | [LICENSE](licenses/github.com_gobwas_ws@v1.4.0_LICENSE) | Yes |
| github.com/gobwas/pool | v0.2.1 | [LICENSE](licenses/github.com_gobwas_pool@v0.2.1_LICENSE) | Yes |
| github.com/gobwas/httphead | v0.1.0 | [LICENSE](licenses/github.com_gobwas_httphead@v0.1.0_LICENSE) | Yes |
