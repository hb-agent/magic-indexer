"use client";
import { cn } from "@/lib/utils";
import { InputHTMLAttributes, forwardRef } from "react";
interface InputProps extends InputHTMLAttributes<HTMLInputElement> { label?: string; error?: string; hint?: string; }
export const Input = forwardRef<HTMLInputElement, InputProps>(({ className, label, error, hint, id, ...props }, ref) => (<div className="space-y-1.5">{label && (<label htmlFor={id} className="block text-sm text-[var(--fg-secondary)]">{label}</label>)}<input id={id} ref={ref} className={cn("w-full h-11 px-4 text-sm rounded-sm bg-[var(--bg-elevated)] border border-[var(--border-default)] text-[var(--fg-primary)] placeholder:text-[var(--fg-muted)] focus:outline-none focus:border-[var(--border-hover)] focus:ring-1 focus:ring-[var(--fg-primary)]/10 transition-all duration-150 disabled:opacity-50 disabled:cursor-not-allowed", error && "border-[var(--color-error)]/40 focus:ring-[var(--color-error)]/20 focus:border-[var(--color-error)]/60", className)} {...props} />{hint && !error && (<p className="text-xs text-[var(--fg-muted)]">{hint}</p>)}{error && (<p className="text-sm text-[var(--color-error)]">{error}</p>)}</div>));
Input.displayName = "Input";
