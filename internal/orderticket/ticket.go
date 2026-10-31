// Package orderticket is the Order Ticket: a small always-on-top window that
// follows the ticker shown in TWS and sends ONE kind of order, on the user's
// LIVE (or paper) TWS account, only after a confirm click:
//
//   - BUY:  BUY LMT floor(position$ / 9EMA) shares at the 1m 9 EMA (tick-rounded
//     down), DAY, outsideRth, with an attached child TAKE PROFIT SELL LMT at
//     fill avg x 1.10 (rounded up), GTC outsideRth.
//   - SELL: SHORT - SELL LMT at the 9 EMA (rounded up), same sizing; child BUY
//     LMT at fill avg x 0.90 (rounded down).
//   - No stop loss (the user's choice).
//
// The entry FOLLOWS the 9 EMA: after each closed 1-minute bar the entry limit is
// modified (same order id) to the new EMA while it is unfilled; the child TP is
// re-priced with it until the first fill and then to the fill average. IB sizes
// an attached child to the parent's filled quantity itself.
//
// The child is attached with IB's parentId/transmit pattern: the parent is sent
// with Transmit=false, the child with ParentID=parent and Transmit=true, so IB
// only activates the TP once the entry fills.
//
// Safety model (see the package tests):
//   - Its own client id (default 80), own socket, own port setting (default live
//     TWS 7496). Starts DISCONNECTED every process; only the user's Connect click
//     opens the socket.
//   - Nothing is transmitted without Prepare -> Confirm (a token from Prepare,
//     expires after 30 s); Confirm re-checks the symbol against TWS's and the
//     EMA's freshness at that moment, and the max position $ cap.
//   - Margin-aware: Prepare first sends a what-if placeOrder (whatIf=true, a
//     fresh order id that is never recorded, adopted, followed or modified;
//     audited only as an id-less "whatif" note) and shows the margin it needs
//     against the account's AvailableFunds (reqAccountSummary, subscribed
//     while connected). Confirm is refused above the margin max share count;
//     if the check times out (5 s) Confirm stays allowed with a warning.
//   - It only ever modifies or cancels orders it created itself: their ids and
//     perm ids are recorded in an append-only JSONL audit (<db dir>/orderticket/
//     audit.jsonl); on reconnect reqOpenOrders is reconciled against it and every
//     other order is ignored (not shown, never touched).
//
// internal/twslink (client 77) stays order-free and the auto-trader (client 78)
// is untouched; this package shares no state with either.
package orderticket

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
	"sync"
	"time"
	_ "time/tzdata"

	"github.com/eelkonkoopman/marketscanner/internal/core"
	"github.com/eelkonkoopman/marketscanner/internal/logx"
)

// EClient is what the ticket needs from a TWS client (TWSClient; tests use a fake).
type EClient interface {
	Connect(host string, port int, clientID int64) error
	Disconnect() error
	IsConnected() bool
	ReqManagedAccts()
	ReqCurrentTime()
	ReqOpenOrders()
	PlaceOrder(orderID int64, contract *Contract, order *Order)
	CancelOrder(orderID int64)
	SubscribeToGroupEvents(reqID int64, groupID int)
	UnsubscribeFromGroupEvents(reqID int64)
	ReqContractDetails(reqID int64, contract *Contract)
	ReqAccountSummary(reqID int64, group, tags string)
	CancelAccountSummary(reqID int64)
	ReqMktData(reqID int64, contract *Contract, genericTicks string)
	CancelMktData(reqID int64)
}

var _ EClient = (*TWSClient)(nil)

// RealClient builds the raw-socket TWS client (twsclient.go).
func RealClient(w Wrapper) EClient { return NewTWSClient(w) }

const (
	MetaKey        = "orderticket.settings.v1"
	refPrefix      = "oticket:"
	groupReqID     = 8001
	cdReqBase      = 8100
	confirmTTL     = 30 * time.Second
	readyTimeout   = 15 * time.Second
	beatEvery      = 5 * time.Second
	beatTimeout    = 8 * time.Second
	retryEvery     = 10 * time.Second
	reseedEvery    = 5 * time.Minute
	legacyTPPct    = 10.0 // orders recorded before the TP selector existed
	maxOrderShares = 1_000_000
	acctReqID      = 8002
	// $LEDGER:USD adds the USD ledger rows (currency "USD"), among them
	// ExchangeRate = base-currency units per 1 USD: the FX rate % risk uses.
	acctTags      = "AvailableFunds,ExcessLiquidity,BuyingPower,NetLiquidation,$LEDGER:USD"
	whatIfTimeout = 5 * time.Second
	whatIfRefPfx  = "oticket-whatif:" // deliberately NOT refPrefix: never adoptable
)

// Settings are persisted in schema_meta (MetaKey).
type Settings struct {
	Host           string  `json:"host"`
	Port           int     `json:"port"`
	ClientID       int     `json:"clientId"`
	MaxPositionUSD float64 `json:"maxPositionUSD"`
	TPLowPct       float64 `json:"tpLowPct"`   // take-profit preset 1 (%)
	TPHighPct      float64 `json:"tpHighPct"`  // take-profit preset 2 (%), the default
	StopPct        float64 `json:"stopPct"`    // soft stop distance from the 9EMA (%)
	StopFollow     bool    `json:"stopFollow"` // stop follows the 9EMA (raise-only / lower-only)
	// StaleMin: orders may use a 9EMA whose newest closed 1m bar ended up to
	// this many minutes ago (thin pre-market names close few bars).
	StaleMin int `json:"staleMin"`
	// EMALive: the 9EMA folds in the forming 1m candle's current price (like
	// TWS's chart EMA); default false = closed candles only.
	EMALive bool `json:"emaLive"`
	// Risk presets (USD lost if the soft stop is hit): the share count is
	// risk / (limit - planned stop).
	RiskLow  float64 `json:"riskLow"`
	RiskMid  float64 `json:"riskMid"`
	RiskHigh float64 `json:"riskHigh"`
	// FollowSec: a working entry is re-priced to the 9EMA every FollowSec
	// seconds (2-60; 60 ~ the old once-per-closed-bar follow).
	FollowSec int `json:"followSec"`
	// EMALiveSet: the user chose the 9EMA mode explicitly (SetEMALive); the
	// rev-2 migration only switches installs without an explicit choice to live.
	EMALiveSet bool `json:"emaLiveSet"`
	// Preset for the next orders: "custom" (these settings) or "power"
	// (preset.go). Trail5m / EntryTimeoutMin are Custom's per-order defaults.
	Preset          string `json:"preset"`
	Trail5m         bool   `json:"trail5m"`
	EntryTimeoutMin int    `json:"entryTimeoutMin"`
	// Auto-cancel on a fast drop into the 9EMA (autocancel.go). Stored as
	// "off" so a zero value means ON.
	DropCancelOff bool `json:"dropCancelOff"`
	// Breakeven: move the soft stop to entry (+ BEOffsetPct) once the price
	// is BEPct in favour. Stored as "off" so a zero value means ON.
	BEOff       bool    `json:"beOff"`
	BEPct       float64 `json:"bePct"`       // 0.5-50 %
	BEOffsetPct float64 `json:"beOffsetPct"` // 0-5 %
	// RiskMode: "$" (RiskLow/Mid/High are USD) or "%" (RiskPct* of the
	// account's NetLiquidation, converted to USD with TWS's USD rate).
	// "" = "$" (settings saved before the mode existed).
	RiskMode    string  `json:"riskMode"`
	RiskPctLow  float64 `json:"riskPctLow"`  // 0.1-10 %
	RiskPctMid  float64 `json:"riskPctMid"`  // 0.1-10 %
	RiskPctHigh float64 `json:"riskPctHigh"` // 0.1-10 %
	DropPct     float64 `json:"dropPct"`     // 0.5-20 %
	DropSec     int     `json:"dropSec"`     // 10-300 s
	NearPct     float64 `json:"nearPct"`     // 0-5 %
	Rev         int     `json:"rev"`
}

// settingsRev is the current settings revision (2: live-candle 9EMA default).
const settingsRev = 3 // 2: live-candle 9EMA default; 3: drop-cancel nearPct default

// migrateSettings upgrades settings saved by an older build, once.
func migrateSettings(s Settings) (Settings, bool) {
	if s.Rev >= settingsRev {
		return s, false
	}
	if s.Rev < 2 && !s.EMALiveSet {
		s.EMALive = true
	}
	if s.Rev < 3 && s.NearPct == 0 {
		s.NearPct = 1
	}
	s.Rev = settingsRev
	return s, true
}

// withTPDefaults fills presets missing from settings saved before they existed.
func (s Settings) withTPDefaults() Settings {
	if s.TPLowPct == 0 {
		s.TPLowPct = 1
	}
	if s.TPHighPct == 0 {
		s.TPHighPct = 10
	}
	if s.StopPct == 0 {
		s.StopPct = 1
	}
	if s.StaleMin == 0 {
		s.StaleMin = 15
	}
	if s.RiskLow == 0 {
		s.RiskLow = 25
	}
	if s.FollowSec == 0 {
		s.FollowSec = 5
	}
	if s.DropPct == 0 {
		s.DropPct = 3
	}
	if s.BEPct == 0 {
		s.BEPct = 5
	}
	if s.Preset == "" {
		s.Preset = presetCustom
	}
	if s.DropSec == 0 {
		s.DropSec = 60
	}
	if s.RiskMid == 0 {
		s.RiskMid = 50
	}
	if s.RiskHigh == 0 {
		s.RiskHigh = 100
	}
	if s.RiskPctLow == 0 {
		s.RiskPctLow = 1
	}
	if s.RiskPctMid == 0 {
		s.RiskPctMid = 2
	}
	if s.RiskPctHigh == 0 {
		s.RiskPctHigh = 3
	}
	if s.RiskMode == "" {
		s.RiskMode = riskModeUSD
	}
	return s
}

// DefaultSettings: live TWS on this machine, client 80, $5,000 cap.
func DefaultSettings() Settings {
	return Settings{Host: "127.0.0.1", Port: 7496, ClientID: 80, MaxPositionUSD: 5000, TPLowPct: 1, TPHighPct: 10, StopPct: 1, StaleMin: 15, RiskLow: 25, RiskMid: 50, RiskHigh: 100,
		FollowSec: 5, EMALive: true, Rev: settingsRev,
		DropPct: 3, DropSec: 60, NearPct: 1, BEPct: 5, Preset: presetCustom,
		RiskMode: riskModeUSD, RiskPctLow: 1, RiskPctMid: 2, RiskPctHigh: 3}
}

func (s Settings) validate() error {
	if s.Host == "" {
		return errors.New("host is empty")
	}
	if s.Port < 1 || s.Port > 65535 {
		return fmt.Errorf("bad port %d", s.Port)
	}
	switch s.ClientID {
	case 0:
		return errors.New("client id 0 is IB's master client (it would bind orders typed into TWS); pick another")
	case 77, 78:
		return fmt.Errorf("client id %d belongs to the scanner's TWS link / auto-trader", s.ClientID)
	}
	if s.ClientID < 0 {
		return fmt.Errorf("bad client id %d", s.ClientID)
	}
	if !(s.MaxPositionUSD > 0) || s.MaxPositionUSD > 1_000_000 {
		return fmt.Errorf("bad max position $ %v", s.MaxPositionUSD)
	}
	for _, v := range []float64{s.TPLowPct, s.TPHighPct} {
		if !(v >= 0.1) || v > 50 {
			return fmt.Errorf("take profit %% must be 0.1-50, got %v", v)
		}
	}
	for _, v := range []float64{s.RiskLow, s.RiskMid, s.RiskHigh} {
		if !(v >= 1) || v > 5000 {
			return fmt.Errorf("risk presets must be $1-5000, got %v", v)
		}
	}
	for _, v := range []float64{s.RiskPctLow, s.RiskPctMid, s.RiskPctHigh} {
		if v == 0 { // unset (validated before withTPDefaults on old saves)
			continue
		}
		if !(v >= 0.1) || v > 10 {
			return fmt.Errorf("risk %% presets must be 0.1-10, got %v", v)
		}
	}
	switch s.RiskMode {
	case "", riskModeUSD, riskModePct:
	default:
		return fmt.Errorf("bad risk mode %q", s.RiskMode)
	}
	if !(s.BEPct >= 0.5) || s.BEPct > 50 {
		return fmt.Errorf("breakeven trigger %% must be 0.5-50, got %v", s.BEPct)
	}
	if !(s.BEOffsetPct >= 0) || s.BEOffsetPct > 5 {
		return fmt.Errorf("breakeven offset %% must be 0-5, got %v", s.BEOffsetPct)
	}
	if !(s.DropPct >= 0.5) || s.DropPct > 20 {
		return fmt.Errorf("drop %% must be 0.5-20, got %v", s.DropPct)
	}
	if s.DropSec < 10 || s.DropSec > 300 {
		return fmt.Errorf("drop window must be 10-300 s, got %d", s.DropSec)
	}
	if !(s.NearPct >= 0) || s.NearPct > 5 {
		return fmt.Errorf("near-9EMA %% must be 0-5, got %v", s.NearPct)
	}
	if s.FollowSec < 2 || s.FollowSec > 60 {
		return fmt.Errorf("follow interval must be 2-60 s, got %d", s.FollowSec)
	}
	if s.StaleMin < 1 || s.StaleMin > 120 {
		return fmt.Errorf("9EMA max age must be 1-120 min, got %d", s.StaleMin)
	}
	if !(s.StopPct >= 0.1) || s.StopPct > 20 {
		return fmt.Errorf("stop %% must be 0.1-20, got %v", s.StopPct)
	}
	return nil
}

