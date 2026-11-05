package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"strings"

	"github.com/toasterbook88/axis/internal/chat"
)

// Each model round sends one request: the conversation as the server sees
// it, plus the tool definitions. This file builds it, fits it to the context
// budget (budget.go) and recovers once from a context-overflow error.
// See docs/superpowers/specs/2026-11-05-agent-context-budget-design.md.

// compactionGuard is the least share of the usable budget a summary must be
// able to free before compaction runs. Below it, summarizing frees too
// little, and with a large fixed prompt it would run every round.
const compactionGuard = 0.10

// retryShrink is the most of the failed request a retry may keep when the
// error did not state the window.
const retryShrink = 0.9

// roundRequest is what ChatStream is given for one model round.
type roundRequest struct {
	msgs []chat.Message
	defs []chat.ToolDef
}

// buildRequest renders the conversation as it is sent this round.
func (a *Agent) buildRequest(userPrompt string, turnContext []string) roundRequest {
	msgs := chat.ConsolidateMessages(a.conv.Messages())
	// Wire invariant: model chat templates behind tool calling (notably Qwen
	// Jinja templates via LiteLLM) reject payloads with no user-role message.
	// Compaction can delete every original user turn, so if none survived,
	// re-inject this turn's prompt after the leading system messages rather
	// than sending a user-less payload into a template guard.
	if !hasUserRole(msgs) {
		insert := firstNonSystemIndexIn(msgs)
		if insert < 0 {
			insert = len(msgs)
		}
		msgs = append(msgs[:insert], append([]chat.Message{{Role: chat.RoleUser, Content: userPrompt}}, msgs[insert:]...)...)
	}
	msgs = injectTurnContext(msgs, turnContext...)
	// Some servers (LiteLLM) reject a request with any empty non-assistant
	// message. Fill them on the way out so a history that already holds one
	// (an older session, a resumed file) still works.
	msgs = fillEmptyContent(msgs)
	return roundRequest{msgs: msgs, defs: a.tools.Defs()}
}

// messageBytes counts what a message puts on the wire, as the conversation
// estimate does: content plus tool-call names and arguments.
func messageBytes(m chat.Message) int {
	n := len(m.Content)
	for _, tc := range m.ToolCalls {
		n += len(tc.Function.Name) + len(tc.Function.Arguments)
	}
	return n
}

// rawTokens is the chars/4 estimate of the whole request, before the learned
// factor: every message plus the JSON of the tool definitions.
func (r roundRequest) rawTokens() int {
	n := 0
	for _, m := range r.msgs {
		n += messageBytes(m)
	}
	if b, err := json.Marshal(r.defs); err == nil {
		n += len(b)
	}
	return n / 4
}

// estimateWith is the request size in tokens, corrected by the factor k.
func estimateWith(r roundRequest, k float64) int {
	return int(math.Ceil(k * float64(r.rawTokens())))
}

// shouldCompact reports whether LLM compaction runs before this request. It
// runs at 70% of the usable budget, when at least minSummarizable messages
// lie outside the protected tail and they hold at least compactionGuard of
// the budget, not counting an earlier summary. force (overflow recovery)
// needs only one summarizable message other than an earlier summary, so a
// retry never re-summarizes its own summary into an equal-size request. It
// counts only the messages compaction would replace (compactionRange).
func (a *Agent) shouldCompact(e, u int, k float64, force bool) bool {
	_, _, cands := a.compactionRange()
	if force {
		for _, m := range cands {
			if !strings.HasPrefix(m.Content, compactedSummaryPrefix) {
				return true
			}
		}
		return false
	}
	if float64(e) < compactionThreshold*float64(u) || len(cands) < minSummarizable {
		return false
	}
	all, freeable := 0, 0
	for _, m := range cands {
		all += messageBytes(m)
		if !strings.HasPrefix(m.Content, compactedSummaryPrefix) {
			freeable += messageBytes(m)
		}
	}
	// When the request would not fit even with every summarizable message
	// gone, a summary cannot help and each attempt is a wasted round trip.
	if float64(e)-k*float64(all/4) > float64(u) {
		return false
	}
	return k*float64(freeable/4) >= compactionGuard*float64(u)
}

// fixedTokens is the part of req that no compaction or trimming can shorten:
// system messages, tool definitions and the turn context.
func fixedTokens(req roundRequest, turnContext []string, k float64) int {
	n := 0
	for _, m := range req.msgs {
		if m.Role == chat.RoleSystem {
			n += messageBytes(m)
		}
	}
	if b, err := json.Marshal(req.defs); err == nil {
		n += len(b)
	}
	for _, c := range turnContext {
		n += len(c)
	}
	return int(math.Ceil(k * float64(n/4)))
}

