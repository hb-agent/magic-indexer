"use client";

import { formatNumber } from "@/lib/utils";

interface StatsCardsProps { recordCount: number; actorCount: number; lexiconCount: number; invalidCount?: number; isLoading?: boolean; }

export function StatsCards({ recordCount, actorCount, lexiconCount, invalidCount = 0, isLoading }: StatsCardsProps) {
  const stats = [
    { name: "Records", value: recordCount },
    { name: "Actors", value: actorCount },
    { name: "Lexicons", value: lexiconCount },
  ];
  return (
    <div className="flex flex-wrap items-center gap-x-6 gap-y-2 text-sm" role="status">
      {stats.map((stat, index) => (
        <div key={stat.name} className="flex items-center gap-2">
          {isLoading ? (<div className="h-5 w-16 rounded-sm skeleton" />) : (
            <><span className="font-semibold tabular-nums" style={{ color: 'var(--fg-primary)', fontFeatureSettings: "'tnum' 1" }}>{formatNumber(stat.value)}</span><span style={{ color: 'var(--fg-muted)' }}>{stat.name}</span></>
          )}
          <span className="ml-4" style={{ color: 'var(--border-default)' }}>&middot;</span>
        </div>
      ))}
      <div className="flex items-center gap-2">
        {isLoading ? (<div className="h-5 w-16 rounded-sm skeleton" />) : (
          <><span className="font-semibold tabular-nums" style={{ color: invalidCount > 0 ? 'var(--color-error)' : 'var(--fg-muted)', fontFeatureSettings: "'tnum' 1" }}>{invalidCount}</span><span style={{ color: 'var(--fg-muted)' }}>Invalid</span></>
        )}
      </div>
    </div>
  );
}
