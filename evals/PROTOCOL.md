# YoloCoder unseen-app validation 002

This suite was authored after the Round 032 winner was frozen. It is validation, not a new hillclimb. The winner and original vanilla binaries are copied verbatim and identified by SHA-256; neither actor is edited or rebuilt.

| App | Distinctive behaviour | Seeded bug | Feature |
|---|---|---|---|
| CSV explorer | Quoted CSV, validation, persistent datasets, row search | Case-sensitive search | Numeric/text sorting composed with search |
| Quiz builder | Question editor, answer validation, practice and scoring | Choice zero never scores | Persisted explanations and schema migration |
| Inventory manager | Case-insensitive SKU uniqueness and atomic stock adjustments | Negative stock is clipped instead of refused | Live low-stock filtering |
| Notes editor | Literal text, partial edits, search and preservation | Title-only updates erase body | Archive/restore with migration and filtering |
| Application form | Drafts, three-step navigation, validation, immutable submissions | Back navigation clears the statement | Duplicate a submission into an independent draft |

Every app also has a scaffold-to-app creation task and a small heading/button-colour edit. All 20 tasks are independent; three repetitions produce 60 attempts per actor, 120 planned in total. Noncreation tasks start from handwritten reference implementations, with pre-existing records. No generated app becomes the next task's starting point.

## Fixed comparison

Both actors use MindsHub Muse `muse-spark-1-3` for coding. The frozen winner additionally uses its existing `jev-1.13.0` router. The original vanilla binary is the exact one used for the first baseline, not the later context/checklist control.

One actor runs at a time. Within matching task/repetition pairs, A/B and B/A order is balanced; creation tasks run first, followed by features, bugs, then edits. Order inside each block is seeded. Both have a 180-second actor budget and 30 forwarded model calls. Setup and independent grading are excluded from agent time. Timeout cleanup can make observed wall time exceed the nominal budget; raw timings are retained.

An attempt passes only if the actor reports no error and all required typecheck, functional browser/API, protected-file, persistence and mobile-fit checks pass. Graders and reference solutions are outside the project given to the actor. This is process and HOME isolation, not an OS filesystem sandbox.

The winner validates only if it has at least as many passes as vanilla, no paired failures of the two storage-preservation checks when vanilla passes those checks, and strictly lower mean and nearest-rank P50 across all attempts, including failures. The candidate's 60/60 result is reported separately. Five app families cannot establish universal reliability, and repetitions are correlated.

The authoritative executable rules are `evals/yoloeval/unseen.py`. A JSON preregistration, manifests, evaluator/dependency hashes, binary hashes and full schedule are written before the first paid call. Do not use the legacy general-purpose comparator's promotion rule for this validation.

Early stopping occurs only after an attempt and all its upstream calls are drained: an unrecoverable pass-count deficit, a paired storage-check regression, or a mathematically impossible latency result. Unknown future vanilla times have no invented upper bound; latency stopping therefore waits until vanilla's full timing set is known. Early-stopped campaigns are incomplete and are never presented as 60-sample results. Infrastructure gaps stop the campaign without silently retrying or replacing an attempt.

## Offline validation

Each task has a correct positive control and a deliberately incomplete or buggy negative control: 40 controls. Noncreation negatives must fail exactly the intended requirement while other checks pass. Creation negatives must fail the app contract but retain valid TypeScript and protected files. Exploratory calibration exposed exact-label matching problems, migration requirements incorrectly grouped with preservation, an overbroad reference colour change and header rows included in CSV sort assertions. The final `calibration-frozen` directory reruns every control against fixed source fingerprints. Earlier calibration directories are preserved as development evidence and excluded from the final calibration gate.

Unit tests cover recorder behaviour, scoring, pairing, deadline cleanup and new stopping rules. The runtime integration check uses a local fake provider and no paid model calls. No source/scoring changes are allowed once the paid comparison begins. Any future product tuning against these tasks makes them development cases, requiring another fresh holdout.

Visual polish, comprehensive accessibility/security, internet integrations and deployment are outside this functional benchmark. Provider token accounting is preserved as reported; unknown prices and inconsistent token usage are not converted into invented cost or TPS estimates.

## Campaign 002: native search-input correction

Campaign 001 was stopped after 24 completed attempts and one interrupted attempt because the grader incorrectly excluded the browser's `searchbox` role. The generated `notes.create` trial 1 app had a correctly associated label and a native `type="search"` input. Its failed search check was a harness false negative. The original artifacts and interruption were retained in the private working archive; that campaign is invalidated and cannot establish acceptance.

Campaign 002 adds searchbox support and a direct associated-label fallback. Browser unit tests cover text, search, number, email, URL, telephone and password inputs, both wrapping and external labels, populated textareas, selects and checkboxes. Additional offline controls test native search inputs in both CSV and notes, and the exact generated notes source that exposed the false negative.

The original vanilla and winning binaries, public tasks, repetitions, budgets, order seed, pass requirements and acceptance rules are unchanged. No YoloCoder source, prompts or model configuration were tuned against these cases. The entire 120-attempt comparison restarts under a newly frozen evaluator; no previous paid row is substituted into it. This is an infrastructure correction, not a candidate revision.

## Portable replication

This published harness preserves the tasks, fixtures, graders, ordering, budgets and decision function from campaign 002. Its launcher accepts locally rebuilt actor hashes rather than requiring the original macOS binaries or the original account-specific credential prefix. `reproduce.py` verifies every actor source file against the frozen inventory, runs offline controls, and records new binary, evaluator and dependency hashes before a new campaign. The published suite digest therefore differs from the historical receipt. See [README.md](README.md).
