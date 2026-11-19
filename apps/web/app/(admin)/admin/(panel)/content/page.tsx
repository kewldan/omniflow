import { requirePermissions } from "@/lib/server-session";

import { ContentPagesScreen } from "./content-pages";
import { NewsEditorCard } from "./news-editor";

export default async function ContentPage() {
  await requirePermissions(["marketing.read"], "/admin/content");
  return (
    <div className="flex flex-col gap-5">
      <ContentPagesScreen />
      {/* News sits beside the pages because both are things the installation
          publishes for customers to read; unlike a page, a post is dated and
          counts towards the unread badge. */}
      <NewsEditorCard />
    </div>
  );
}
