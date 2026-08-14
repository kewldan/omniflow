import { requirePermissions } from "@/lib/server-session";

import { TrafficReportScreen } from "./traffic-report";

export default async function TrafficPage() {
  await requirePermissions(["customers.read"], "/admin/traffic");
  return <TrafficReportScreen />;
}
