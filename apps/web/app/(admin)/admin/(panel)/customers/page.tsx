import { requirePermissions } from "@/lib/server-session";

import { AdvancedCustomerList } from "./advanced-list";
import { CustomerSearch } from "./customer-search";

export default async function CustomersPage() {
  await requirePermissions(["customers.read"], "/admin/customers");
  return (
    <div className="flex flex-col gap-5">
      <CustomerSearch />
      <AdvancedCustomerList />
    </div>
  );
}
