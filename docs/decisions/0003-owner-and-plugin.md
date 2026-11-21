# 0003 · Private single-owner deployment

Bootstrap a high-entropy owner credential, exchange it for a short-lived HttpOnly session and derive the workspace from that session. Device credentials and workspace-scoped plugin grants use independent routes. An operator-provisioned plugin credential supports testing a private desktop install. The hosted OAuth resource accepts tokens from a configured authorization server with introspection, issuer, audience, expiry, scope and owner-subject checks. Register the plugin client at that authorization server.

This design makes one-owner setup possible without implementing account registration or an OAuth authorization server. Expanding to multiple accounts will require an explicit subject-to-account directory.
