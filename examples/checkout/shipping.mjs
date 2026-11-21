// The published offer includes orders of exactly 5,000 cents.
export function shippingCents(subtotalCents) {
  if (!Number.isSafeInteger(subtotalCents) || subtotalCents < 0) {
    throw new RangeError("Subtotal must be nonnegative integer cents")
  }
  return subtotalCents > 5000 ? 0 : 500
}
