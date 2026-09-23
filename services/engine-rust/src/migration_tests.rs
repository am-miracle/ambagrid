use sqlx::{
    Acquire, PgPool, Postgres, Transaction,
    postgres::{PgArguments, PgPoolOptions},
    query,
    query::Query,
    query_as, query_scalar,
};
use uuid::Uuid;

use crate::MIGRATOR;

#[tokio::test]
#[ignore = "requires DATABASE_URL and a writable Postgres database"]
async fn assignment_constraints_reject_invalid_links() {
    let pool = migrated_pool().await;
    let mut tx = pool.begin().await.unwrap();
    let fixture = seed(&mut tx).await;

    assert_database_error(
        &mut tx,
        query("INSERT INTO meter_assignments (site_id, meter_id, customer_id) VALUES ($1, $2, $3)")
            .bind(&fixture.site_id)
            .bind(&fixture.meter_id)
            .bind(&fixture.other_site_customer_id),
        "23503",
    )
    .await;

    let assignment_id = open_assignment(&mut tx, &fixture, &fixture.customer_id).await;

    assert_database_error(
        &mut tx,
        query("INSERT INTO meter_assignments (site_id, meter_id, customer_id) VALUES ($1, $2, $3)")
            .bind(&fixture.site_id)
            .bind(&fixture.meter_id)
            .bind(&fixture.next_customer_id),
        "23505",
    )
    .await;

    assert_database_error(
        &mut tx,
        query("UPDATE meter_assignments SET customer_id = $1 WHERE assignment_id = $2")
            .bind(&fixture.next_customer_id)
            .bind(assignment_id),
        "23514",
    )
    .await;

    assert_database_error(
        &mut tx,
        query("UPDATE tariff_plans SET price_per_kwh = 300 WHERE tariff_plan_id = $1")
            .bind(&fixture.tariff_plan_id),
        "23514",
    )
    .await;

    tx.rollback().await.unwrap();
}

#[tokio::test]
#[ignore = "requires DATABASE_URL and a writable Postgres database"]
async fn reassignment_preserves_credit_history() {
    let pool = migrated_pool().await;
    let mut tx = pool.begin().await.unwrap();
    let fixture = seed(&mut tx).await;

    let original_assignment = open_assignment(&mut tx, &fixture, &fixture.customer_id).await;
    let source_id = format!("it-credit-{}", Uuid::new_v4().simple());
    query(
        "INSERT INTO energy_credits (site_id, assignment_id, tariff_plan_id, source_type, source_id, kwh_granted) VALUES ($1, $2, $3, 'adjustment', $4, 4)",
    )
    .bind(&fixture.site_id)
    .bind(original_assignment)
    .bind(&fixture.tariff_plan_id)
    .bind(&source_id)
    .execute(&mut *tx)
    .await
    .unwrap();

    close_assignment(&mut tx, original_assignment).await;
    let next_assignment = open_assignment(&mut tx, &fixture, &fixture.next_customer_id).await;

    let (credit_customer_id, credit_meter_id, credit_site_id): (String, String, String) = query_as(
        "SELECT a.customer_id, a.meter_id, a.site_id
             FROM energy_credits c
             JOIN meter_assignments a ON a.assignment_id = c.assignment_id
             WHERE c.source_id = $1",
    )
    .bind(&source_id)
    .fetch_one(&mut *tx)
    .await
    .unwrap();

    assert_eq!(credit_customer_id, fixture.customer_id);
    assert_eq!(credit_meter_id, fixture.meter_id);
    assert_eq!(credit_site_id, fixture.site_id);
    assert_ne!(next_assignment, original_assignment);

    tx.rollback().await.unwrap();
}

