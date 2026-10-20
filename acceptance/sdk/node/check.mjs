// SPDX-License-Identifier: Apache-2.0
// TRG-05: official OpenAI Node SDK checks.
// Unmodified against any OpenAI-compatible endpoint: reads OPENAI_BASE_URL,
// OPENAI_API_KEY and the model names from the environment. Prints one JSON line
// per check and a final summary line.
import assert from "node:assert/strict";
import OpenAI from "openai";

const env = process.env;
const CHAT_MODEL = env.CHECK_CHAT_MODEL ?? "gpt-4o-mini";
const TOOL_MODEL = env.CHECK_TOOL_MODEL ?? "gpt-4o-mini";
const EMBED_MODEL = env.CHECK_EMBED_MODEL ?? "text-embedding-3-small";
const UNKNOWN_MODEL = env.CHECK_UNKNOWN_MODEL ?? "no-such-model";
const LIST_MODELS = (env.CHECK_LIST_MODELS ?? CHAT_MODEL).split(",").filter(Boolean);

const client = new OpenAI();
const results = [];

async function check(name, fn) {
  let ok = true;
  let detail;
  try {
    detail = await fn();
  } catch (err) {
    ok = false;
    detail = `${err?.name}: ${err?.message}`;
  }
  results.push(ok);
  console.log(JSON.stringify({ check: name, ok, detail }));
}

async function expectError(fn, cls, status) {
  try {
    await fn();
  } catch (err) {
    assert.ok(err instanceof cls, `want ${cls.name}, got ${err?.name}: ${err?.message}`);
    assert.equal(err.status, status);
    return err;
  }
  throw new Error("no error raised");
}

await check("node.chat", async () => {
  const r = await client.chat.completions.create({
    model: CHAT_MODEL,
    messages: [{ role: "user", content: "say hello" }],
  });
  assert.ok(r.choices[0].message.content, "empty content");
  assert.ok(r.usage.prompt_tokens > 0);
  assert.equal(r.choices[0].finish_reason, "stop");
  return r.choices[0].message.content;
});

await check("node.stream", async () => {
  const s = await client.chat.completions.create({
    model: CHAT_MODEL,
    messages: [{ role: "user", content: "say hello again" }],
    stream: true,
    stream_options: { include_usage: true },
  });
  let text = "";
  let usage = null;
  for await (const chunk of s) {
    if (chunk.choices.length > 0) text += chunk.choices[0].delta.content ?? "";
    if (chunk.usage) usage = chunk.usage;
  }
  assert.ok(text, "no streamed content");
  assert.ok(usage, "no usage chunk");
  assert.ok(usage.completion_tokens > 0);
  return text;
});

await check("node.tools", async () => {
  const r = await client.chat.completions.create({
    model: TOOL_MODEL,
    messages: [{ role: "user", content: "weather in Paris" }],
    tools: [
      {
        type: "function",
        function: {
          name: "get_weather",
          description: "Get the weather",
          parameters: { type: "object", properties: { input: { type: "string" } } },
        },
      },
    ],
  });
  const calls = r.choices[0].message.tool_calls;
  assert.ok(calls && calls.length > 0, "no tool_calls");
  assert.equal(calls[0].function.name, "get_weather");
  const args = JSON.parse(calls[0].function.arguments);
  assert.equal(args.input, "weather in Paris");
  assert.equal(r.choices[0].finish_reason, "tool_calls");
  return calls[0].function.arguments;
});

await check("node.embeddings", async () => {
  const r = await client.embeddings.create({ model: EMBED_MODEL, input: ["alpha", "beta"] });
  assert.equal(r.data.length, 2);
  assert.ok(r.data.every((d) => d.embedding.length > 0));
  return `${r.data.length} vectors of ${r.data[0].embedding.length}`;
});

await check("node.models", async () => {
  const ids = [];
  for await (const m of client.models.list()) ids.push(m.id);
  const missing = LIST_MODELS.filter((m) => !ids.includes(m));
  assert.equal(missing.length, 0, `missing ${missing} in ${ids}`);
  return ids.sort().join(",");
});

await check("node.unknown_model_404", async () => {
  const err = await expectError(
    () =>
      client.chat.completions.create({
        model: UNKNOWN_MODEL,
        messages: [{ role: "user", content: "hi" }],
      }),
    OpenAI.NotFoundError,
    404,
  );
  assert.ok(err.error?.message, "no OpenAI error body");
  return String(err.code);
});

await check("node.bad_key_401", async () => {
  const bad = new OpenAI({ apiKey: "sk-wrong-key", maxRetries: 0 });
  await expectError(
    () =>
      bad.chat.completions.create({
        model: CHAT_MODEL,
        messages: [{ role: "user", content: "hi" }],
      }),
    OpenAI.AuthenticationError,
    401,
  );
  return "401";
});

const passed = results.filter(Boolean).length;
console.log(JSON.stringify({ summary: true, passed, total: results.length }));
process.exit(passed === results.length ? 0 : 1);