// Config wires the ticket into the process.
type Config struct {
	Dir     string // audit directory (created on first write)
	Audit   Audit  // replaces the file audit (tests)
	GetMeta func(ctx context.Context, key string) (string, error)
	SetMeta func(ctx context.Context, key, value string) error
	Group   int // the TWS colored link group (same as twslink's; default 1)
	// Seed fetches 1-minute bars (previous day + today) for a symbol.
	Seed    func(ctx context.Context, sym string) ([]core.Bar, error)
	Factory func(w Wrapper) EClient
	Now     func() time.Time
	// OnTracked is told the symbols the ticket needs live aggregates for.
	OnTracked func(syms []string)
	// Shortable reads the scanner's IBKR short-stock store: the symbol's row,
	// whether it is listed, and the file's own unix timestamp (0 = not loaded).
	Shortable func(sym string) (core.ShortRef, bool, int64)
	// PowerSetup reads sym's latest Power Setup today (flaglog): whether it was
	// graded A+, its base range % and its bar time. nil = none (State.PsGrade nil).
	PowerSetup func(sym string) (aplus bool, baseRange float64, barMs int64, ok bool)
	// LatestVwapBull reads sym's newest 9EMA VWAP bull cross today (flaglog):
	// the cross candle's START ms. nil = the VWAP X preset is unavailable.
	LatestVwapBull func(sym string) (barStartMs int64, ok bool)
	// LatestVwapBear: the same for the 9EMA VWAP BEAR cross (VWAP X down, a short).
	LatestVwapBear func(sym string) (barStartMs int64, ok bool)
	// WhatIfTimeout bounds the margin check in Prepare (default 5 s).
	WhatIfTimeout time.Duration
}

// MarginCheck is the what-if result shown on the confirm line.
//
//	Status: ok | over (qty > MaxShares: Confirm refused) | unavailable (no
//	answer / no funds yet: Confirm allowed with a warning)
type MarginCheck struct {
	Status      string  `json:"status"`
	Needed      float64 `json:"needed"`   // max(init, maint) margin change for Qty
	PerShare    float64 `json:"perShare"` // Needed / Qty
	Avail       float64 `json:"avail"`    // AvailableFunds
	HaveAvail   bool    `json:"haveAvail"`
	MaxShares   int64   `json:"maxShares"` // min(margin max, max position $ / limit)
	InitChange  float64 `json:"initChange"`
	MaintChange float64 `json:"maintChange"`
	Commission  float64 `json:"commission"`
	Warning     string  `json:"warning"` // IB warningText
	Note        string  `json:"note"`    // why unavailable
	Text        string  `json:"text"`
}

// Funds are the account-summary values for the ticket's account.
type Funds struct {
	Account     string  `json:"account"`
	Avail       float64 `json:"avail"`
	Excess      float64 `json:"excess"`
	BuyingPower float64 `json:"buyingPower"`
	NetLiq      float64 `json:"netLiq"`
	HaveAvail   bool    `json:"haveAvail"`
	Currency    string  `json:"currency"` // account base currency, e.g. GBP
	// USD view of NetLiq for % risk: USDRate = base units per 1 USD as TWS
	// reports it (ledger ExchangeRate, or 1 for a USD account). HaveUSD false
	// = no rate (or no NetLiq) yet: % risk is unavailable, USDNote says why.
	USDRate   float64 `json:"usdRate"`
	NetLiqUSD float64 `json:"netLiqUsd"`
	HaveUSD   bool    `json:"haveUsd"`
	USDNote   string  `json:"usdNote,omitempty"`
}

// usdView fills f's USD fields. rate is TWS's base-per-USD rate (0 = none).
// A USD-based account needs no rate; anything else without one stays
// unavailable: a rate is never guessed.
func (f *Funds) usdView(netLiq float64, haveNetLiq bool, rate float64) {
	if strings.EqualFold(f.Currency, "USD") {
		rate = 1
	}
	switch {
	case !haveNetLiq || !(netLiq > 0):
		f.USDNote = "no NetLiquidation from TWS yet"
	case !(rate > 0) || math.IsInf(rate, 0):
		f.USDNote = "no USD→" + f.Currency + " rate from TWS yet; % risk off"
	default:
		f.USDRate, f.NetLiqUSD, f.HaveUSD = rate, netLiq/rate, true
	}
}

type whatIfRes struct {
	st  *OrderState
	err string
}

// Pending is the order waiting for the user's confirm click.
type Pending struct {
	Token string  `json:"token"`
	Side  string  `json:"side"` // BUY | SELL (short)
	Kind  string  `json:"kind"` // "" (9EMA order) | "vwapx" (vwapx.go)
	Sym   string  `json:"sym"`
	Qty   int64   `json:"qty"`
	Limit float64 `json:"limit"`
	TP    float64 `json:"tp"`
	EMA   float64 `json:"ema"`
	Text  string  `json:"text"`
	At    int64   `json:"at"`
	TPPct float64 `json:"tpPct"` // the take-profit percent chosen for this order
	Warn  string  `json:"warn"`  // marketable now (amber, warning only)
	// preset (preset.go)
	Preset          string  `json:"preset"`
	TPR             float64 `json:"tpR"` // TP as an R multiple (0 = % TP)
	Trail5m         bool    `json:"trail5m"`
	DropOff         bool    `json:"dropOff"`
	EntryTimeoutMin int     `json:"entryTimeoutMin"`
	ShortNote       string  `json:"shortNote"` // short: " · shortable 150K (fee 12.5%)"
	// risk sizing: RiskUSD requested (0 = shares / legacy position $),
	// PlannedStop the soft-stop level if filled at Limit now, Risk the $ lost
	// at that stop for Qty, Bound the cap that trimmed Qty ("" = none).
	RiskUSD     float64 `json:"riskUsd"`
	PlannedStop float64 `json:"plannedStop"`
	Risk        float64 `json:"risk"`
	Bound       string  `json:"bound"`
	// soft stop for this order: StopOff = placed with its stop disabled
	StopOff bool    `json:"stopOff"`
	StopPct float64 `json:"stopPct"`
	// vwapx: the cross candle's start (ms); PlannedStop is the REAL IB stop
	CrossMs int64 `json:"crossMs"`
	// Margin is the what-if check for exactly this Qty/Limit.
	Margin *MarginCheck `json:"margin"`
}

// OrderView is one working (or recently finished) ticket order.
type OrderView struct {
	ID          int64   `json:"id"`
	ChildID     int64   `json:"childId"`
	Sym         string  `json:"sym"`
	Side        string  `json:"side"`
	Qty         int64   `json:"qty"`
	Limit       float64 `json:"limit"`
	Filled      int64   `json:"filled"`
	Avg         float64 `json:"avg"`
	TP          float64 `json:"tp"`
	Status      string  `json:"status"`
	ChildStatus string  `json:"childStatus"`
	ChildFilled int64   `json:"childFilled"`
	Following   bool    `json:"following"`
	Note        string  `json:"note"`
	Active      bool    `json:"active"`
	StopLevel   float64 `json:"stopLevel"`
	StopState   string  `json:"stopState"` // "" | armed | triggered | exiting | done | disarmed
	StopOn      bool    `json:"stopOn"`
	StopBE      bool    `json:"stopBe"`      // breakeven latched
	CanBE       bool    `json:"canBe"`       // the row's "→ BE" button
	PresetLabel string  `json:"presetLabel"` // "PS · SL x · TP 3R y · 5m trail"
	StopLabel   string  `json:"stopLabel"`   // "SL 2.41 (soft)"
	FollowLabel string  `json:"followLabel"` // "follows 9EMA live · every 5 s"
	ExitID      int64   `json:"exitId"`
	ExitQty     int64   `json:"exitQty"`
	ExitFilled  int64   `json:"exitFilled"`
	// vwapx: the real IB stop child (its own status, like the TP child)
	Kind     string  `json:"kind"`
	SLID     int64   `json:"slId"`
	CanStop  bool    `json:"canStop"`
	SL       float64 `json:"sl"`
	SLStatus string  `json:"slStatus"`
	SLFilled int64   `json:"slFilled"`
}

// State is the whole picture the pane renders.
type State struct {
	Conn      string      `json:"conn"` // disconnected | connecting | connected | lost | error
	ConnNote  string      `json:"connNote"`
	Addr      string      `json:"addr"`
	ClientID  int         `json:"clientId"`
	Account   string      `json:"account"`
	Live      bool        `json:"live"`
	Sym       string      `json:"sym"`
	SymSource string      `json:"symSource"` // TWS | scanner | ""
	TWSSym    string      `json:"twsSym"`
	Quote     Quote       `json:"quote"`
	Settings  Settings    `json:"settings"`
	Pending   *Pending    `json:"pending"`
	Orders    []OrderView `json:"orders"`
	Note      string      `json:"note"`
	Now       int64       `json:"now"`
	Funds     *Funds      `json:"funds"`
	Preview   *Preview    `json:"preview"` // margin preview for Sym (nil until one arrives)
	// CLOSE / CANCEL buttons for Sym (ticket orders only)
	CloseQty    int64      `json:"closeQty"` // shares CLOSE would exit (0 = disabled)
	EMAOK       bool       `json:"emaOk"`    // BUY/SELL allowed: 9EMA age <= StaleMin
	EMANote     string     `json:"emaNote"`  // "loading 9EMA" while the bars are incomplete
	Currency    string     `json:"currency"`
	Short       *ShortInfo `json:"short"`       // short availability for Sym (nil = no store)
	Sizing      *Sizing    `json:"sizing"`      // what BUY would size now (pane line)    // account base currency (AvailableFunds, margins)
	CancelCount int        `json:"cancelCount"` // working entries CANCEL would cancel
	BECount     int        `json:"beCount"`     // orders "SL → ENTRY" can move (ticket orders only)
	// StopUnwatched: an armed soft stop while not connected (red banner).
	StopUnwatched bool   `json:"stopUnwatched"`
	StopWarn      string `json:"stopWarn"`
	// StopAlert: a TP fill that could not be confirmed; the stop stays armed (red).
	StopAlert string `json:"stopAlert"`
	// PsGrade: Sym's latest Power Setup today (nil = none / not wired). The pane
	// sizes an A+ one at Mult x the selected risk while the Power Setup preset is
	// active, shown on the risk row and overridable; the server sizes whatever
	// risk the pane sends.
	PsGrade *PsGrade `json:"psGrade"`
	// Vwapx: the VWAP X preset for Sym (vwapx.go); always present
	Vwapx *Vwapx `json:"vwapx"`
	// VwapxBear: the short mirror (VWAP X down); always present
	VwapxBear *Vwapx `json:"vwapxBear"`
}

// PsGrade is the symbol's latest Power Setup grade (core.PsIsAplus).
type PsGrade struct {
	Aplus     bool    `json:"aplus"`
	BaseRange float64 `json:"baseRange"` // % (0 when not A+)
	BarMs     int64   `json:"barMs"`
	Mult      float64 `json:"mult"` // the risk multiplier: core.PsAplusRiskMult for A+, else 1
}

type ord struct {
	ID, ChildID         int64
	PermID, ChildPermID int64
	Sym, Side           string
	Qty                 int64
	Limit, TP           float64
	Filled, Remaining   int64
	Avg                 float64
	Status, ChildStatus string
	ChildFilled         int64
	Following           bool
	Note                string
	LastFollowBar       int64
	TPPct               float64 // this order's take-profit percent (0 = legacy 10%)
	EMALive             bool    // this order follows the live-candle 9EMA (mode at placement)
	lastFollowAt        time.Time
	modAt               time.Time // last entry modify sent; cleared by its openOrder echo
	dropHits            int       // fast-drop guard debounce (autocancel.go)
	// preset (preset.go)
	Preset        string
	TPR           float64
	Trail5m       bool
	DropOff       bool
	EntryDeadline int64 // ms; 0 = none, -1 = done
	FillAt        int64 // ms of the first fill
	trailHits     int
	dropFirst     time.Time
	stop                // soft stop (stop.go)
	FollowUntil   int64 // ms
	PlacedAt      int64
	tpGuard            // TP-fill confirmation (tpguard.go)
	tpVerify           // TP limit verified against TWS's echo (tpverify.go)
	tpFillSent bool    // the TP was re-sent at the entry's fill average
	cancelled     bool // the user asked to cancel
	// VWAP X (vwapx.go): Kind "vwapx" = entry + TP child + REAL stop child at IB
	Kind     string
	SLID     int64   // the IB stop child
	SLPermID int64   // its perm id
	SLStatus string  // its status
	SLFilled int64   // shares the stop sold
	SLPx     float64 // stop trigger
	CrossMs  int64
}