#[tokio::test]
#[ignore = "requires DATABASE_URL and a writable Postgres database"]
async fn credit_balance_survives_meter_leaving_service() {
    let pool = migrated_pool().await;
    let mut tx = pool.begin().await.unwrap();
    let fixture = seed(&mut tx).await;

    let assignment_id = open_assignment(&mut tx, &fixture, &fixture.customer_id).await;
    query("INSERT INTO credit_balances (assignment_id, remaining_kwh) VALUES ($1, 2)")
        .bind(assignment_id)
        .execute(&mut *tx)
        .await
        .unwrap();

    assert_database_error(
        &mut tx,
        query("DELETE FROM credit_balances WHERE assignment_id = $1").bind(assignment_id),
        "23514",
    )
    .await;

    close_assignment(&mut tx, assignment_id).await;

    let balance_intact: bool =
        query_scalar("SELECT remaining_kwh = 2 FROM credit_balances WHERE assignment_id = $1")
            .bind(assignment_id)
            .fetch_one(&mut *tx)
            .await
            .unwrap();
    assert!(balance_intact);

    let next_assignment = open_assignment(&mut tx, &fixture, &fixture.next_customer_id).await;
    let next_balances: i64 =
        query_scalar("SELECT count(*) FROM credit_balances WHERE assignment_id = $1")
            .bind(next_assignment)
            .fetch_one(&mut *tx)
            .await
            .unwrap();
    assert_eq!(next_balances, 0);

    query("UPDATE credit_balances SET remaining_kwh = 0 WHERE assignment_id = $1")
        .bind(assignment_id)
        .execute(&mut *tx)
        .await
        .unwrap();
    query("DELETE FROM credit_balances WHERE assignment_id = $1")
        .bind(assignment_id)
        .execute(&mut *tx)
        .await
        .unwrap();

    tx.rollback().await.unwrap();
}

#[tokio::test]
#[ignore = "requires DATABASE_URL and a writable Postgres database"]
async fn emergency_credit_advance_tracks_repayment() {
    let pool = migrated_pool().await;
    let mut tx = pool.begin().await.unwrap();
    let fixture = seed(&mut tx).await;

    let assignment_id = open_assignment(&mut tx, &fixture, &fixture.customer_id).await;
    let credit_id: Uuid = query_scalar(
        "INSERT INTO energy_credits (site_id, assignment_id, source_type, source_id, kwh_granted) VALUES ($1, $2, 'emergency_credit', $3, 5) RETURNING credit_id",
    )
    .bind(&fixture.site_id)
    .bind(assignment_id)
    .bind(format!("it-emergency-{}", Uuid::new_v4().simple()))
    .fetch_one(&mut *tx)
    .await
    .unwrap();

    let advance_id: Uuid = query_scalar(
        "INSERT INTO emergency_credit_advances (site_id, assignment_id, credit_id, tariff_plan_id, advanced_kwh, advanced_minor_units, outstanding_kwh) VALUES ($1, $2, $3, $4, 5, 125000, 5) RETURNING advance_id",
    )
    .bind(&fixture.site_id)
    .bind(assignment_id)
    .bind(credit_id)
    .bind(&fixture.tariff_plan_id)
    .fetch_one(&mut *tx)
    .await
    .unwrap();

    assert_database_error(
        &mut tx,
        query("UPDATE emergency_credit_advances SET outstanding_kwh = 6 WHERE advance_id = $1")
            .bind(advance_id),
        "23514",
    )
    .await;

    assert_database_error(
        &mut tx,
        query("UPDATE emergency_credit_advances SET outstanding_kwh = 0 WHERE advance_id = $1")
            .bind(advance_id),
        "23514",
    )
    .await;

    let payment_id: Uuid = query_scalar(
        "INSERT INTO payments (provider, external_reference, customer_id, amount_minor_units, currency, status, confirmed_at) VALUES ('paystack', $1, $2, 500000, 'NGN', 'confirmed', now()) RETURNING payment_id",
    )
    .bind(format!("it-ref-{}", Uuid::new_v4().simple()))
    .bind(&fixture.customer_id)
    .fetch_one(&mut *tx)
    .await
    .unwrap();

    query(
        "INSERT INTO emergency_credit_repayments (advance_id, payment_id, repaid_kwh, repaid_minor_units) VALUES ($1, $2, 5, 125000)",
    )
    .bind(advance_id)
    .bind(payment_id)
    .execute(&mut *tx)
    .await
    .unwrap();
    query(
        "UPDATE emergency_credit_advances SET outstanding_kwh = 0, settled_at = now() WHERE advance_id = $1",
    )
    .bind(advance_id)
    .execute(&mut *tx)
    .await
    .unwrap();

    assert_database_error(
        &mut tx,
        query(
            "INSERT INTO emergency_credit_repayments (advance_id, payment_id, repaid_kwh, repaid_minor_units) VALUES ($1, $2, 5, 125000)",
        )
        .bind(advance_id)
        .bind(payment_id),
        "23505",
    )
    .await;

    let settled: bool = query_scalar(
        "SELECT outstanding_kwh = 0 AND settled_at IS NOT NULL FROM emergency_credit_advances WHERE advance_id = $1",
    )
    .bind(advance_id)
    .fetch_one(&mut *tx)
    .await
    .unwrap();
    assert!(settled);

    tx.rollback().await.unwrap();
}

