import { requirePermissions } from "@/lib/server-session";

import { CatalogBrowser } from "./catalog-browser";
import { CodeBatchesScreen } from "./code-batches";

export default async function CatalogPage() {
  await requirePermissions(["catalog.read"], "/admin/catalog");
  return (
    <div className="flex flex-col gap-8">
      <CatalogBrowser />
      {/*
       * Wholesale batches sit under the catalogue because a batch decides what
       * is sold and at what agreed price, which is the same decision as
       * publishing a plan version. The money never moves through Omniflow — the
       * distributor paid outside it — so there is nothing here for the finance
       * permissions to be protecting.
       */}
      <CodeBatchesScreen />
    </div>
  );
}