func finished(st string) bool {
	switch st {
	case "Filled", "Cancelled", "ApiCancelled", "Inactive":
		return true
	}
	return false
}

func (o *ord) parentActive() bool {
	return !finished(o.Status) && !(o.Qty > 0 && o.Filled >= o.Qty)
}
func (o *ord) childActive() bool { return o.ChildID != 0 && !finished(o.ChildStatus) }

// Ticket is the order ticket engine.
type Ticket struct {
	cfg  Config
	book *book

	mu       sync.Mutex
	settings Settings
	audit    Audit
	known    map[int64]auditRec // ids this tool created (from the audit)

	// connection
	want      bool // the user wants a session (Connect clicked, not Disconnect)
	gen       int64
	client    EClient
	conn      string
	connNote  string
	account   string
	accounts  []string
	nextID    int64
	haveNext  bool
	ready     chan struct{}
	readyDone bool
	beatSent  time.Time
	beatAns   time.Time
	retryAt   time.Time

	// symbol
	twsSym      string
	twsConID    int64
	fallbackSym string
	conSym      map[int64]string
	cdReq       map[int64]int64
	nextCD      int64

	pending *Pending
	orders  map[int64]*ord // by parent id
	byChild map[int64]*ord
	bySL    map[int64]*ord // VWAP X stop child id -> order
	note    string
	tracked string
	seeding map[string]bool
	tokSeq  int64

	funds    map[string]map[string]float64 // account -> tag -> value (this session)
	whatIfs  map[int64]chan whatIfRes      // what-if order ids awaiting their reply
	wiIDs    map[int64]bool                // every what-if id of this process (late errors are dropped)
	fundsCur map[string]string             // account -> currency of AvailableFunds
	lastSym  string                        // symbol shown at the last retrack (note clears on change)

	byExit      map[int64]*ord // soft-stop exit order id -> order
	tape        map[string][]tapePt
	stopRecheck bool // check armed stops against the current price (after connect)

	// margin preview (preview.go)
	pvBusy   bool
	pvPos    float64
	pvShares int64
	pvRisk   float64
	md       mdSub // live shortable-shares subscription (mktdata.go)
	mdSeq    int64

	// soft-stop price lines + last price per symbol (stopsrc.go)
	stopMD    map[string]*stopSub
	stopMDIDs map[int64]string
	stopMDSeq int64
	pxSeen    map[string]time.Time
	pvMark   map[string]previewMark
	previews map[string]*Preview
}

// New builds a DISCONNECTED ticket: settings loaded, audit replayed.
func New(cfg Config) (*Ticket, error) {
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.Factory == nil {
		cfg.Factory = RealClient
	}
	if cfg.Group <= 0 || cfg.Group > 7 {
		cfg.Group = 1
	}
	t := &Ticket{
		cfg: cfg, book: newBook(), settings: DefaultSettings(), conn: "disconnected",
		known: map[int64]auditRec{}, conSym: map[int64]string{}, cdReq: map[int64]int64{}, nextCD: cdReqBase,
		orders: map[int64]*ord{}, byChild: map[int64]*ord{}, bySL: map[int64]*ord{}, seeding: map[string]bool{},
		funds: map[string]map[string]float64{}, whatIfs: map[int64]chan whatIfRes{},
		pvMark: map[string]previewMark{}, previews: map[string]*Preview{}, byExit: map[int64]*ord{}, tape: map[string][]tapePt{},
		wiIDs: map[int64]bool{}, fundsCur: map[string]string{},
	}
	if t.cfg.WhatIfTimeout <= 0 {
		t.cfg.WhatIfTimeout = whatIfTimeout
	}
	if cfg.GetMeta != nil {
		if v, err := cfg.GetMeta(context.Background(), MetaKey); err == nil && v != "" {
			var s Settings
			if json.Unmarshal([]byte(v), &s) == nil && s.withTPDefaults().validate() == nil {
				s = s.withTPDefaults()
				var migrated bool
				if s, migrated = migrateSettings(s); migrated && cfg.SetMeta != nil {
					b, _ := json.Marshal(s)
					_ = cfg.SetMeta(context.Background(), MetaKey, string(b))
				}
				t.settings = s
			}
		}
	}
	t.audit = cfg.Audit
	if t.audit == nil {
		t.audit = &fileAudit{dir: cfg.Dir}
	}
	recs, err := t.audit.Load()
	if err != nil {
		return nil, fmt.Errorf("order ticket audit: %w", err)
	}
	for _, r := range recs {
		if r.ID == 0 || r.Ev == "whatif" { // what-if notes never confer ownership
			continue
		}
		k := t.known[r.ID]
		if k.ID == 0 {
			k = r
		}
		if r.PermID != 0 {
			k.PermID = r.PermID
		}
		if r.TPPct > 0 && k.TPPct == 0 {
			k.TPPct = r.TPPct
		}
		if r.Ev == "stop" { // latest stop record wins
			k.StopLevel, k.StopPct, k.StopFollow, k.StopOff, k.StopState = r.StopLevel, r.StopPct, r.StopFollow, r.StopOff, r.StopState
			k.StopBE, k.BEOff, k.BEPct, k.BEOffsetPct = r.StopBE, r.BEOff, r.BEPct, r.BEOffsetPct
			if r.FillAt > 0 {
				k.FillAt = r.FillAt
			}
		}
		t.known[r.ID] = k
	}
	return t, nil
}

func (t *Ticket) now() time.Time { return t.cfg.Now() }

// run executes client calls collected under the lock, after it is released
// (the library may call the wrapper synchronously from inside a request).
func run(acts []func()) {
	for _, a := range acts {
		a()
	}
}

// ─── prices ──────────────────────────────────────────────────────────────

// snapPrice rounds to the US-equity tick: $0.0001 under $1, $0.01 from $1.
func snapPrice(p float64, up bool) float64 {
	tick := 0.0001
	if p >= 1 {
		tick = 0.01
	}
	n := math.Round(p/tick*1e6) / 1e6
	if up {
		n = math.Ceil(n)
	} else {
		n = math.Floor(n)
	}
	out := n * tick
	if out >= 1 {
		return math.Round(out*100) / 100
	}
	return math.Round(out*10000) / 10000
}

// entryPrice: a BUY rests at the EMA rounded DOWN, a short at the EMA rounded UP.
func entryPrice(side string, ema float64) float64 { return snapPrice(ema, side != "BUY") }

// tpPrice: long TP = base x (1 + pct%) rounded up; short TP = base x (1 - pct%)
// rounded down.
func tpPrice(side string, base, pct float64) float64 {
	if side == "BUY" {
		return snapPrice(base*(1+pct/100), true)
	}
	return snapPrice(base*(1-pct/100), false)
}

// pctOrLegacy: an order recorded without a TP percent used the old fixed 10%.
func pctOrLegacy(p float64) float64 {
	if p > 0 {
		return p
	}
	return legacyTPPct
}

func fmtPct(p float64) string { return strconv.FormatFloat(p, 'f', -1, 64) + "%" }

// sizeQty: floor(position$ / ema), then trimmed so qty x limit stays within the cap.
func sizeQty(positionUSD, ema, limit, cap float64) int64 {
	if !(ema > 0) || !(limit > 0) || !(positionUSD > 0) {
		return 0
	}
	q := int64(math.Floor(positionUSD/ema + 1e-9))
	for q > 0 && float64(q)*limit > cap+1e-9 {
		q--
	}
	if q > maxOrderShares {
		q = 0
	}
	return q
}

func fmtPx(p float64) string {
	if p < 1 {
		return strconv.FormatFloat(p, 'f', 4, 64)
	}
	return strconv.FormatFloat(p, 'f', 2, 64)
}

func fmtQty(q int64) string {
	s := strconv.FormatInt(q, 10)
	var b strings.Builder
	for i, c := range s {
		if i > 0 && (len(s)-i)%3 == 0 {
			b.WriteByte(',')
		}
		b.WriteRune(c)
	}
	return b.String()
}

func confirmText(p Pending) string {
	if p.Kind == kindVwapx {
		return vwapxText(p)
	}
	verb := "BUY"
	if p.Side == "SELL" {
		verb = "SHORT"
	}
	sign := "+"
	if p.Side == "SELL" {
		sign = "-"
	}
	s := fmt.Sprintf("%s %s %s @ %s (9EMA) ~$%s, TP %s%s %s", verb, fmtQty(p.Qty), p.Sym, fmtPx(p.Limit), fmtQty(int64(math.Round(float64(p.Qty)*p.Limit))), sign, fmtPct(pctOrLegacy(p.TPPct)), fmtPx(p.TP))
	if p.TPR > 0 {
		s = fmt.Sprintf("%s %s %s @ %s (9EMA) ~$%s, TP %sR %s", verb, fmtQty(p.Qty), p.Sym, fmtPx(p.Limit), fmtQty(int64(math.Round(float64(p.Qty)*p.Limit))), trimFloat(p.TPR), fmtPx(p.TP))
	}
	if p.Preset == presetPower {
		s = "POWER SETUP: " + s
	}
	if p.Preset == presetNews {
		s = "NEWS FLAG: " + s
	}
	if p.PlannedStop > 0 && (p.RiskUSD > 0 || p.Risk > 0) {
		s += fmt.Sprintf(", risk $%s (stop %s)", fmtQty(int64(math.Round(p.Risk))), fmtPx(p.PlannedStop))
		if p.Bound != "" {
			s += " [" + p.Bound + "]"
		}
		if p.StopOff {
			s += " (SL off — risk is notional)"
		}
	}
	if p.StopOff {
		s += ", no SL"
	} else {
		dir := "under"
		if p.Side == "SELL" {
			dir = "over"
		}
		s += fmt.Sprintf(", SL %s %s 9EMA (soft)", fmtPct(p.StopPct), dir)
	}
	s += p.ShortNote
	if p.Trail5m {
		s += " · 5m trail"
	}
	if p.EntryTimeoutMin > 0 {
		s += fmt.Sprintf(" · cancel if unfilled %dm", p.EntryTimeoutMin)
	}
	return s
}

// emaUsableLocked: an EMA exists and its newest closed bar ended at most
// StaleMin minutes ago.
func (t *Ticket) emaUsableLocked(q Quote) bool {
	if ok, _ := t.book.ready(q.Sym); !ok {
		return false
	}
	return q.EMA > 0 && q.AgeSec <= int64(t.settings.StaleMin)*60
}

// quoteLocked reads the book in the configured EMA mode (closed candles, or
// incl. the live candle).
func (t *Ticket) quoteLocked(sym string, now time.Time) Quote {
	return t.book.quoteMode(sym, now, t.settings.EMALive)
}

// freshenBook re-seeds sym synchronously (<= 2 s) when its bars have a hole
// before the live feed (REST lags the newest minutes). Called before Prepare
// and Confirm compute the 9EMA; never under t.mu.
func (t *Ticket) freshenBook(sym string) {
	if sym == "" || t.cfg.Seed == nil {
		return
	}
	if ok, _ := t.book.ready(sym); ok {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if bars, err := t.cfg.Seed(ctx, sym); err == nil {
		t.book.seed(sym, bars, t.now())
	}
}

func (t *Ticket) curSym() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	s, _ := t.currentSymLocked()
	return s
}

func tickOf(p float64) float64 {
	if p < 1 {
		return 0.0001
	}
	return 0.01
}

// marketableWarn: a BUY limit at/above the last trade (price below the 9EMA)
// or a SELL limit at/below it fills at market now.
func marketableWarn(side string, limit, last float64) string {
	if !(last > 0) {
		return ""
	}
	if side == "BUY" && limit >= last {
		return "price is below the 9EMA — fills at market now"
	}
	if side == "SELL" && limit <= last {
		return "price is above the 9EMA — fills at market now"
	}
	return ""
}

func (t *Ticket) staleMsgLocked(q Quote) string {
	if ok, why := t.book.ready(q.Sym); !ok {
		return why + " — try again"
	}
	if q.EMA <= 0 {
		return "9EMA missing or stale (not enough closed 1m bars)"
	}
	return fmt.Sprintf("9EMA stale: newest closed 1m bar is %dm old (max %dm, see Set)", q.AgeSec/60, t.settings.StaleMin)
}

// ─── orders on the wire ──────────────────────────────────────────────────

func contractFor(sym string) *Contract {
	c := &Contract{}
	c.Symbol = strings.ReplaceAll(sym, ".", " ") // IB writes class shares with a space (BRK B)
	c.SecType = "STK"
	c.Exchange = "SMART"
	c.Currency = "USD"
	return c
}

