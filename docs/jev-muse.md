# Muse + Jev build

Use Jev to select initial context and route small UI edits to a bounded Muse conversation. Other coding tasks use the configured primary model, with explicit completion checks and bounded recovery from supported provider errors.

## Build and configure

Requires Go 1.24+, Node.js 22 for web projects, and a MindsHub account serving `muse-spark-1-3` and `jev-1.13.0`.

```sh
./scripts/build-jev-muse.sh
YOLOCODER_NO_AUTOUPDATE=1 ./dist/yolocoder-jev-muse config
YOLOCODER_NO_AUTOUPDATE=1 ./dist/yolocoder-jev-muse model
```

Configure MindsHub and select **muse-spark-1-3** as the primary coding model. The build selects **jev-1.13.0** for routing and **muse-spark-1-3** for the small-edit writer. The model picker changes the primary model; the small-edit model is set at build time.

From an empty app directory or an existing YoloCoder web project, launch the absolute path to the built binary with `--web`. The normal CLI also works. Keep `YOLOCODER_NO_AUTOUPDATE=1` when running this custom build so an update does not replace its compiled model configuration.

## Behaviour

Jev selects bounded initial context; uncertainty or an unavailable router lets the coding model obtain its own context. Small UI edits can use exact literal substitutions, checked for a unique match before the batch is written. The project's detected check still runs after edits.

Normal tasks finish only after an explicit completion signal, successful tool results, a complete response and the project's detected check. Rejected edits receive path and remaining-budget feedback. A bounded diagnostic check can help with an already-required continuation, and narrowly recognized provider errors have a finite recovery budget.

The Jev router takes precedence over `/preselect` in this build, avoiding two selectors per request. Plain builds still support `/preselect`; their normal completion and recovery improvements apply without the new router. Architecture notes, pattern-based reads, search pre-reading and rejected-patch logging remain available.
