# Branch: main

**Purpose:** Primary development branch

_Commits will be appended below._

## Commit 6a73b4b3 — 2026-08-05 22:09 UTC

### Branch Purpose
Primary development branch

### Previous Progress Summary


### This Commit's Contribution
LICENSE file + README License section, committed b260779, pushed to origin/main over HTTPS (SSH key not in agent)

---

## Commit 6aa071c8 — 2026-09-08 20:36 UTC

### Branch Purpose
Primary development branch

### Previous Progress Summary
LICENSE file + README License section, committed b260779, pushed to origin/main over HTTPS (SSH key not in agent)

### This Commit's Contribution
GH issue #1 (find_dead_code FP). 4 workstreams: WS1 parser (8bit, w8:p2, worktree ws/issue1-parser), WS2 orphans (4bit, w8:p3, worktree ws/issue1-orphans) running; WS3 integrate+docs and WS4 QA briefs pre-written in scratchpad. Fix branch fix/issue-1-dead-code-false-positives.

---

## Commit 6aa0882e — 2026-09-08 22:11 UTC

### Branch Purpose
Primary development branch

### Previous Progress Summary
GH issue #1 (find_dead_code FP). 4 workstreams: WS1 parser (8bit, w8:p2, worktree ws/issue1-parser), WS2 orphans (4bit, w8:p3, worktree ws/issue1-orphans) running; WS3 integrate+docs and WS4 QA briefs pre-written in scratchpad. Fix branch fix/issue-1-dead-code-false-positives.

### This Commit's Contribution
Commit 1ab31e6 on ws/issue1-orphans: DeadCodeOptions, ClassifyFile, OrphanFile.Lang/Category, test/docs orphans hidden by default, MCP params include_test_orphans/include_doc_orphans, 4 tests. Reviewed: build/vet/gofmt/tests clean, prose untouched. WS1 parser still in progress after steer to stop narrating. Pre-existing gofmt issue in internal/daemon/daemon.go deferred to WS3 chore commit.

---

## Commit 6aa08e2a — 2026-09-08 22:37 UTC

### Branch Purpose
Primary development branch

### Previous Progress Summary
Commit 1ab31e6 on ws/issue1-orphans: DeadCodeOptions, ClassifyFile, OrphanFile.Lang/Category, test/docs orphans hidden by default, MCP params include_test_orphans/include_doc_orphans, 4 tests. Reviewed: build/vet/gofmt/tests clean, prose untouched. WS1 parser still in progress after steer to stop narrating. Pre-existing gofmt issue in internal/daemon/daemon.go deferred to WS3 chore commit.

### This Commit's Contribution
WS1 commit 6485505 on ws/issue1-parser: insideFunctionScope helper drops Python function-local variable defs; 30 @name.reference.value captures; 12 parser tests. WS2 commit 1ab31e6 on ws/issue1-orphans: DeadCodeOptions, ClassifyFile, orphan lang/category, hidden test/docs orphans, MCP flags; 4 tests. Next: WS3 integrate+docs in main checkout on fix branch, then WS4 QA.

---

## Commit 6aa09311 — 2026-09-08 22:58 UTC

### Branch Purpose
Primary development branch

### Previous Progress Summary
WS1 commit 6485505 on ws/issue1-parser: insideFunctionScope helper drops Python function-local variable defs; 30 @name.reference.value captures; 12 parser tests. WS2 commit 1ab31e6 on ws/issue1-orphans: DeadCodeOptions, ClassifyFile, orphan lang/category, hidden test/docs orphans, MCP flags; 4 tests. Next: WS3 integrate+docs in main checkout on fix branch, then WS4 QA.

### This Commit's Contribution
1579f4f parser, f0e196a graph, 937f072 gofmt chore daemon.go, 237f102 docs (tool description, kinds help, explain prompt row + example 3, README table/accuracy notes/markdown section). go build/vet/gofmt/test clean. Launching WS4 QA (pi 8bit).

