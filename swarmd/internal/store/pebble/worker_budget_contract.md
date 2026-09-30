# Worker budget enforcement

Worker policies are account/worker-scoped and separate from AI-editable worker definitions. Numeric caps default to zero (unset). Only authenticated, unscoped user HTTP ingress may change the policy; PUT requires the exact policy revision. Updates do not change account limits, provider/model settings, usage receipts, or reservations.

Admission and every integrated provider/media boundary resolve worker ownership through the existing corroborated durable usage lineage. Current UTC-day indexed receipt totals govern spending, not context occupancy. Catalog, provider and provider-estimate costs count once; nominal subscription-equivalent costs are not dollar charges. Unknown receipts block dollar-capped work.

A capped worker reserves its entire remaining allowance exclusively for one billable operation. This intentionally serializes capped worker descendants and runs rather than guessing output/price ceilings. Reservations are durable and survive restart and UTC rollover. Receipt revisions are written in the canonical receipt batch. Only a terminated operation with a newer canonical receipt revision and day projection can release its reservation; missing receipts do not trigger timed refunds. A daemon interruption can therefore require explicit accounting/recovery investigation before further capped work. No AI reset endpoint exists.

This is stop-before-next-call enforcement, **not an invoice-hard cap**. Already-dispatched work may exceed the remaining allowance before provider usage is returned. Late genuine canonical receipts remain counted irrespective of cancellation/failure. Transport that never supplies a receipt cannot be reported as measured zero.

Conservative limitations:

- Cost-capped media generation is rejected because the tool ingress currently lacks a dimension-specific verified pre-charge quote. Token-only policies use the canonical media receipt boundary.
- Capped background titles, direct one-shot Designer generation, AI-task metadata preparation and session-bound memory extraction are rejected where their existing callers have no canonical receipt persistence boundary.
- Provider watchdog retries for capped workers stop and return available usage for settlement before any new attempt.
- This implementation does not add invoice reconciliation or recover provider usage that never reaches an existing receipt boundary. Source-backed tests require parent execution; no live qualification is claimed.
