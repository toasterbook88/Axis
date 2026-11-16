# Delegation brief validation

Date: 2026-11-16.

## Problem

`spawn_subagent` previously accepted only a prompt, without explicit success criteria,
allowed sources, or a boundary. The parent cannot reliably assess a child's result
when those constraints were never stated. Empty child answers already fail separately.

## Non-goals

- Enabling delegation in scopes where `neverTool` already excludes it.
- Granting additional tools, permissions, cluster authority, or nesting depth.
- Replacing missing child output with a parent-generated answer.
- Changing the synchronous or asynchronous task lifecycle.

## Design

Require four non-whitespace string arguments alongside the existing non-empty
`prompt`: `objective`, `output_format`, `sources`, and `boundary`. Advertise all
five in the JSON schema. Validate the brief before constructing or running a child.
Return an error listing missing fields in stable schema order.

Include the four trimmed fields in the child system prompt. These fields explain
the task; they cannot override scope filtering, confirmation, recursion limits,
or the existing no-further-delegation instruction. Preserve the parent backend,
child tool limits, synchronous final-answer validation, and asynchronous task receipts.

This is an intentional contract change: a prompt-only call is rejected. The
tool description and schema must state the new requirements together. The tool
remains prohibited by the existing public scope charter; validation does not enable it.

## Alternatives

- Infer a brief from the prompt: ambiguous and impossible to validate reliably.
- Make the four fields optional: preserves the failure that this change addresses.
- Enable delegation while adding validation: expands authority and is out of scope.

## Research

Repository prior art is `dispatchSubagent` and `buildChildAgent` in
[`internal/agent/subagent.go`](../../../internal/agent/subagent.go), the
[`neverTool` scope check](../../../internal/agent/scope.go), and the already
enforced empty-final-answer rule. JSON Schema `required` alone does not reject whitespace,
so validation must also run in the dispatcher. No performance optimization applies;
four string checks are bounded by the input already decoded for one tool call.

## Validation

- Missing and whitespace-only fields fail before any child backend call.
- A complete brief reaches the child prompt with the stated values.
- Existing synchronous and asynchronous final-answer tests remain green.
- Existing scope/recursion/blocked-session tests remain green.
- Run the hermetic agent package tests and the full repository preflight.

## Review sequencing

The implementation in `ff7fc45a89651c69f6912dfdc860b37cedccdcd9` predates this
specification. Reviews recorded here use their actual dates and hashes. They do
not claim a pre-implementation review; the maintainer must explicitly waive that
missed sequence in PR #648 before merge.

## Review