---

## Commit 6aa0af60 — 2026-09-09 00:59 UTC

### Branch Purpose
Primary development branch

### Previous Progress Summary
1579f4f parser, f0e196a graph, 937f072 gofmt chore daemon.go, 237f102 docs (tool description, kinds help, explain prompt row + example 3, README table/accuracy notes/markdown section). go build/vet/gofmt/test clean. Launching WS4 QA (pi 8bit).

### This Commit's Contribution
bb0b658 adds project-level e2e test over a Python fixture reproducing classes A/B/C plus true positives, and an MCP schema test. QA findings (not fixed, for follow-up): parser still misses dict keys / except classes / annotations / comprehension iterables / *args refs; min_rank MCP description direction inverted (pre-existing); negative min_rank hides all orphans; spec/ dir classified as test may be over-broad; name-based referenced set means local refs can shadow dead module-level names. Final review via /code-review in progress.

---

## Commit 6aa160b3 — 2026-09-09 13:35 UTC

### Branch Purpose
Primary development branch

### Previous Progress Summary
bb0b658 adds project-level e2e test over a Python fixture reproducing classes A/B/C plus true positives, and an MCP schema test. QA findings (not fixed, for follow-up): parser still misses dict keys / except classes / annotations / comprehension iterables / *args refs; min_rank MCP description direction inverted (pre-existing); negative min_rank hides all orphans; spec/ dir classified as test may be over-broad; name-based referenced set means local refs can shadow dead module-level names. Final review via /code-review in progress.

### This Commit's Contribution
User approved: cache invalidation via parser fingerprint, local-shadowing rule in graph.Build (ref resolves locally when the file defines the name), min_rank description fix + clamp <=0. Follow-ups (parser coverage gaps, test funcs in dead_symbols, dir heuristics/Windows paths, changed_symbols enclosing function, MinRank zero trap, Go/TS parity, query compile caching, name-based shadowing) to be tracked as GitHub issues pending user confirmation.

---

## Commit 6aa1633b — 2026-09-09 13:46 UTC

### Branch Purpose
Primary development branch

### Previous Progress Summary
User approved: cache invalidation via parser fingerprint, local-shadowing rule in graph.Build (ref resolves locally when the file defines the name), min_rank description fix + clamp <=0. Follow-ups (parser coverage gaps, test funcs in dead_symbols, dir heuristics/Windows paths, changed_symbols enclosing function, MinRank zero trap, Go/TS parity, query compile caching, name-based shadowing) to be tracked as GitHub issues pending user confirmation.

### This Commit's Contribution
WS6 (pi 4bit) filed 4 enhancement issues from review findings: #2 parser coverage + TS/Go parity, #3 dead-symbol categorization/heuristics/Windows paths, #4 changed_symbols enclosing definition, #5 robustness/perf. WS5 must-fix still running.

---

## Commit 6aa16ae4 — 2026-09-09 14:19 UTC

### Branch Purpose
Primary development branch

### Previous Progress Summary
WS6 (pi 4bit) filed 4 enhancement issues from review findings: #2 parser coverage + TS/Go parity, #3 dead-symbol categorization/heuristics/Windows paths, #4 changed_symbols enclosing definition, #5 robustness/perf. WS5 must-fix still running.

### This Commit's Contribution
fix/issue-1-dead-code-false-positives: 9 commits, 44 tests. Must-fix items landed: cache versioned by parser fingerprint (+legacy purge), local-shadowing edge rule + self/cls dropped, min_rank description/clamp. Follow-ups tracked as GH #2-#5. Branch not pushed; PR and issue reply left to the user.

---

## Commit 6aa17b77 — 2026-09-09 15:29 UTC

### Branch Purpose
Primary development branch

### Previous Progress Summary
fix/issue-1-dead-code-false-positives: 9 commits, 44 tests. Must-fix items landed: cache versioned by parser fingerprint (+legacy purge), local-shadowing edge rule + self/cls dropped, min_rank description/clamp. Follow-ups tracked as GH #2-#5. Branch not pushed; PR and issue reply left to the user.

