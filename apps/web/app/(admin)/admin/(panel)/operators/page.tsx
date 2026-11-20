import { requirePermissions } from "@/lib/server-session";

import { CustomRoles } from "./custom-roles";
import { OperatorList } from "./operator-list";

export default async function OperatorsPage() {
  await requirePermissions(["admins.read"], "/admin/operators");
  return (
    <div className="flex flex-col gap-5">
      <OperatorList />
      <CustomRoles />
    </div>
  );
}
