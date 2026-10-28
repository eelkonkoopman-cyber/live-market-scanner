package summarize

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
)

// TapeContext is everything Claude gets to interpret the order flow: the tape
// engine's metric digest plus the surrounding price context, so the read can
// reference levels (VWAP, HOD) and the day's catalyst rather than describing
// numbers in a vacuum.
type TapeContext struct {
	Sym       string
	Digest    string // tape.Engine.Digest() — flow metrics + notable prints
	Last      float64
	VWAP      float64
	HOD       float64
	LOD       float64
	ChgDay    float64
	NewsBrief string // the day's Claude news summary, if one exists
}

const tapeSystemPrompt = `You are reading the time-and-sales tape for an intraday momentum trader who is watching this stock RIGHT NOW. You are given computed order-flow metrics (not raw ticks): volume delta (aggressive buys minus aggressive sells), the percentage of volume hitting the ask vs the bid, trade rate, average trade size, and notable large prints.

Rules:
- Maximum 70 words. No preamble, no restating the raw numbers back.
- Lead with WHO IS IN CONTROL right now (buyers / sellers / balanced) and the evidence in one line.
- Then the notable behavior: sweeps, absorption (aggressive selling that isn't breaking the bid, or buying that isn't lifting the offer), a delta trend that disagrees with price, size stepping in or drying up.
- Reference the levels you were given (VWAP, HOD/LOD) when the flow is happening around one.
- Final line exactly: "Read: Buyers|Sellers|Balanced — <what would invalidate it>".
- If the sample is thin (few trades, short watch window), say so plainly instead of over-reading it. Never invent prints or levels you were not given.`

// ReadTape asks Claude to interpret the current order flow. Uses the same
// backend/semaphore as news summaries, so a tape read and a summary can't
// stampede the API (or spawn two CLIs) at once.
func (s *Service) ReadTape(ctx context.Context, tc TapeContext) (string, error) {
	if strings.TrimSpace(tc.Digest) == "" {
		return "", errors.New("no tape data yet — watch the symbol for a few seconds first")
	}
	be, err := s.pick()
	if err != nil {
		return "", err
	}

	select {
	case s.sem <- struct{}{}:
		defer func() { <-s.sem }()
	case <-ctx.Done():
		return "", ctx.Err()
	}

	var b strings.Builder
	fmt.Fprintf(&b, "Ticker: %s\n", tc.Sym)
	if tc.Last > 0 {
		fmt.Fprintf(&b, "Price: last %.4g", tc.Last)
		if tc.VWAP > 0 {
			fmt.Fprintf(&b, ", VWAP %.4g", tc.VWAP)
		}
		if tc.HOD > 0 {
			fmt.Fprintf(&b, ", HOD %.4g", tc.HOD)
		}
		if tc.LOD > 0 {
			fmt.Fprintf(&b, ", LOD %.4g", tc.LOD)
		}
		fmt.Fprintf(&b, ", day %+.1f%%\n", tc.ChgDay)
	}
	if tc.NewsBrief != "" {
		fmt.Fprintf(&b, "\nToday's catalyst (already summarized):\n%s\n", clip(tc.NewsBrief, 1200))
	}
	fmt.Fprintf(&b, "\nOrder flow:\n%s\n", tc.Digest)
	b.WriteString("\nRead the tape for the trader now.")

	cctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	out, err := be.run(cctx, tapeSystemPrompt, b.String())
	if err != nil {
		return "", fmt.Errorf("tape read (%s): %w", be.name(), err)
	}
	out = strings.TrimSpace(out)
	if out == "" {
		return "", errors.New("tape reader returned empty output")
	}
	return out, nil
}
