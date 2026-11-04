import { CircleCheckIcon, CircleHelpIcon, CircleXIcon, ShieldAlertIcon, TriangleAlertIcon } from "lucide-react";
import type { LeadEmail, LeadStatus } from "@/lib/api/models/app/leads/Lead";

export const STATUS_LABEL: Record<LeadStatus, string> = {
    new: "New",
    saved: "Saved",
    contacted: "Contacted",
    dismissed: "Dismissed",
};

export function emailVerdict(e?: LeadEmail): { label: string; tone: string; icon: React.ReactNode } {
    switch (e?.status) {
        case "valid":
            return { label: "Valid", tone: "text-emerald-700", icon: <CircleCheckIcon className="size-3" /> };
        case "risky":
            return { label: "Risky", tone: "text-amber-700", icon: <TriangleAlertIcon className="size-3" /> };
        case "invalid":
            return { label: "Invalid", tone: "text-rose-700", icon: <CircleXIcon className="size-3" /> };
        case "suppressed":
            return { label: "Suppressed", tone: "text-slate-500", icon: <ShieldAlertIcon className="size-3" /> };
        default:
            return { label: "Unchecked", tone: "text-slate-500", icon: <CircleHelpIcon className="size-3" /> };
    }
}
