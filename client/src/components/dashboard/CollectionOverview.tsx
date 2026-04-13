"use client";

import { formatNumber } from "@/lib/utils";
import type { CollectionOverview as CollectionOverviewType } from "@/types";

interface CollectionOverviewProps {
  collections: CollectionOverviewType[];
  isLoading?: boolean;
}

export function CollectionOverview({ collections, isLoading }: CollectionOverviewProps) {
  return (
    <div className="space-y-4">
      <h3 className="font-[family-name:var(--font-garamond)] text-xl" style={{ color: 'var(--fg-primary)' }}>Collections</h3>
      <div className="rounded-sm" style={{ backgroundColor: 'var(--bg-elevated)', border: '1px solid var(--border-default)' }}>
        {isLoading ? (
          <div className="p-4 space-y-3">{[...Array(3)].map((_, i) => (<div key={i} className="h-8 rounded-sm skeleton" />))}</div>
        ) : collections.length === 0 ? (
          <div className="py-12 text-center text-sm" style={{ color: 'var(--fg-muted)' }}>No collections found</div>
        ) : (
          <div>
            {/* Header */}
            <div className="flex items-center px-4 py-2 text-xs font-medium uppercase tracking-wider" style={{ color: 'var(--fg-muted)', borderBottom: '1px solid var(--border-default)' }}>
              <span className="flex-1">Collection</span>
              <span className="w-24 text-right">Records</span>
              <span className="w-24 text-right">Invalid</span>
            </div>
            {/* Rows */}
            {collections.map((c) => (
              <div key={c.collection} className="flex items-center px-4 py-3" style={{ borderBottom: '1px solid var(--border-subtle)' }}>
                <span className="flex-1 text-sm font-mono truncate" style={{ color: 'var(--fg-primary)' }}>{c.collection}</span>
                <span className="w-24 text-right text-sm tabular-nums" style={{ color: 'var(--fg-secondary)', fontFeatureSettings: "'tnum' 1" }}>{formatNumber(c.recordCount)}</span>
                <span className="w-24 text-right text-sm tabular-nums" style={{ color: c.invalidCount > 0 ? 'var(--color-error)' : 'var(--fg-muted)', fontFeatureSettings: "'tnum' 1" }}>{c.invalidCount}</span>
              </div>
            ))}
          </div>
        )}
      </div>
    </div>
  );
}