struct Fixture {
    site_id: String,
    customer_id: String,
    next_customer_id: String,
    other_site_customer_id: String,
    meter_id: String,
    tariff_plan_id: String,
}

async fn seed(tx: &mut Transaction<'_, Postgres>) -> Fixture {
    let suffix = Uuid::new_v4().simple().to_string();
    let fixture = Fixture {
        site_id: format!("it-site-{suffix}"),
        customer_id: format!("it-customer-a-{suffix}"),
        next_customer_id: format!("it-customer-b-{suffix}"),
        other_site_customer_id: format!("it-customer-other-{suffix}"),
        meter_id: format!("it-meter-{suffix}"),
        tariff_plan_id: format!("it-tariff-{suffix}"),
    };
    let operator_id = format!("it-operator-{suffix}");
    let other_site_id = format!("it-other-site-{suffix}");
    let asset_id = format!("it-asset-{suffix}");

    query("INSERT INTO grid_operators (operator_id, name) VALUES ($1, 'Integration Operator')")
        .bind(&operator_id)
        .execute(&mut **tx)
        .await
        .unwrap();
    query(
        "INSERT INTO sites (site_id, name, operator_id) VALUES ($1, 'Integration Site', $2), ($3, 'Other Integration Site', $2)",
    )
    .bind(&fixture.site_id)
    .bind(&operator_id)
    .bind(&other_site_id)
    .execute(&mut **tx)
    .await
    .unwrap();
    query(
        "INSERT INTO customers (customer_id, site_id, display_name) VALUES ($1, $2, 'Original Customer'), ($3, $2, 'Next Customer'), ($4, $5, 'Other Site Customer')",
    )
    .bind(&fixture.customer_id)
    .bind(&fixture.site_id)
    .bind(&fixture.next_customer_id)
    .bind(&fixture.other_site_customer_id)
    .bind(&other_site_id)
    .execute(&mut **tx)
    .await
    .unwrap();
    query(
        "INSERT INTO assets (asset_id, site_id, asset_type, last_seen_at) VALUES ($1, $2, 'smart_meter', now())",
    )
    .bind(&asset_id)
    .bind(&fixture.site_id)
    .execute(&mut **tx)
    .await
    .unwrap();
    query("INSERT INTO smart_meters (meter_id, site_id, asset_id) VALUES ($1, $2, $3)")
        .bind(&fixture.meter_id)
        .bind(&fixture.site_id)
        .bind(&asset_id)
        .execute(&mut **tx)
        .await
        .unwrap();
    query(
        "INSERT INTO tariff_plans (tariff_plan_id, site_id, name, currency, price_per_kwh, effective_from) VALUES ($1, $2, 'Integration Tariff', 'NGN', 250, now())",
    )
    .bind(&fixture.tariff_plan_id)
    .bind(&fixture.site_id)
    .execute(&mut **tx)
    .await
    .unwrap();

    fixture
}

async fn open_assignment(
    tx: &mut Transaction<'_, Postgres>,
    fixture: &Fixture,
    customer_id: &str,
) -> Uuid {
    query_scalar(
        "INSERT INTO meter_assignments (site_id, meter_id, customer_id) VALUES ($1, $2, $3) RETURNING assignment_id",
    )
    .bind(&fixture.site_id)
    .bind(&fixture.meter_id)
    .bind(customer_id)
    .fetch_one(&mut **tx)
    .await
    .unwrap()
}

async fn close_assignment(tx: &mut Transaction<'_, Postgres>, assignment_id: Uuid) {
    query("UPDATE meter_assignments SET ended_at = now() WHERE assignment_id = $1")
        .bind(assignment_id)
        .execute(&mut **tx)
        .await
        .unwrap();
}

async fn assert_database_error<'q>(
    tx: &mut Transaction<'_, Postgres>,
    statement: Query<'q, Postgres, PgArguments>,
    expected_code: &str,
) {
    let mut savepoint = tx.begin().await.unwrap();
    let error = statement.execute(&mut *savepoint).await.unwrap_err();
    let database_error = error.as_database_error().expect("expected database error");
    assert_eq!(database_error.code().as_deref(), Some(expected_code));
    savepoint.rollback().await.unwrap();
}

async fn migrated_pool() -> PgPool {
    let database_url =
        std::env::var("DATABASE_URL").expect("DATABASE_URL must be set for ignored test");
    let pool = PgPoolOptions::new()
        .max_connections(1)
        .connect(&database_url)
        .await
        .unwrap();
    MIGRATOR.run(&pool).await.unwrap();
    pool
}
