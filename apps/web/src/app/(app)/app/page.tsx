"use client";
import { useRouter } from "next/navigation";
import { useEffect } from "react";
import { useMe } from "@/components/AppShell";

export default function AppIndex() {
  const me = useMe();
  const router = useRouter();
  const first = me.organizations[0]?.projects[0]?.id;
  useEffect(() => {
    if (first) router.replace(`/app/${first}`);
  }, [first, router]);
  return <p className="muted">{first ? "Opening your project…" : "You are not a member of any organization yet."}</p>;
}
