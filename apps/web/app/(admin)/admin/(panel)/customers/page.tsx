import { requirePermissions } from "@/lib/server-session";

import { CustomerSearch } from "./customer-search";

export default async function CustomersPage() {
  await requirePermissions(["customers.read"], "/admin/customers");
  return <CustomerSearch />;
}
