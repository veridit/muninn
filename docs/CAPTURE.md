# Automatic capture (opt-in hooks)

`muninn capture` extracts durable memories from conversation text. It is separate
from recall and from jcode's existing extractor. Do not disable an upstream
extractor until a side-by-side comparison meets your capture-quality needs.
No hook or live configuration is changed by `capture`.

## Provider and privacy

The default provider is **remote**: OpenAI-compatible chat completions at
`https://api.deepseek.com`, model `deepseek-flash`. The model id was verified
against DeepSeek's official model list on 2026-11-05:
<https://api-docs.deepseek.com/api/list-models/>. `deepseek-chat` is not silently
assumed to be current. Provider/model selection is configurable.

Add an `extract` object to muninn's `config.json`:

```json
{
  "extract": {
    "provider": "openai-compatible",
    "base_url": "https://api.deepseek.com",
    "model": "deepseek-flash",
    "api_key_env": "DEEPSEEK_API_KEY",
    "env_file": "/absolute/path/to/private/provider.env",
    "every_turns": 12,
    "timeout_seconds": 45,
    "top_k": 12,
    "dedupe_cosine": 0.98,
    "audit_days": 30
  }
}
```

`api_key_env` names an environment variable, **not** a key. If that variable is
unset, `env_file` optionally supplies `KEY=value` lines. The file must be private
(mode 0600). Its content is never copied into config, exported to subprocesses,
or printed. HTTP errors redact response bodies and underlying transport errors.
Remote endpoints require HTTPS. Redirects are refused. Only loopback permits
plain HTTP.

Remote capture sends user/assistant text plus the most relevant memories visible
in the same Binding. Text matching muninn's existing credential patterns is
withheld before sending. Tool blocks, background messages, system notices, and
obvious inter-agent DMs are excluded. Credential-like **candidates** are also
dropped before storage or dry-run output. Pattern matching is a safety net, not
a guarantee that arbitrary confidential prose can be detected. Use local capture
or `"provider": "off"` if remote processing is inappropriate.

For local extraction through Ollama's OpenAI-compatible endpoint:

```json
{
  "extract": {
    "provider": "ollama",
    "base_url": "http://127.0.0.1:11434/v1",
    "model": "your-installed-chat-model"
  }
}
```

Set an installed **chat** model explicitly, not the embedding model. No download
or model installation is performed by capture. Ollama does not receive a key.
Setting `provider` to `off` disables extraction, requests, and checkpoint updates.

`skip_dirs` lists working-directory substrings whose sessions are never captured,
for example eval sandboxes: `"skip_dirs": ["/.jcode/scratch/"]`. The check runs
before any provider call. Hook runs log `skipped=skip_dirs`.

## Manual use

```sh
# Any harness can submit a text chunk and a stable session id.
printf '%s\n' 'user: I prefer tabs in Go.' | muninn capture --session example

# Read a jcode snapshot plus its append journal, read-only.
muninn capture --harness jcode --session SESSION_ID --jcode-home /path/to/.jcode

# Explain proposed writes without saving or advancing capture checkpoints.
muninn capture --harness jcode --session SESSION_ID --dry-run
muninn capture --harness jcode --session SESSION_ID --dry-run --json

# Deliberately process history skipped when hooks first encountered an old session.
# Completed chunks are checkpointed, so repeat this after a timeout to continue.
muninn capture --harness jcode --session SESSION_ID --backfill
```

The default dry-run view is a category/trust/action/content table. JSON additionally
reports prompt/completion/total tokens, provider latency, processed-message count,
and rejected-candidate count. All costs are reported as tokens, not guessed bills.
A dry run may create/update muninn's index, but never memories or capture progress.
Use a throwaway `MUNINN_HOME` for comparisons with no live DB changes.

A jcode session uses **its persisted working directory** and its JCODE_HOME to
resolve `app.Binding`, not the shell's current directory. Client roots stay strict
and instances remain isolated. Generic stdin uses the caller's working directory.
Do not combine multiple clients or instances into one stdin transcript.

## jcode observer wiring

The bundled jcode `docs/HOOKS.md` documents `turn_end` and `session_end` as
**detached fire-and-forget observers**. After comparison and owner approval, add
these commands to the existing `[hooks]` section of the intended jcode instance:

```toml
[hooks]
turn_end = "/absolute/path/to/muninn capture --hook"
session_end = "/absolute/path/to/muninn capture --hook"
```

If a key already has a command, preserve it in an array rather than overwriting:

```toml
turn_end = ["existing-notifier", "/absolute/path/to/muninn capture --hook"]
```

No shell `&` or extra daemon is required: jcode detaches observer processes. They
read `JCODE_HOOK_EVENT`, `JCODE_HOOK_SESSION_ID`, and `JCODE_HOME`. The turn-end
observer extracts after at least `every_turns` unprocessed user turns (12 by
default). On first contact with an existing session, hooks retain only the last
`every_turns * 2` user turns (24 by default), mark older message ids as seen,
and record `backlog_skipped` in the audit. This avoids retrying years of history
on every live turn. Skipped ids remain recoverable with manual `--backfill`.
Session-end flushes the remainder regardless of cadence. Hook output
contains only counts/status in muninn's `data/hook.log`, never memory text.
The complete capture invocation has a 45-second default budget, capped at 120
seconds. Each provider chunk uses the remaining deadline with a persistence
reserve, and completed earlier chunks survive a later timeout. Hook indexing
caps embedding work to one unit per pass. Failures emit exactly one content-free
summary naming `timeout`, `no_key`, `provider_http_STATUS`, `provider_network`,
`parse`, `scope`, `index`, `config`, or `capture`, while returning success to the harness.
Failed chunks remain pending for the next hook. This does not edit or
disable jcode's sidecar.

