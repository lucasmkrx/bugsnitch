import { shippingCents } from "./shipping.mjs"
import { startReporting } from "./telemetry.mjs"

const result = document.querySelector("output")
const submit = document.querySelector("button")
let report
try {
  report = await startReporting()
  document.querySelector("#status").textContent = report ? "Sentry reporting enabled for this test project." : "Sentry reporting is off."
} catch {
  document.querySelector("#status").textContent = "Test project reporting could not initialize."
}
submit.addEventListener("click", async () => {
  const fee = shippingCents(5000)
  result.textContent = fee === 0 ? "Free shipping applied." : "A $5.00 shipping fee was added."
  if (fee > 0 && report) {
    const error = new Error("Shipping offer mismatch")
    error.name = "ShippingThresholdError"
    const flushed = await report(error)
    document.querySelector("#status").textContent = flushed ? "The SDK finished sending queued reports. Verify receipt in Sentry." : "The SDK did not finish sending the report."
  }
})
