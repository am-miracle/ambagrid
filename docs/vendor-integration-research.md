# First vendor integrations

Researched 2026-10-25 against manufacturer/provider primary sources. The selected targets are Victron Energy Cerbo GX with a connected battery monitor, and Prism's PrismVend STS vending service. These are implementation targets; this research does not establish compatibility with installed customer hardware or a live vending account.

## Victron Cerbo GX / SmartShunt or BMV

The [GX Modbus-TCP manual](https://www.victronenergy.com/live/ccgx:modbustcp_faq) confirms Cerbo GX support. Enable Modbus TCP and obtain the connected battery service's Unit ID from the GX available-services menu. Unit 100 represents the overall system, not this battery register set. Function codes 3 and 4 both read registers. Some connected devices omit readings; avoid requesting unsupported register gaps. The manual defines exceptions 0x01 as unsupported function, 0x02 as missing register, 0x03 as invalid count, 0x0A as no device at a mapped port, and 0x0B as an unmapped Unit ID.

Victron's [server register definitions](https://github.com/victronenergy/dbus_modbustcp/blob/master/attributes.csv) specify these read-only `com.victronenergy.battery` fields. Decode signed values before dividing by the scale.

| Address | Field | Wire type | Divide by | Unit |
| --- | --- | --- | --- | --- |
| 259 | Battery voltage | uint16 | 100 | V |
| 261 | Battery current | int16 | 10 | A |
| 262 | Battery temperature | int16 | 10 | °C |
| 266 | State of charge | uint16 | 10 | % |

Implementation decision: start with configurable individual FC3 reads of these fields, normalize into existing domain readings, and timestamp successful acquisition locally. Implement the telemetry interface only; no command adapter is exposed. Use finite exchange deadlines and context cancellation. Validate framing and transaction/unit/function identifiers before accepting data. Report unavailable registers explicitly rather than manufacturing zero readings.

## PrismVend STS

The [official API, PR-D2-1112 revision 5.22.2](https://stsweb.prismcrypto.co.za/docs/PrismVend/PrismVend%20Web%20Vending%20API.pdf), sections 3.3, 4.4, 4.6 and 5.4.5–6, establishes:

- POST form fields to `/stsvend/VendCredit2.ini`: `meterId`, `subclass=0`, `value` in currency hundredths, and `messageId`. Optional `vendType` defaults to 1.
- Registered meters accept DRN11 or PAN16/17/18; blind vending accepts IDRecord35.
- `messageId` permits ASCII letters/digits and `_ - . ,`, maximum 40 characters. Reuse prevents another vend but returns a duplicate error, illustrated as HTTP 500, rather than the original token.
- Preserve ordered `tokenDec_1`…`tokenDec_N` (20 digits each) and `numTokens`: key-change tokens can accompany credit. Other outputs include `unitsActual`, `unitName`, `vendTimeUnix`, `idRecord`, and `toIdRecord`.
- `QueryTx` counts remaining module transactions; no transaction-result lookup is documented.
- The API provides no authentication and requires secured-network/localhost deployment. Vending requires the appropriate Security Module, keys and configured tariffs.

Implementation decision: keep STS vending on the platform side, separate from device command execution. Vend the payment's currency amount, not a kWh quantity. Persist the request identity before transmission and the complete token sequence after success. On ambiguous outcomes, retain that identity for reconciliation; never create a new one automatically. Require deployment configuration and test with local protocol fixtures; live provisioning and physical token acceptance remain commissioning work.
