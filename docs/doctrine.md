# AXIS Doctrine

## Purpose

AXIS exists to give humans and models one accurate, structured view of the
compute available across a small cluster.

Its primary job is to:

1. collect node facts
2. assemble a trustworthy `ClusterSnapshot`
3. make deterministic advisory placement decisions from that snapshot

If AXIS does those three things well, it is useful.

## What AXIS Is

AXIS is:

- a single-binary Go CLI
- local-first and operator-invoked
- agentless by default
- snapshot-oriented
- deterministic in its placement logic
- optimized for verifiable output over hidden behavior

The core product is the fact plane:

`config -> discovery -> facts -> snapshot -> placement`

That pipeline is the center of the project.

## What AXIS Is Not

AXIS is not, by default:

- a daemon-first control plane
- a general-purpose orchestrator
- a background scheduler
- a hidden agent runtime
- a replacement for SSH, Docker, Tailscale, or Ollama

AXIS may integrate with those tools, but it should not lose its identity by
trying to become all of them.

## Product Boundary

AXIS has two layers:

### Layer 1: Fact Plane

This is the primary product.

Responsibilities:

- load cluster config
- discover nodes
- collect local and remote facts
- represent uncertainty honestly
- emit `ClusterSnapshot`
- provide deterministic advisory placement

This layer must remain:

- correct
- minimal
- well-tested
- explainable

### Layer 2: Optional Execution Surfaces

Examples:

- chat
- task execution
- scripts
- learned skills
- MCP tools
- stateful placement memory

These are useful only if they stay subordinate to Layer 1.

They must not weaken the trustworthiness of the fact plane.

## Core Principles

### 1. Live Reality Beats Narrative

The code and command behavior are the source of truth.

Docs, white papers, and roadmap language must follow verified behavior, not lead
it.

### 2. Accuracy Beats Cleverness

A partial but honest snapshot is better than a polished lie.

AXIS should prefer explicit degraded status, warnings, and failure reasoning over
guessing.

### 3. Advisory Before Automatic

Placement should be trustworthy before execution becomes ambitious.

If AXIS executes anything, that layer must be more explicit and more reversible
than the advisory layer beneath it.

### 4. Minimalism Is a Product Constraint

"Small binary, no daemon, no server" is not just implementation trivia.
It is part of the value proposition.

New features should be judged partly on whether they preserve that shape.

### 5. Typed Contracts Beat Hidden Conventions

If a feature depends on hidden temp files, shell folklore, or undocumented side
channels, it is probably not mature enough yet.

Important system contracts should live in typed models or explicit CLI surfaces.

### 6. Cluster RAM Is a Shared Resource

AXIS should treat cluster memory as a pooled capacity that must be balanced
across nodes, not as a series of isolated per-node numbers.

That means:

- placement should prefer healthy headroom across the cluster, not just the
  single highest instantaneous free-RAM value
- stateful RAM accounting should represent soft claims against a node's share of
  the cluster memory pool
- "reserved" RAM is useful only if it improves balancing and prevents one node
  from being overloaded while others sit idle

The point of RAM balancing is to help the cluster share memory pressure across
nodes.

## Decision Rules

When evaluating a change, ask these questions in order:

1. Does this improve fact quality, snapshot quality, or placement quality?
2. Does it preserve the single-binary, local-first shape?
3. Does it make behavior more explicit, deterministic, or testable?
4. Does it reduce operator confusion?
5. Does it introduce hidden state or hidden execution?

If a change fails the first three, it is probably out of bounds.

For RAM-aware changes, also ask:

6. Does this improve how the cluster shares and balances memory across nodes?

## In-Bounds Work

Examples of work that fits AXIS well:

- better local and remote fact collection
- stronger SSH transport behavior
- better snapshot assembly and warnings
- clearer placement reasoning
- better cluster-level RAM balancing and headroom accounting
- improved tool detection
- safer, read-only MCP exposure of snapshot and diagnostics
- better docs that match live behavior
- tests around discovery, facts, placement, and transport

## Out-of-Bounds Work Unless Deliberately Reframed

Examples of work that should be treated as a major product decision:

- implicit task execution
- autonomous background coordination
- hidden model downloads
- daemon-first architecture
- large stateful scheduling systems
- broad workflow automation that outruns the fact plane

These are not forbidden forever, but they should not be smuggled in as "small
features."

## Execution Doctrine

If AXIS executes tasks:

- execution must be explicit
- the selected node and reasoning must be visible first
- destructive actions must require clear opt-in
- state updates must reflect reality, not wishful bookkeeping
- failure modes must be specific and actionable

Execution is an extension of the fact plane, not a substitute for it.

If execution updates placement memory, those updates should help AXIS balance
cluster RAM, not just record that "something ran somewhere."

## Documentation Doctrine

Every operator-facing feature should have:

- one truthful command surface
- one short runbook or usage note
- one live verification command

Do not describe a phase as active unless the code and the CLI actually support
it.

Do not describe a feature as stable if the tests and runtime behavior disagree.

## The Standard

The best version of AXIS is boring in the right places:

- snapshots are reliable
- warnings are honest
- placement is deterministic
- integrations are explicit
- operators know what will happen before it happens

That is the bar.
