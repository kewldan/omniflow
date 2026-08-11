import { Slot } from "@radix-ui/react-slot";
import { cva, type VariantProps } from "class-variance-authority";
import type { ButtonHTMLAttributes } from "react";

import { cn } from "./lib/utils";

/*
 * The design treats the foreground tone as the single accent: a filled button
 * is near-black on light and near-white on dark. Controls sit on 9px corners,
 * scale down slightly while pressed, and never rely on colour alone for state.
 */
const buttonVariants = cva(
  cn(
    "inline-flex shrink-0 items-center justify-center gap-2 rounded-md font-medium",
    "whitespace-nowrap outline-none transition-[background-color,color,border-color,transform]",
    "focus-visible:ring-[3px] focus-visible:ring-ring/45",
    "disabled:pointer-events-none disabled:opacity-50",
    "active:scale-[0.98]",
    "[&_svg]:pointer-events-none [&_svg]:shrink-0 [&_svg:not([class*='size-'])]:size-4",
  ),
  {
    defaultVariants: { size: "default", variant: "default" },
    variants: {
      size: {
        default: "h-9 px-4 text-sm",
        icon: "size-9",
        "icon-sm": "size-8 rounded-sm",
        lg: "h-11 rounded-lg px-6 text-[15px]",
        sm: "h-8 rounded-sm px-3 text-[13px]",
      },
      variant: {
        default: "bg-primary text-primary-foreground hover:bg-primary/90",
        destructive:
          "bg-destructive text-destructive-foreground hover:bg-destructive/90 focus-visible:ring-destructive/35",
        ghost: "hover:bg-accent hover:text-accent-foreground",
        link: "text-foreground underline underline-offset-4 hover:text-muted-foreground",
        outline: "border border-border bg-card hover:bg-accent hover:text-accent-foreground",
        secondary: "bg-secondary text-secondary-foreground hover:bg-secondary/80",
      },
    },
  },
);

export type ButtonProps = ButtonHTMLAttributes<HTMLButtonElement> &
  VariantProps<typeof buttonVariants> & {
    /** Render the child element instead of a `button`, for links styled as buttons. */
    asChild?: boolean;
  };

export function Button({ asChild = false, className, size, variant, ...props }: ButtonProps) {
  const Component = asChild ? Slot : "button";
  return (
    <Component
      className={cn(buttonVariants({ className, size, variant }))}
      data-slot="button"
      {...props}
    />
  );
}

export { buttonVariants };
