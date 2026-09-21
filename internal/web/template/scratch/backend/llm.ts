// Text generation, through whichever inference provider yolocoder is
// connected to. https://docs.mindshub.ai/inference/
//
// The key and base URL arrive in this process's environment — yolocoder
// puts them there when it starts the dev server. Nothing to configure,
// and nothing to commit.
//
// This is the ordinary OpenAI chat-completions API, so anything written
// against that works here unchanged. If you would rather use the SDK
// than fetch: `npm i openai`, then `new OpenAI()` reads exactly the same
// two variables with no arguments.

const BASE = process.env.OPENAI_BASE_URL ?? "https://api.mindshub.ai/v1";
const KEY = process.env.OPENAI_API_KEY;

export const MODEL = process.env.OPENAI_MODEL ?? "mindshub_air";

export type Message = { role: "system" | "user" | "assistant"; content: string };

/** The reply to a conversation, as plain text. */
export async function complete(messages: Message[], model = MODEL): Promise<string> {
  const body = await chat({ model, messages });
  return body.choices?.[0]?.message?.content ?? "";
}

/**
 * The raw chat-completions call, for anything `complete` does not cover —
 * tools, streaming, temperature, a different response shape. `body` is
 * sent as-is, so it takes every parameter the API does.
 */
export async function chat(body: Record<string, unknown>): Promise<any> {
  if (!KEY) {
    throw new Error(
      "No inference key in the environment. yolocoder sets OPENAI_API_KEY when it " +
        "starts the dev server; if you ran scripts/start.sh yourself, export it first.",
    );
  }
  const response = await fetch(`${BASE}/chat/completions`, {
    method: "POST",
    headers: { "Content-Type": "application/json", Authorization: `Bearer ${KEY}` },
    body: JSON.stringify(body),
  });
  if (!response.ok) {
    throw new Error(`inference ${response.status}: ${await response.text()}`);
  }
  return response.json();
}
