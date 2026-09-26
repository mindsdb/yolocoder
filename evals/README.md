# Replicate the Muse + Jev comparison

This reproduces the **frozen Round 032 winner versus original vanilla**, using the five-app unseen-validation suite. It does not substitute current main or the new integration branch for either actor. Source files are checked against [actors.json](actors.json) before building. Build hashes vary across platforms/toolchains and are recorded as a new campaign.

## Requirements

- A full clone of this repository, including `review/frozen-r032` (or fetch that branch).
- Go 1.24+, Python 3.10+, Node.js 22 and npm; macOS or Linux with `git`, `tar`, and `/bin/sh`. SQLite's native dependency may need local build tools.
- For the paid step only, your own YoloCoder MindsHub configuration in `~/.config/yolocoder/config.json` and `credentials.json`. Set it up through YoloCoder's `config` command. The harness requires `https://api.mindshub.ai`, pins coding to `muse-spark-1-3`, and allows `jev-1.13.0` only for the candidate. Never commit credentials.

## Workflow

From the repository root:

```sh
python3 evals/reproduce.py setup
python3 evals/reproduce.py prepare
python3 evals/reproduce.py check
# Paid step: up to 120 attempts; it is not started by setup/prepare/check.
python3 evals/reproduce.py run
```

Use `--output /absolute/path/to/new-campaign` on prepare/check/run to choose a different output directory. `GO=/path/to/go` selects a Go executable. Local output and dependencies are ignored by Git. Preparation will not overwrite an existing build; use a fresh directory for another campaign. Setup installs pinned npm dependencies and Playwright Chromium.

The offline check runs unit/browser tests, all **40 positive/negative controls**, and fake-provider timeout/cleanup checks. The paid runner rejects changed actors, graders or dependencies; preregisters the whole schedule; preserves all attempts; and cannot retry an interrupted attempt in place. Resuming a fully recorded boundary reuses its original rows. Do not edit source/dependencies or run multiple campaigns against the same cache while a campaign is active. Eval apps receive a proxy credential, not your provider key. Isolation is per process/project/HOME, not an OS security sandbox.

Results appear in `evals/runs/replication/comparison/`: `decision.json` is the authoritative acceptance result; each actor has a report and every attempt's result, model-call metadata, generated source changes, browser screenshots and logs. Auto-update is disabled for actor processes. Provider errors and timeouts count in the primary outcome; no retries or deletions improve the score.

## Tasks and scoring

Five apps: CSV explorer, quiz builder, inventory manager, notes editor, and a three-step application form. Each has app creation, a small UI edit, a feature, and a seeded bug, repeated three times per actor. Tasks start independently from a scaffold or handwritten reference with existing data. The coding agent does not receive the graders or solutions.

Passing requires every functional browser/API, TypeScript, data-preservation/restart, protected-file and mobile-fit check, plus no actor error. A new candidate is accepted only with at least vanilla's passes, no paired storage regressions, and lower mean **and** nearest-rank P50 agent time over all 60 attempts. Setup/grading time is excluded. Difficult tasks run first, with balanced serial A/B order. Mathematically impossible candidates stop early and are labelled incomplete.

See [the full protocol](PROTOCOL.md), [task definitions](yoloeval/catalog.py), [grading code](browser/checks.mjs), and [acceptance/early-stop rules](yoloeval/unseen.py). Jev routes the candidate; deterministic checks grade the results. There is no LLM judge in this protocol.

## Recorded evidence

The [recorded result](results/unseen-002/README.md) includes all 120 outcomes, 393 model-call records, source diffs, provenance, and an offline audit. Run `python3 evals/audit_results.py` to recalculate its acceptance and timings without API calls.

## Interpretation

The recorded unseen result was **45/60 → 53/60**, mean **70.58s → 47.37s**, P50 **54.30s → 30.92s**. This is a complete-bundle result, not an isolated Jev ablation. Campaign 001 was invalidated for a native-search-input grader bug; campaign 002 restarted all 120 attempts after correcting it. A provider outage was held between attempts; all scored service errors remained in the final data.

The earlier tuned suite reached 60/60; it is distinct from this holdout. These published tasks are now exposed and must not become evidence of generalization after further tuning. Use fresh apps for the next holdout. This checks a specific React/Express/SQLite stack, not comprehensive visual design, accessibility, security, deployment, or other languages. A replicated run measures current provider conditions, so identical scores and timings are not guaranteed.
