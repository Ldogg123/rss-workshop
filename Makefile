GO ?= go
GO_PACKAGES ?= ./cmd/... ./internal/...
PYTHON ?= python3
DOCKER ?= docker
VERSION ?= dev
COMMIT ?= $(shell git rev-parse --verify HEAD 2>/dev/null || printf unknown)
BUILD_DATE ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
SOURCE_URL ?= https://github.com/Ldogg123/rss-workshop
STATIC_IMAGE ?= rss-workshop:static-check
BROWSER_IMAGE ?= rss-workshop:browser-check
BROWSER_TEST_IMAGE ?= rss-workshop:browser-tests
# Set to 1 to also capture the documentation screenshots into artifacts/browser.
RSS_UI_DOCS ?= 0

BUILD_ARGS = --build-arg VERSION="$(VERSION)" --build-arg COMMIT="$(COMMIT)" --build-arg BUILD_DATE="$(BUILD_DATE)" --build-arg SOURCE_URL="$(SOURCE_URL)"

.PHONY: build test race vet fmt-check backup-test release-test smoke-test compose-test check smoke postgres-test postgres-smoke run docker-static docker-browser docker-browser-tests browser-test docker-smoke docker-postgres-smoke check-containers
build:
	CGO_ENABLED=0 $(GO) build -trimpath -buildvcs=false -ldflags="-s -w -X main.version=$(VERSION) -X main.commit=$(COMMIT) -X main.buildDate=$(BUILD_DATE)" -o bin/rss-workshop ./cmd/server
test:
	$(GO) test $(GO_PACKAGES)
race:
	$(GO) test -race $(GO_PACKAGES)
vet:
	$(GO) vet $(GO_PACKAGES)
fmt-check:
	@files=$$($(GO)fmt -l cmd internal) || exit $$?; if [ -n "$$files" ]; then printf 'Run gofmt on:\n%s\n' "$$files"; exit 1; fi
backup-test:
	$(PYTHON) scripts/backup_test.py
smoke-test:
	$(PYTHON) scripts/smoke_test.py
compose-test:
	$(DOCKER) compose version
	DOCKER='$(DOCKER)' $(PYTHON) scripts/compose_test.py
release-test:
	$(PYTHON) scripts/collect_debian_sources_test.py
	$(PYTHON) scripts/release_sources_test.py
	$(PYTHON) scripts/promote_images_test.py
check: fmt-check race vet backup-test release-test smoke-test smoke
smoke: build
	$(PYTHON) scripts/smoke.py
postgres-test:
	@test -n "$$RSS_TEST_POSTGRES_URL" || { printf 'Set RSS_TEST_POSTGRES_URL to a disposable PostgreSQL database.\n'; exit 1; }
	$(GO) test -race ./cmd/server ./internal/store ./internal/web ./internal/scheduler
postgres-smoke: build
	@test -n "$$RSS_TEST_POSTGRES_URL" || { printf 'Set RSS_TEST_POSTGRES_URL to an empty disposable PostgreSQL database.\n'; exit 1; }
	RSS_SMOKE_POSTGRES=1 $(PYTHON) scripts/smoke.py
run:
	$(GO) run ./cmd/server
docker-static:
	$(DOCKER) build $(BUILD_ARGS) -t $(STATIC_IMAGE) .
docker-browser:
	$(DOCKER) build $(BUILD_ARGS) -f Dockerfile.browser --target app -t $(BROWSER_IMAGE) .
docker-browser-tests:
	$(DOCKER) build -f Dockerfile.browser --target browser-tests -t $(BROWSER_TEST_IMAGE) .
browser-test: docker-browser-tests
	mkdir -p artifacts/browser
	chmod 0777 artifacts/browser
	$(DOCKER) run --rm --read-only --cap-drop ALL --security-opt no-new-privileges:true \
		--security-opt seccomp=./deploy/chromium-seccomp.json \
		--tmpfs /tmp:size=256m,mode=1777 --shm-size=256m --pids-limit 256 --memory 2g \
		--mount type=bind,src="$(CURDIR)/artifacts/browser",dst=/artifacts \
		-e RSS_SITE_ARTIFACTS=/artifacts -e RSS_UI_DOCS=$(RSS_UI_DOCS) $(BROWSER_TEST_IMAGE)
docker-smoke: compose-test docker-static docker-browser
	DOCKER='$(DOCKER)' RSS_SMOKE_COMPOSE=1 RSS_SMOKE_IMAGE=$(STATIC_IMAGE) $(PYTHON) scripts/smoke.py
	DOCKER='$(DOCKER)' RSS_SMOKE_COMPOSE=1 RSS_SMOKE_BROWSER=1 RSS_SMOKE_IMAGE=$(BROWSER_IMAGE) $(PYTHON) scripts/smoke.py
docker-postgres-smoke: docker-static
	DOCKER='$(DOCKER)' RSS_SMOKE_COMPOSE=1 RSS_SMOKE_POSTGRES=1 RSS_SMOKE_IMAGE=$(STATIC_IMAGE) $(PYTHON) scripts/smoke.py
check-containers: browser-test docker-smoke docker-postgres-smoke
