import { requirePermissions } from "@/lib/server-session";

import { ContentPagesScreen } from "./content-pages";

export default async function ContentPage() {
  await requirePermissions(["marketing.read"], "/admin/content");
  return <ContentPagesScreen />;
}
