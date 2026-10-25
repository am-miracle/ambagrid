# STS vending with PrismVend

The platform includes a durable manual vending command using PrismVend's
VendCredit2 API revision 5.22.2. Its protocol, provisioning requirements and
first-party documentation are recorded in [vendor research](vendor-integration-research.md).

Provision the PrismVend Security Module, meter registration, supply-group keys,
provider tariff, and currency first. The service has no built-in authentication:
use a secured operator network or an authenticated gateway, never a public endpoint.
Apply engine migrations, including `202610250002_sts_vends.sql`.

From `services/api-go`, set `DATABASE_URL`, `PRISMVEND_URL` (the provisioned base
URL), and `PRISMVEND_INSTALLATION` (a stable unique name for that installation):

```sh
go run ./cmd/sts-vend -reference purchase-unique-001 -meter 12345678901 -amount-minor 5001
```

The meter number is a provider-registered DRN/PAN or a 35-digit IDRecord, not an
AmbaGrid asset ID. The amount is **currency hundredths net of fees**, e.g. 5001
for 50.01 in the installation's configured currency. No currency conversion occurs.
The provider tariff determines the returned units; this command does not convert
platform kWh balances into tokens. Do not vend and separately apply the existing
kWh credit command for the same purchase.

The command prints JSON with the ordered token sequence, actual units and unit
name (which may be `hWh`, meaning 100 Wh), vend time and meter records. Deliver
all tokens in order, including any key-change tokens, using the operator's manual
workflow. Keep token output restricted to the intended operator/customer.
This does not mark a meter command acknowledged or update customer balances.
Automatic payment-to-token dispatch and physical token entry remain separate work.

The database reserves the reference before contacting PrismVend. Repeating a
completed request returns its saved tokens without contacting the provider.
Reusing the reference for different parameters or a different installation fails.
Keep the same installation name when changing its network address; do not reuse
an old name for a different vending backend.

A pending record means the outcome is uncertain, including a crash or malformed
response. The command refuses to send it again. Investigate the provider's
transaction records using the original reference; recover the complete ordered
token result through the operator's provider procedures. **Do not delete the
reservation or invent a fresh reference to retry an uncertain purchase.** There
is no documented automatic transaction-result recovery endpoint in this API.
Successful replies are persisted with a separate bounded context if the caller's
deadline expires. Tokens have only been exercised against local protocol fixtures;
production vending and physical acceptance require commissioning.
