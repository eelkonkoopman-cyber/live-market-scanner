package summarize

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/eelkonkoopman/marketscanner/internal/core"
)

// setupReadSystemPrompt is the product. It documents every number the user
// message supplies (in the same snake_case names that message uses), fixes the
// meaning and the DIRECTION of each one — retrace is a share of the pole and
// lower is better, vol_dry below 1.0 is constructive, state 2 means price has
// already cleared the base high — and then spends most of its length forcing
// the model to be willing to say NO. A shortlist that has already passed a
// numeric filter is full of mediocre setups; a reader that finds something nice
// to say about all of them tells the trader nothing they couldn't get from the
// score alone.
const setupReadSystemPrompt = `You are a setup analyst inside a live stock scanner used by a discretionary
swing trader. A deterministic nightly pass has already measured a bull-flag /
tight-consolidation pattern on one ticker's DAILY bars, and that ticker has just
entered a scan filtering on those measurements. You read that ONE setup and say
plainly how good it is.

The shape being measured is the classic pole then base: a sharp advance (the
POLE), then a tight sideways range (the BASE) that holds most of the advance on
drying-up volume. You are given it as numbers:

- score: the scanner's own 0-100 composite of the four ingredients below. A
  starting point, not the answer. Say so when your read disagrees with it.
- state: 0 = no pattern, 1 = COILING (the base is intact, price is still inside
  it), 2 = BREAKING OUT (price has ALREADY traded above base_high today — the
  move is underway, not pending).
- pole_gain_pct: size of the advance into the base, in percent. A PRECONDITION,
  NOT A VIRTUE: big poles fail all the time, and a huge one is not by itself a
  reason to like a setup.
- base_range_pct: base tightness, (base high - base low) / midpoint, percent.
  LOWER IS BETTER. Under ~6% is genuinely coiled; over ~12% is a swing range
  wearing a flag's clothes.
- retrace_pct: the deepest giveback since the run high, as a percent OF THE POLE
  — not of the price. THIS IS THE DISCRIMINATOR, because a 6% dip is nothing
  after a 100% run and everything after a 10% one. LOWER IS BETTER. Under ~20%
  the advance is resting; over ~35% it has largely failed and the "base" is a
  downtrend pausing.
- vol_dry: base average daily volume divided by the pole's average daily volume.
  BELOW 1.0 IS CONSTRUCTIVE — supply drying up, sellers exhausted; 0.5-0.7 is
  the classic flag. ABOVE 1.0 means the base is absorbing real supply
  (distribution) and counts AGAINST the setup.
- base_bars: how many daily sessions the base has held. 3 is the minimum that
  counts, 5-15 is normal. Length is a gate, not a virtue: do not prefer a longer
  base to a shorter one without another reason.
- base_high: the price that defines the breakout level.
- as_of: the trade date the geometry was measured for.

You are also given today's live numbers (last, change on the day, relative
volume, float where known) and the recent daily bars as CSV. Use them to say
where price sits relative to base_high RIGHT NOW and whether today's volume is
confirming or not. Any of these may read "unknown" — then say nothing about it.

RULES:
- GROUND EVERY CLAIM IN THE SUPPLIED NUMBERS and cite them. If you cannot point
  at a number in the input, do not say it.
- INVENT NOTHING. You do not know this company's earnings date, news, catalysts,
  filings, sector, index membership, short interest or analyst views. None of
  that is in the input. Do not mention it, not even to speculate about it, and
  never give a price target.
- Be DESCRIPTIVE, NOT ADVISORY. Describe the shape and its quality. Do not tell
  the trader to buy, sell, size, or where to stop. This is not financial advice.
- SAY WHEN A SETUP IS WEAK, and lead with it. Most symbols that clear a numeric
  filter are still mediocre. If the retrace is deep, the base loose, or volume
  expanding, that is the read — score it low and name it in the first sentence.
  Do not hunt for something nice to say. A scorer that never says no is
  worthless.
- quality is an integer 1-5 and MUST use the whole range:
    1 = broken. The numbers argue against the setup.
    2 = weak. At least one ingredient is clearly bad.
    3 = ordinary. Nothing wrong, nothing special.
    4 = good. Tight, shallow, volume drying up.
    5 = textbook. Every ingredient near its best AND today confirming.
  Reserve 5. If two or more of retrace_pct, base_range_pct and vol_dry are poor,
  quality is at most 2.
- verdict: at most 6 words, the whole read as one phrase — "tight base, volume
  dried up", "deep retrace, advance already failed".
- whats_happening: AT MOST 40 WORDS, one or two plain sentences, scannable at a
  glance. Name the shape and the single number that decides it. No field names
  strung together.
- risks: up to 4 short, concrete, number-backed ways this setup fails. Never
  empty — every setup has at least one.
- invalidation: one sentence naming the price level or condition that would mean
  the setup is done, stated as an OBSERVATION ("losing the 11.40 base low ends
  the consolidation"), not as an instruction.

Respond ONLY with the JSON object matching the provided schema.`

