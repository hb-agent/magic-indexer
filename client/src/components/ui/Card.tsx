import { cn } from "@/lib/utils";
import { HTMLAttributes, forwardRef } from "react";
interface CardProps extends HTMLAttributes<HTMLDivElement> {}
export const Card = forwardRef<HTMLDivElement, CardProps>(({ className, ...props }, ref) => (<div ref={ref} className={cn("rounded-sm border border-[var(--border-default)] bg-[var(--bg-elevated)] transition-colors duration-150", className)} {...props} />));
Card.displayName = "Card";
export const CardHeader = forwardRef<HTMLDivElement, CardProps>(({ className, ...props }, ref) => (<div ref={ref} className={cn("flex flex-col space-y-1.5 p-6", className)} {...props} />));
CardHeader.displayName = "CardHeader";
export const CardTitle = forwardRef<HTMLHeadingElement, HTMLAttributes<HTMLHeadingElement>>(({ className, ...props }, ref) => (<h3 ref={ref} className={cn("font-[family-name:var(--font-garamond)] text-xl leading-none tracking-tight text-[var(--fg-primary)]", className)} {...props} />));
CardTitle.displayName = "CardTitle";
export const CardDescription = forwardRef<HTMLParagraphElement, HTMLAttributes<HTMLParagraphElement>>(({ className, ...props }, ref) => (<p ref={ref} className={cn("text-sm text-[var(--fg-muted)]", className)} {...props} />));
CardDescription.displayName = "CardDescription";
export const CardContent = forwardRef<HTMLDivElement, CardProps>(({ className, ...props }, ref) => (<div ref={ref} className={cn("p-6 pt-0", className)} {...props} />));
CardContent.displayName = "CardContent";
export const CardFooter = forwardRef<HTMLDivElement, CardProps>(({ className, ...props }, ref) => (<div ref={ref} className={cn("flex items-center p-6 pt-0", className)} {...props} />));
CardFooter.displayName = "CardFooter";
