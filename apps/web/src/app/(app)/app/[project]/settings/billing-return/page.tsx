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

  return (
    <section className="card stack">
      <h1>Payment status</h1>
      {(!valid || result.error) && <p role="alert">{result.error?.message ?? "Order not found."}</p>}
      {valid && !result.error && (result.data ? (
        <>
          <h2>{productName[result.data.sku]}</h2>
          <p>{orderState[result.data.state] ?? result.data.state}</p>
          {state === "creating" && <p>Your checkout is being prepared. Return to Billing to resume it.</p>}
          {state === "open" && <p>No payment has been confirmed yet. If you left Checkout without paying, return to Billing to continue. If you paid, wait for confirmation here before trying again.</p>}
          {state === "payment_pending" && <p>Your payment is processing. You can leave this page and check Billing later. Wait for the result before trying again.</p>}
        </>
      ) : <p>Checking your order…</p>)}
      <Link href={`/app/${project}/settings`}>Back to settings</Link>
    </section>
  );
}