## Local capture audit

Every capture invocation, including dry runs, skipped intervals and provider
failures, appends a local audit record. `capture_runs` and `capture_actions` live
in muninn's private SQLite index in the data directory, not in `memories.json`
or any project export. SQLite makes append and retention pruning atomic across
concurrent observers. The database and its WAL sidecars are private (0600 on
Unix). Never add the data directory to git, backup sharing or project sync.

```sh
muninn capture log --since 24h
muninn capture log --session SESSION_ID --json
muninn capture log 42                 # one run, including requests and actions
muninn capture stats --since 168h
```

Records include time, harness/session/instance, binding scope, trigger, dry-run
status, processed message ids/roles/content hashes and withheld-safe text, the
exact prepared model request (including system prompt and known-memory sids),
model output, usage/latency, parser rejection reasons, and candidate
category/trust/content/action/reason/target/cosine. A prepared request on a failed
run does not prove that a provider received it. Output matching credential
patterns is withheld from the audit too, even when the provider returns it.
Raw provider output is otherwise retained locally, not sent to a second service.
Audit text is private conversation data even after credential filtering. The
same pattern-matching limitations described above apply.

`extract.audit_days` defaults to 30 (non-positive values also use 30). Expired
runs and their actions are pruned on the next audit write. This is logical
retention, not a promise of forensic erasure from filesystem snapshots/backups.
Dry runs write audit records but never mutate native memories, capture bindings,
or high-water checkpoints. `show` and `list` display memory category. Recall's
shipped SDD row format remains unchanged.

## Storage, dedupe and limits

* Candidates use `fact`, `preference`, `correction`, or `decision`, with `high`
  (user stated), `medium` (observed), or `low` (inferred) trust. The model is asked
  for one self-contained sentence, preferably under 200 characters. Invalid
  lines are dropped independently, and content above 1000 characters is rejected.
* New memories are native `memories.json` entries with `capture:<harness>:<session>`
  origins, a binding-aware batch discriminator and candidate ordinal. Trust and
  replay receipts are retained. No harness memory file is modified.
* Exact text reinforces an existing visible memory regardless of category.
  Semantic reinforcement requires an available embedder and cosine at least
  `dedupe_cosine`. Without embeddings capture conservatively adds non-exact text.
  Do not lower the threshold to recall's much looser relevance threshold.
  The default 0.98 was calibrated on 2026-11-05 using the installed
  `nomic-embed-text` and muninn's no-prefix recipe: 14 labelled challenge pairs
  (1 exact, 4 equivalent, 9 different/contradictory). Equivalent pairs ranged
  0.905622-0.995958. The most similar contradiction (always/never ask before
  deployment) scored 0.945438, making an initial 0.94 unsafe. At 0.98, 3/4
  equivalent pairs reinforce and 0/9 contradictory pairs reinforce. This small
  precision-first calibration is not a universal guarantee for other models.
* Explicit high-trust corrections take precedence over similarity. A superseded
  sid must resolve to an active memory visible and writable in the same Binding.
  Native targets are retired, upstream targets suppressed only in muninn.
* Reinforcing native memories increases strength. Reinforcing read-only upstream
  memories records an event in `capture_reinforcements`, without modifying the
  upstream graph or creating a duplicate copy.
* SQLite keeps per-session high-water counts plus stable processed message ids.
  Snapshot/journal checkpointing cannot replay a message just by moving its
  storage location. A session's first captured binding is pinned: later attempts
  to reuse that session id within an instance across client scopes fail closed,
  rather than re-extracting its old transcript into a different client.
  Generic stdin identities are content plus occurrence ordinal
  within the supplied chunk: use a full growing transcript for consistent
  repeated-line identity across invocations.
* Capture workers serialize through an expiring DB lease. A periodic observer
  skips a busy lease, while a final observer waits within its hard deadline so
  an in-flight periodic request does not silently swallow the final flush.
  Receipts make native
  reinforcement replay safe if a process dies before its DB checkpoint. Native
  writes use the existing atomic-file store. Other mutation commands retain
  their existing concurrency semantics, so do not concurrently rewrite native
  storage with a separate importer while capture is writing.
* Inputs are bounded (256 KiB stdin, 256 MiB snapshot/journal), requests use
  message-aligned 48 KiB chunks, and output is capped at 4096 tokens. Oversized
  individual messages fail closed instead of silently losing their tail. A
  truncated provider output fails the chunk and does not advance its checkpoint.
* Missing final lifecycle events, deleted session files, or process termination
  can defer capture. A later manual `capture` retries pending messages. Capture
  intentionally does not promise exactly-once remote LLM billing after crashes.