// fitRequest builds this round's request and makes it fit the usable budget:
// LLM compaction when shouldCompact says so, then truncating the oldest tool
// results outside the newest 4 messages. Both change the stored
// conversation, so the request is rebuilt and measured again after each.
// What cannot be made to fit is sent anyway, with a one-time warning; the
// server is the authority. bud is the budget state captured with backend.
func (a *Agent) fitRequest(ctx context.Context, backend ChatBackend, bud budgetState, userPrompt string, turnContext []string, force bool) (roundRequest, int) {
	_, u := a.limitsOf(bud)
	k := factorOf(bud)
	req := a.buildRequest(userPrompt, turnContext)
	e := estimateWith(req, k)
	if a.shouldCompact(e, u, k, force) {
		if err := a.compactContext(ctx, backend); err != nil && a.verbose {
			a.emitCompactionSkipped(err)
		}
		req = a.buildRequest(userPrompt, turnContext)
		e = estimateWith(req, k)
	}
	for e > u && a.conv.TruncateOldestToolResult() {
		req = a.buildRequest(userPrompt, turnContext)
		e = estimateWith(req, k)
	}
	if e > u && a.warnOnce() {
		a.emitCompactionSkipped(fmt.Errorf("request is about %d tokens, over the usable budget of %d, after compaction and trimming; the system prompt, tool definitions and turn context hold about %d of it; sending it anyway", e, u, fixedTokens(req, turnContext, k)))
	}
	return req, e
}

// sendRound fits and sends one model request. If the server rejects it as
// longer than its context window, it learns the stated window and size,
// compacts, and retries once, only when the rebuilt request is smaller and
// predicted to fit. Any other error, a second overflow, or a request that
// cannot shrink returns the error as before. bud is the budget state read
// together with backend (backendState): the request is fitted to that
// backend's limits, and a model switch in between teaches nothing.
func (a *Agent) sendRound(ctx context.Context, backend ChatBackend, bud budgetState, userPrompt string, turnContext []string) (chat.Message, error) {
	req, e := a.fitRequest(ctx, backend, bud, userPrompt, turnContext, false)
	resp, err := a.stream(ctx, backend, req, e)
	o, overflow := parseContextOverflow(err)
	if !overflow || !a.learnOverflow(bud.gen, o, req) {
		return resp, err
	}
	_, _, learned := a.backendState()
	if learned.gen != bud.gen {
		return resp, err
	}
	retry, eNew := a.fitRequest(ctx, backend, learned, userPrompt, turnContext, true)
	limit := int(retryShrink * float64(e))
	if o.window > 0 {
		_, limit = a.limitsOf(learned)
	}
	// The raw size must fall too: with an unlearned ratio, an unchanged
	// request can pass the fit check and would be resent as it was.
	if eNew > limit || retry.rawTokens() >= req.rawTokens() {
		return resp, err
	}
	return a.stream(ctx, backend, retry, eNew)
}

// learnOverflow records what an overflow error states, if the backend that
// failed is still the current one. The window lowers the budget; the stated
// request size against the uncorrected estimate gives the factor k, unless
// the ratio is implausible. It reports whether the backend is unchanged.
func (a *Agent) learnOverflow(gen uint64, o contextOverflow, sent roundRequest) bool {
	if gen != a.backendGeneration() {
		return false
	}
	if b, _ := a.budget(); o.window > 0 && o.window <= b {
		a.learnWindow(gen, o.window)
	}
	if raw := sent.rawTokens(); o.total > 0 && raw > 0 && (o.window == 0 || o.total > o.window) {
		ratio := float64(o.total) / float64(raw)
		if !a.learnFactor(gen, ratio) && ratio > estimateFactorMax {
			a.emitCompactionSkipped(fmt.Errorf("the server counted %d tokens for a request estimated at %d; a ratio of %.1f is not learned", o.total, raw, ratio))
		}
	}
	return true
}

// stream sends req and records its size for the footer.
func (a *Agent) stream(ctx context.Context, backend ChatBackend, req roundRequest, e int) (chat.Message, error) {
	a.noteRequest(e)
	cw := NewColorWriter(a.output)
	defer cw.Close()
	return backend.ChatStream(ctx, req.msgs, req.defs, cw)
}