func (t *Ticket) entryOrder(o *ord, transmit bool) *Order {
	x := &Order{}
	x.Action = o.Side
	x.OrderType = "LMT"
	x.TotalQuantity = Qty(o.Qty)
	x.LmtPrice = o.Limit
	x.TIF = "DAY"
	x.OutsideRTH = true
	x.OrderRef = refPrefix + o.Sym
	x.Account = t.account
	x.Transmit = transmit
	return x
}

func (t *Ticket) tpOrder(o *ord) *Order {
	if o.Kind == kindVwapx { // a re-send of the bracket's TP (tpverify): keeps its shape
		return t.vwapxTP(o, true)
	}
	x := &Order{}
	if o.Side == "BUY" {
		x.Action = "SELL"
	} else {
		x.Action = "BUY"
	}
	x.OrderType = "LMT"
	x.TotalQuantity = Qty(o.Qty)
	x.LmtPrice = o.TP
	x.TIF = "GTC"
	x.OutsideRTH = true
	x.OrderRef = refPrefix + o.Sym + ":tp"
	x.Account = t.account
	x.ParentID = o.ID
	x.Transmit = true
	return x
}

// ─── user actions ────────────────────────────────────────────────────────

// SetSettings validates and persists the settings; refused while connected.
func (t *Ticket) SetSettings(ctx context.Context, s Settings) error {
	if s.Host == "" {
		s.Host = "127.0.0.1"
	}
	t.mu.Lock()
	cur := t.settings
	t.mu.Unlock()
	s.EMALive, s.EMALiveSet, s.Rev = cur.EMALive, cur.EMALiveSet, cur.Rev // set by their own actions
	if s.Preset == "" {                                                   // the connection form never sends the preset fields
		s.Preset, s.Trail5m, s.EntryTimeoutMin = cur.Preset, cur.Trail5m, cur.EntryTimeoutMin
	}
	if s.BEPct == 0 { // form without the breakeven fields
		s.BEOff, s.BEPct, s.BEOffsetPct = cur.BEOff, cur.BEPct, cur.BEOffsetPct
	}
	if s.NearPct == 0 && s.DropPct == 0 { // form without the drop-cancel fields
		s.DropCancelOff, s.DropPct, s.DropSec, s.NearPct = cur.DropCancelOff, cur.DropPct, cur.DropSec, cur.NearPct
	}
	if s.FollowSec == 0 {
		s.FollowSec = cur.FollowSec
	}
	if s.RiskMode == "" { // form without the % risk fields
		s.RiskMode, s.RiskPctLow, s.RiskPctMid, s.RiskPctHigh = cur.RiskMode, cur.RiskPctLow, cur.RiskPctMid, cur.RiskPctHigh
	}
	s = s.withTPDefaults()
	if err := s.validate(); err != nil {
		return err
	}
	t.mu.Lock()
	if t.want || t.conn == "connected" || t.conn == "connecting" {
		t.mu.Unlock()
		return errors.New("disconnect before changing the connection settings")
	}
	t.settings = s
	t.mu.Unlock()
	if t.cfg.SetMeta != nil {
		b, _ := json.Marshal(s)
		return t.cfg.SetMeta(ctx, MetaKey, string(b))
	}
	return nil
}

// SetMaxPosition changes only the cap (allowed while connected).
func (t *Ticket) SetMaxPosition(ctx context.Context, v float64) error {
	t.mu.Lock()
	s := t.settings
	s.MaxPositionUSD = v
	if err := s.validate(); err != nil {
		t.mu.Unlock()
		return err
	}
	t.settings = s
	t.mu.Unlock()
	if t.cfg.SetMeta != nil {
		b, _ := json.Marshal(s)
		return t.cfg.SetMeta(ctx, MetaKey, string(b))
	}
	return nil
}

func (t *Ticket) saveSettings(ctx context.Context, s Settings) error {
	if t.cfg.SetMeta != nil {
		b, _ := json.Marshal(s)
		return t.cfg.SetMeta(ctx, MetaKey, string(b))
	}
	return nil
}

// SetTPPresets changes the two take-profit presets (allowed while connected;
// working orders keep their own percent).
func (t *Ticket) SetTPPresets(ctx context.Context, low, high float64) error {
	t.mu.Lock()
	s := t.settings
	s.TPLowPct, s.TPHighPct = low, high
	if err := s.validate(); err != nil {
		t.mu.Unlock()
		return err
	}
	t.settings = s
	t.mu.Unlock()
	if t.cfg.SetMeta != nil {
		b, _ := json.Marshal(s)
		return t.cfg.SetMeta(ctx, MetaKey, string(b))
	}
	return nil
}

// SetRiskPresets sets the three risk levels (USD, 1-5000).
func (t *Ticket) SetRiskPresets(ctx context.Context, low, mid, high float64) error {
	t.mu.Lock()
	s := t.settings
	s.RiskLow, s.RiskMid, s.RiskHigh = low, mid, high
	if err := s.validate(); err != nil {
		t.mu.Unlock()
		return err
	}
	t.settings = s
	t.mu.Unlock()
	return t.saveSettings(ctx, s)
}

// SetRiskPctPresets sets the three % risk levels (0.1-10 % of NetLiquidation).
func (t *Ticket) SetRiskPctPresets(ctx context.Context, low, mid, high float64) error {
	for _, v := range []float64{low, mid, high} {
		if !(v >= 0.1) || v > 10 {
			return fmt.Errorf("risk %% presets must be 0.1-10, got %v", v)
		}
	}
	t.mu.Lock()
	s := t.settings
	s.RiskPctLow, s.RiskPctMid, s.RiskPctHigh = low, mid, high
	if err := s.validate(); err != nil {
		t.mu.Unlock()
		return err
	}
	t.settings = s
	t.mu.Unlock()
	return t.saveSettings(ctx, s)
}

// SetRiskMode picks "$" or "%" risk buttons.
func (t *Ticket) SetRiskMode(ctx context.Context, mode string) error {
	if mode != riskModeUSD && mode != riskModePct {
		return fmt.Errorf("bad risk mode %q", mode)
	}
	t.mu.Lock()
	s := t.settings
	s.RiskMode = mode
	t.settings = s
	t.mu.Unlock()
	return t.saveSettings(ctx, s)
}

// SetDropCancel sets the fast-drop auto-cancel guard.
func (t *Ticket) SetDropCancel(ctx context.Context, on bool, pct float64, sec int, near float64) error {
	t.mu.Lock()
	s := t.settings
	s.DropCancelOff = !on
	if pct != 0 || sec != 0 || near != 0 {
		s.DropPct, s.DropSec, s.NearPct = pct, sec, near
	}
	if err := s.validate(); err != nil {
		t.mu.Unlock()
		return err
	}
	t.settings = s
	t.mu.Unlock()
	return t.saveSettings(ctx, s)
}

// SetFollowSec sets how often a working entry is re-priced (2-60 s).
func (t *Ticket) SetFollowSec(ctx context.Context, sec int) error {
	t.mu.Lock()
	s := t.settings
	s.FollowSec = sec
	if err := s.validate(); err != nil {
		t.mu.Unlock()
		return err
	}
	t.settings = s
	t.mu.Unlock()
	return t.saveSettings(ctx, s)
}

// SetEMALive picks the 9EMA mode: closed candles (false) or incl. the live candle.
func (t *Ticket) SetEMALive(ctx context.Context, on bool) error {
	t.mu.Lock()
	s := t.settings
	s.EMALive, s.EMALiveSet = on, true
	t.settings = s
	t.mu.Unlock()
	return t.saveSettings(ctx, s)
}

// SetStaleMin sets how old (minutes) the 9EMA's newest closed bar may be.
func (t *Ticket) SetStaleMin(ctx context.Context, m int) error {
	t.mu.Lock()
	s := t.settings
	s.StaleMin = m
	if err := s.validate(); err != nil {
		t.mu.Unlock()
		return err
	}
	t.settings = s
	t.mu.Unlock()
	return t.saveSettings(ctx, s)
}

// SetFallbackSymbol is the scanner's last selected symbol, used when TWS's
// display group has not told the ticket what it shows.
func (t *Ticket) SetFallbackSymbol(sym string) {
	sym = strings.ToUpper(strings.TrimSpace(sym))
	if sym == "" {
		return
	}
	t.mu.Lock()
	t.fallbackSym = sym
	t.mu.Unlock()
	t.retrack()
}

// FeedAgg folds one live per-second aggregate (only tracked symbols are kept).
func (t *Ticket) FeedAgg(a core.Agg) {
	t.book.feed(a)
	t.onAggPx(a.Sym, a.Close, a.Low, a.High)
}

func (t *Ticket) currentSymLocked() (string, string) {
	if t.twsSym != "" {
		return t.twsSym, "TWS"
	}
	if t.fallbackSym != "" {
		return t.fallbackSym, "scanner"
	}
	return "", ""
}

// Prepare builds the confirmation for a BUY or a SELL (short). Nothing is sent.
func (t *Ticket) Prepare(side string, positionUSD float64, sym string) (Pending, error) {
	return t.PrepareQty(side, positionUSD, 0, sym)
}

// PrepareQty is Prepare with an explicit share count: shares > 0 sets the
// quantity directly (position $ ignored); 0 sizes floor(position$ / EMA).
//
// Before the confirmation is shown, a what-if placeOrder (whatIf=true, fresh
// order id, never recorded as an order, never followed or modified) asks TWS
// for the margin this exact order needs; see MarginCheck. The call blocks up
// to Config.WhatIfTimeout for the answer.
func (t *Ticket) PrepareQty(side string, positionUSD float64, shares int64, sym string) (Pending, error) {
	return t.PrepareTP(side, positionUSD, shares, sym, 0)
}

// PrepareTP is PrepareQty with the take-profit percent: it must be one of the
// two configured presets (0 = the high preset, the default).
func (t *Ticket) PrepareTP(side string, positionUSD float64, shares int64, sym string, tpPct float64) (Pending, error) {
	return t.PrepareOrder(side, positionUSD, shares, sym, tpPct, false, 0)
}

// PrepareOrder is PrepareTP with the soft-stop choice: stopOff places the
// order with its stop disabled (the per-order toggle can enable it later).
//
// riskUSD > 0 (and shares == 0) sizes by risk: floor(risk / per-share loss at
// the planned soft stop), trimmed by the max position $ and, after the
// what-if, the margin max (Pending.Bound names the cap).
func (t *Ticket) PrepareOrder(side string, positionUSD float64, shares int64, sym string, tpPct float64, stopOff bool, riskUSD float64) (Pending, error) {
	t.freshenBook(t.curSym())
	p, err := t.preparePending(side, positionUSD, shares, sym, tpPct, riskUSD)
	if err == nil {
		t.mu.Lock()
		p.StopOff, p.StopPct = stopOff, t.effStopPctLocked()
		t.mu.Unlock()
	}
	if err != nil {
		return Pending{}, err
	}
	return t.finishPrepare(p)
}

// finishPrepare is the tail every prepare shares: the what-if margin check,
// the share-count trim, the pending line. p is already sized and validated.
func (t *Ticket) finishPrepare(p Pending) (Pending, error) {
	t.mu.Lock()
	if t.conn != "connected" || t.client == nil || !t.haveNext {
		err := t.refuse("not connected to TWS")
		t.mu.Unlock()
		return Pending{}, err
	}
	gen, c, wid := t.gen, t.client, t.nextID
	t.nextID++ // a fresh id; never reused for a real order
	ch := make(chan whatIfRes, 1)
	t.whatIfs[wid] = ch
	t.wiIDs[wid] = true
	acct := t.account
	t.mu.Unlock()

	res := t.whatIf(c, ch, wid, acct, p)

	t.mu.Lock()
	defer t.mu.Unlock()
	delete(t.whatIfs, wid)
	if t.gen != gen || t.conn != "connected" {
		return Pending{}, t.refuse("connection changed during the margin check")
	}
	if cur, _ := t.currentSymLocked(); !strings.EqualFold(cur, p.Sym) {
		return Pending{}, t.refuse(fmt.Sprintf("symbol changed during the margin check (%s -> %s)", p.Sym, cur))
	}
	avail, have := t.availLocked()
	mc := computeMargin(res, p.Qty, p.Limit, avail, have, t.settings.MaxPositionUSD, t.baseCurLocked())
	if p.RiskUSD > 0 && mc.Status == "over" && mc.MaxShares > 0 { // risk sizing: trim to the margin max
		p.Qty = mc.MaxShares
		p.Bound = "margin max"
		p.Risk = float64(p.Qty) * perShareRisk(p.Side, p.Limit, p.PlannedStop)
		mc.Status = "ok"
		mc.Needed = mc.PerShare * float64(p.Qty)
		mc.Text = marginLine(mc, t.baseCurLocked())
	}
	p.Margin = &mc
	p.At = t.now().UnixMilli() // the confirm window starts when the line is shown
	if p.Side == "SELL" {
		note, warn := shortNote(t.shortInfoLocked(p.Sym, t.now()), p.Qty)
		p.ShortNote = note
		if warn != "" {
			if p.Warn != "" {
				p.Warn += "; "
			}
			p.Warn += warn
		}
	}
	p.Text = confirmText(p)
	t.pending = &p
	t.note = ""
	return p, nil
}

