package pebblestore

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"
)

// Unpriced receipts are not free, and they are not a reason to stop all work
// for the rest of the UTC day. Each unpriced receipt carries a conservative
// estimate, recorded once with the receipt so replacement subtracts exactly what
// was added. Daily cost caps compare known cost plus these estimates.
//
// Estimate rules (documented in docs and CHANGELOG):
//   - Model turn: its billed tokens at the highest input and output token rates
//     known for the model's family (catalog records and built-in rates of the
//     same provider whose model id shares the first name segment, such as
//     "gemini"); otherwise the provider's highest rates; otherwise the highest
//     rates known for any provider.
//   - Media: a fixed ceiling per receipt kind (unpricedMediaReceiptCeilingUSD).
//   - Receipts recorded before estimates existed carry no estimate and count at
//     unpricedLegacyReceiptCeilingUSD each.
var unpricedMediaReceiptCeilingUSD = map[string]float64{
	"image": 0.50,
	"audio": 1.00,
	"video": 10.00,
}

const (
	unpricedMediaOtherKindCeilingUSD = 10.00
	unpricedLegacyReceiptCeilingUSD  = 1.00
	unpricedCatalogScanLimit         = 5000
)

func receiptUnpricedForBudget(u SessionTurnUsageSnapshot) bool {
	return unknownBudgetReceipt(u.PriceStatus) != 0 || usageUnknownCount(u) != 0
}

// stampTurnUnpricedEstimate replaces any caller-supplied estimate: only the
// store decides how much an unpriced receipt may cost.
func (s *SessionStore) stampTurnUnpricedEstimate(u *SessionTurnUsageSnapshot) {
	u.UnpricedEstimateUSD = nil
	if !receiptUnpricedForBudget(*u) {
		return
	}
	value := s.unpricedTurnEstimateUSD(*u)
	u.UnpricedEstimateUSD = &value
}

func stampMediaUnpricedEstimate(m *SessionMediaUsageRecord) {
	m.UnpricedEstimateUSD = nil
	if unknownBudgetReceipt(m.PriceStatus) == 0 {
		return
	}
	value := unpricedMediaCeilingUSD(m.Kind)
	m.UnpricedEstimateUSD = &value
}

func unpricedMediaCeilingUSD(kind string) float64 {
	if value, ok := unpricedMediaReceiptCeilingUSD[strings.ToLower(strings.TrimSpace(kind))]; ok {
		return value
	}
	return unpricedMediaOtherKindCeilingUSD
}

// unpricedMediaAdmissionUSD is what an unpriced media request reserves under a
// daily cost cap: the receipt ceiling of its kind once per requested output,
// never less than any partial estimate the caller resolved.
func unpricedMediaAdmissionUSD(estimate MediaCostEstimate) float64 {
	cost := unpricedMediaCeilingUSD(estimate.AdmissionKind) * float64(max(estimate.AdmissionCount, 1))
	if validTokenRate(estimate.CostUSD) && estimate.CostUSD > cost {
		return estimate.CostUSD
	}
	return cost
}

func (s *SessionStore) unpricedTurnEstimateUSD(u SessionTurnUsageSnapshot) float64 {
	_, input, output, read, write, thinking := billedComponents(u)
	in, out := s.highestTokenRates(u.Provider, u.Model)
	regular := uncachedInputTokens(u.Provider, input, read)
	inputTokens := float64(max(regular, 0) + max(read, 0) + max(write, 0))
	outputTokens := float64(max(output, 0) + max(thinking, 0))
	cost := (inputTokens*in + outputTokens*out) / 1e6
	if math.IsNaN(cost) || math.IsInf(cost, 0) || cost < 0 {
		return unpricedLegacyReceiptCeilingUSD
	}
	return cost
}

func modelFamily(model string) string {
	model = strings.ToLower(strings.TrimSpace(model))
	if i := strings.LastIndex(model, "/"); i >= 0 {
		model = model[i+1:]
	}
	if i := strings.Index(model, "-"); i > 0 {
		return model[:i]
	}
	return model
}

