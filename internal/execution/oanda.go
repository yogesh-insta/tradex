package execution

import (
	"context"
	"fmt"
	"log/slog"
	"math"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/yogesh-insta/tradex/internal/logging"
	"github.com/yogesh-insta/tradex/internal/oanda"
	"github.com/yogesh-insta/tradex/pkg/types"
	"github.com/yogesh-insta/tradex/pkg/utils"
)

// OANDAExecutor implements OrderExecutor against one OANDA account. It is the
// single writer to that account.
type OANDAExecutor struct {
	client    *oanda.Client
	accountID string
	account   string // logical account name (config)
	tif       string
	log       *slog.Logger
	pub       logging.Publisher

	metaMu sync.RWMutex
	meta   map[string]InstrumentMeta

	// stateMu guards the entry-context maps used to enrich reconciled trades
	// (policy + initial risk distance survive in RAM; OANDA is still truth
	// for what's open).
	stateMu   sync.Mutex
	byClient  map[string]entryContext // clientOrderID -> context
	byTradeID map[string]entryContext
}

type entryContext struct {
	clientOrderID string
	policy        types.ManagementPolicy
	riskDistance  float64
	strategy      string
	reason        string
}

const initialRiskCommentPrefix = "tradex:initial-risk="

// NewOANDAExecutor builds the executor. Call LoadInstruments before trading.
func NewOANDAExecutor(client *oanda.Client, accountID, accountName, timeInForce string, log *slog.Logger, pub logging.Publisher) *OANDAExecutor {
	return &OANDAExecutor{
		client:    client,
		accountID: accountID,
		account:   accountName,
		tif:       timeInForce,
		log:       log,
		pub:       pub,
		meta:      map[string]InstrumentMeta{},
		byClient:  map[string]entryContext{},
		byTradeID: map[string]entryContext{},
	}
}

// LoadInstruments fetches and caches instrument metadata at boot. Fails if a
// required instrument is missing — the system must not trade blind (spec 07).
func (e *OANDAExecutor) LoadInstruments(ctx context.Context, required []string) error {
	summary, err := e.client.AccountSummary(ctx, e.accountID)
	if err != nil {
		return fmt.Errorf("executor: load account currency: %w", err)
	}
	accountCurrency := strings.ToUpper(summary.Currency)
	if len(accountCurrency) != 3 {
		return fmt.Errorf("executor: invalid account currency %q", summary.Currency)
	}
	resp, err := e.client.Instruments(ctx, e.accountID, nil)
	if err != nil {
		return fmt.Errorf("executor: load instruments: %w", err)
	}
	available := make(map[string]oanda.RESTInstrument, len(resp.Instruments))
	for _, ri := range resp.Instruments {
		available[ri.Name] = ri
	}
	e.metaMu.Lock()
	defer e.metaMu.Unlock()
	for _, sym := range required {
		ri, ok := available[sym]
		if !ok {
			return fmt.Errorf("executor: required instrument %s missing from account %s", sym, e.account)
		}
		minUnits := float64(ri.MinimumTradeSize)
		if minUnits <= 0 {
			minUnits = 1
		}
		meta := InstrumentMeta{
			Symbol:         ri.Name,
			DisplayName:    ri.DisplayName,
			PricePrecision: ri.DisplayPrecision,
			PipLocation:    ri.PipLocation,
			UnitsPrecision: ri.TradeUnitsPrecision,
			MinUnits:       minUnits,
			MarginRate:     float64(ri.MarginRate),
		}
		pointValue, err := pointValueForAccount(meta, accountCurrency, available, func(instrument string) (float64, error) {
			return e.client.MidPrice(ctx, e.accountID, instrument)
		})
		if err != nil {
			return fmt.Errorf("executor: derive point value for %s: %w", sym, err)
		}
		meta.PointValue = pointValue
		e.meta[ri.Name] = meta
	}
	return nil
}

