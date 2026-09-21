// Decisions — judgement with numbers behind it.
// https://docs.mindshub.ai/inference/decisions
//
// Unlike llm.ts this is not the OpenAI API and nothing you have read
// elsewhere describes it, so the whole of it is below.
//
// WHAT IT IS FOR
//
// Ask a chat model to classify something and you get a word back, with
// no way to know whether it was sure. This returns the answer *and* the
// probability of every option it considered, plus a confidence. Use it
// to route, classify, rate and judge — anywhere the next line of code is
// an `if` rather than a paragraph on a screen.
//
// HOW A CALL IS SHAPED
//
//   state      what is being judged. A string, an object, or an array —
//              whatever carries the information. Never a bare number,
//              boolean or null.
//   questions  one or more named questions asked about that state. Each
//              comes back under the same name.
//
// Every question takes optional `instructions`: the judgement being made,
// in plain words. Say what you mean in `instructions`, in `criteria`, or
// in both — a question with neither is a guess.
//
// THE THREE KINDS OF QUESTION
//
//   noul    a yes/no judgement. Returns a probability from 0 to 1 that
//           the answer is yes. `criteria` is optional: give it
//           { true: "...", false: "..." } when the boundary is unclear.
//
//   choice  pick one of yours. `criteria` is required, an object of
//           option name to what that option means. Returns the name.
//
//   score   a rating. `criteria` is required, an ARRAY where position is
//           the level — index 0 is the bottom, the last index the top.
//           Returns a probability-weighted mean, so 2.4 is a real answer
//           and means "between 2 and 3, nearer 2".
//
// AN EXAMPLE, END TO END
//
//   const { answers } = await decide(
//     { subject: "Refund please", body: "Arrived smashed in two." },
//     {
//       urgent:  { type: "noul",   instructions: "Does this need a human today?" },
//       team:    { type: "choice", criteria: { packaging: "Damaged in transit",
//                                              product:   "Faulty as made",
//                                              billing:   "Money, not goods" } },
//       damage:  { type: "score",  criteria: ["untouched", "scuffed", "dented", "unusable"] },
//     },
//   );
//
//   answers.urgent.noul          // 0.93
//   answers.team.choice          // "packaging"
//   answers.team.probabilities   // { packaging: 0.88, product: 0.09, billing: 0.03 }
//   answers.damage.score         // 2.7
//   answers.urgent.confidence    // 0.81  — how sure it is, on every answer
//
// Read `confidence` before you act on `choice`. A confident wrong answer
// and an unsure right one look identical if you only take the value.

const BASE = process.env.OPENAI_BASE_URL ?? "https://api.mindshub.ai/v1";
const KEY = process.env.OPENAI_API_KEY;

export type Question =
  | { type: "noul"; instructions?: unknown; criteria?: { true?: string; false?: string } }
  | { type: "choice"; instructions?: unknown; criteria: Record<string, string> }
  | { type: "score"; instructions?: unknown; criteria: string[] };

export type Answer = {
  type: "noul" | "choice" | "score";
  noul?: number;
  choice?: string;
  score?: number;
  probabilities?: Record<string, number>;
  confidence?: number;
};

export type Decision = {
  model: string;
  answers: Record<string, Answer>;
  usage?: { input_tokens: number; output_tokens: number };
};

/** Ask one or more questions about some state. */
export async function decide(
  state: unknown,
  questions: Record<string, Question>,
  model = "jev",
): Promise<Decision> {
  if (!KEY) {
    throw new Error(
      "No inference key in the environment. yolocoder sets OPENAI_API_KEY when it " +
        "starts the dev server; if you ran scripts/start.sh yourself, export it first.",
    );
  }
  const response = await fetch(`${BASE}/decisions`, {
    method: "POST",
    headers: { "Content-Type": "application/json", Authorization: `Bearer ${KEY}` },
    body: JSON.stringify({ model, state, questions }),
  });
  if (!response.ok) {
    throw new Error(`decisions ${response.status}: ${await response.text()}`);
  }
  return response.json();
}
