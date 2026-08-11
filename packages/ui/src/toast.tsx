"use client";

import type { ComponentProps, CSSProperties } from "react";
import { Toaster as SonnerToaster, toast } from "sonner";

/**
 * Global notification surface. Colours come from the shared tokens rather than
 * sonner's own palette, so a toast matches whichever theme the operator picked.
 */
export function Toaster({ ...props }: ComponentProps<typeof SonnerToaster>) {
  return (
    <SonnerToaster
      className="toaster group"
      style={
        {
          "--normal-bg": "var(--popover)",
          "--normal-border": "var(--border)",
          "--normal-text": "var(--popover-foreground)",
          "--error-bg": "var(--popover)",
          "--error-border": "var(--destructive)",
          "--error-text": "var(--destructive)",
          "--success-bg": "var(--popover)",
          "--success-border": "var(--success)",
          "--success-text": "var(--success)",
        } as CSSProperties
      }
      toastOptions={{
        classNames: {
          description: "text-muted-foreground",
          toast: "rounded-lg border shadow-[var(--glass-shadow)] font-sans",
        },
      }}
      {...props}
    />
  );
}

export { toast };