// pointValueForAccount converts one pip per unit into the account currency.
// An instrument's suffix is its quote/P&L currency (for example USD_JPY and
// DE30_EUR). OANDA's current midpoint converts that currency when necessary.
func pointValueForAccount(meta InstrumentMeta, accountCurrency string, available map[string]oanda.RESTInstrument, midPrice func(string) (float64, error)) (float64, error) {
	parts := strings.Split(meta.Symbol, "_")
	if len(parts) != 2 || len(parts[1]) != 3 {
		return 0, fmt.Errorf("cannot determine quote currency")
	}
	quoteCurrency := strings.ToUpper(parts[1])
	pipValue := math.Pow10(meta.PipLocation)
	if pipValue <= 0 {
		return 0, fmt.Errorf("invalid pip location %d", meta.PipLocation)
	}
	if quoteCurrency == accountCurrency {
		return pipValue, nil
	}

	var conversion string
	invert := false
	if len(parts[0]) == 3 && strings.EqualFold(parts[0], accountCurrency) {
		// USD_JPY in a USD account: 1 JPY = 1 / USD_JPY USD.
		conversion = strings.ToUpper(parts[0]) + "_" + quoteCurrency
		invert = true
	} else {
		direct := quoteCurrency + "_" + accountCurrency
		inverse := accountCurrency + "_" + quoteCurrency
		if _, ok := available[direct]; ok {
			conversion = direct
		} else if _, ok := available[inverse]; ok {
			conversion = inverse
			invert = true
		} else {
			return 0, fmt.Errorf("no direct conversion from %s to %s", quoteCurrency, accountCurrency)
		}
	}
	if _, ok := available[conversion]; !ok {
		return 0, fmt.Errorf("conversion instrument %s is unavailable", conversion)
	}
	mid, err := midPrice(conversion)
	if err != nil {
		return 0, fmt.Errorf("price %s: %w", conversion, err)
	}
	if mid <= 0 {
		return 0, fmt.Errorf("invalid midpoint for %s", conversion)
	}
	if invert {
		return pipValue / mid, nil
	}
	return pipValue * mid, nil
}

// Instrument implements OrderExecutor.
func (e *OANDAExecutor) Instrument(sym string) (InstrumentMeta, error) {
	e.metaMu.RLock()
	defer e.metaMu.RUnlock()
	m, ok := e.meta[sym]
	if !ok {
		return InstrumentMeta{}, fmt.Errorf("executor: unknown instrument %s", sym)
	}
	return m, nil
}

// AccountSummary exposes equity/margin for the portfolio manager.
func (e *OANDAExecutor) AccountSummary(ctx context.Context) (oanda.AccountSummary, error) {
	return e.client.AccountSummary(ctx, e.accountID)
}

// Open implements OrderExecutor: one atomic POST with bracket SL/TP and the
// clientExtensions.id idempotency key.
func (e *OANDAExecutor) Open(ctx context.Context, req types.OrderRequest) (types.OpenTrade, error) {
	// Idempotency: if a live trade already carries this client id, the entry
	// already happened (e.g. a retried request) — return it, place nothing.
	if existing, ok, err := e.findByClientID(ctx, req.ClientOrderID); err == nil && ok {
		e.log.Warn("duplicate open suppressed by client order id", "client_order_id", req.ClientOrderID)
		return existing, nil
	}

	meta, err := e.Instrument(req.Instrument)
	if err != nil {
		return types.OpenTrade{}, err
	}
	order := oanda.MarketOrder{
		Type:             req.OrderType,
		Instrument:       req.Instrument,
		Units:            strconv.FormatFloat(utils.RoundToPrecision(req.Units, meta.UnitsPrecision), 'f', -1, 64),
		TimeInForce:      e.tif,
		PositionFill:     "DEFAULT",
		TakeProfitOnFill: &oanda.OnFillPrice{Price: utils.FormatPrice(req.TakeProfit, meta.PricePrecision)},
		StopLossOnFill:   &oanda.OnFillPrice{Price: utils.FormatPrice(req.StopLoss, meta.PricePrecision)},
		ClientExtensions: &oanda.ClientExtensions{ID: req.ClientOrderID, Tag: req.Strategy},
	}
	if req.OrderType == types.OrderTypeLimit {
		order.Price = utils.FormatPrice(req.LimitPrice, meta.PricePrecision)
		order.TimeInForce = "GTD"
	}
	if req.Policy.Trail != "" {
		order.TrailingStopLossOnFill = &oanda.OnFillDistance{Distance: req.Policy.Trail}
	}

	resp, err := e.client.CreateOrder(ctx, e.accountID, order)
	if err != nil {
		e.publishReject(req, err.Error())
		return types.OpenTrade{}, fmt.Errorf("executor: open %s: %w", req.Instrument, err)
	}
	if resp.OrderRejectTransaction != nil {
		reason := resp.OrderRejectTransaction.RejectReason
		e.publishReject(req, reason)
		return types.OpenTrade{}, fmt.Errorf("executor: order rejected: %s", reason)
	}
	if resp.OrderFillTransaction == nil || resp.OrderFillTransaction.TradeOpened == nil {
		if resp.OrderCancelTransaction != nil {
			e.publishReject(req, resp.OrderCancelTransaction.Reason)
			return types.OpenTrade{}, fmt.Errorf("executor: order cancelled: %s", resp.OrderCancelTransaction.Reason)
		}
		// LIMIT orders rest unfilled — no OpenTrade yet (US path, future).
		return types.OpenTrade{}, fmt.Errorf("executor: order created but not filled (client id %s)", req.ClientOrderID)
	}

	fill := resp.OrderFillTransaction
	entry := float64(fill.TradeOpened.Price)
	trade := types.OpenTrade{
		TradeID:       fill.TradeOpened.TradeID,
		ClientOrderID: req.ClientOrderID,
		Account:       e.account,
		Instrument:    req.Instrument,
		Units:         float64(fill.TradeOpened.Units), // actual filled units (partial fills)
		Entry:         entry,
		CurrentSL:     req.StopLoss,
		CurrentTP:     req.TakeProfit,
		RiskDistance:  math.Abs(entry - req.StopLoss),
		Policy:        req.Policy,
		OpenedAt:      fill.Time,
	}
	// The fill price is not known when the market order is created, so persist
	// the exact initial distance on the resulting trade. This survives a
	// restart after the stop has moved to breakeven.
	if err := e.client.SetTradeClientExtensions(ctx, e.accountID, trade.TradeID, oanda.ClientExtensions{
		ID:      req.ClientOrderID,
		Tag:     req.Strategy,
		Comment: initialRiskComment(trade.RiskDistance),
	}); err != nil {
		// The trade is already live; retain RAM context for this process rather
		// than reporting it as unopened. Reconciliation still falls back safely
		// to the current stop if OANDA could not persist the extension.
		e.log.Error("persist initial risk distance failed", "trade_id", trade.TradeID, "error", err)
	}
	e.remember(trade, req)
	e.pub.Publish(types.TradeEvent{
		Type: "opened", TradeID: trade.TradeID, ClientOrderID: req.ClientOrderID,
		Account: e.account, Instrument: req.Instrument, Strategy: req.Strategy,
		Direction: req.Direction, Units: trade.Units, EntryPrice: entry,
		StopLoss: req.StopLoss, TakeProfit: req.TakeProfit, Reason: req.Reason,
		OpenTime: trade.OpenedAt, At: time.Now().UTC(),
	})
	e.log.Info("trade opened", "trade_id", trade.TradeID, "instrument", req.Instrument,
		"units", trade.Units, "entry", entry, "sl", req.StopLoss, "tp", req.TakeProfit)
	return trade, nil
}