// whatIf sends the margin-check order and waits for its openOrder (or error).
// It is audited only as a "whatif" note (ID 0), never as an order.
func (t *Ticket) whatIf(c EClient, ch chan whatIfRes, wid int64, acct string, p Pending) whatIfRes {
	x := &Order{
		Action: p.Side, OrderType: "LMT", TotalQuantity: Qty(p.Qty), LmtPrice: p.Limit, TIF: "DAY",
		OutsideRTH: true, OrderRef: whatIfRefPfx + p.Sym, Account: acct, Transmit: true, WhatIf: true,
	}
	t.record(auditRec{Ev: "whatif", Sym: p.Sym, Side: p.Side, Qty: p.Qty, Limit: p.Limit,
		Note: fmt.Sprintf("what-if margin check, TWS id %d (not an order)", wid)})
	c.PlaceOrder(wid, contractFor(p.Sym), x)
	select {
	case r := <-ch:
		return r
	case <-time.After(t.cfg.WhatIfTimeout):
		return whatIfRes{err: "no answer from TWS within " + t.cfg.WhatIfTimeout.String()}
	}
}

// baseCurLocked: the currency IB reports AvailableFunds in (the account's
// base currency; what-if margins come in the same currency).
func (t *Ticket) baseCurLocked() string {
	if c := t.fundsCur[t.account]; c != "" {
		return c
	}
	return "USD"
}

// curSym: "$", "£", "€" or "CHF ".
func curSym(c string) string {
	switch strings.ToUpper(c) {
	case "USD", "":
		return "$"
	case "GBP":
		return "£"
	case "EUR":
		return "€"
	}
	return strings.ToUpper(c) + " "
}

func (t *Ticket) availLocked() (float64, bool) {
	v, ok := t.funds[t.account]["AvailableFunds"]
	return v, ok
}

// computeMargin turns a what-if answer into the confirm line's numbers:
// perShare = max(init, maint margin change) / qty, maxShares = floor(avail /
// perShare), capped by floor(maxPositionUSD / limit).
//
// Units: IB reports the what-if margin figures AND AvailableFunds in the
// account's base currency (cur, e.g. GBP), so avail / perShare never mixes
// currencies. Only the max-position cap is in USD, against the USD limit
// price. Nothing is converted.
func computeMargin(r whatIfRes, qty int64, limit, avail float64, haveAvail bool, maxUSD float64, cur string) MarginCheck {
	cs := curSym(cur)
	mc := MarginCheck{Avail: avail, HaveAvail: haveAvail}
	capMax := int64(0)
	if limit > 0 {
		capMax = int64(math.Floor(maxUSD/limit + 1e-9))
	}
	unavailable := func(note string) MarginCheck {
		mc.Status, mc.Note, mc.MaxShares = "unavailable", note, capMax
		mc.Text = "margin check unavailable (" + note + ")"
		if mc.Warning != "" {
			mc.Text += "; IB: " + mc.Warning
		}
		return mc
	}
	if r.err != "" {
		return unavailable(r.err)
	}
	if r.st == nil || !r.st.Parsed {
		return unavailable("TWS answer could not be decoded")
	}
	mc.Warning = strings.TrimSpace(r.st.WarningText)
	mc.Commission = r.st.Commission
	ini, ok1 := marginNum(r.st.InitMarginChange)
	mnt, ok2 := marginNum(r.st.MaintMarginChange)
	if !ok1 && !ok2 {
		return unavailable("TWS returned no margin figures")
	}
	mc.InitChange, mc.MaintChange = ini, mnt
	mc.Needed = math.Max(math.Max(ini, mnt), 0)
	if qty > 0 {
		mc.PerShare = mc.Needed / float64(qty)
	}
	if !haveAvail {
		return unavailable(fmt.Sprintf("needs %s%s margin; account funds not received yet", cs, fmtQty(int64(math.Round(mc.Needed)))))
	}
	mc.MaxShares = capMax
	if mc.PerShare > 0 {
		byMargin := int64(0)
		if avail > 0 {
			byMargin = int64(math.Floor(avail/mc.PerShare + 1e-9))
		}
		if byMargin < mc.MaxShares {
			mc.MaxShares = byMargin
		}
	}
	mc.Status = "ok"
	if qty > mc.MaxShares {
		mc.Status = "over"
	}
	mc.Text = marginLine(mc, cur)
	return mc
}

// marginLine: "needs £M margin (avail £A) — max N sh[; IB: warning]".
func marginLine(mc MarginCheck, cur string) string {
	cs := curSym(cur)
	txt := fmt.Sprintf("needs %s%s margin (avail %s%s) — max %s sh", cs, fmtQty(int64(math.Round(mc.Needed))), cs,
		fmtQty(int64(math.Floor(mc.Avail))), fmtQty(mc.MaxShares))
	if mc.Warning != "" {
		txt += "; IB: " + mc.Warning
	}
	return txt
}

// Sizing is the pane's "= N sh · stop 2.41 · ≈ $X position" for a BUY now.
type Sizing struct {
	Qty      int64   `json:"qty"`
	Limit    float64 `json:"limit"`
	Stop     float64 `json:"stop"`
	Position float64 `json:"position"`
	Risk     float64 `json:"risk"`
	Bound    string  `json:"bound"`
}

func (t *Ticket) sizingLocked(q Quote) *Sizing {
	limit := entryPrice("BUY", q.EMA)
	if !(limit > 0) {
		return nil
	}
	stop := stopLevelFor("BUY", q.EMA, limit, t.effStopPctLocked())
	z := &Sizing{Limit: limit, Stop: stop}
	switch {
	case t.pvShares > 0:
		z.Qty = t.pvShares
	case t.pvRisk > 0:
		z.Qty, z.Bound = riskSize("BUY", t.pvRisk, limit, stop, t.settings.MaxPositionUSD)
	default:
		return nil
	}
	z.Position = float64(z.Qty) * limit
	z.Risk = float64(z.Qty) * perShareRisk("BUY", limit, stop)
	return z
}

// perShareRisk: what one share loses if the soft stop is hit.
func perShareRisk(side string, limit, stop float64) float64 {
	if side == "BUY" {
		return limit - stop
	}
	return stop - limit
}

// commissionPerShare estimates the round trip's commission and fees per share
// (VCIG 2026-10-30: about $18 on 3,037 shares); sizing counts it as risk.
var commissionPerShare = 0.006

// riskSize: floor(risk / (per-share loss + commission)), trimmed by the max position $.
func riskSize(side string, risk, limit, stop, maxUSD float64) (int64, string) {
	per := perShareRisk(side, limit, stop)
	if !(per > 0) || !(limit > 0) {
		return 0, ""
	}
	q := int64(math.Floor(risk/(per+commissionPerShare) + 1e-9))
	bound := ""
	if capQ := int64(math.Floor(maxUSD/limit + 1e-9)); q > capQ {
		q, bound = capQ, "max position $"
	}
	if q > maxOrderShares {
		q, bound = maxOrderShares, "max shares"
	}
	return q, bound
}

// marginNum parses one of IB's margin strings; empty / unset (Double.MAX) is not a number.
func marginNum(s string) (float64, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, false
	}
	v, err := strconv.ParseFloat(s, 64)
	if err != nil || math.IsNaN(v) || math.IsInf(v, 0) || math.Abs(v) >= math.MaxFloat64/2 {
		return 0, false
	}
	return v, true
}

// preparePending validates and sizes the order (no what-if, nothing sent).
func (t *Ticket) preparePending(side string, positionUSD float64, shares int64, sym string, tpPct float64, riskUSD float64) (Pending, error) {
	side = strings.ToUpper(side)
	if side != "BUY" && side != "SELL" {
		return Pending{}, fmt.Errorf("bad side %q", side)
	}
	now := t.now()
	t.mu.Lock()
	defer t.mu.Unlock()
	t.pending = nil
	if tpPct == 0 {
		tpPct = t.settings.TPHighPct
	}
	if math.Abs(tpPct-t.settings.TPLowPct) > 1e-9 && math.Abs(tpPct-t.settings.TPHighPct) > 1e-9 {
		return Pending{}, t.refuse(fmt.Sprintf("take profit %v%% is not one of the presets (%v%% / %v%%)", tpPct, t.settings.TPLowPct, t.settings.TPHighPct))
	}
	if t.conn != "connected" {
		return Pending{}, t.refuse("not connected to TWS")
	}
	cur, _ := t.currentSymLocked()
	if cur == "" {
		return Pending{}, t.refuse("no symbol: TWS has not reported one")
	}
	if sym != "" && !strings.EqualFold(sym, cur) {
		return Pending{}, t.refuse(fmt.Sprintf("symbol mismatch: ticket %s, TWS %s", sym, cur))
	}
	if shares < 0 || shares > maxOrderShares {
		return Pending{}, t.refuse("bad share count")
	}
	if riskUSD < 0 || riskUSD > 5000 {
		return Pending{}, t.refuse("bad risk $")
	}
	if shares == 0 && !(positionUSD > 0) && !(riskUSD > 0) {
		return Pending{}, t.refuse("pick a risk or enter shares")
	}
	if shares == 0 && riskUSD == 0 && positionUSD > t.settings.MaxPositionUSD {
		return Pending{}, t.refuse(fmt.Sprintf("position $%.0f is above the max $%.0f", positionUSD, t.settings.MaxPositionUSD))
	}
	q := t.quoteLocked(cur, now)
	if !t.emaUsableLocked(q) {
		return Pending{}, t.refuse(t.staleMsgLocked(q))
	}
	limit := entryPrice(side, q.EMA)
	if !(limit > 0) {
		return Pending{}, t.refuse("price rounds to zero")
	}
	qty := shares
	stop := stopLevelFor(side, q.EMA, limit, t.effStopPctLocked())
	bound := ""
	switch {
	case qty > 0:
		if float64(qty)*limit > t.settings.MaxPositionUSD+1e-9 {
			return Pending{}, t.refuse(fmt.Sprintf("%d shares x %s is above the max $%.0f", qty, fmtPx(limit), t.settings.MaxPositionUSD))
		}
	case riskUSD > 0:
		qty, bound = riskSize(side, riskUSD, limit, stop, t.settings.MaxPositionUSD)
	default:
		qty = sizeQty(positionUSD, q.EMA, limit, t.settings.MaxPositionUSD)
	}
	if qty < 1 {
		if riskUSD > 0 && shares == 0 {
			return Pending{}, t.refuse(fmt.Sprintf("risk $%v buys 0 shares (loss per share at the stop %s is %s)", riskUSD, fmtPx(stop), fmtPx(perShareRisk(side, limit, stop))))
		}
		return Pending{}, t.refuse("quantity is 0")
	}
	t.tokSeq++
	p := Pending{
		Token: fmt.Sprintf("%d-%d", now.UnixNano(), t.tokSeq), Side: side, Sym: cur, Qty: qty,
		Limit: limit, TP: tpPrice(side, limit, tpPct), EMA: q.EMA, At: now.UnixMilli(), TPPct: tpPct,
		Warn:    marketableWarn(side, limit, q.Price),
		RiskUSD: riskUSD, PlannedStop: stop, Bound: bound,
	}
	t.applyPresetLocked(&p, q.EMA)
	if shares > 0 || riskUSD > 0 {
		p.Risk = float64(qty) * perShareRisk(side, limit, stop)
	}
	return p, nil
}

func (t *Ticket) refuse(msg string) error {
	t.note = "refused: " + msg
	return errors.New(msg)
}

// Abort drops the pending confirmation.
func (t *Ticket) Abort() {
	t.mu.Lock()
	t.pending = nil
	t.mu.Unlock()
}

