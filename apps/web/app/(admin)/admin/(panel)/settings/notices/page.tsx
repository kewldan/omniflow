import { requirePermissions } from "@/lib/server-session";

import { SettingsGroupHeader } from "../settings-group-header";
import { NoticeEditor } from "./notice-editor";

export default async function NoticesSettingsPage() {
  await requirePermissions(["settings.read"], "/admin/settings/notices");
  return (
    <div className="flex flex-col gap-5">
      <SettingsGroupHeader group="notices" />
      <NoticeEditor />
    </div>
  );
}