// ModifyStop implements OrderExecutor.
func (e *OANDAExecutor) ModifyStop(ctx context.Context, tradeID string, price float64) error {
	inst, err := e.instrumentForTrade(ctx, tradeID)
	if err != nil {
		return err
	}
	meta, err := e.Instrument(inst)
	if err != nil {
		return err
	}
	err = e.client.SetTradeOrders(ctx, e.accountID, tradeID, oanda.TradeOrdersBody{
		StopLoss: &oanda.OnFillPrice{Price: utils.FormatPrice(price, meta.PricePrecision)},
	})
	if err != nil {
		return fmt.Errorf("executor: modify stop %s: %w", tradeID, err)
	}
	e.pub.Publish(types.TradeEvent{
		Type: "modified", TradeID: tradeID, Account: e.account,
		Instrument: inst, StopLoss: price, At: time.Now().UTC(),
	})
	return nil
}

// Close implements OrderExecutor (full market close).
func (e *OANDAExecutor) Close(ctx context.Context, tradeID string) error {
	resp, err := e.client.CloseTrade(ctx, e.accountID, tradeID)
	if err != nil {
		return fmt.Errorf("executor: close %s: %w", tradeID, err)
	}
	ev := types.TradeEvent{Type: "closed", TradeID: tradeID, Account: e.account, At: time.Now().UTC()}
	if fill := resp.OrderFillTransaction; fill != nil {
		ev.ExitPrice = float64(fill.Price)
		ev.RealizedPL = float64(fill.PL)
		ev.Instrument = fill.Instrument
		ev.CloseTime = fill.Time
	}
	e.pub.Publish(ev)
	e.log.Info("trade closed", "trade_id", tradeID, "realized_pl", ev.RealizedPL)
	return nil
}

// CancelOrder implements OrderExecutor (resting orders; US path, future).
func (e *OANDAExecutor) CancelOrder(ctx context.Context, clientOrderID string) error {
	resp, err := e.client.PendingOrders(ctx, e.accountID)
	if err != nil {
		return err
	}
	for _, o := range resp.Orders {
		if o.ClientExtensions != nil && o.ClientExtensions.ID == clientOrderID {
			return e.client.CancelOrder(ctx, e.accountID, o.ID)
		}
	}
	return nil // not found = already cancelled/filled (idempotent)
}

// CancelAllOrders cancels every pending order (FLATTEN support).
func (e *OANDAExecutor) CancelAllOrders(ctx context.Context) error {
	resp, err := e.client.PendingOrders(ctx, e.accountID)
	if err != nil {
		return err
	}
	for _, o := range resp.Orders {
		if err := e.client.CancelOrder(ctx, e.accountID, o.ID); err != nil {
			return err
		}
	}
	return nil
}

