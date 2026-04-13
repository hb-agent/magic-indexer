"use client";

import { formatTimestamp } from "@/lib/utils";
import type { ActivityEntry } from "@/types";

interface RecentActivityProps { entries: ActivityEntry[]; isLoading?: boolean; }

function getRecordUrl(entry: ActivityEntry): string | null {
  if (!entry.rkey || entry.operation === "delete") return null;
  return `https://impactindexer.org/data?${new URLSearchParams({ did: entry.did, collection: entry.collection, rkey: entry.rkey }).toString()}`;
}

export function RecentActivity({ entries, isLoading }: RecentActivityProps) {
  return (
    <div className="space-y-4">
      <h3 className="font-[family-name:var(--font-garamond)] text-xl" style={{ color: 'var(--fg-primary)' }}>Recent Activity</h3>
      <div className="rounded-sm" style={{ backgroundColor: 'var(--bg-elevated)', border: '1px solid var(--border-default)' }}>
        {isLoading ? (<div className="p-4 space-y-3">{[...Array(5)].map((_, i) => (<div key={i} className="h-10 rounded-sm skeleton" />))}</div>) : entries.length === 0 ? (<div className="py-12 text-center text-sm" style={{ color: 'var(--fg-muted)' }}>No recent activity</div>) : (
          <div>{entries.slice(0, 10).map((entry) => {
            const recordUrl = getRecordUrl(entry);
            const opColors: Record<string, string> = { create: 'var(--color-success)', update: 'var(--fg-muted)', delete: 'var(--color-warning)' };
            const content = (<>
              <div className="flex items-center gap-3 min-w-0">
                <span className="w-2 h-2 rounded-full shrink-0" style={{ backgroundColor: entry.status === 'success' ? 'var(--color-success)' : entry.status === 'error' ? 'var(--color-error)' : 'var(--color-warning)' }} />
                <div className="min-w-0">
                  <div className="flex items-center gap-2">
                    <span className="px-1.5 py-0.5 rounded-sm text-[10px] font-medium uppercase tracking-wider text-white" style={{ backgroundColor: opColors[entry.operation] || 'var(--fg-muted)', opacity: 0.85 }}>{entry.operation}</span>
                    <span className="text-sm font-medium truncate" style={{ color: 'var(--fg-primary)' }}>{entry.collection}</span>
                  </div>
                  <p className="text-xs truncate font-mono" style={{ color: 'var(--fg-muted)' }}>{entry.did.slice(0, 32)}...</p>
                </div>
              </div>
              <div className="text-right shrink-0 ml-4">
                <p className="text-xs font-mono" style={{ color: 'var(--fg-muted)', fontFeatureSettings: "'tnum' 1" }}>{formatTimestamp(entry.timestamp)}</p>
                {entry.errorMessage && <p className="text-xs truncate max-w-[150px]" style={{ color: 'var(--color-error)' }}>{entry.errorMessage}</p>}
              </div>
            </>);
            const style = { borderBottom: '1px solid var(--border-subtle)' };
            if (recordUrl) return (<a key={entry.id} href={recordUrl} target="_blank" rel="noopener noreferrer" className="flex items-center justify-between px-4 py-3 hover:bg-[var(--overlay-weak)] transition-colors duration-150 cursor-pointer" style={style}>{content}</a>);
            return (<div key={entry.id} className="flex items-center justify-between px-4 py-3" style={style}>{content}</div>);
          })}</div>
        )}
      </div>
    </div>
  );
}
