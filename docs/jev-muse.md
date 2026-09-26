# Muse + Jev review build

This ports the Round 032 winning approach to current upstream main. It is a review build, **not the binary measured in the frozen benchmark**. The exact benchmark source remains on `review/frozen-r032`; the companion evaluation PR contains a portable source-verified reproduction workflow.

## Build and try it

Requires Go 1.24+, Node 22 for web projects, and a MindsHub account serving both `muse-spark-1-3` and `jev-1.13.0`.

```sh
./scripts/build-jev-muse.sh
YOLOCODER_NO_AUTOUPDATE=1 ./dist/yolocoder-jev-muse config
YOLOCODER_NO_AUTOUPDATE=1 ./dist/yolocoder-jev-muse model
```

Configure MindsHub and select **muse-spark-1-3** as the coding model. The build selects **jev-1.13.0** for routing and **muse-spark-1-3** for the bounded small-edit writer. The primary model remains the user's saved configuration; the model picker does not change the compiled small-edit writer. Keep auto-update disabled when trying the review build so it cannot replace itself with a published main binary.

From an empty app directory or an existing YoloCoder web project, launch the absolute path to the built binary with `--web`. The normal CLI works too. No API key is stored in this repository.

## What changed

- Jev classifies bounded UI changes and selects initial context. Confident small edits use a short Muse conversation with exact literal replacements. Normal tasks can receive a bounded snapshot of application source. Errors or uncertainty leave the coding model able to obtain its own context.
- Completion requires an explicit `task_complete` boolean on normal edits, successful tool results, a complete response envelope, and the project's detected check. A closing sentence alone cannot finish a normal task. Prompts reconcile the whole request and use observed data schemas.
- Patch rejections include bounded path and remaining-edit feedback; elapsed-time feedback and one bounded diagnostic check support required continuations. Narrow transport/policy recovery has finite budgets and does not replay accepted edits.
- Literal replacements validate uniqueness and overlap before applying the batch. Compact-patch errors explain the anchor needed to repair an insertion.

## Integration with newer main

Main added a separate `/preselect` implementation, architecture notes, read-by-pattern, search result pre-reading, and rejected-patch records after the benchmark winner was frozen. These features are retained. In a Jev review build the winner's router takes precedence over `/preselect`, avoiding two selectors for one request. A normal build still uses `/preselect` when enabled.

The winner's stricter `task_complete` tool contract supersedes main's `turn_is_complete`/closing-note completion for normal tasks. Small UI edits retain the winner's closing-note contract. Main's search tools remain available on both paths. Plain builds omit the new router and small-edit writer unless the linker variables are set; normal completion/recovery changes still apply.

The integrated branch needs a fresh paired paid benchmark before its speed/delivery numbers can be claimed. Offline Go tests validate integration behavior, not model performance. The frozen winner scored **53/60 versus vanilla 45/60** on five unseen apps, with mean agent time **47.37s versus 70.58s**. Failures, including upstream errors, were retained. This tested the complete bundle, not Jev in isolation.
