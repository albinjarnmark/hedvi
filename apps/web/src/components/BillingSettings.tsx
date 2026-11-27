"use client";

import { useState } from "react";
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
  baseline: string;
  has_credits: boolean;
  has_pro: boolean;
  currency: string;
  countries: string[];
  has_customer: boolean;
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

  if (load.error) {
    return <section className="card"><h2>Billing</h2><p role="alert">{load.error.message}</p></section>;
  }
  const data = load.data;
  if (!data?.enabled) return null;
  const currency = data.currency || choice;
  const chooseBaseline = data.baseline === "flex" || (data.baseline === "free" && data.has_credits);

  function openStripe(action: "checkout" | "portal", body: Record<string, unknown>) {
    return run(async () => {
      const result = await api<{ url: string }>(`${path}/${action}`, { method: "POST", body });
      window.location.assign(result.url);
    });
  }

  return (
    <section className="card stack">
      <h2>Billing</h2>
      <p>Free includes 5,000 recipients per UTC calendar month. A Flex pack covers 10,000 recipients for 365 days. Pro renews its allowance each paid billing period.</p>
      <form className="stack" onSubmit={event => {
        event.preventDefault();
        const country = new FormData(event.currentTarget).get("country");
        void openStripe("checkout", { sku, currency, country, request_id: crypto.randomUUID() });
      }}>
        <div className="row">
          <div>
            <label htmlFor="billing-currency">Billing currency</label>
            <select id="billing-currency" value={currency} disabled={!!data.currency || pending} onChange={e => setChoice(e.target.value)}>
              <option value="sek">SEK</option><option value="usd">USD</option>
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
          <div>
            <label htmlFor="billing-country">Billing country</label>
            <select required id="billing-country" name="country">
              {data.countries.map(country => (
                <option key={country} value={country}>{new Intl.DisplayNames(["en"], { type: "region" }).of(country) ?? country}</option>
              ))}
            </select>
          </div>
        </div>
        <p className="small muted">
          {sku === "flex"
            ? "A first Flex purchase moves a Free organization to prepaid usage. Each pack includes 250 MiB of attachment data."
            : "The subscription renews monthly until canceled. Pro 50k includes 1 GiB of attachment data per cycle and Pro 100k includes 2 GiB."}
          {" "}Prices exclude VAT. Stripe shows your total before payment. Your sender approval and sending limits still apply.
        </p>
        <button className="btn btn-primary" disabled={pending}>Continue to Stripe Checkout</button>
      </form>

      {chooseBaseline && (
        <div>
          <p>Outside a paid Pro cycle, your organization uses {data.baseline === "flex" ? "Flex" : "Free"}.</p>
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