// Confirm transmits the pending order - the only path that places one.
func (t *Ticket) Confirm(token, sym string) error {
	t.freshenBook(t.curSym())
	now := t.now()
	t.mu.Lock()
	p := t.pending
	t.pending = nil
	if p == nil || token == "" || p.Token != token {
		t.mu.Unlock()
		return errors.New("nothing to confirm (press BUY or SELL first)")
	}
	fail := func(msg string) error { err := t.refuse(msg); t.mu.Unlock(); return err }
	if now.Sub(time.UnixMilli(p.At)) > confirmTTL {
		return fail("confirmation expired; press BUY/SELL again")
	}
	if p.Side == "CLOSE" {
		acts, err := t.confirmCloseLocked(p, now)
		t.mu.Unlock()
		run(acts)
		t.retrack()
		return err
	}
	if t.conn != "connected" || t.client == nil || !t.haveNext {
		return fail("not connected to TWS")
	}
	cur, _ := t.currentSymLocked()
	if !strings.EqualFold(cur, p.Sym) || (sym != "" && !strings.EqualFold(sym, p.Sym)) {
		return fail(fmt.Sprintf("symbol mismatch: ticket %s, TWS %s", p.Sym, cur))
	}
	if p.Kind == kindVwapx { // 3-order bracket; no 9EMA involved (vwapx.go)
		acts, err := t.confirmVwapxLocked(p, now)
		t.mu.Unlock()
		run(acts)
		t.retrack()
		return err
	}
	q := t.quoteLocked(p.Sym, now)
	if !t.emaUsableLocked(q) {
		return fail(t.staleMsgLocked(q))
	}
	// The 9EMA moved since the line was built: rebuild it at the new price and
	// require a second Confirm instead of sending a stale limit.
	if nl := entryPrice(p.Side, q.EMA); nl > 0 && math.Abs(nl-p.Limit) > tickOf(p.Limit)+1e-9 {
		np := *p
		t.tokSeq++
		np.Token = fmt.Sprintf("%d-%d", now.UnixNano(), t.tokSeq)
		np.Limit, np.EMA, np.At = nl, q.EMA, now.UnixMilli()
		if np.TPR > 0 {
			np.TP = tpForR(np.Side, nl, stopLevelFor(np.Side, q.EMA, nl, np.StopPct), np.TPR)
		} else {
			np.TP = tpPrice(np.Side, nl, pctOrLegacy(np.TPPct))
		}
		np.Warn = marketableWarn(np.Side, nl, q.Price)
		for np.Qty > 0 && float64(np.Qty)*nl > t.settings.MaxPositionUSD+1e-9 {
			np.Qty--
		}
		np.Text = confirmText(np)
		old := p.Limit
		t.pending = &np
		return fail(fmt.Sprintf("9EMA moved (%s -> %s): check the new line and press Confirm again", fmtPx(old), fmtPx(nl)))
	}
	if p.Qty < 1 || !(p.Limit > 0) || !(p.TP > 0) {
		return fail("quantity or price invalid")
	}
	if float64(p.Qty)*p.Limit > t.settings.MaxPositionUSD+1e-9 {
		return fail("above the max position $")
	}
	if m := p.Margin; m != nil && m.Status != "unavailable" && p.Qty > m.MaxShares {
		return fail(fmt.Sprintf("%d shares is above the margin max %d (use max)", p.Qty, m.MaxShares))
	}
	o := &ord{
		ID: t.nextID, ChildID: t.nextID + 1, Sym: p.Sym, Side: p.Side, Qty: p.Qty,
		Limit: p.Limit, TP: p.TP, Remaining: p.Qty, Status: "Sent", ChildStatus: "Sent",
		Following: true, LastFollowBar: q.LastClosed, PlacedAt: now.UnixMilli(),
		FollowUntil: followUntil(now), TPPct: p.TPPct,
	}
	o.StopOff, o.StopPct, o.StopFollow = p.StopOff, p.StopPct, t.settings.StopFollow
	t.presetToOrderLocked(o, p, now)
	o.EMALive, o.lastFollowAt = t.settings.EMALive, now
	o.noteTP(o.TP)
	o.noteTPSent(o.TP, now)
	o.childQty = o.Qty
	t.nextID += 2
	t.orders[o.ID] = o
	t.byChild[o.ChildID] = o
	cid := t.settings.ClientID
	t.known[o.ID] = auditRec{ID: o.ID, ClientID: cid, Sym: o.Sym, Side: o.Side, TPPct: o.TPPct,
		StopPct: o.StopPct, StopFollow: o.StopFollow, StopOff: o.StopOff,
		Preset: o.Preset, TPR: o.TPR, Trail5m: o.Trail5m, DropOff: o.DropOff, EntryDeadline: o.EntryDeadline,
		BEOff: o.BEOff, BEPct: o.BEPct, BEOffsetPct: o.BEOffsetPct}
	t.known[o.ChildID] = auditRec{ID: o.ChildID, ParentID: o.ID, ClientID: cid, Sym: o.Sym, Side: o.Side, TPPct: o.TPPct}
	c := t.client
	parent, child := t.entryOrder(o, false), t.tpOrder(o)
	t.note = "sent: " + p.Text
	t.mu.Unlock()

	t.record(auditRec{Ev: "place", ID: o.ID, ClientID: cid, Sym: o.Sym, Side: o.Side, Qty: o.Qty, Limit: o.Limit, TP: o.TP, TPPct: o.TPPct,
		Preset: o.Preset, TPR: o.TPR, Trail5m: o.Trail5m, DropOff: o.DropOff, EntryDeadline: o.EntryDeadline})
	t.record(auditRec{Ev: "stop", ID: o.ID, Sym: o.Sym, StopPct: o.StopPct, StopFollow: o.StopFollow, StopOff: o.StopOff, Note: "placed"})
	t.record(auditRec{Ev: "place", ID: o.ChildID, ParentID: o.ID, ClientID: cid, Sym: o.Sym, Side: o.Side, Qty: o.Qty, Limit: o.TP, TPPct: o.TPPct})
	c.PlaceOrder(o.ID, contractFor(o.Sym), parent)
	c.PlaceOrder(o.ChildID, contractFor(o.Sym), child)
	t.retrack()
	return nil
}

// followUntil: a DAY entry follows until 16:00 ET; one placed after 16:00 ET
// follows until 20:00 ET (the end of the extended session it rests in).
func followUntil(now time.Time) int64 {
	et := now.In(etLoc())
	y, m, d := et.Date()
	close := time.Date(y, m, d, 16, 0, 0, 0, etLoc())
	if !et.Before(close) {
		close = time.Date(y, m, d, 20, 0, 0, 0, etLoc())
	}
	return close.UnixMilli()
}

var etOnce sync.Once
var etL *time.Location

func etLoc() *time.Location {
	etOnce.Do(func() {
		l, err := time.LoadLocation("America/New_York")
		if err != nil {
			l = time.FixedZone("ET", -5*3600)
		}
		etL = l
	})
	return etL
}

// Cancel cancels one ticket order: the entry while it works (IB then cancels
// the attached TP), else the TP. Orders this tool did not create are refused.
func (t *Ticket) Cancel(id int64) error {
	t.mu.Lock()
	o := t.orders[id]
	if o == nil {
		t.mu.Unlock()
		return fmt.Errorf("order %d is not a ticket order", id)
	}
	acts, err := t.cancelLocked(o)
	t.mu.Unlock()
	run(acts)
	return err
}

// CancelAll cancels every active ticket order.
func (t *Ticket) CancelAll() error {
	t.mu.Lock()
	var acts []func()
	for _, o := range t.orders {
		if o.parentActive() || o.childActive() || o.slActive() {
			a, _ := t.cancelLocked(o)
			acts = append(acts, a...)
		}
	}
	t.mu.Unlock()
	run(acts)
	return nil
}

func (t *Ticket) cancelLocked(o *ord) ([]func(), error) {
	o.Following = false
	if t.client == nil || t.conn != "connected" {
		return nil, errors.New("not connected to TWS")
	}
	c := t.client
	o.cancelled = true
	var id int64
	switch {
	case o.parentActive():
		id = o.ID
		o.Note = "cancel sent"
	case o.childActive():
		id = o.ChildID
		o.Note = "TP cancel sent"
	case o.slActive():
		id = o.SLID
		o.Note = "stop cancel sent"
	default:
		return nil, errors.New("order is not working")
	}
	rec := auditRec{Ev: "cancel", ID: id, Sym: o.Sym}
	return []func(){func() { t.record(rec); c.CancelOrder(id) }}, nil
}

// ─── connection ──────────────────────────────────────────────────────────

// Connect starts a session (asynchronously); Disconnect ends it.
func (t *Ticket) Connect() {
	t.mu.Lock()
	t.want = true
	start := t.conn != "connecting" && t.conn != "connected"
	t.mu.Unlock()
	if start {
		go t.session()
	}
}

func (t *Ticket) Disconnect() {
	t.mu.Lock()
	t.want = false
	t.pending = nil
	wasUp := t.conn == "connected"
	mdID := t.md.reqID
	stopIDs := t.stopMDIDsLocked()
	c := t.teardownLocked("disconnected", "")
	t.mu.Unlock()
	if c != nil {
		go func() {
			if wasUp {
				c.CancelAccountSummary(acctReqID)
				if mdID != 0 {
					c.CancelMktData(mdID)
				}
				for _, id := range stopIDs {
					c.CancelMktData(id)
				}
			}
			_ = c.Disconnect()
		}()
	}
}

// teardownLocked ends the current session; the caller disconnects the returned client.
func (t *Ticket) teardownLocked(state, note string) EClient {
	c := t.client
	t.client = nil
	t.gen++
	t.conn, t.connNote = state, note
	t.haveNext, t.readyDone = false, false
	t.twsSym, t.twsConID = "", 0
	t.funds = map[string]map[string]float64{}
	t.md = mdSub{}
	t.stopMD, t.stopMDIDs = map[string]*stopSub{}, map[int64]string{} // TWS drops them with the socket
	t.pvMark, t.previews = map[string]previewMark{}, map[string]*Preview{}
	for id, ch := range t.whatIfs {
		select {
		case ch <- whatIfRes{err: "disconnected"}:
		default:
		}
		delete(t.whatIfs, id)
	}
	for _, o := range t.orders {
		if o.Following {
			o.Note = "follow paused (disconnected)"
		}
	}
	return c
}

func (t *Ticket) session() {
	t.mu.Lock()
	if !t.want || t.conn == "connecting" || t.conn == "connected" {
		t.mu.Unlock()
		return
	}
	t.gen++
	gen := t.gen
	s := t.settings
	t.conn, t.connNote = "connecting", fmt.Sprintf("%s:%d client %d", s.Host, s.Port, s.ClientID)
	t.accounts, t.account = nil, ""
	t.haveNext, t.readyDone = false, false
	t.ready = make(chan struct{})
	ready := t.ready
	c := t.cfg.Factory(&wrapper{t: t, gen: gen})
	t.client = c
	t.mu.Unlock()

	fail := func(note string) {
		t.mu.Lock()
		if t.gen == gen {
			t.teardownLocked("error", note)
			t.retryAt = t.now().Add(retryEvery)
		}
		t.mu.Unlock()
		go func() { _ = c.Disconnect() }()
	}
	errc := make(chan error, 1)
	go func() { errc <- c.Connect(s.Host, s.Port, int64(s.ClientID)) }()
	select {
	case err := <-errc:
		if err != nil {
			fail("TWS not reachable: " + err.Error())
			return
		}
	case <-time.After(readyTimeout):
		fail("TWS did not answer the handshake")
		return
	}
	if !c.IsConnected() {
		fail("TWS closed the connection during the handshake (API enabled? client id in use?)")
		return
	}
	c.ReqManagedAccts()
	select {
	case <-ready:
	case <-time.After(readyTimeout):
		fail("no account / next order id from TWS within 15 s")
		return
	}
	t.mu.Lock()
	if t.gen != gen {
		t.mu.Unlock()
		return
	}
	t.conn, t.connNote = "connected", ""
	t.stopRecheck = true // re-armed stops: check the current price at once
	now := t.now()
	t.beatSent, t.beatAns = now, now
	// Ids: never below anything this tool already used.
	for id := range t.known {
		if id >= t.nextID {
			t.nextID = id + 1
		}
	}
	t.mu.Unlock()
	logx.L().Info("order ticket connected", "addr", fmt.Sprintf("%s:%d", s.Host, s.Port), "client", s.ClientID)
	c.SubscribeToGroupEvents(groupReqID, t.cfg.Group)
	c.ReqAccountSummary(acctReqID, "All", acctTags) // stays subscribed; cancelled on Disconnect
	t.mu.Lock()
	mdActs := append(t.syncMktDataLocked(), t.syncStopMDLocked()...)
	t.mu.Unlock()
	run(mdActs)
	c.ReqOpenOrders() // reconcile: only this client id's orders; only known ids adopted
}

// ─── wrapper callbacks ───────────────────────────────────────────────────

func (t *Ticket) live(gen int64) bool { return t.gen == gen }

func (t *Ticket) onNextValidID(gen, id int64) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if !t.live(gen) {
		return
	}
	if id > t.nextID {
		t.nextID = id
	}
	t.haveNext = true
	t.checkReadyLocked()
}

func (t *Ticket) onAccounts(gen int64, accts []string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if !t.live(gen) {
		return
	}
	var clean []string
	for _, a := range accts {
		for _, x := range strings.Split(a, ",") {
			if x = strings.TrimSpace(x); x != "" {
				clean = append(clean, x)
			}
		}
	}
	t.accounts = clean
	if len(clean) > 0 {
		t.account = clean[0]
	}
	t.checkReadyLocked()
}

