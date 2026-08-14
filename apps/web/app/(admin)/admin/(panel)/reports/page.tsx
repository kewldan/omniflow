import { requirePermissions } from "@/lib/server-session";

import { SalesReportScreen } from "./sales-report";

export default async function ReportsPage() {
  await requirePermissions(["finance.read"], "/admin/reports");
  return <SalesReportScreen />;
}