// setupReadSchema constrains the reply, declared the same way
// tapeControlSchema is (explicit required list, additionalProperties: false).
// quality carries minimum/maximum as well — belt and braces alongside the
// clamp in ReadSetup, since a schema bound is advisory enough that a model can
// still hand back a 0 or a 7.
var setupReadSchema = map[string]any{
	"type": "object",
	"properties": map[string]any{
		"quality":         map[string]any{"type": "integer", "minimum": 1, "maximum": 5},
		"verdict":         map[string]any{"type": "string"},
		"whats_happening": map[string]any{"type": "string"},
		"risks":           map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
		"invalidation":    map[string]any{"type": "string"},
	},
	"required":             []string{"quality", "verdict", "whats_happening", "risks", "invalidation"},
	"additionalProperties": false,
}

// ErrSetupReadUnavailable is returned by ReadSetup when the resolved backend
// isn't the Anthropic API — the setup-reader twin of ErrTapeControlUnavailable
// and for the same reason: structured JSON-schema output needs output_config,
// which the local Claude Code CLI backend has no equivalent for. A caller
// should treat it as "no Claude read is possible at all" (a permanent
// condition, unlike a failed call) and keep the deterministic flag numbers as
// the only read, rather than retrying and burning budget.
var ErrSetupReadUnavailable = errors.New("setup read requires the API backend")

// setupReadJSON is the shape of the JSON structured output returns, kept
// separate from core.SetupRead because the model's field names are snake_case
// (they are named literally in setupReadSystemPrompt) while the wire type is
// camelCase.
type setupReadJSON struct {
	Quality        int      `json:"quality"`
	Verdict        string   `json:"verdict"`
	WhatsHappening string   `json:"whats_happening"`
	Risks          []string `json:"risks"`
	Invalidation   string   `json:"invalidation"`
}

const (
	// maxSetupRisks caps a runaway risks array the way ReadTapeControl caps
	// Evidence at 6. Lower here because the prompt asks for at most 4.
	maxSetupRisks = 5
	// maxSetupBars caps how many daily bars are rendered into the prompt.
	// The geometry spans at most poleMaxBars+maxBaseBars = 40 sessions
	// (internal/pattern), so 40 is every bar the numbers were measured over
	// and anything beyond it is tokens spent on history the read cannot use.
	maxSetupBars = 40
	// setupReadTimeout: longer than ReadTapeControl's 30s (a regime shift is
	// spoken mid-session and stale in seconds) but far short of Summarize's 3
	// minutes. A setup read carries ~40 bars and lands in a panel, not an ear.
	setupReadTimeout = 60 * time.Second
)

// setupHeadline renders the deterministic geometry as one line. It is what a
// failed read shows instead of Claude's whats_happening — the same trick
// controlFallback plays with the regime headline, and the point is the same:
// the fallback still says something TRUE, taken from input the caller already
// had, rather than rendering blank.
func setupHeadline(in core.SetupContext) string {
	f := in.Flag
	state := "coiling"
	switch {
	case f.State >= 2:
		state = "breaking out"
	case f.State < 1:
		state = "no pattern"
	}
	return fmt.Sprintf("%s %s: score %.0f, pole +%.1f%%, base %.1f%% over %d bars, retrace %.0f%% of pole, volume %.2fx.",
		in.Sym, state, f.Score, f.PoleGain, f.RangePct, f.Bars, f.Retrace, f.VolDry)
}

// setupFallback is the setup-reader twin of controlFallback: a valid,
// Quality-0 core.SetupRead carrying the reason in Err, for every failure path
// that actually reached the API. Quality 0 is out of the model's 1-5 range on
// purpose, so "this read failed" is unambiguous downstream.
func setupFallback(in core.SetupContext, ts int64, model, errMsg string) core.SetupRead {
	return core.SetupRead{
		Sym:            in.Sym,
		Ts:             ts,
		AsOf:           in.Flag.AsOf,
		Quality:        0,
		Verdict:        "unrated",
		WhatsHappening: setupHeadline(in),
		Model:          model,
		Err:            errMsg,
	}
}

// num renders a core.Row float, mapping the Null sentinel to "unknown" so a
// missing metric never reaches the model as the literal "NaN" (which it would
// read as a number and might well describe).
func num(v float64, prec int) string {
	if core.IsNull(v) {
		return "unknown"
	}
	return strconv.FormatFloat(v, 'f', prec, 64)
}

