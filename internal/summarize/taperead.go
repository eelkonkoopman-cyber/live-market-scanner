package summarize

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/eelkonkoopman/marketscanner/internal/core"
)

// tapeControlSystemPrompt is the Go mirror of claude_reader.py's SYSTEM_PROMPT,
// verbatim — a tuned prompt, not something to paraphrase. It documents every
// field of the core.TapeSnapshot JSON payload (see tapeSnapshotForClaude
// below for why book.* is re-tagged snake_case to match this prompt's literal
// field names) and mandates the same descriptive-not-advisory tone, a
// whats_happening capped at 25 words (it is read aloud via TTS), and detail
// evidence for on-screen display only.
const tapeControlSystemPrompt = `You are a tape-reading assistant for a discretionary trader. You read the time
& sales (the "tape") for one instrument and judge who is in control of the price
action right now: aggressive buyers, aggressive sellers, or a balanced/unclear
market.

You are given a compact numeric snapshot computed locally from every print. Key
fields:
- last_price, bid, ask, spread, vwap, session_high, session_low.
- cvd_session: cumulative volume delta = running sum of buyer-initiated volume
  minus seller-initiated volume for the session. Rising CVD = buyers lifting
  offers; falling = sellers hitting bids.
- delta_10s / delta_60s: signed volume (buy - sell) over the last 10s / 60s.
- buy_vol_60s / sell_vol_60s: aggressive volume by side over the last 60s.
- prints_per_sec_now vs prints_per_sec_baseline: tape speed vs its recent norm.
- block_threshold / median_print_size: what counts as an unusually large print.
- control_score: local buy/sell imbalance over 60s, from -1 (sellers) to +1 (buyers).
- recent_prints: the last ~20 prints with price, size, side, and a block flag.
- bid_size / ask_size: size resting at the inside bid and offer.
- book: order-book behaviour over the last 30s —
    bid_absorb / ask_absorb: times the level was HIT by aggressive trades and
      then REFRESHED. This is the classic "refreshing bid / refreshing offer":
      a large passive buyer (or seller) sitting at that price soaking up flow.
      Strong signal — much stronger than size merely being added.
    bid_absorbed_vol / ask_absorbed_vol: volume traded into the CURRENT inside
      level while it held. bid_refills / ask_refills: how many times it came back.
    bid_add / ask_add: size added to a quote nobody is hitting — weak signal,
      do not call this absorption.
    bid_pull / ask_pull: times size DISAPPEARED with no trade — liquidity being
      pulled, i.e. that side backing away rather than being hit.
    bid_up/bid_down/ask_up/ask_down: the inside quote stepping toward or away.
    inside_imbalance: -1 (offer heavy) .. +1 (bid heavy) at the inside.
    depth_imbalance: same across the visible Level 2 ladder (0 if no depth data).
- book_bids / book_asks: Level 2 ladder levels, best first. MAY BE EMPTY when the
  account lacks depth entitlement for the venue — if so, judge the book only from
  bid_size/ask_size and the refresh/pull counts, and do not invent depth detail.
- trigger / headline: what local detector flagged this moment.

Interpret the microstructure. Look for: buyers lifting offers vs sellers hitting
bids, absorption (heavy one-sided volume that fails to move price — someone large
passively soaking it up), exhaustion, sweeps, and CVD/price divergences (e.g.
price making highs while CVD fades).

RULES:
- Be descriptive, not advisory. Describe who appears in control and why. Do NOT
  give buy/sell recommendations, price targets, or personalized financial advice.
- Ground every claim in the numbers provided. If the picture is genuinely mixed,
  say "balanced" with lower confidence rather than forcing a call.
- ` + "`whats_happening`" + ` IS READ ALOUD to the trader mid-session, so it must be
  SHORT: at most 25 words, one sentence, plain spoken English. Name the level and
  the behaviour ("bid at 1.82 absorbing repeated selling and refreshing"). No
  field names, no numbers strung together, nothing the ear can't follow.
- Put the supporting detail in ` + "`evidence`" + ` instead — that is displayed on screen,
  never spoken, so it can be precise and cite the exact figures.

Respond ONLY with the JSON object matching the provided schema.`

