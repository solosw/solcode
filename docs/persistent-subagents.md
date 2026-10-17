# Persistent subagents

Task remains available only in explicit Ultra mode. Bypass alone does not grant
Task access. No new public tool is introduced; existing Task graphs, dependencies,
fast-model selection, retries and automatic background completion remain supported.

## Create and continue

Children belong to the project working directory, not one parent session. Any
main session using the same working directory and session storage can list,
inspect, resume or archive them. Different project working directories remain
isolated, even when session.dir is shared. Parent agent/session IDs are retained
as run provenance rather than access restrictions.

Create a child with action=spawn:

~~~json
{"action":"spawn","description":"Inspect context","prompt":"Inspect the context builder","fork_context":"working_set","allowed_tools":["View","Grep"]}
~~~

The result includes an opaque, stable agent_id and an independent run_id. Pass the
agent_id in a follow-up Task to append a new prompt to that child's conversation:

~~~json
{"action":"resume","description":"Continue analysis","prompt":"Check the remaining edge cases","agent_id":"agent-id-from-the-result"}
~~~

spawn rejects agent_id; resume requires it on every task. Both actions may call
the model. For compatibility, omitting action or using action=run still infers
creation/continuation from the absence/presence of agent_id, including mixed
legacy task graphs.

Do not substitute task_id for agent_id. Task item IDs still describe dependency
edges in one graph. Each execution has an execution ID; automatic retries share
that execution ID and agent ID, with a fresh run ID and incremented attempt.
Retries do not append the same prompt again. Pre-tool model failures can retry;
a run which has issued a tool call must not retry automatically after failure.

Within a graph, set agent_id separately on each item. Two tasks sharing an agent
must have a dependency edge, or use serial execution. Results from a predecessor
using the same agent are not injected again as prompt text.

## Context inheritance

Inheritance applies only when creating a child. Resuming always restores the
child's own active context; it does not fork the parent's context again.

| fork_context | Inherited context |
| --- | --- |
| none (default) | No parent messages or working set |
| last_n | Last fork_last_n user turns (default 10), including their tool exchanges |
| full | Completed parent conversation |
| working_set | Bounded file/hash/fact/open-question metadata, not tool payloads |

The current unfinished parent tool call and transient mode/skill overlays are not
forked. Immutable, content-addressed fork snapshots are shared by children. A child
stores only its own messages and indexes into the shared snapshot. Overlapping
parent snapshots also share individual message objects, not just identical whole
snapshots. Working-set
restoration marks file versions stale; a hash is never treated as file contents.

The existing local compactor and context budget apply independently to each
child. Compaction changes the active-context indexes, not the archival transcript.
No extra LLM summary request is made. Old tool payloads can be offloaded to the
existing observation store and retrieved with ReadObservation. Inherited masked
observations resolve through the original durable parent-session relationship,
even after a different project session resumes the child. The normal
shared tools/system prefix remains unchanged between new and resumed runs.

## Lifecycle operations

~~~json
{"action":"list"}
{"action":"status","agent_id":"agent-id-from-the-result"}
{"action":"history","agent_id":"agent-id-from-the-result","offset":0,"limit":20}
{"action":"history","agent_id":"agent-id-from-the-result","history_kind":"runs","offset":0,"limit":20}
{"action":"history","agent_id":"agent-id-from-the-result","history_kind":"events","offset":0,"limit":20}
{"action":"wait","agent_id":"agent-id-from-the-result"}
{"action":"cancel","agent_id":"agent-id-from-the-result"}
{"action":"archive","agent_id":"agent-id-from-the-result"}
~~~

list returns all children in the current project working directory across parent
sessions, including archived records. list/status/history/wait/cancel/archive do
not launch model requests.

History is paginated (at most 100 entries per page). Messages, runs and events are
separate streams, so inspecting one page does not repeat the entire audit log.
Ordinary task results are capped at 12,000 Unicode characters; the independent
child transcript retains the full output. Status contains recorded API usage,
including cache creation and cache-read tokens. Run history records the actual
resolved model, attempt, tools ceiling and result.

An idle child can resume. Failed or cancelled children can also receive explicit
follow-ups if their history has no unconfirmed tool calls. Archived children are
readable but cannot resume. Foreign project/workdir scopes, unknown IDs and
busy IDs return errors; they never silently create replacement children.

Cancel targets the current run, and holds the execution lock until its runner
actually exits. Cancellation and waiting operate in the owning process; another
process can inspect history/status, but cannot signal or wait on a live worker it
does not own. There is no cross-process job daemon.

## Persistence and crash recovery

Persistence follows the existing session.enabled and session.persist settings.
When both are enabled, state is stored under session.dir. Its default is the
project-scoped runtime directory outside the source tree:

~~~text
~/.solcode/projects/<project-path-key>/sessions/
~~~

The existing storage layout is unchanged; older child records become accessible
from other sessions of the same project without moving files:

~~~text
subagents/<agent_id>.json       metadata, run/event audit, local messages, context indexes
subagents/<agent_id>.run.lock   OS-owned exclusive run lock (never manually remove it)
subagents/forks/<hash>.json     immutable inherited-context manifest
subagents/forks/messages/      shared content-addressed message objects
observations/<agent_id>/       offloaded tool payloads
~~~

Without persistence, the same reuse API works within the current process only.
Main-session listing does not include the separate child-session namespace.
Session saves use a same-directory temporary file, file sync, atomic replacement
and directory sync on Unix. One process-owned advisory lock covers the entire
run; it is released by the OS after process death. Cancelled-but-still-running
workers remain busy. Idle persistent children drop their in-memory transcripts.

A model response is saved before executing its tool calls. Each tool result is
saved before starting the next call, including partial multi-call exchanges. A
checkpoint failure stops execution and disables automatic retries. On recovery,
old tool calls are never dispatched from stored history. An orphaned running
record is reported as interrupted rather than as an active worker.

If a tool may have taken effect before its result was durably saved, Resume is
blocked. Inspect history and verify actual file/process/external state first.
After reconciliation, explicitly resume with acknowledge_interruption=true and
explain what was verified in the prompt. Missing results are marked unknown, not
fabricated as successful, and old calls are not replayed.

This does not promise exactly-once external side effects: no local session file
can atomically commit an arbitrary shell command or remote API operation.

## Permissions and measurement

Stored history never restores a revoked grant. Every tool still passes the
current executor, permission service, hooks, BatchTool/PTC child checks and
guardrails. A resume may narrow the original allowed_tools ceiling but cannot
implicitly broaden it. Model configuration is resolved from the current host;
an explicit model override still works.

Tests cover project-wide cross-session/cross-restart continuation and listing,
foreign-project isolation, explicit spawn/resume validation, model-free management,
legacy records and inherited observation provenance, process termination after a
mutation, concurrent Resume, cancellation before runner exit, permission revocation,
checkpoint failures, context compaction with preserved audit, fork deduplication,
retry correlation, and Task graph compatibility/background delivery. Usage-counter
tests use deterministic mocked API responses. They are not evidence of a real
provider's cache hit rate or a measured percentage of billed-token savings.
For a live comparison, replay the same multi-stage workload with and without
agent reuse and compare persisted input/cache-read/cache-write/output counters,
model request counts and repeated file reads. No paid-provider benchmark is run
by the offline test suite.
