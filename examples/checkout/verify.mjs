import assert from "node:assert/strict"
import { shippingCents } from "./shipping.mjs"
assert.equal(shippingCents(4999), 500, "Orders below $50 pay $5 shipping")
assert.equal(shippingCents(5000), 0, "The advertised $50 threshold includes equality")
assert.equal(shippingCents(5001), 0, "Orders above $50 ship free")
console.log("All shipping acceptance checks passed")