// highestTokenRates returns per-million input and output rates no lower than
// any rate known for the model's family, falling back to wider sets when the
// narrower one has no rates.
func (s *SessionStore) highestTokenRates(provider, model string) (float64, float64) {
	provider = strings.ToLower(strings.TrimSpace(provider))
	if provider == "vertex" {
		provider = "google"
	}
	family := modelFamily(model)
	type rates struct{ in, out float64 }
	var familyRates, providerRates, allRates rates
	consider := func(r *rates, in, out float64) {
		if validTokenRate(in) && in > r.in {
			r.in = in
		}
		if validTokenRate(out) && out > r.out {
			r.out = out
		}
	}
	if s != nil && s.store != nil && provider != "" {
		records, err := NewModelCatalogStore(s.store).ListProvider(provider, unpricedCatalogScanLimit)
		if err == nil {
			for _, rec := range records {
				var p struct {
					Input  *float64 `json:"input_price_per_million_tokens"`
					Output *float64 `json:"output_price_per_million_tokens"`
				}
				if len(rec.Pricing) == 0 || json.Unmarshal(rec.Pricing, &p) != nil {
					continue
				}
				in, out := 0.0, 0.0
				if p.Input != nil {
					in = *p.Input
				}
				if p.Output != nil {
					out = *p.Output
				}
				consider(&providerRates, in, out)
				if family != "" && modelFamily(rec.Model) == family {
					consider(&familyRates, in, out)
				}
			}
		}
	}
	for key, p := range baselinePricingTable {
		consider(&allRates, p.InputPricePerMillion, p.OutputPricePerMillion)
		keyProvider, keyModel, _ := strings.Cut(key, ":")
		if keyProvider != provider {
			continue
		}
		consider(&providerRates, p.InputPricePerMillion, p.OutputPricePerMillion)
		if family != "" && modelFamily(keyModel) == family {
			consider(&familyRates, p.InputPricePerMillion, p.OutputPricePerMillion)
		}
	}
	for _, r := range []rates{familyRates, providerRates} {
		if r.in > 0 && r.out > 0 {
			return r.in, r.out
		}
	}
	return max(allRates.in, providerRates.in, familyRates.in), max(allRates.out, providerRates.out, familyRates.out)
}

func unpricedContribution(estimate *float64) (float64, int64) {
	if estimate == nil || math.IsNaN(*estimate) || math.IsInf(*estimate, 0) || *estimate < 0 {
		return 0, 0
	}
	return *estimate, 1
}

func unpricedEstimateTotal(unknown, estimated int64, recordedUSD float64) float64 {
	legacy := unknown - estimated
	if legacy < 0 {
		legacy = 0
	}
	return max(recordedUSD, 0) + float64(legacy)*unpricedLegacyReceiptCeilingUSD
}

func (acc *DailyUsageAccumulator) addUnpricedEstimate(estimate *float64, sign int64) {
	value, count := unpricedContribution(estimate)
	acc.UnpricedEstimateRecordedUSD = max(acc.UnpricedEstimateRecordedUSD+float64(sign)*value, 0)
	acc.UnpricedEstimateRecordedReceipts = max(acc.UnpricedEstimateRecordedReceipts+sign*count, 0)
}

// UnpricedEstimateUSD is the conservative cost counted for today's unpriced
// receipts on top of TotalCostUSD.
func (acc DailyUsageAccumulator) UnpricedEstimateUSD() float64 {
	return unpricedEstimateTotal(acc.UnknownReceipts, acc.UnpricedEstimateRecordedReceipts, acc.UnpricedEstimateRecordedUSD)
}

func (t *UsageScopeTotal) addUnpricedEstimate(estimate *float64, sign int64) {
	value, count := unpricedContribution(estimate)
	t.UnpricedEstimateRecordedUSD = max(t.UnpricedEstimateRecordedUSD+float64(sign)*value, 0)
	t.UnpricedEstimateRecordedReceipts = max(t.UnpricedEstimateRecordedReceipts+sign*count, 0)
}

// UnpricedEstimateUSD is the conservative cost counted for the scope's
// unpriced receipts on top of its known cost.
func (t UsageScopeTotal) UnpricedEstimateUSD() float64 {
	return unpricedEstimateTotal(t.UnknownReceipts, t.UnpricedEstimateRecordedReceipts, t.UnpricedEstimateRecordedUSD)
}

// GetTodayUnpricedUsage reports today's (UTC) unpriced receipts and the
// conservative estimate the daily cost cap counts for them.
func (s *SessionStore) GetTodayUnpricedUsage(accountScopeID string) (int64, float64, error) {
	if s == nil || s.store == nil {
		return 0, 0, errors.New("store is not configured")
	}
	acc, _, err := s.GetDailyUsageAccumulator(accountScopeID, time.Now().UTC().Format("2006-01-02"))
	if err != nil {
		return 0, 0, err
	}
	return acc.UnknownReceipts, acc.UnpricedEstimateUSD(), nil
}

func unpricedLimitDetail(receipts int64, estimate float64) string {
	if receipts <= 0 {
		return ""
	}
	return fmt.Sprintf(" (includes $%.2f estimated for %d unpriced receipts)", estimate, receipts)
}

func unpricedAdmissionDetail(unpriced bool, cost float64) string {
	if !unpriced {
		return ""
	}
	return fmt.Sprintf(" (unpriced media request counted at $%.2f)", cost)
}
