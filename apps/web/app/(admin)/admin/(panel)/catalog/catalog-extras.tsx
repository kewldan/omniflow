"use client";

import { Skeleton } from "@omniflow/ui/skeleton";
import dynamic from "next/dynamic";

// The cards below the catalogue are loaded on demand, so the plan list an
// operator opens the page for does not wait for — or download — their editors.
const cardFallback = () => <Skeleton className="h-40 w-full" />;
const ServerCatalog = dynamic(
  () => import("./server-catalog").then((module) => module.ServerCatalog),
  { loading: cardFallback },
);

/** The server catalogue, below the plans. */
export function CatalogExtras() {
  return <ServerCatalog />;
}