func (t *Ticket) checkReadyLocked() {
	if t.haveNext && t.account != "" && !t.readyDone && t.ready != nil {
		t.readyDone = true
		close(t.ready)
	}
}

func (t *Ticket) onBeat(gen int64) {
	t.mu.Lock()
	if t.live(gen) {
		t.beatAns = t.now()
	}
	t.mu.Unlock()
}

func (t *Ticket) onClosed(gen int64) {
	t.mu.Lock()
	var c EClient
	if t.live(gen) && (t.conn == "connected" || t.conn == "connecting") {
		c = t.teardownLocked("lost", "TWS closed the connection")
		t.retryAt = t.now().Add(retryEvery)
	}
	t.mu.Unlock()
	if c != nil {
		go func() { _ = c.Disconnect() }()
	}
}

func (t *Ticket) onError(gen, reqID, code int64, msg string) {
	if code >= 2100 && code < 2200 { // farm status notices
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if !t.live(gen) {
		return
	}
	if t.stopMDErrorLocked(reqID, code, msg) { // stop price line: shown with SL blind
		return
	}
	if t.mdErrorLocked(reqID, code, msg) { // market data: tile tooltip only
		return
	}
	text := fmt.Sprintf("IB %d: %s", code, msg)
	if ch, ok := t.whatIfs[reqID]; ok { // the margin check failed: margin line only
		select {
		case ch <- whatIfRes{err: text}:
		default:
		}
		return
	}
	if t.wiIDs[reqID] { // a late answer to a finished what-if: never the banner
		return
	}
	if f := t.orderErrorRecLocked(reqID, code, msg); f != nil {
		f()
	}
	if o := t.orders[reqID]; o != nil {
		o.Note = text
	} else if o := t.byChild[reqID]; o != nil {
		o.Note = "TP: " + text
	}
	switch code {
	case 1100:
		t.connNote = "TWS lost its connection to IB"
	case 1101, 1102:
		t.connNote = ""
	case 10141:
		t.connNote = "paper disclaimer not accepted in TWS"
	}
	if reqID >= cdReqBase && reqID < cdReqBase+100000 {
		delete(t.cdReq, reqID)
	}
	t.note = text
}

func (t *Ticket) onAccountSummary(gen, reqID int64, account, tag, value, currency string) {
	if reqID != acctReqID {
		return
	}
	v, err := strconv.ParseFloat(strings.TrimSpace(value), 64)
	if err != nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if !t.live(gen) {
		return
	}
	m := t.funds[account]
	if m == nil {
		m = map[string]float64{}
		t.funds[account] = m
	}
	if tag == "ExchangeRate" { // $LEDGER:USD row: base units per 1 USD
		if strings.EqualFold(strings.TrimSpace(currency), "USD") && v > 0 {
			m[fxUSDKey] = v
		}
		return
	}
	if c := strings.TrimSpace(currency); tag != "AvailableFunds" && c != "" && c != "BASE" && account != "" && t.fundsCur[account] != "" && !strings.EqualFold(c, t.fundsCur[account]) {
		return // a ledger row in another currency must never overwrite a base value
	}
	m[tag] = v
	if tag == "AvailableFunds" && strings.TrimSpace(currency) != "" {
		t.fundsCur[account] = strings.TrimSpace(currency)
	}
}

func (t *Ticket) onDisplayGroup(gen, reqID int64, info string) {
	t.mu.Lock()
	if !t.live(gen) || reqID != groupReqID {
		t.mu.Unlock()
		return
	}
	info = strings.TrimSpace(info)
	at := strings.IndexByte(info, '@')
	head := info
	if at >= 0 {
		head = info[:at]
	}
	conID, err := strconv.ParseInt(head, 10, 64)
	if err != nil || conID <= 0 { // "none" or unparseable: TWS shows nothing for the group
		t.twsSym, t.twsConID = "", 0
		t.pending = nil
		t.mu.Unlock()
		t.retrack()
		return
	}
	t.twsConID = conID
	if sym, ok := t.conSym[conID]; ok {
		if sym != t.twsSym {
			t.pending = nil
		}
		t.twsSym = sym
		t.mu.Unlock()
		t.retrack()
		return
	}
	// Unknown contract: until it resolves the ticket must not trade the old one.
	t.twsSym = ""
	t.pending = nil
	id := t.nextCD
	t.nextCD++
	t.cdReq[id] = conID
	c := t.client
	t.mu.Unlock()
	if c != nil {
		cc := &Contract{}
		cc.ConID = conID
		c.ReqContractDetails(id, cc)
	}
}

func (t *Ticket) onContractDetails(gen, reqID int64, cd *ContractDetails) {
	if cd == nil {
		return
	}
	t.mu.Lock()
	if !t.live(gen) {
		t.mu.Unlock()
		return
	}
	conID, ok := t.cdReq[reqID]
	if !ok {
		t.mu.Unlock()
		return
	}
	delete(t.cdReq, reqID)
	sym := strings.ToUpper(strings.ReplaceAll(cd.Contract.Symbol, " ", "."))
	if cd.Contract.SecType != "" && cd.Contract.SecType != "STK" {
		t.note = "TWS shows a non-stock contract (" + cd.Contract.SecType + ")"
		t.mu.Unlock()
		return
	}
	t.conSym[conID] = sym
	if t.twsConID == conID {
		t.twsSym = sym
	}
	t.mu.Unlock()
	t.retrack()
}

// onOpenOrder adopts ONLY orders this tool created (audit ids, matching client
// id and perm id, our orderRef); everything else is ignored.
func (t *Ticket) onOpenOrder(gen, id int64, c *Contract, x *Order, st *OrderState) {
	if c == nil || x == nil {
		return
	}
	var post []func()
	defer func() { run(post) }() // after the unlock below
	t.mu.Lock()
	defer t.mu.Unlock()
	if !t.live(gen) {
		return
	}
	if ch, ok := t.whatIfs[id]; ok { // our margin check: deliver, never adopt
		select {
		case ch <- whatIfRes{st: st}:
		default:
		}
		return
	}
	if st != nil && st.WhatIf { // a what-if preview is never an order
		return
	}
	k, ok := t.known[id]
	if !ok || !strings.HasPrefix(x.OrderRef, refPrefix) {
		return
	}
	if k.ClientID != 0 && x.ClientID != 0 && int(x.ClientID) != k.ClientID {
		return
	}
	if k.PermID != 0 && x.PermID != 0 && x.PermID != k.PermID {
		return
	}
	if k.PermID == 0 && x.PermID != 0 {
		k.PermID = x.PermID
		t.known[id] = k
		rec := auditRec{Ev: "perm", ID: id, PermID: x.PermID, Sym: k.Sym}
		go t.record(rec)
	}
	sym := strings.ToUpper(strings.ReplaceAll(c.Symbol, " ", "."))
	stat := ""
	if st != nil {
		stat = st.Status
	}
	if strings.HasSuffix(x.OrderRef, ":stop") { // a soft-stop exit: attach to its order
		if o := t.orders[k.ParentID]; o != nil {
			o.ExitID, o.ExitQty, o.ExitPx = id, x.TotalQuantity.Int(), x.LmtPrice
			o.StopState, o.exitReprices = stExiting, stopMaxReprices
			if stat != "" {
				o.ExitStatus = stat
			}
			t.byExit[id] = o
		}
		return
	}
	if x.ParentID == 0 {
		o := t.orders[id]
		if o == nil {
			o = &ord{ID: id, Sym: sym, Side: x.Action, Qty: x.TotalQuantity.Int(), Remaining: x.TotalQuantity.Int(),
				FollowUntil: followUntil(t.now()), Note: "adopted after reconnect", TPPct: pctOrLegacy(k.TPPct)}
			o.Following = x.TIF == "DAY"
			o.EMALive = t.settings.EMALive // not audited: the current mode
			t.orders[id] = o
			t.restoreStopLocked(o, k) // (a VWAP X order: no follow, no soft stop)
			// a child seen first
			for cid, k2 := range t.known {
				if k2.ParentID == id {
					if k2.Note == vwapxSLNote {
						o.SLID = cid
						t.bySL[cid] = o
						continue
					}
					o.ChildID = cid
					t.byChild[cid] = o
				}
			}
		}
		o.PermID = x.PermID
		o.Limit = x.LmtPrice
		o.modAt = time.Time{} // TWS echoed the (modified) entry
		if stat != "" {
			o.Status = stat
		}
		if finished(o.Status) {
			o.Following = false
		}
		return
	}
	o := t.orders[x.ParentID]
	if o == nil {
		pk, pok := t.known[x.ParentID]
		if !pok {
			return
		}
		side := pk.Side
		if side == "" {
			side = "SELL"
			if x.Action == "SELL" {
				side = "BUY"
			}
		}
		// Parent no longer open (filled): show the TP only.
		o = &ord{ID: x.ParentID, Sym: sym, Side: side, Qty: x.TotalQuantity.Int(), Status: "Filled", Filled: x.TotalQuantity.Int(), Note: "adopted after reconnect", TPPct: pctOrLegacy(pk.TPPct)}
		t.orders[o.ID] = o
		t.restoreStopLocked(o, pk)
	}
	if strings.HasSuffix(x.OrderRef, ":sl") && o.Kind == kindVwapx { // the real IB stop child
		o.SLID, o.SLPermID = id, x.PermID
		if stat != "" {
			o.SLStatus = stat
		}
		t.bySL[id] = o
		return
	}
	o.ChildID = id
	o.ChildPermID = x.PermID
	// TWS's limit is recorded and checked against what the ticket sent; it
	// never silently becomes the intended TP (tpverify.go).
	post = append(post, t.tpEchoLocked(o, x.LmtPrice, t.now())...)
	if q := x.TotalQuantity.Int(); q > 0 {
		o.childQty = q
	}
	if stat != "" && !(stat == "Filled" && o.ChildFilled <= 0) {
		// an openOrder "Filled" with no orderStatus fill behind it never counts
		o.ChildStatus = stat
	}
	t.byChild[id] = o
}

func (t *Ticket) onOrderStatus(gen int64, m statusMsg) {
	t.mu.Lock()
	if !t.live(gen) {
		t.mu.Unlock()
		return
	}
	id, status, filled, remaining, avg, permID := m.ID, m.Status, m.Filled, m.Remaining, m.Avg, m.PermID
	var acts []func()
	if why := t.foreignStatusLocked(m); why != "" {
		// Not provably ours: never let it move fills / the stop (audited).
		if o := t.ownerLocked(id); o != nil {
			acts = append(acts, t.statusRec(o, m, "IGNORED orderStatus: "+why))
		}
	} else if o := t.orders[id]; o != nil {
		acts = append(acts, t.statusRec(o, m, "entry"))
		o.Status = status
		if permID != 0 {
			o.PermID = permID
		}
		o.Filled, o.Remaining = filled, remaining
		if avg > 0 {
			o.Avg = avg
		}
		if finished(status) || (remaining == 0 && filled > 0) {
			if o.Following {
				o.Following = false
			}
		}
		now := t.now()
		if filled > 0 && !o.StopOff {
			acts = append(acts, t.armStopLocked(o, now)...)
			acts = append(acts, t.beRefillLocked(o)...)
		}
		// The TP follows the fill average once there is one (not once the stop fired).
		// On the first fill it is ALWAYS re-sent (and then verified by the echo).
		if filled > 0 && o.Avg > 0 && o.childActive() && (o.StopState == "" || o.StopState == stArmed) && o.Kind != kindVwapx {
			if tp := o.tpAt(o.Avg, o.StopLevel); tp != o.TP || !o.tpFillSent {
				o.TP = tp
				o.tpFillSent = true
				acts = append(acts, t.sendTPLocked(o, "tp->fill avg")...)
			}
		}
		o.lastMsg = m.String()
		acts = append(acts, t.stopProgressLocked(o, now)...)
	} else if o := t.byChild[id]; o != nil {
		acts = append(acts, t.statusRec(o, m, "tp"))
		o.ChildStatus = status
		o.ChildFilled = filled
		if permID != 0 && o.ChildPermID == 0 {
			o.ChildPermID = permID
		}
		mm := m
		o.childMsg = &mm
		o.lastMsg = m.String()
		acts = append(acts, t.stopProgressLocked(o, t.now())...)
	} else if o := t.bySL[id]; o != nil {
		acts = append(acts, t.statusRec(o, m, "sl"))
		o.SLStatus = status
		o.SLFilled = filled
		if permID != 0 && o.SLPermID == 0 {
			o.SLPermID = permID
		}
		o.lastMsg = m.String()
		acts = append(acts, t.stopProgressLocked(o, t.now())...)
	} else if o := t.byExit[id]; o != nil {
		acts = append(acts, t.statusRec(o, m, "stop-exit"))
		o.ExitStatus = status
		o.ExitFilled = filled
		o.lastMsg = m.String()
		acts = append(acts, t.stopProgressLocked(o, t.now())...)
	}
	t.mu.Unlock()
	run(acts)
	t.retrack()
}

func (t *Ticket) sendTPLocked(o *ord, why string) []func() {
	c := t.client
	if c == nil {
		return nil
	}
	o.TP = o.tpSafe(o.TP) // never a long TP at/below the fill (short: at/above)
	x := t.tpOrder(o)
	o.noteTP(o.TP)
	o.noteTPSent(o.TP, t.now())
	rec := auditRec{Ev: "modify", ID: o.ChildID, ParentID: o.ID, Sym: o.Sym, Limit: o.TP, Note: why}
	id := o.ChildID
	return []func(){func() { t.record(rec); c.PlaceOrder(id, contractFor(o.Sym), x) }}
}

// ─── the loop ────────────────────────────────────────────────────────────

// Run drives the heartbeat, the EMA follow, seeding and reconnects.
func (t *Ticket) Run(ctx context.Context) error {
	tk := time.NewTicker(time.Second)
	defer tk.Stop()
	for {
		select {
		case <-ctx.Done():
			t.Disconnect()
			return nil
		case <-tk.C:
			t.Tick(ctx)
		}
	}
}

// Tick is one pass of the loop (exported for tests).
func (t *Ticket) Tick(ctx context.Context) {
	now := t.now()
	t.mu.Lock()
	var acts []func()
	var lost EClient
	reconnect := false
	if t.conn == "connected" && t.client != nil {
		if t.beatAns.Before(t.beatSent) && now.Sub(t.beatSent) > beatTimeout {
			lost = t.teardownLocked("lost", "TWS stopped answering")
			t.retryAt = now.Add(retryEvery)
		} else if now.Sub(t.beatSent) >= beatEvery {
			t.beatSent = now
			c := t.client
			acts = append(acts, c.ReqCurrentTime)
		}
	}
	if t.want && (t.conn == "lost" || t.conn == "error") && !now.Before(t.retryAt) {
		reconnect = true
	}
	if t.conn == "connected" {
		acts = append(acts, t.dropTickLocked(now)...) // before follow: never re-price into the drop
		acts = append(acts, t.entryTimeoutLocked(now)...)
		acts = append(acts, t.followLocked(now)...)
		acts = append(acts, t.stopTickLocked(now)...)
		acts = append(acts, t.syncStopMDLocked()...) // after the stop pass: a finished stop's line goes now
		acts = append(acts, t.tpVerifyTickLocked(now)...)
	}
	t.mu.Unlock()
	if lost != nil {
		go func() { _ = lost.Disconnect() }()
	}
	run(acts)
	if reconnect {
		go t.session()
	}
	t.seedTracked(ctx, now)
	t.maybePreview(now)
}

// followLocked re-prices every following entry once per newly closed bar.
func (t *Ticket) followLocked(now time.Time) []func() {
	var acts []func()
	c := t.client
	for _, o := range t.orders {
		if !o.Following || o.Kind == kindVwapx {
			continue
		}
		if !o.parentActive() || o.cancelled {
			o.Following = false
			continue
		}
		if now.UnixMilli() >= o.FollowUntil {
			o.Following = false
			o.Note = "follow stopped (session close)"
			continue
		}
		// every FollowSec; never while the previous modify is unacknowledged (3 s max)
		if now.Sub(o.lastFollowAt) < time.Duration(t.settings.FollowSec)*time.Second {
			continue
		}
		if !o.modAt.IsZero() && now.Sub(o.modAt) < 3*time.Second {
			continue
		}
		q := t.book.quoteMode(o.Sym, now, o.EMALive) // the order's own 9EMA mode
		if !t.emaUsableLocked(q) {                   // not ready / gap / stale: skip this tick
			continue
		}
		o.lastFollowAt = now
		o.LastFollowBar = q.LastClosed
		lim := entryPrice(o.Side, q.EMA)
		if !(lim > 0) || math.Abs(lim-o.Limit) < tickOf(o.Limit)-1e-9 {
			continue
		}
		if float64(o.Qty)*lim > t.settings.MaxPositionUSD+1e-9 {
			o.Note = "follow skipped: new limit is above the max position $"
			continue
		}
		rem := o.Qty - o.Filled
		if rem <= 0 {
			o.Following = false
			continue
		}
		o.Limit = lim
		o.Note = ""
		o.modAt = now
		x := t.entryOrder(o, true)
		id, sym := o.ID, o.Sym
		rec := auditRec{Ev: "modify", ID: id, Sym: sym, Limit: lim, Note: "follow 9EMA"}
		acts = append(acts, func() { t.record(rec); c.PlaceOrder(id, contractFor(sym), x) })
		if o.Filled == 0 && o.childActive() {
			if tp := o.tpAt(lim, stopLevelFor(o.Side, q.EMA, lim, o.StopPct)); tp != o.TP {
				o.TP = tp
				acts = append(acts, t.sendTPLocked(o, "tp follows entry")...)
			}
		}
	}
	return acts
}

func (t *Ticket) followLabelLocked(o *ord) string {
	if !o.Following {
		return ""
	}
	mode := "closed"
	if o.EMALive {
		mode = "live"
	}
	return fmt.Sprintf("follows 9EMA %s · every %d s", mode, t.settings.FollowSec)
}

// trackedLocked: the ticket symbol plus every symbol with an active order.
func (t *Ticket) trackedLocked() []string {
	set := map[string]bool{}
	if s, _ := t.currentSymLocked(); s != "" {
		set[s] = true
	}
	for _, o := range t.orders {
		if o.Following || o.stopWatching() {
			set[o.Sym] = true
		}
	}
	out := make([]string, 0, len(set))
	for s := range set {
		out = append(out, s)
	}
	sortStrings(out)
	return out
}

func sortStrings(s []string) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j] < s[j-1]; j-- {
			s[j], s[j-1] = s[j-1], s[j]
		}
	}
}