### This Commit's Contribution
Empirical validation: old installed daemon (f142c39) vs new build on repomap-go (Go: expect identical dead symbols, test/docs orphans hidden) and vllm-mlx (Python: expect issue #1 false positives gone). Measures cache purge+reparse time, verifies meta.parser_version, restores launchd daemon. Instruments in scratchpad/selfcheck/.

---

## Commit 6aa17d0b — 2026-09-09 15:36 UTC

### Branch Purpose
Primary development branch

### Previous Progress Summary
Empirical validation: old installed daemon (f142c39) vs new build on repomap-go (Go: expect identical dead symbols, test/docs orphans hidden) and vllm-mlx (Python: expect issue #1 false positives gone). Measures cache purge+reparse time, verifies meta.parser_version, restores launchd daemon. Instruments in scratchpad/selfcheck/.

### This Commit's Contribution
vllm-mlx unexported_only: 426 (old) -> 62 (new); kinds recipe 89 -> 26; orphans 42 -> 7 shown + 68 hidden test/docs. repomap-go: Go symbols unchanged except daemon.go 'payload' now marked referenced by a Python fixture ref (cross-language name shadowing, issue #5). Migration: meta table + parser_version fingerprint written, full reparse of vllm-mlx in 3.5s. Hazards: launchctl bootout left old daemon pid 52687 running for 30+ min; when later SIGTERMed it exited in 2s but Go's unix listener unlink-on-close removed the socket now owned by the restored daemon, leaving it unreachable; fixed with launchctl kickstart -k. Daemon now pid 40949, healthy.

---

## Commit 6aa18674 — 2026-09-09 16:16 UTC

### Branch Purpose
Primary development branch

### Previous Progress Summary
vllm-mlx unexported_only: 426 (old) -> 62 (new); kinds recipe 89 -> 26; orphans 42 -> 7 shown + 68 hidden test/docs. repomap-go: Go symbols unchanged except daemon.go 'payload' now marked referenced by a Python fixture ref (cross-language name shadowing, issue #5). Migration: meta table + parser_version fingerprint written, full reparse of vllm-mlx in 3.5s. Hazards: launchctl bootout left old daemon pid 52687 running for 30+ min; when later SIGTERMed it exited in 2s but Go's unix listener unlink-on-close removed the socket now owned by the restored daemon, leaving it unreachable; fixed with launchctl kickstart -k. Daemon now pid 40949, healthy.

### This Commit's Contribution
Closed tabs for WS1-WS7, removed merged worktrees and ws/issue1-* branches. Issue #6 body drafted from self-check evidence (socket unlink-on-close steals current daemon's socket; no single-instance guard; bootout left daemon alive). WS9 works on fix/issue-6-daemon-lifecycle off main in a new worktree, forbidden from touching the real launchd job.

---

## Commit 6aa1a9bc — 2026-09-09 18:47 UTC

### Branch Purpose
Primary development branch

### Previous Progress Summary
Closed tabs for WS1-WS7, removed merged worktrees and ws/issue1-* branches. Issue #6 body drafted from self-check evidence (socket unlink-on-close steals current daemon's socket; no single-instance guard; bootout left daemon alive). WS9 works on fix/issue-6-daemon-lifecycle off main in a new worktree, forbidden from touching the real launchd job.

### This Commit's Contribution
A e907a9c inode-checked socket unlink; B 4708551 socket-probe single-instance guard (pidfile was a stale sidecar with an unlocked read-check-write); C bc252db shutdown proven bounded by test, bootout incident explained as signal delivery to an orphan proxy-spawned daemon; D 31c6a56 README upgrade section + install-service note. Branch unpushed in worktree ~/sourcecode/repomap-go-ws-daemon. Known nit: active SSE handlers ride out the full 5s shutdown cap. All herdr tabs except mine closed.

---

