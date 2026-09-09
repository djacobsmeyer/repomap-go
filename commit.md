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

