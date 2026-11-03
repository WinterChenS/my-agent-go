import { clsx, type ClassValue } from "clsx";
import { twMerge } from "tailwind-merge";
import { cva } from "class-variance-authority";
import { Sparkles } from "lucide-react";

function cn(...inputs: ClassValue[]) {
  return twMerge(clsx(inputs));
}

const buttonVariants = cva(
  "inline-flex items-center gap-2 rounded-md px-3 py-2 text-sm font-medium",
  {
    variants: {
      intent: {
        primary: "bg-slate-900 text-white",
        ghost: "bg-transparent text-slate-700",
      },
    },
    defaultVariants: { intent: "primary" },
  }
);

export default function App() {
  return (
    <main className="flex min-h-screen flex-col items-center justify-center gap-4 p-8">
      <div className="flex items-center gap-2 text-slate-900">
        <Sparkles className="h-5 w-5" />
        <h1 className="text-2xl font-semibold">MyAgent</h1>
      </div>
      <p className="text-slate-500">Foundation runtime placeholder.</p>
      <button className={cn(buttonVariants({ intent: "primary" }))}>
        Get started
      </button>
    </main>
  );
}
