# Investor Reporting

Many mini-grids are funded by development banks, climate funds, government programs, and private investors. These stakeholders need reliable portfolio-level reporting, not screenshots from an operations dashboard.

Investor reporting should be built after the operational data is trustworthy.

## Reporting Principle

Do not invent impact metrics from weak data.

Reports should show what AmbaGrid can defend:

- where the data came from
- when it was measured
- how the metric was calculated
- which assumptions were used
- whether any data was estimated

## Core Reports

### Portfolio Overview

Shows performance across many sites.

Useful metrics:

- number of active sites
- connected households
- connected businesses
- total meters
- active meters
- sites online
- sites delayed
- sites offline
- open critical alerts

### Reliability Report

Shows whether customers are actually receiving power.

Useful metrics:

- uptime by site
- outage count
- outage duration
- affected households
- mean time to detect
- mean time to resolve
- repeat outage sites

### Energy Report

Shows generation and consumption.

Useful metrics:

- energy generated
- energy delivered
- energy consumed by customers
- technical losses estimate
- peak demand
- battery state of charge history
- curtailed energy where available

### Revenue Report

Shows payment and credit health.

Useful metrics:

- total payments confirmed
- total credit issued
- active credit balances
- meters disconnected for non-payment
- meters reconnected after payment
- payment-to-credit failures
- command acknowledgement failures

### ESG Report

Shows development and climate impact.

Useful metrics:

- households connected
- businesses connected
- clinics or public facilities served
- clean energy delivered
- diesel displacement estimate where hybrid baseline exists
- CO2 avoided estimate with documented assumptions

## Impact Metrics API

The impact metrics API should expose portfolio and site-level summaries.

Potential consumers:

- investors
- donors
- government agencies
- operator finance teams
- internal business intelligence tools

API responses should include:

- metric value
- reporting period
- source data freshness
- calculation method
- estimate flag
- confidence or completeness score where possible

## Data Dependencies

Investor reporting depends on reliable records for:

- sites
- customers
- households
- meters
- telemetry
- generation
- outages
- payments
- credits
- meter commands
- alerts

If those records are incomplete, reports should say so.

## What To Avoid Early

Avoid:

- custom dashboards for every funder
- climate claims without assumptions
- CO2 calculations without a documented baseline
- manual spreadsheet exports as the primary workflow
- reporting that cannot be traced back to operational data

## First Build Target

The first reporting implementation should be simple:

```text
Site count
Connected households
Active meters
Energy delivered
Outage duration
Payments confirmed
Meters disconnected and reconnected
```

Add ESG calculations only after the source data and assumptions are explicit.
