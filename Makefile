GO ?= go
COMPOSE := docker compose -f deploy/docker-compose.yaml
WORKER_BIN := bin/worker
SMOKETEST_BIN := bin/smoketest

.PHONY: build test smoketest compose-down

build:
	$(GO) build ./...

test:
	$(GO) test ./...

$(WORKER_BIN): FORCE
	$(GO) build -o $(WORKER_BIN) ./cmd/worker

$(SMOKETEST_BIN): FORCE
	$(GO) build -o $(SMOKETEST_BIN) ./cmd/smoketest

.PHONY: FORCE
FORCE:

# make smoketest wipes any prior Temporal/Postgres state before every run
# (the fixed workflow IDs in cmd/smoketest would otherwise collide with a
# prior run's history), brings the stack back up, then runs the worker +
# smoketest binary and propagates its exit code. Both binaries dial
# Temporal with their own bounded retry (internal/temporalconn) rather
# than the Makefile blindly sleeping — temporalio/auto-setup accepts TCP
# connections before its schema-setup/namespace-registration finishes, so
# a single dial attempt right after `up` is not a reliable readiness
# signal. It leaves Temporal/Postgres running afterward for inspection via
# the UI at localhost:8080 — the next `make smoketest` wipes it again on
# entry, so no separate reset step is needed.
#
# FACTORY_ROOT is pinned to a fresh temp dir for the worker process only
# (not exported to the whole shell): worktree.create is the real gitops
# Activity now, and its default root (/var/lib/factory) both risks a
# permission error on a dev machine and would accumulate stale clones
# across runs otherwise. A fresh root every run matches the same
# clean-slate approach already used for Temporal/Postgres above.
#
# SMOKETEST_REPO_CLONE_URL/SMOKETEST_REPO_NAME are required (see
# cmd/smoketest's doc comment) — pr.create_and_link needs a real
# GitHub-hosted repo, so there's no default to fall back to here. Set
# them in your own shell before running this target. Checked before
# bringing Temporal/Postgres up so a missing config fails in <1s, not
# after a ~20s compose cycle.
#
# FACTORY_STUB_HARNESS_INVOKE forces the worker to use the stub
# harness.invoke instead of the real one (internal/activities/harness) —
# unlike every other real Activity, harness.invoke costs real API credits
# per call, so this target always sets it: routine dev-loop smoketest
# runs stay free, unconditionally, not just by default. To exercise the
# real harness adapters (real cost), run the worker directly rather than
# through this target — see cmd/worker's doc comment.
smoketest: $(WORKER_BIN) $(SMOKETEST_BIN)
	@if [ -z "$$SMOKETEST_REPO_CLONE_URL" ]; then \
		echo "smoketest: SMOKETEST_REPO_CLONE_URL is not set — see cmd/smoketest/main.go's doc comment" >&2; \
		exit 1; \
	fi
	$(COMPOSE) down -v --remove-orphans
	$(COMPOSE) up -d --wait
	export FACTORY_ROOT="$$(mktemp -d)"; \
	export FACTORY_STUB_HARNESS_INVOKE=1; \
	./$(WORKER_BIN) & \
	WORKER_PID=$$!; \
	./$(SMOKETEST_BIN); CODE=$$?; \
	kill $$WORKER_PID 2>/dev/null; \
	rm -rf "$$FACTORY_ROOT"; \
	exit $$CODE

compose-down:
	$(COMPOSE) down -v --remove-orphans