// tapeControlSchema is the Go mirror of claude_reader.py's SCHEMA, verbatim
// (including the required-fields list and additionalProperties: false).
var tapeControlSchema = map[string]any{
	"type": "object",
	"properties": map[string]any{
		"control":         map[string]any{"type": "string", "enum": []string{"buyers", "sellers", "balanced"}},
		"confidence":      map[string]any{"type": "number"},
		"whats_happening": map[string]any{"type": "string"},
		"evidence":        map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
		"watch_for":       map[string]any{"type": "string"},
	},
	"required":             []string{"control", "confidence", "whats_happening", "evidence", "watch_for"},
	"additionalProperties": false,
}

// bookStatsSnake re-tags core.BookStats' fields snake_case for the outbound
// Claude payload only. core.BookStats itself stays camelCase (it is also
// embedded in TapeStat, the dashboard wire message, which is camelCase
// throughout — see internal/core/tape.go's doc comment on TapeSnapshot); this
// shim exists so the JSON actually sent to the model lines up with the
// literal book.bid_absorb-style field names tapeControlSystemPrompt names.
type bookStatsSnake struct {
	BidAbsorb       int     `json:"bid_absorb"`
	AskAbsorb       int     `json:"ask_absorb"`
	BidAdd          int     `json:"bid_add"`
	AskAdd          int     `json:"ask_add"`
	BidPull         int     `json:"bid_pull"`
	AskPull         int     `json:"ask_pull"`
	BidUp           int     `json:"bid_up"`
	BidDown         int     `json:"bid_down"`
	AskUp           int     `json:"ask_up"`
	AskDown         int     `json:"ask_down"`
	BidRefills      int     `json:"bid_refills"`
	AskRefills      int     `json:"ask_refills"`
	BidAbsorbedVol  float64 `json:"bid_absorbed_vol"`
	AskAbsorbedVol  float64 `json:"ask_absorbed_vol"`
	BidDepth        float64 `json:"bid_depth"`
	AskDepth        float64 `json:"ask_depth"`
	DepthImbalance  float64 `json:"depth_imbalance"`
	InsideImbalance float64 `json:"inside_imbalance"`
}

func newBookStatsSnake(b core.BookStats) bookStatsSnake {
	return bookStatsSnake{
		BidAbsorb:       b.BidAbsorb,
		AskAbsorb:       b.AskAbsorb,
		BidAdd:          b.BidAdd,
		AskAdd:          b.AskAdd,
		BidPull:         b.BidPull,
		AskPull:         b.AskPull,
		BidUp:           b.BidUp,
		BidDown:         b.BidDown,
		AskUp:           b.AskUp,
		AskDown:         b.AskDown,
		BidRefills:      b.BidRefills,
		AskRefills:      b.AskRefills,
		BidAbsorbedVol:  b.BidAbsorbedVol,
		AskAbsorbedVol:  b.AskAbsorbedVol,
		BidDepth:        b.BidDepth,
		AskDepth:        b.AskDepth,
		DepthImbalance:  b.DepthImbalance,
		InsideImbalance: b.InsideImbalance,
	}
}

// tapeSnapshotForClaude marshals exactly like core.TapeSnapshot except for
// Book: the outer Book field here (bookStatsSnake) sits at shallower struct
// depth than the one promoted from the embedded TapeSnapshot, so
// encoding/json prefers it — see bookStatsSnake's doc comment.
type tapeSnapshotForClaude struct {
	core.TapeSnapshot
	Book bookStatsSnake `json:"book"`
}

// ControlRead is the Go mirror of claude_reader.py's ClaudeRead: the
// structured "who is in control" verdict a regime shift gets back from
// Claude. Ts is unix millis (this repo's convention — see core.RegimeEvent,
// core.TapeCardMsg — rather than Python's float unix seconds). Untagged: like
// core.RegimeEvent, this is an internal handoff type, not itself a wire
// message (a later orchestration stage turns it into a core.TapeCardMsg).
type ControlRead struct {
	Ts             int64
	Trigger        string
	Control        string // "buyers" | "sellers" | "balanced"
	Confidence     float64
	WhatsHappening string
	Evidence       []string
	WatchFor       string
	Model          string
	Err            string // non-empty if the read failed / was refused
}

