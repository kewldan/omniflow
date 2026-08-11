import { requirePermissions } from "@/lib/server-session";

import { CustomerProfileView } from "./customer-profile";

export default async function CustomerDetailPage({
  params,
}: {
  params: Promise<{ customerId: string }>;
}) {
  const { customerId } = await params;
  await requirePermissions(["customers.read"], `/admin/customers/${customerId}`);
  return <CustomerProfileView customerId={customerId} />;
}
