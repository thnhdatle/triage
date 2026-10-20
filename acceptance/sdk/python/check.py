# SPDX-License-Identifier: Apache-2.0
"""TRG-05: official OpenAI Python SDK checks.

Unmodified against any OpenAI-compatible endpoint: reads OPENAI_BASE_URL,
OPENAI_API_KEY and the model names from the environment. Prints one JSON line
per check and a final summary line.
"""

import json
import os
import sys
from collections.abc import Callable

import openai
from openai import OpenAI

CHAT_MODEL = os.environ.get("CHECK_CHAT_MODEL", "gpt-4o-mini")
TOOL_MODEL = os.environ.get("CHECK_TOOL_MODEL", "gpt-4o-mini")
EMBED_MODEL = os.environ.get("CHECK_EMBED_MODEL", "text-embedding-3-small")
UNKNOWN_MODEL = os.environ.get("CHECK_UNKNOWN_MODEL", "no-such-model")
LIST_MODELS = [m for m in os.environ.get("CHECK_LIST_MODELS", CHAT_MODEL).split(",") if m]


class CheckError(Exception):
    """A check expectation did not hold."""


INPUTS = ["alpha", "beta"]
NOT_FOUND = 404
UNAUTHORIZED = 401
client = OpenAI()
results: list[bool] = []


def require(cond: object, msg: str = "check failed") -> None:
    if not cond:
        raise CheckError(msg)


def check(name: str, fn: Callable[[], str]) -> None:
    try:
        detail = fn()
        ok = True
    except Exception as exc:
        detail = f"{type(exc).__name__}: {exc}"
        ok = False
    results.append(ok)
    print(json.dumps({"check": name, "ok": ok, "detail": detail}), flush=True)


def chat() -> str:
    r = client.chat.completions.create(
        model=CHAT_MODEL, messages=[{"role": "user", "content": "say hello"}]
    )
    content = r.choices[0].message.content
    if not content:
        raise CheckError("empty content")
    if r.usage is None:
        raise CheckError("no usage")
    require(r.usage.prompt_tokens > 0)
    require(r.choices[0].finish_reason == "stop")
    return content


def stream() -> str:
    text = ""
    usage = None
    with client.chat.completions.create(
        model=CHAT_MODEL,
        messages=[{"role": "user", "content": "say hello again"}],
        stream=True,
        stream_options={"include_usage": True},
    ) as s:
        for chunk in s:
            if chunk.choices:
                text += chunk.choices[0].delta.content or ""
            if chunk.usage is not None:
                usage = chunk.usage
    require(text, "no streamed content")
    if usage is None:
        raise CheckError("no usage chunk")
    require(usage.completion_tokens > 0)
    return text


def tools() -> str:
    r = client.chat.completions.create(
        model=TOOL_MODEL,
        messages=[{"role": "user", "content": "weather in Paris"}],
        tools=[
            {
                "type": "function",
                "function": {
                    "name": "get_weather",
                    "description": "Get the weather",
                    "parameters": {
                        "type": "object",
                        "properties": {"input": {"type": "string"}},
                    },
                },
            }
        ],
    )
    calls = r.choices[0].message.tool_calls
    if not calls:
        raise CheckError("no tool_calls")
    call = calls[0]
    if call.type != "function":
        raise CheckError(f"tool call type {call.type}")
    require(call.function.name == "get_weather")
    args = json.loads(call.function.arguments)
    require(args.get("input") == "weather in Paris", str(args))
    require(r.choices[0].finish_reason == "tool_calls")
    return call.function.arguments


def embeddings() -> str:
    r = client.embeddings.create(model=EMBED_MODEL, input=INPUTS)
    require(len(r.data) == len(INPUTS))
    require(all(len(d.embedding) > 0 for d in r.data))
    return f"{len(r.data)} vectors of {len(r.data[0].embedding)}"


def models() -> str:
    ids = {m.id for m in client.models.list()}
    missing = [m for m in LIST_MODELS if m not in ids]
    require(not missing, f"missing {missing} in {sorted(ids)}")
    return ",".join(sorted(ids))


def unknown_model() -> str:
    try:
        client.chat.completions.create(
            model=UNKNOWN_MODEL, messages=[{"role": "user", "content": "hi"}]
        )
    except openai.NotFoundError as exc:
        require(exc.status_code == NOT_FOUND)
        body = exc.body
        if not isinstance(body, dict) or not body.get("message"):
            raise CheckError(f"not an OpenAI error body: {body}") from exc
        return str(body.get("code"))
    msg = "no error raised"
    raise CheckError(msg)


def bad_key() -> str:
    bad = OpenAI(api_key="sk-wrong-key", max_retries=0)
    try:
        bad.chat.completions.create(model=CHAT_MODEL, messages=[{"role": "user", "content": "hi"}])
    except openai.AuthenticationError as exc:
        require(exc.status_code == UNAUTHORIZED)
        return str(exc.status_code)
    msg = "no error raised"
    raise CheckError(msg)


check("py.chat", chat)
check("py.stream", stream)
check("py.tools", tools)
check("py.embeddings", embeddings)
check("py.models", models)
check("py.unknown_model_404", unknown_model)
check("py.bad_key_401", bad_key)
print(json.dumps({"summary": True, "passed": sum(results), "total": len(results)}), flush=True)
sys.exit(0 if all(results) else 1)
