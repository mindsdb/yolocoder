# Scoring

An attempt passes only with no actor error and all mandatory checks passing. Weighted scores (acceptance 60%, regression 30%, usability 10%) are diagnostic only and cannot turn a failed attempt into a success.

Campaign acceptance requires at least vanilla's pass count, no paired storage-check regression, and lower mean and nearest-rank median agent time across all attempts. The full executable rules are yoloeval/unseen.py and the generated preregistration.json. First-visible-correct-preview latency is not measured.

Five new applications × four independent tasks × three repetitions = 60 attempts per actor. The repeat count does not mean 60 independent applications. No model judge determines functional pass/fail.