func (t *Ticket) retrack() {
	t.mu.Lock()
	syms := t.trackedLocked()
	for _, s := range syms {
		t.book.ensure(s)
	}
	if cur, _ := t.currentSymLocked(); cur != t.lastSym {
		t.lastSym = cur
		t.note = "" // a banner note belongs to the symbol it was about
	}
	mdActs := append(t.syncMktDataLocked(), t.syncStopMDLocked()...)
	key := strings.Join(syms, ",")
	changed := key != t.tracked
	t.tracked = key
	t.mu.Unlock()
	run(mdActs)
	if changed && t.cfg.OnTracked != nil {
		t.cfg.OnTracked(syms)
	}
}

// Tracked is the symbol set the ticket needs live aggregates for.
func (t *Ticket) Tracked() []string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.trackedLocked()
}

func (t *Ticket) seedTracked(ctx context.Context, now time.Time) {
	if t.cfg.Seed == nil {
		return
	}
	for _, sym := range t.Tracked() {
		last, ok := t.book.lastSeed(sym)
		rdy, _ := t.book.ready(sym)
		if ok && now.Sub(last) < reseedEvery && (rdy || now.Sub(last) < 10*time.Second) {
			continue
		}
		t.mu.Lock()
		busy := t.seeding[sym]
		t.seeding[sym] = true
		t.mu.Unlock()
		if busy {
			continue
		}
		go func(sym string) {
			defer func() { t.mu.Lock(); delete(t.seeding, sym); t.mu.Unlock() }()
			sctx, cancel := context.WithTimeout(ctx, 20*time.Second)
			defer cancel()
			bars, err := t.cfg.Seed(sctx, sym)
			if err != nil {
				logx.L().Debug("order ticket: bar seed failed", "sym", sym, "err", err)
				t.book.seed(sym, nil, t.now().Add(-reseedEvery+30*time.Second)) // retry in 30 s
				return
			}
			t.book.seed(sym, bars, t.now())
		}(sym)
	}
}

// SeedBars is the synchronous seed (tests, and a pane asking for a refresh).
func (t *Ticket) SeedBars(sym string, bars []core.Bar) { t.book.seed(sym, bars, t.now()) }

// State is the snapshot the pane renders.
func (t *Ticket) State() State {
	now := t.now()
	t.mu.Lock()
	defer t.mu.Unlock()
	s := State{
		Conn: t.conn, ConnNote: t.connNote, Addr: fmt.Sprintf("%s:%d", t.settings.Host, t.settings.Port),
		ClientID: t.settings.ClientID, Account: t.account, Settings: t.settings, Note: t.note, Now: now.UnixMilli(),
		TWSSym: t.twsSym,
	}
	s.Live = t.account != "" && !strings.HasPrefix(strings.ToUpper(t.account), "D")
	s.Sym, s.SymSource = t.currentSymLocked()
	if s.Sym != "" {
		s.Quote = t.quoteLocked(s.Sym, now)
		s.EMAOK = t.emaUsableLocked(s.Quote)
		if ok, why := t.book.ready(s.Sym); !ok {
			s.EMANote = why
		}
	}
	if t.pending != nil {
		p := *t.pending
		s.Pending = &p
	}
	if m := t.funds[t.account]; len(m) > 0 {
		f := &Funds{Account: t.account, Avail: m["AvailableFunds"], Excess: m["ExcessLiquidity"],
			BuyingPower: m["BuyingPower"], NetLiq: m["NetLiquidation"]}
		_, f.HaveAvail = m["AvailableFunds"]
		f.Currency = t.baseCurLocked()
		nl, haveNL := m["NetLiquidation"]
		f.usdView(nl, haveNL, m[fxUSDKey])
		s.Funds = f
	}
	s.Currency = t.baseCurLocked()
	if s.Sym != "" && s.EMAOK {
		s.Sizing = t.sizingLocked(s.Quote)
	}
	if s.Sym != "" {
		s.CloseQty, _ = t.closeQtyLocked(s.Sym)
		s.CancelCount = t.cancelCountLocked(s.Sym)
		s.BECount = t.manualBECountLocked(s.Sym)
	}
	s.Short = t.shortInfoLocked(s.Sym, now)
	if s.Sym != "" && t.cfg.PowerSetup != nil {
		if aplus, rng, barMs, ok := t.cfg.PowerSetup(s.Sym); ok {
			g := &PsGrade{Aplus: aplus, BarMs: barMs, Mult: 1}
			if aplus {
				g.BaseRange, g.Mult = rng, core.PsAplusRiskMult
			}
			s.PsGrade = g
		}
	}
	s.Vwapx = t.vwapxLocked(s.Sym, s.Quote, now)
	s.VwapxBear = t.vwapxSideLocked("SELL", s.Sym, s.Quote, now)
	if p := t.previews[s.Sym]; p != nil && s.Sym != "" {
		cp := *p
		s.Preview = &cp
	}
	for _, o := range t.orders {
		active := o.parentActive() || o.childActive() || o.slActive() || o.stopWatching()
		if o.stopWatching() {
			s.StopWarn = "Soft stop needs the scanner + TWS running"
			if t.conn != "connected" {
				s.StopUnwatched = true
			}
		}
		if o.stopAlert != "" && s.StopAlert == "" {
			s.StopAlert = o.stopAlert
		}
		if o.tpAlert != "" && !finished(o.ChildStatus) {
			if s.StopAlert != "" {
				s.StopAlert += " · "
			}
			s.StopAlert += o.tpAlert
		}
		if o.blindAlert != "" {
			if s.StopAlert != "" {
				s.StopAlert += " · "
			}
			s.StopAlert += o.blindAlert
		}
		if !active && now.UnixMilli()-o.PlacedAt > 6*3600*1000 && o.PlacedAt != 0 {
			continue
		}
		s.Orders = append(s.Orders, OrderView{
			ID: o.ID, ChildID: o.ChildID, Sym: o.Sym, Side: o.Side, Qty: o.Qty, Limit: o.Limit, Filled: o.Filled,
			Avg: o.Avg, TP: o.TP, Status: o.Status, ChildStatus: o.ChildStatus, ChildFilled: o.ChildFilled,
			Following: o.Following, Note: o.Note, Active: active,
			StopLevel: o.StopLevel, StopState: o.StopState, StopOn: !o.StopOff, StopLabel: stopLabel(o), StopBE: o.BE, CanBE: o.manualBEOK(), PresetLabel: presetLabel(o),
			ExitID: o.ExitID, ExitQty: o.ExitQty, ExitFilled: o.ExitFilled, FollowLabel: t.followLabelLocked(o),
			CanStop: o.canMoveStop(), Kind: o.Kind, SLID: o.SLID, SL: o.SLPx, SLStatus: o.SLStatus, SLFilled: o.SLFilled,
		})
	}
	for i := 1; i < len(s.Orders); i++ {
		for j := i; j > 0 && s.Orders[j].ID > s.Orders[j-1].ID; j-- {
			s.Orders[j], s.Orders[j-1] = s.Orders[j-1], s.Orders[j]
		}
	}
	return s
}

func (t *Ticket) record(r auditRec) {
	if r.T == 0 {
		r.T = t.now().UnixMilli()
	}
	if err := t.audit.Append(r); err != nil {
		logx.L().Warn("order ticket audit write failed", "err", err)
	}
}