// buildSetupPrompt renders the user message: the geometry under the exact
// snake_case names setupReadSystemPrompt documents, today's live numbers, then
// the recent daily bars.
//
// The bars go out as CSV with ONE header line rather than a JSON array of
// objects: a JSON array repeats six key names, quotes and braces on every bar,
// which for 40 bars is several hundred wasted tokens per call for zero added
// clarity — and this call runs once per shortlisted symbol, so that cost is
// paid over and over.
func buildSetupPrompt(in core.SetupContext) string {
	f := in.Flag
	var b strings.Builder
	fmt.Fprintf(&b, "Symbol: %s\n\n", in.Sym)

	b.WriteString("Setup geometry (measured on daily bars):\n")
	fmt.Fprintf(&b, "score: %.1f\n", f.Score)
	fmt.Fprintf(&b, "state: %.0f\n", f.State)
	fmt.Fprintf(&b, "pole_gain_pct: %.2f\n", f.PoleGain)
	fmt.Fprintf(&b, "base_range_pct: %.2f\n", f.RangePct)
	fmt.Fprintf(&b, "retrace_pct: %.2f\n", f.Retrace)
	fmt.Fprintf(&b, "vol_dry: %.2f\n", f.VolDry)
	fmt.Fprintf(&b, "base_bars: %d\n", f.Bars)
	fmt.Fprintf(&b, "base_high: %.4f\n", f.BaseHigh)
	fmt.Fprintf(&b, "as_of: %s\n", f.AsOf)

	b.WriteString("\nLive now:\n")
	fmt.Fprintf(&b, "last: %s\n", num(in.Last, 4))
	fmt.Fprintf(&b, "chg_day_pct: %s\n", num(in.ChgDay, 2))
	fmt.Fprintf(&b, "rvol: %s\n", num(in.Rvol, 2))
	if in.Float != nil {
		fmt.Fprintf(&b, "float_shares: %d\n", *in.Float)
	} else {
		b.WriteString("float_shares: unknown\n")
	}

	bars := in.Bars
	if len(bars) > maxSetupBars {
		bars = bars[len(bars)-maxSetupBars:] // newest maxSetupBars, oldest first
	}
	if len(bars) == 0 {
		b.WriteString("\nNo daily bars available.\n")
	} else {
		b.WriteString("\nRecent daily bars, oldest first (CSV):\ndate,open,high,low,close,volume\n")
		for _, bar := range bars {
			fmt.Fprintf(&b, "%s,%.4f,%.4f,%.4f,%.4f,%d\n", bar.Date, bar.O, bar.H, bar.L, bar.C, bar.V)
		}
	}

	b.WriteString("\nRead this setup.")
	return b.String()
}

// ReadSetup asks Claude to score and explain one bull-flag setup a scan just
// matched — the flag-scanner twin of ReadTapeControl, and modeled on it in
// every respect. It uses structured JSON-schema output so the result feeds
// straight into a wire message, and therefore requires the API backend
// specifically (see ErrSetupReadUnavailable).
//
// It reproduces ReadTapeControl's three error paths — network/API failure, a
// refusal stop reason, and unparseable JSON — each returning a VALID,
// Quality-0 core.SetupRead with Err set and a nil Go error, so a failed read
// still renders. The distinction matters for the caller's budget accounting:
// a non-nil error means no API call was billed, while a nil error with Err set
// means one was.
func (s *Service) ReadSetup(ctx context.Context, in core.SetupContext) (core.SetupRead, error) {
	be, err := s.pick()
	if err != nil {
		return core.SetupRead{}, err
	}
	ab, ok := be.(*apiBackend)
	if !ok {
		return core.SetupRead{}, ErrSetupReadUnavailable
	}

	select {
	case s.sem <- struct{}{}:
		defer func() { <-s.sem }()
	case <-ctx.Done():
		return core.SetupRead{}, ctx.Err()
	}

	user := buildSetupPrompt(in)

	ts := time.Now().UnixMilli()
	cctx, cancel := context.WithTimeout(ctx, setupReadTimeout)
	defer cancel()
	text, refused, err := ab.runStructured(cctx, setupReadSystemPrompt, user, setupReadSchema)
	if err != nil {
		return setupFallback(in, ts, s.model, fmt.Sprintf("Claude request failed: %v", err)), nil
	}
	if refused {
		return setupFallback(in, ts, s.model, "Claude declined to read this setup."), nil
	}

	var data setupReadJSON
	if err := json.Unmarshal([]byte(text), &data); err != nil {
		return setupFallback(in, ts, s.model, "Claude returned unparseable output."), nil
	}

	// Clamp into 1-5: the schema's minimum/maximum is not a guarantee, and 0 is
	// reserved for failed reads (setupFallback) — a model returning 0 must not
	// be mistaken downstream for a call that never landed.
	quality := data.Quality
	switch {
	case quality < 1:
		quality = 1
	case quality > 5:
		quality = 5
	}
	verdict := data.Verdict
	if strings.TrimSpace(verdict) == "" {
		verdict = "unrated"
	}
	risks := data.Risks
	if len(risks) > maxSetupRisks {
		risks = risks[:maxSetupRisks]
	}

	return core.SetupRead{
		Sym:            in.Sym,
		Ts:             ts,
		AsOf:           in.Flag.AsOf,
		Quality:        quality,
		Verdict:        verdict,
		WhatsHappening: data.WhatsHappening,
		Risks:          risks,
		Invalidation:   data.Invalidation,
		Model:          s.model,
	}, nil
}
