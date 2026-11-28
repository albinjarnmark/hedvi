"use client";

import { useState } from "react";
import { billingProfileFromForm, type BillingProfile } from "@/lib/billing-profile";
import { api, formatDate } from "@/lib/api";
import { useAction, useLoad } from "@/lib/hooks";

export type BillingOrder = {
  id: string;
  sku: string;
  currency: string;
  state: string;
  created_at: string;
  cancel_at?: string;
  paid_through?: string;
};

type Billing = {
  enabled: boolean;
  complimentary?: { plan: string; ends_at: string };
  baseline: string;
  has_credits: boolean;
  has_pro: boolean;
  currency: string;
  countries: string[];
  has_customer: boolean;
  billing_profile: BillingProfile | null;
  billing_profile_editable: boolean;
  orders: BillingOrder[];
};

export const productName: Record<string, string> = {
  flex: "Flex 10k",
  pro_50k: "Pro 50k",
  pro_100k: "Pro 100k",
};

export const orderState: Record<string, string> = {
  creating: "Preparing checkout",
  open: "Awaiting confirmation",
  paid: "Payment confirmed",
  active: "Subscription active",
  ended: "Subscription ended",
  expired: "Checkout expired",
  review: "Under review",
  reversed: "Payment reversed",
  payment_review: "Payment under review",
  past_due: "Payment past due",
  payment_pending: "Payment pending",
};

const countryNames = new Intl.DisplayNames(["en"], { type: "region" });
const countryName = (code: string) => countryNames.of(code) ?? code;

const prices: Record<string, Record<string, string>> = {
  sek: { flex: "39 kr once", pro_50k: "129 kr/month", pro_100k: "249 kr/month" },
  usd: { flex: "$4 once", pro_50k: "$13/month", pro_100k: "$25/month" },
};

