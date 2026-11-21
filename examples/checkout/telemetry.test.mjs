import assert from "node:assert/strict"
import test from "node:test"
import { projectEvent, startReporting } from "./telemetry.mjs"
test("offline fixture sends nothing and projects only the deliberate signal", async () => {
  assert.equal(await startReporting(), null)
  const event = { event_id:"example", request:{cookies:"PRIVATE_MARKER"}, extra:{input:"PRIVATE_MARKER"} }
  assert.equal(projectEvent(event), null)
  event.exception = {values:[{type:"ShippingThresholdError",value:"PRIVATE_MARKER",stacktrace:{frames:["PRIVATE_MARKER"]}}]}
  const projected = projectEvent(event)
  assert.equal(projected.fingerprint[0], "checkout")
  assert.ok(!JSON.stringify(projected).includes("PRIVATE_MARKER"))
})
