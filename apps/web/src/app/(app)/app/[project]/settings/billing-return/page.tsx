"use client";

import Link from "next/link";
import { useSearchParams } from "next/navigation";
import { useProjectId } from "@/components/AppShell";
import { orderState, productName, type BillingOrder } from "@/components/BillingSettings";
import { useLoad } from "@/lib/hooks";

export default function BillingReturn() {
  const project = useProjectId();
  const order = useSearchParams().get("order");
  const valid = !!order && /^[0-9a-f]{8}(?:-[0-9a-f]{4}){3}-[0-9a-f]{12}$/i.test(order);
  const result = useLoad<BillingOrder>(valid ? `/projects/${project}/billing/orders/${order}` : null, 5000);
  const state = result.data?.state;
  const pending = state === "creating" || state === "open" || state === "payment_pending";

  return (
    <section className="card stack">
      <h1>Payment status</h1>
      {(!valid || result.error) && <p role="alert">{result.error?.message ?? "Order not found."}</p>}
      {valid && !result.error && (result.data ? (
        <>
          <h2>{productName[result.data.sku]}</h2>
          <p>{orderState[result.data.state] ?? result.data.state}</p>
          {pending && <p>Hedvi is waiting for Stripe to confirm your order. You can leave this page and check Billing later.</p>}
        </>
      ) : <p>Checking your order…</p>)}
      <Link href={`/app/${project}/settings`}>Back to settings</Link>
    </section>
  );
}
