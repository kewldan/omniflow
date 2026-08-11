import { requirePermissions } from "@/lib/server-session";

import { OperatorList } from "./operator-list";

export default async function OperatorsPage() {
  await requirePermissions(["admins.read"], "/admin/operators");
  return <OperatorList />;
}
