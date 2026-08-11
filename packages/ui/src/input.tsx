import type { InputHTMLAttributes, TextareaHTMLAttributes } from "react";

import { cn } from "./lib/utils";

const field = cn(
  "w-full rounded-md border border-input bg-card text-foreground transition-[color,border-color,box-shadow]",
  "placeholder:text-subtle-foreground",
  "focus-visible:border-ring focus-visible:ring-[3px] focus-visible:ring-ring/25 focus-visible:outline-none",
  "disabled:cursor-not-allowed disabled:opacity-50",
  "aria-invalid:border-destructive aria-invalid:ring-destructive/25",
);

export function Input({ className, type, ...props }: InputHTMLAttributes<HTMLInputElement>) {
  return (
    <input
      className={cn(
        field,
        "h-9 px-3 py-1 text-sm",
        "file:mr-3 file:border-0 file:bg-transparent file:font-medium file:text-foreground file:text-sm",
        className,
      )}
      data-slot="input"
      type={type}
      {...props}
    />
  );
}

export function Textarea({ className, ...props }: TextareaHTMLAttributes<HTMLTextAreaElement>) {
  return (
    <textarea
      className={cn(field, "min-h-20 px-3 py-2 text-sm", className)}
      data-slot="textarea"
      {...props}
    />
  );
}