// ErrTapeControlUnavailable is returned by ReadTapeControl when the resolved
// backend isn't the Anthropic API. Structured JSON-schema output needs
// output_config, which the local Claude Code CLI backend has no equivalent
// for — Python's app never had this fork, since it only ever spoke to the API
// directly. A later orchestration stage should treat this the same as
// Python's has_claude=false: skip the Claude card and keep the local verdict.
var ErrTapeControlUnavailable = errors.New("tape control read requires the API backend")

// controlReadJSON is the shape of the JSON Claude's structured output returns
// — the Go mirror of the dict local_verdict()/ClaudeReader.read() picks apart
// with .get() and defensive coercion in Python.
type controlReadJSON struct {
	Control        string   `json:"control"`
	Confidence     float64  `json:"confidence"`
	WhatsHappening string   `json:"whats_happening"`
	Evidence       []string `json:"evidence"`
	WatchFor       string   `json:"watch_for"`
}

// controlFallback is the Go mirror of claude_reader.py's ClaudeReader._error:
// a valid, "balanced"/0-confidence ControlRead with WhatsHappening falling
// back to the headline and Err carrying the reason, for every failure path
// that reaches the API (as opposed to ErrTapeControlUnavailable, which means
// the API was never reached at all).
func controlFallback(ts int64, trigger, headline, model, errMsg string) ControlRead {
	return ControlRead{
		Ts:             ts,
		Trigger:        trigger,
		Control:        "balanced",
		Confidence:     0,
		WhatsHappening: headline,
		Model:          model,
		Err:            errMsg,
	}
}

// ReadTapeControl asks Claude who is in control of the tape right now, for a
// regime shift the local detector just flagged — the Go mirror of
// claude_reader.py's ClaudeReader.read(). Unlike ReadTape's free-text digest
// read above, this uses structured JSON-schema output so the result feeds
// directly into a tape card; it therefore requires the API backend
// specifically (see ErrTapeControlUnavailable) and only apiBackend implements
// it. Reproduces all three of Python's error paths — network/API failure,
// a refusal stop reason, and unparseable JSON — each still returning a valid,
// "balanced"/0-confidence ControlRead rather than a bare error.
func (s *Service) ReadTapeControl(ctx context.Context, snap core.TapeSnapshot, headline string) (ControlRead, error) {
	be, err := s.pick()
	if err != nil {
		return ControlRead{}, err
	}
	ab, ok := be.(*apiBackend)
	if !ok {
		return ControlRead{}, ErrTapeControlUnavailable
	}

	select {
	case s.sem <- struct{}{}:
		defer func() { <-s.sem }()
	case <-ctx.Done():
		return ControlRead{}, ctx.Err()
	}

	payload := tapeSnapshotForClaude{TapeSnapshot: snap, Book: newBookStatsSnake(snap.Book)}
	snapJSON, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		return ControlRead{}, fmt.Errorf("marshal tape snapshot: %w", err)
	}
	user := fmt.Sprintf(
		"Regime shift flagged: %s\n\nSnapshot (JSON):\n%s\n\nWho is in control right now?",
		headline, snapJSON,
	)

	ts := time.Now().UnixMilli()
	// Python: self.client.with_options(timeout=30.0) — regime shifts need a
	// fast turnaround, unlike the multi-minute budget ReadTape/Summarize allow.
	cctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	text, refused, err := ab.runStructured(cctx, tapeControlSystemPrompt, user, tapeControlSchema)
	if err != nil {
		return controlFallback(ts, snap.Trigger, headline, s.model, fmt.Sprintf("Claude request failed: %v", err)), nil
	}
	if refused {
		return controlFallback(ts, snap.Trigger, headline, s.model, "Claude declined to read this snapshot."), nil
	}

	var data controlReadJSON
	if err := json.Unmarshal([]byte(text), &data); err != nil {
		return controlFallback(ts, snap.Trigger, headline, s.model, "Claude returned unparseable output."), nil
	}

	conf := data.Confidence
	switch {
	case conf < 0:
		conf = 0
	case conf > 1:
		conf = 1
	}
	control := data.Control
	if control == "" {
		control = "balanced"
	}
	evidence := data.Evidence
	if len(evidence) > 6 {
		evidence = evidence[:6]
	}

	return ControlRead{
		Ts:             ts,
		Trigger:        snap.Trigger,
		Control:        control,
		Confidence:     conf,
		WhatsHappening: data.WhatsHappening,
		Evidence:       evidence,
		WatchFor:       data.WatchFor,
		Model:          s.model,
	}, nil
}
