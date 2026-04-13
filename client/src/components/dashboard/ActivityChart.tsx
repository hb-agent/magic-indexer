"use client";

import type { ActivityBucket, TimeRange } from "@/types";
import { AreaChart, Area, XAxis, YAxis, CartesianGrid, Tooltip, ResponsiveContainer } from "recharts";
import { format } from "date-fns";

interface ActivityChartProps { data: ActivityBucket[]; timeRange: TimeRange; onTimeRangeChange: (range: TimeRange) => void; isLoading?: boolean; }

const timeRanges: { value: TimeRange; label: string }[] = [{ value: "ONE_HOUR", label: "1h" }, { value: "THREE_HOURS", label: "3h" }, { value: "SIX_HOURS", label: "6h" }, { value: "ONE_DAY", label: "24h" }, { value: "SEVEN_DAYS", label: "7d" }];

export function ActivityChart({ data, timeRange, onTimeRangeChange, isLoading }: ActivityChartProps) {
  const chartData = data.map((b) => ({ timestamp: b.timestamp, creates: b.creates, updates: b.updates, deletes: b.deletes, total: b.creates + b.updates + b.deletes }));
  return (
    <div className="space-y-4">
      <div className="flex items-center justify-between">
        <h3 className="font-[family-name:var(--font-garamond)] text-xl" style={{ color: 'var(--fg-primary)' }}>Activity</h3>
        <div className="flex items-center gap-1">
          {timeRanges.map((r) => (<button key={r.value} onClick={() => onTimeRangeChange(r.value)} className="px-2 py-0.5 rounded-sm text-xs transition-colors duration-150 cursor-pointer" style={{ color: timeRange === r.value ? 'var(--fg-primary)' : 'var(--fg-muted)', fontWeight: timeRange === r.value ? 500 : 400, backgroundColor: timeRange === r.value ? 'var(--overlay-weak)' : 'transparent' }}>{r.label}</button>))}
        </div>
      </div>
      <div className="rounded-sm p-4" style={{ backgroundColor: 'var(--bg-elevated)', border: '1px solid var(--border-default)' }}>
        {isLoading ? (<div className="h-48 rounded-sm skeleton" />) : data.length === 0 ? (<div className="flex h-48 items-center justify-center text-sm" style={{ color: 'var(--fg-muted)' }}>No activity data available</div>) : (
          <div className="h-48">
            <ResponsiveContainer width="100%" height="100%">
              <AreaChart data={chartData}>
                <CartesianGrid strokeDasharray="3 3" stroke="var(--border-subtle)" />
                <XAxis dataKey="timestamp" tickFormatter={(v) => format(new Date(v), timeRange === "SEVEN_DAYS" ? "MMM d" : "HH:mm")} fontSize={11} stroke="var(--fg-muted)" tickLine={false} axisLine={false} />
                <YAxis fontSize={11} stroke="var(--fg-muted)" tickLine={false} axisLine={false} />
                <Tooltip contentStyle={{ backgroundColor: "var(--bg-elevated)", border: "1px solid var(--border-default)", borderRadius: "2px", fontSize: "12px", boxShadow: "var(--shadow-md)", color: "var(--fg-secondary)" }} labelFormatter={(v) => format(new Date(v), "MMM d, yyyy HH:mm")} />
                <Area type="monotone" dataKey="creates" stackId="1" stroke="#111111" fill="#111111" fillOpacity={0.15} name="Creates" />
                <Area type="monotone" dataKey="updates" stackId="1" stroke="#7e7576" fill="#7e7576" fillOpacity={0.12} name="Updates" />
                <Area type="monotone" dataKey="deletes" stackId="1" stroke="#ba1a1a" fill="#ba1a1a" fillOpacity={0.08} name="Deletes" />
              </AreaChart>
            </ResponsiveContainer>
          </div>
        )}
      </div>
    </div>
  );
}
