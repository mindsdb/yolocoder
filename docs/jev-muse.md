# Models and context selection

MindsHub connections use the complete Muse + Jev approach automatically in the CLI and web app. Normal installs, source builds and automatic updates all include it; no special build or activation setting is needed. Both models use the same MindsHub API key.

- **Muse 1.3** (`muse-spark-1-3`) is the default coding model.
- **Jev** (`jev-1.13.0`) selects initial context and identifies small UI edits.
- Small UI edits use a bounded Muse conversation with literal replacements checked for a unique match before the batch is written.
- Other coding tasks use the selected primary model, with bounded initial context, explicit completion checks and recovery from narrowly recognized provider errors.

The project's detected check runs after edits. Jev cannot skip checks or declare completion. Its existing time, confidence and context limits still apply; when routing is uncertain or a request fails, the coding model can obtain its own context. Image requests and projects outside the router's supported bounds also use the normal coding conversation.

## Existing connections and model choices

Saved MindsHub connections automatically gain Jev context selection and small-edit routing. Configurations from before this change that selected the old `mindshub_air` default now use Muse 1.3. Other saved model choices are preserved. Use the normal model picker to choose another primary model; small UI edits still use Muse 1.3. An explicit selection of `mindshub_air` saved with the new version is also preserved.

Connections to the MindsHub inference URL through the custom-provider form or environment variables receive the same behaviour. For an environment connection to MindsHub, omitting `OPENAI_MODEL` selects Muse 1.3. The supported domain override applies to endpoint recognition too.

Jev routing takes precedence over the older `/preselect` preference, so enabling that preference does not add another selector request.

## Other inference providers

Other providers use their configured model to read context and make edits. They do not automatically receive Jev decision requests or Muse model requests. Explicit completion checks, checked edits and applicable bounded error recovery remain active. Both Responses and chat-completions endpoints remain supported.
