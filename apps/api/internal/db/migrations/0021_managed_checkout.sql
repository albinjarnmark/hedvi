-- Version 3 uses Stripe as merchant of record. Earlier intents retain their original seller and parameters.
ALTER TABLE billing_orders DROP CONSTRAINT billing_orders_request_version_check,
 ADD CONSTRAINT billing_orders_request_version_check CHECK (request_version IN (1, 2, 3));