export function BillingSettings({ project, preferred }: { project: string; preferred: string }) {
  const path = `/projects/${project}/billing`;
  const load = useLoad<Billing>(path, 30000);
  const { pending, error, run } = useAction();
  const [choice, setChoice] = useState(preferred.toLowerCase() === "sek" ? "sek" : "usd");
  const [sku, setSKU] = useState("flex");
  const [countryChoice, setCountryChoice] = useState<string | null>(null);

  if (load.error) {
    return <section className="card"><h2>Billing</h2><p role="alert">{load.error.message}</p></section>;
  }
  const data = load.data;
  const grant = data?.complimentary;
  const gift = grant && <p><strong>Complimentary {productName[grant.plan]} membership</strong> · Ends {formatDate(grant.ends_at)}. There is no charge or renewal. A paid Pro cycle takes precedence over a complimentary allowance.</p>;
  if (!data?.enabled) return gift ? <section className="card"><h2>Membership</h2>{gift}</section> : null;
  const currency = data.currency || choice;
  const country = countryChoice ?? data.billing_profile?.country ?? "";
  const regionRequired = country === "US" || country === "CA";
  const postalRequired = country !== "HK" && country !== "AE";
  const chooseBaseline = data.baseline === "flex" || (data.baseline === "free" && data.has_credits);

  function openStripe(action: "checkout" | "portal", body: Record<string, unknown>) {
    return run(async () => {
      try {
        const result = await api<{ url: string }>(`${path}/${action}`, { method: "POST", body });
        window.location.assign(result.url);
      } catch (error) {
        // A timed-out provider response may still have bound the invoice address.
        await load.reload();
        throw error;
      }
    });
  }

  return (
    <section className="card stack">
      <h2>Billing</h2>
      {gift}
      <p>Free includes 5,000 recipients per UTC calendar month at up to 50/hour and 200/day. Flex and Pro have no hourly or daily plan limits while their paid allowance is active. A Flex pack covers 10,000 recipients for 365 days. Pro includes 50,000 or 100,000 recipients per paid billing period. Your projects share the same allowance; traffic peaks are queued for delivery.</p>
      <form className="stack" onSubmit={event => {
        event.preventDefault();
        const profile = data.billing_profile && !data.billing_profile_editable ? data.billing_profile : billingProfileFromForm(new FormData(event.currentTarget));
        void openStripe("checkout", { sku, currency, billing_profile: profile, request_id: crypto.randomUUID() });
      }}>
        <div className="row">
          <div>
            <label htmlFor="billing-currency">Billing currency</label>
            <select id="billing-currency" value={currency} disabled={!!data.currency || pending} onChange={e => setChoice(e.target.value)}>
              <option value="usd">USD</option><option value="sek">SEK</option>
            </select>
          </div>
          <div>
            <label htmlFor="billing-product">Product</label>
            <select id="billing-product" value={sku} onChange={e => setSKU(e.target.value)}>
              {Object.entries(productName).map(([key, name]) => (
                <option key={key} value={key} disabled={data.has_pro && key !== "flex"}>{name} · {prices[currency]?.[key]}</option>
              ))}
            </select>
          </div>

        </div>
        {data.billing_profile && !data.billing_profile_editable ? (
          <div>
            <h3>Company billing address</h3>
            <p>{data.billing_profile.name}<br />{data.billing_profile.line1}
              {data.billing_profile.line2 && <><br />{data.billing_profile.line2}</>}
              <br />{data.billing_profile.postal_code} {data.billing_profile.city}
              {data.billing_profile.state && <><br />{data.billing_profile.state}</>}
              <br />{countryName(data.billing_profile.country)}
            </p>
            <p className="small muted">This address is used for invoices and tax. Contact support to change it. Stripe may ask for your legal business name when you provide a tax ID.</p>
          </div>
        ) : (
          <fieldset className="stack" disabled={pending}>
            <legend>Company billing address</legend>
            <p className="small muted">Enter the address of the company buying Hedvi. We validate and save it for invoices and tax before opening Stripe. You can correct an address that Stripe rejects. Once setup succeeds or its result is uncertain, contact support to change the saved address.</p>
            <label htmlFor="billing-name">Company name</label>
            <input required id="billing-name" name="name" defaultValue={data.billing_profile?.name} autoComplete="organization" maxLength={200} />
            <label htmlFor="billing-line1">Street address</label>
            <input required id="billing-line1" name="line1" defaultValue={data.billing_profile?.line1} autoComplete="billing address-line1" maxLength={200} />
            <label htmlFor="billing-line2">Address line 2 (optional)</label>
            <input id="billing-line2" name="line2" defaultValue={data.billing_profile?.line2} autoComplete="billing address-line2" maxLength={200} />
            <label htmlFor="billing-postal">Postal code{!postalRequired && " (if applicable)"}</label>
            <input required={postalRequired} id="billing-postal" name="postal_code" defaultValue={data.billing_profile?.postal_code} autoComplete="billing postal-code" maxLength={20} />
            <label htmlFor="billing-city">City</label>
            <input required id="billing-city" name="city" defaultValue={data.billing_profile?.city} autoComplete="billing address-level2" maxLength={200} />
            <label htmlFor="billing-state">State or region{!regionRequired && " (optional)"}</label>
            <input required={regionRequired} id="billing-state" name="state" defaultValue={data.billing_profile?.state} autoComplete="billing address-level1" maxLength={200} />
            <label htmlFor="billing-country">Company billing country</label>
            <select required id="billing-country" name="country" value={country} onChange={e => setCountryChoice(e.target.value)} autoComplete="billing country">
              <option value="" disabled>Select your country</option>
              {[...data.countries].sort((a, b) => countryName(a).localeCompare(countryName(b), "en")).map(country => (
                <option key={country} value={country}>{countryName(country)}</option>
              ))}
            </select>
          </fieldset>
        )}
        <p className="small muted">Your card can be issued in a different country. Payment details entered in Stripe do not change your company billing address.</p>
        <p className="small muted">
          {sku === "flex"
            ? "A first Flex purchase moves a Free organization to prepaid usage. Each pack includes 250 MiB of attachment data."
            : "The subscription renews monthly until canceled. Pro 50k includes 1 GiB of attachment data per cycle and Pro 100k includes 2 GiB."}
          {" "}Prices exclude VAT. Stripe shows your total before payment. Domain verification and sending limits still apply.
        </p>
        <button className="btn btn-primary" disabled={pending}>Continue to Stripe Checkout</button>
      </form>

      {chooseBaseline && (
        <div>
          <p>Outside a paid or complimentary membership, your organization uses {data.baseline === "flex" ? "Flex" : "Free"}.</p>
          <button className="btn" disabled={pending} onClick={() => void run(async () => {
            await api(path + "/baseline", { method: "POST", body: { plan: data.baseline === "flex" ? "free" : "flex" } });
            await load.reload();
          })}>
            Use {data.baseline === "flex" ? "Free monthly allowance" : "prepaid Flex credits"} outside Pro cycles
          </button>
        </div>
      )}
      {data.has_customer && (
        <button className="btn" disabled={pending} onClick={() => void openStripe("portal", {})}>
          Manage payment method, invoices or cancellation
        </button>
      )}
      {data.has_pro && <p className="small muted">To change Pro tiers, cancel at the end of the current period and purchase the new tier after the subscription ends.</p>}
      {error && <p role="alert" className="error">{error}</p>}
      {data.orders.length > 0 && (
        <div>
          <h3>Recent purchases</h3>
          <ul>{data.orders.slice(0, 5).map(order => (
            <li key={order.id}>
              {productName[order.sku]} · {orderState[order.state] ?? order.state} · {formatDate(order.created_at)}
              {order.cancel_at && <> · Cancellation scheduled {formatDate(order.cancel_at)}</>}
              {order.paid_through && <> · Paid through {formatDate(order.paid_through)}</>}
            </li>
          ))}</ul>
        </div>
      )}
    </section>
  );
}
