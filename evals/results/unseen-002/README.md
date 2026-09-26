# Recorded unseen validation: 26 September 2026

| Metric | Original vanilla | Frozen winner |
|---|---:|---:|
| Passed | 45/60 | 53/60 |
| Mean agent time | 70.58s | 47.37s |
| Nearest-rank P50 | 54.30s | 30.92s |
| P95 | 180.06s | 130.77s |

The winner met the preregistered gate: at least vanilla's passes, no paired storage-check regressions, and lower mean and P50. Mean time fell **32.9%** and P50 **43.1%**, including failures. This comparison used the original vanilla revision `6a00e38` and the frozen Round 032 winner (`b1d5a3f`, published with identical product source at `review/frozen-r032`). Both used Muse `muse-spark-1-3`; the winner also used Jev `jev-1.13.0`.

## Audit without running models

From the repository root:

```sh
python3 evals/audit_results.py
```

The audit checks evidence hashes, all 120 task/repetition identities, every pass flag against its checks/error, all 393 drained model calls and model identities, and recalculates the acceptance decision and timings. `results.jsonl`, `calls.jsonl`, and `changes.jsonl` each retain one entry per attempt, including failures. `preregistration.json` contains the original schedule and hashes. `provenance.json` documents source hashes and replacement of local filesystem prefixes.

The full 109 MB working archive includes screenshots, traces, app trees and offline controls. It is retained locally and **not** uploaded here. The compact published data supports numeric and check-outcome auditing; a fresh [replication](../../README.md) regenerates browser and application evidence. It is not a byte-for-byte substitute for that archive.

## Failures and limitations

Vanilla had five attempts ending in upstream service errors, four timeouts and six requirement failures. The winner had four upstream service errors, one timeout and two requirement failures. Its three non-service failures were quiz cases: creation used the wrong option labels, a feature repair exhausted the edit budget, and one bug fix retained the old grading loop alongside its replacement.

A service outage caused seven consecutive upstream failures. The campaign paused for approximately 561 seconds between completed attempts, then resumed after recovery. Ten tiny operational probes are logged separately and excluded; every scored service error remained in the 120 results. Removing both sides of any pair with a non-200 call leaves 54 pairs: vanilla 44/54, winner 51/54, with approximately 35.2% lower winner mean time. That is a post-hoc sensitivity check, not the acceptance rule.

Campaign 001 was invalidated after a native search-input grader false negative. Campaign 002 reran all 120 attempts with the corrected grader and unchanged actors. None of campaign 001's rows is substituted here. The previously tuned, different suite reached 60/60; that exposed result is not this unseen validation.

Five app families and correlated repetitions do not establish universal reliability. These tasks are now published and exposed. The later integration with current main has offline test coverage but has **not** been assigned these frozen binary's performance numbers.
