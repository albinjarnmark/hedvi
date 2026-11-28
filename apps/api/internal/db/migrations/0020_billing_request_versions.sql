-- Preserve the exact Stripe request body across releases and uncertain retries.
-- Old writers omit this column and continue creating v1 intents. New writers
-- explicitly choose v2; a database default must not upgrade an old writer.
ALTER TABLE billing_customers ADD COLUMN request_version integer NOT NULL DEFAULT 1 CHECK (request_version IN (1,2));
ALTER TABLE billing_orders ADD COLUMN request_version integer NOT NULL DEFAULT 1 CHECK (request_version IN (1,2));
GRANT INSERT(request_version) ON billing_customers,billing_orders TO hedvi_api;

CREATE FUNCTION hedvi_billing_request_version_immutable() RETURNS trigger
LANGUAGE plpgsql SET search_path=pg_catalog,public AS $$
BEGIN
 IF NEW.request_version<>OLD.request_version THEN
   RAISE EXCEPTION 'immutable billing request version';
 END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER billing_customer_request_version BEFORE UPDATE OF request_version ON billing_customers
 FOR EACH ROW EXECUTE FUNCTION hedvi_billing_request_version_immutable();
CREATE TRIGGER billing_order_request_version BEFORE UPDATE OF request_version ON billing_orders
 FOR EACH ROW EXECUTE FUNCTION hedvi_billing_request_version_immutable();