// OpenTrades implements OrderExecutor: OANDA is the source of truth; entry
// context is re-attached from RAM when known and initial risk is restored from
// the broker-visible trade extension after a restart.
func (e *OANDAExecutor) OpenTrades(ctx context.Context) ([]types.OpenTrade, error) {
	resp, err := e.client.OpenTrades(ctx, e.accountID)
	if err != nil {
		return nil, fmt.Errorf("executor: open trades: %w", err)
	}
	e.stateMu.Lock()
	defer e.stateMu.Unlock()
	out := make([]types.OpenTrade, 0, len(resp.Trades))
	for _, rt := range resp.Trades {
		t := types.OpenTrade{
			TradeID:    rt.ID,
			Account:    e.account,
			Instrument: rt.Instrument,
			Units:      float64(rt.CurrentUnits),
			Entry:      float64(rt.Price),
			OpenedAt:   rt.OpenTime,
		}
		if rt.ClientExtensions != nil {
			t.ClientOrderID = rt.ClientExtensions.ID
		}
		if rt.StopLossOrder != nil {
			t.CurrentSL = float64(rt.StopLossOrder.Price)
		}
		if rt.TakeProfitOrder != nil {
			t.CurrentTP = float64(rt.TakeProfitOrder.Price)
		}
		persistedRisk, hasPersistedRisk := initialRiskFromExtensions(rt.ClientExtensions)
		if ctxE, ok := e.byTradeID[rt.ID]; ok {
			t.Policy = ctxE.policy
			t.RiskDistance = ctxE.riskDistance
		} else if ctxE, ok := e.byClient[t.ClientOrderID]; ok {
			t.Policy = ctxE.policy
			t.RiskDistance = ctxE.riskDistance
			e.byTradeID[rt.ID] = ctxE
		} else if hasPersistedRisk {
			t.RiskDistance = persistedRisk
		} else {
			// Restart without RAM context: best-effort risk distance from the
			// current SL. Trades opened before initial-risk persistence was
			// deployed cannot recover the original distance after breakeven.
			t.RiskDistance = math.Abs(t.Entry - t.CurrentSL)
		}
		out = append(out, t)
	}
	return out, nil
}

func (e *OANDAExecutor) findByClientID(ctx context.Context, clientOrderID string) (types.OpenTrade, bool, error) {
	trades, err := e.OpenTrades(ctx)
	if err != nil {
		return types.OpenTrade{}, false, err
	}
	for _, t := range trades {
		if t.ClientOrderID == clientOrderID {
			return t, true, nil
		}
	}
	return types.OpenTrade{}, false, nil
}

func (e *OANDAExecutor) instrumentForTrade(ctx context.Context, tradeID string) (string, error) {
	trades, err := e.OpenTrades(ctx)
	if err != nil {
		return "", err
	}
	for _, t := range trades {
		if t.TradeID == tradeID {
			return t.Instrument, nil
		}
	}
	return "", fmt.Errorf("executor: trade %s not found (already closed?)", tradeID)
}

func (e *OANDAExecutor) remember(t types.OpenTrade, req types.OrderRequest) {
	e.stateMu.Lock()
	defer e.stateMu.Unlock()
	ec := entryContext{
		clientOrderID: req.ClientOrderID,
		policy:        req.Policy,
		riskDistance:  t.RiskDistance,
		strategy:      req.Strategy,
		reason:        req.Reason,
	}
	e.byClient[req.ClientOrderID] = ec
	e.byTradeID[t.TradeID] = ec
}

func initialRiskComment(distance float64) string {
	return initialRiskCommentPrefix + strconv.FormatFloat(distance, 'g', -1, 64)
}

func initialRiskFromExtensions(extensions *oanda.ClientExtensions) (float64, bool) {
	if extensions == nil || !strings.HasPrefix(extensions.Comment, initialRiskCommentPrefix) {
		return 0, false
	}
	distance, err := strconv.ParseFloat(strings.TrimPrefix(extensions.Comment, initialRiskCommentPrefix), 64)
	if err != nil || math.IsNaN(distance) || math.IsInf(distance, 0) || distance <= 0 {
		return 0, false
	}
	return distance, true
}

func (e *OANDAExecutor) publishReject(req types.OrderRequest, reason string) {
	e.pub.Publish(types.TradeEvent{
		Type: "rejected", ClientOrderID: req.ClientOrderID, Account: e.account,
		Instrument: req.Instrument, Strategy: req.Strategy, Direction: req.Direction,
		Units: req.Units, Reason: reason, At: time.Now().UTC(),
	})
}
