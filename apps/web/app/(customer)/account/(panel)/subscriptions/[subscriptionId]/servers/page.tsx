"use client";

import { useParams } from "next/navigation";

import { SmartSubscriptionScreen } from "@/components/account/smart-subscription";

/** Servers and routing for one subscription: the smart subscription screen. */
export default function SubscriptionServersPage() {
  const params = useParams<{ subscriptionId: string }>();
  return <SmartSubscriptionScreen subscriptionId={params.subscriptionId} />;
}
