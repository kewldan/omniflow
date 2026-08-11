import { requirePermissions } from "@/lib/server-session";

import { OrderDetailView } from "./order-detail";

export default async function OrderDetailPage({
  params,
}: {
  params: Promise<{ orderId: string }>;
}) {
  const { orderId } = await params;
  await requirePermissions(["finance.read"], `/admin/finance/${orderId}`);
  return <OrderDetailView orderId={orderId} />;
}
