import { dsn, release, environment } from "./config.mjs"

// Project error reports onto a fixed schema for this one fixture. The provider
// derives grouping from the stable fingerprint across distinct releases.
export function projectEvent(event) {
  if (event.exception?.values?.at(-1)?.type !== "ShippingThresholdError") return null
  return {
    event_id: event.event_id,
    timestamp: event.timestamp,
    platform: "javascript",
    level: "error",
    release,
    environment,
    fingerprint: ["checkout", "free-shipping-threshold-v1"],
    exception: { values: [{ type: "ShippingThresholdError", value: "Advertised free shipping was unavailable at the threshold" }] }
  }
}

export async function startReporting() {
  if (!dsn) return null
  const sdk = await import("./sentry-sdk.mjs")
  sdk.init({
    dsn, release, environment,
    defaultIntegrations: false,
    tracesSampleRate: 0,
    sendClientReports: false,
    maxBreadcrumbs: 0,
    beforeSend: projectEvent,
  })
  return async error => {
    sdk.captureException(error)
    return sdk.flush(3000)
  }
}
