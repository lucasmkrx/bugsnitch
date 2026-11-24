# 0003 · Private single-owner deployment

Bootstrap a high-entropy owner credential, exchange it for a short-lived HttpOnly session and derive the workspace from that session. Device credentials and workspace-scoped plugin grants use independent routes. An operator-provisioned plugin credential supports testing a private desktop install. The hosted OAuth resource accepts tokens from a configured authorization server with introspection, issuer, audience, expiry, scope and owner-subject checks. Register the plugin client at that authorization server.

This design makes one-owner setup possible without implementing account registration or an OAuth authorization server. Expanding to multiple accounts will require an explicit subject-to-account directory.

Each workspace still has one owner. The owner can now issue and revoke named operator and approver keys and scope them to a project. The hosted pilot provisions a separate instance per customer. [Workspace access](../OPERATIONS.md#workspace-controls-and-explicit-diff-sharing) · [Pilot provisioning](../OPERATIONS.md#provision-a-hosted-pilot-workspace)
