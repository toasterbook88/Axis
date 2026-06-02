// Package axismcp is EXPERIMENTAL — read-only MCP diagnostic server.
// It is subordinate to observed state and emits warnings automatically.
//
// # Defense in depth
//
// MCP tool annotations (ToolAnnotation.ReadOnlyHint, DestructiveHint,
// IdempotentHint, OpenWorldHint) are advisory metadata emitted in tools/list
// responses so well-behaved clients can suppress redundant confirmations.
// They are NOT a security boundary.
//
// All write attempts, regardless of how a client sets its hints, pass through
// the AXIS safety blocker (internal/safety.Block) and the execution layer
// (internal/execution). Those layers are independent of MCP and authoritative:
// a client that mis-declares a read-only tool as write-capable (or vice
// versa) cannot weaken the safety gate, and a client that suppresses its
// confirmations on a destructive tool still cannot bypass internal/safety.
//
// In short: client-supplied hints are untrusted metadata. The execution and
// safety layers are the only authoritative source of truth for whether a
// given operation is permitted.
package axismcp
