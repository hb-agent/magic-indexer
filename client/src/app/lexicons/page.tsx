"use client";

import { useState, useMemo } from "react";
import { useQuery, useMutation, useQueryClient } from "@tanstack/react-query";
import { graphqlClient } from "@/lib/graphql/client";
import { GET_LEXICONS } from "@/lib/graphql/queries";
import { REGISTER_LEXICON, DELETE_LEXICON } from "@/lib/graphql/mutations";
import { Button } from "@/components/ui/Button";
import { Input } from "@/components/ui/Input";
import { Alert } from "@/components/ui/Alert";
import type { LexiconsResponse, Lexicon } from "@/types";

// NSID validation
function isValidNsid(nsid: string): boolean {
  const parts = nsid.split(".");
  if (parts.length < 3) return false;
  return parts.every((p) => /^[a-z][a-z0-9-]*$/i.test(p));
}

// Tree node structure
interface TreeNode {
  name: string;
  fullPath: string;
  lexicon?: Lexicon;
  children: Map<string, TreeNode>;
}

// Build hierarchical tree from flat lexicon list
function buildTree(lexicons: Lexicon[]): Map<string, TreeNode> {
  const root = new Map<string, TreeNode>();

  for (const lexicon of lexicons) {
    const parts = lexicon.id.split(".");
    const rootKey = parts.slice(0, 2).join(".");
    const remaining = parts.slice(2);

    if (!root.has(rootKey)) {
      root.set(rootKey, { name: rootKey, fullPath: rootKey, children: new Map() });
    }

    let current = root.get(rootKey)!.children;
    let path = rootKey;

    for (let i = 0; i < remaining.length; i++) {
      const part = remaining[i];
      path = `${path}.${part}`;

      if (!current.has(part)) {
        current.set(part, { name: part, fullPath: path, children: new Map() });
      }

      const node = current.get(part)!;
      if (i === remaining.length - 1) {
        node.lexicon = lexicon;
      }
      current = node.children;
    }
  }

  return root;
}

// Count leaf nodes
function countLeaves(node: TreeNode): number {
  let count = node.lexicon ? 1 : 0;
  for (const child of node.children.values()) {
    count += countLeaves(child);
  }
  return count;
}

// Get description from lexicon JSON
function getDescription(lexicon: Lexicon): string | null {
  try {
    const parsed = JSON.parse(lexicon.json);
    return parsed?.defs?.main?.description || parsed?.description || null;
  } catch {
    return null;
  }
}

// Tree Branch Component
function TreeBranch({
  node,
  isLast = false,
  prefix = "",
  isRoot = false,
  onDelete,
  deletingNsid,
  expandedId,
  onToggleExpand,
}: {
  node: TreeNode;
  isLast?: boolean;
  prefix?: string;
  isRoot?: boolean;
  onDelete: (nsid: string) => void;
  deletingNsid: string | null;
  expandedId: string | null;
  onToggleExpand: (id: string) => void;
}) {
  const [collapsed, setCollapsed] = useState(false);
  const children = Array.from(node.children.entries()).sort(([a], [b]) => a.localeCompare(b));
  const hasChildren = children.length > 0;
  const branch = isLast ? "└── " : "├── ";
  const childPrefix = prefix + (isLast ? "    " : "│   ");

  // Root authority node (e.g., "org.impactindexer")
  if (isRoot) {
    return (
      <div className="mb-3 last:mb-0">
        <button
          onClick={() => setCollapsed(!collapsed)}
          className="flex items-center gap-2 group py-0.5"
        >
          <span
            className="text-zinc-300 text-xs transition-transform duration-200"
            style={{ transform: collapsed ? "rotate(-90deg)" : "rotate(0deg)" }}
          >
            ▾
          </span>
          <span className="font-mono text-sm font-medium text-zinc-700">{node.name}</span>
          <span className="text-zinc-300 text-xs">{countLeaves(node)}</span>
        </button>
        {!collapsed && hasChildren && (
          <div className="mt-1">
            {children.map(([key, child], i) => (
              <TreeBranch
                key={key}
                node={child}
                isLast={i === children.length - 1}
                prefix="    "
                onDelete={onDelete}
                deletingNsid={deletingNsid}
                expandedId={expandedId}
                onToggleExpand={onToggleExpand}
              />
            ))}
          </div>
        )}
      </div>
    );
  }

  // Leaf node with lexicon
  if (node.lexicon) {
    const isExpanded = expandedId === node.lexicon.id;
    const isDeleting = deletingNsid === node.lexicon.id;
    const description = getDescription(node.lexicon);

    return (
      <div>
        <div className="group flex items-center py-0.5 hover:bg-zinc-50/50 -mx-1 px-1 rounded transition-colors">
          <span className="font-mono text-xs text-zinc-200 whitespace-pre select-none shrink-0 hidden sm:inline">
            {prefix}{branch}
          </span>
          <button
            onClick={() => onToggleExpand(node.lexicon!.id)}
            className="font-mono text-sm text-emerald-600 hover:text-emerald-700 transition-colors text-left"
          >
            {node.name}
          </button>
          {description && (
            <span className="text-xs text-zinc-300 ml-2 truncate hidden sm:inline">
              {description}
            </span>
          )}
          <button
            onClick={(e) => {
              e.stopPropagation();
              if (!isDeleting) onDelete(node.lexicon!.id);
            }}
            disabled={isDeleting}
            className="opacity-0 group-hover:opacity-100 ml-auto p-1 text-zinc-300 hover:text-red-400 transition-all disabled:opacity-50"
            title={`Delete ${node.lexicon.id}`}
          >
            {isDeleting ? (
              <div className="w-3 h-3 rounded-full border-2 border-zinc-300 border-t-zinc-500 animate-spin" />
            ) : (
              <svg className="w-3 h-3" fill="none" viewBox="0 0 24 24" strokeWidth={1.5} stroke="currentColor">
                <path strokeLinecap="round" strokeLinejoin="round" d="M6 18 18 6M6 6l12 12" />
              </svg>
            )}
          </button>
        </div>
        {isExpanded && (
          <div className="ml-4 sm:ml-8 mt-2 mb-3">
            <pre className="text-xs bg-zinc-50 text-zinc-600 p-3 rounded-lg overflow-x-auto border border-zinc-100">
              {JSON.stringify(JSON.parse(node.lexicon.json), null, 2)}
            </pre>
          </div>
        )}
      </div>
    );
  }

  // Intermediate directory node
  return (
    <div>
      <div className="flex items-center py-0.5 hover:bg-zinc-50/50 -mx-1 px-1 rounded transition-colors">
        <span className="font-mono text-xs text-zinc-200 whitespace-pre select-none shrink-0 hidden sm:inline">
          {prefix}{branch}
        </span>
        <button
          onClick={() => setCollapsed(!collapsed)}
          className="flex items-center"
        >
          <span className="font-mono text-sm text-zinc-500">{node.name}</span>
          <span
            className="text-zinc-300 text-[10px] ml-1 transition-transform duration-200"
            style={{ transform: collapsed ? "rotate(-90deg)" : "rotate(0deg)" }}
          >
            ▾
          </span>
        </button>
      </div>
      {!collapsed && hasChildren && (
        <div>
          {children.map(([key, child], i) => (
            <TreeBranch
              key={key}
              node={child}
              isLast={i === children.length - 1}
              prefix={childPrefix}
              onDelete={onDelete}
              deletingNsid={deletingNsid}
              expandedId={expandedId}
              onToggleExpand={onToggleExpand}
            />
          ))}
        </div>
      )}
    </div>
  );
}

export default function LexiconsPage() {
  const queryClient = useQueryClient();
  const [searchQuery, setSearchQuery] = useState("");
  const [nsidInput, setNsidInput] = useState("");
  const [error, setError] = useState<string | null>(null);
  const [success, setSuccess] = useState<string | null>(null);
  const [deletingNsid, setDeletingNsid] = useState<string | null>(null);
  const [expandedId, setExpandedId] = useState<string | null>(null);

  const { data, isLoading, error: fetchError } = useQuery({
    queryKey: ["lexicons"],
    queryFn: () => graphqlClient.request<LexiconsResponse>(GET_LEXICONS),
  });

  // Batch-aware registration status. Operators paste multiple
  // NSIDs (comma- or newline-separated) and we surface per-item
  // pending/success/error state in a persistent list so they can
  // see exactly which entries succeeded and which need attention.
  // Auto-clearing alerts thrash for batches >1 item and lose error
  // detail, so the list stays put until the operator dismisses it.
  type BatchStatus = "pending" | "success" | "error" | "skipped";
  interface BatchItem {
    nsid: string;
    status: BatchStatus;
    message?: string;
  }
  const [batchItems, setBatchItems] = useState<BatchItem[]>([]);
  const [batchRunning, setBatchRunning] = useState(false);

  const registerMutation = useMutation({
    mutationFn: (nsid: string) =>
      graphqlClient.request(REGISTER_LEXICON, { nsid }),
    onSuccess: (_, nsid) => {
      setSuccess(`Registered ${nsid}`);
      setError(null);
      setNsidInput("");
      queryClient.invalidateQueries({ queryKey: ["lexicons"] });
      setTimeout(() => setSuccess(null), 3000);
    },
    onError: (err: Error) => {
      setError(err.message);
      setSuccess(null);
    },
  });

  const deleteMutation = useMutation({
    mutationFn: (nsid: string) =>
      graphqlClient.request(DELETE_LEXICON, { nsid }),
    onMutate: (nsid) => setDeletingNsid(nsid),
    onSuccess: (_, nsid) => {
      setSuccess(`Deleted ${nsid}`);
      setError(null);
      if (expandedId === nsid) setExpandedId(null);
      queryClient.invalidateQueries({ queryKey: ["lexicons"] });
      setTimeout(() => setSuccess(null), 3000);
    },
    onError: (err: Error) => {
      setError(err.message);
      setSuccess(null);
    },
    onSettled: () => setDeletingNsid(null),
  });

  const handleRegister = async (e: React.FormEvent) => {
    e.preventDefault();
    if (!nsidInput.trim() || batchRunning) return;

    // Parse: split on commas + any whitespace (including newlines),
    // trim, drop empties, dedupe in input order. Operators paste
    // from specs and spreadsheets — both extra delimiters and
    // duplicates are common.
    const seen = new Set<string>();
    const tokens: string[] = [];
    for (const raw of nsidInput.split(/[\s,]+/)) {
      const t = raw.trim();
      if (!t || seen.has(t)) continue;
      seen.add(t);
      tokens.push(t);
    }
    if (tokens.length === 0) return;

    // Pre-validate every token so we don't fire mutations for
    // obviously-bad input. Invalid entries land in the status
    // list as "skipped" with a clear reason; valid ones queue up.
    const items: BatchItem[] = tokens.map((nsid) =>
      isValidNsid(nsid)
        ? { nsid, status: "pending" }
        : { nsid, status: "skipped", message: "invalid NSID format" }
    );
    setBatchItems(items);
    setError(null);
    setSuccess(null);

    const todo = items.filter((it) => it.status === "pending");
    if (todo.length === 0) return;

    // Serialize: backend lexicon resolution hits DNS / a network
    // registry; running N in parallel would just burn rate-limit
    // headroom. The visible per-item spinner is the affordance
    // that explains why it's slower than a single-shot register.
    setBatchRunning(true);
    for (const it of todo) {
      try {
        await graphqlClient.request(REGISTER_LEXICON, { nsid: it.nsid });
        setBatchItems((prev) =>
          prev.map((p) => (p.nsid === it.nsid ? { ...p, status: "success" } : p))
        );
      } catch (err) {
        const message = err instanceof Error ? err.message : String(err);
        setBatchItems((prev) =>
          prev.map((p) =>
            p.nsid === it.nsid ? { ...p, status: "error", message } : p
          )
        );
      }
    }
    setBatchRunning(false);
    setNsidInput("");
    queryClient.invalidateQueries({ queryKey: ["lexicons"] });
  };

  const filteredLexicons = useMemo(() => {
    if (!data?.lexicons) return [];
    if (!searchQuery) return data.lexicons;

    const query = searchQuery.toLowerCase();
    return data.lexicons.filter(
      (lex) =>
        lex.id.toLowerCase().includes(query) ||
        lex.json.toLowerCase().includes(query)
    );
  }, [data?.lexicons, searchQuery]);

  const tree = useMemo(() => buildTree(filteredLexicons), [filteredLexicons]);
  const roots = Array.from(tree.entries()).sort(([a], [b]) => a.localeCompare(b));

  if (fetchError) {
    return (
      <div className="py-8">
        <Alert variant="error">Failed to load lexicons: {(fetchError as Error).message}</Alert>
      </div>
    );
  }

  return (
    <div className="py-8 space-y-6">
      {/* Header */}
      <div>
        <h2 className="font-[family-name:var(--font-garamond)] text-2xl text-zinc-900">
          Lexicons
        </h2>
        <p className="text-sm text-zinc-400 mt-1">
          Register AT Protocol lexicon schemas for your AppView
        </p>
      </div>

      {/* Alerts */}
      {error && (
        <Alert variant="error" onClose={() => setError(null)}>
          {error}
        </Alert>
      )}
      {success && <Alert variant="success">{success}</Alert>}

      {/* Register — single textarea accepts one NSID or many
          (comma- and/or newline-separated). Single-line muscle
          memory is preserved: typing one NSID + Enter submits
          via the form. Multi-line input grows the box. */}
      <form onSubmit={handleRegister} className="flex flex-col gap-2 sm:flex-row sm:items-start">
        <textarea
          value={nsidInput}
          onChange={(e) => {
            setNsidInput(e.target.value);
            setError(null);
          }}
          onKeyDown={(e) => {
            // Enter submits (single-line muscle memory); Shift+Enter
            // inserts a newline for batch input.
            if (e.key === "Enter" && !e.shiftKey) {
              e.preventDefault();
              (e.currentTarget.form as HTMLFormElement | null)?.requestSubmit();
            }
          }}
          rows={Math.min(6, Math.max(1, nsidInput.split("\n").length))}
          placeholder="Enter one NSID, or paste many separated by commas or newlines..."
          className="flex-1 px-3 py-1.5 bg-white/50 border border-zinc-200/60 rounded-lg
                     text-sm text-zinc-800 placeholder-zinc-400 font-mono
                     focus:outline-none focus:border-emerald-400 focus:ring-1 focus:ring-emerald-100
                     transition-all resize-y"
        />
        <Button
          type="submit"
          variant="primary"
          disabled={batchRunning || registerMutation.isPending || !nsidInput.trim()}
          loading={batchRunning || registerMutation.isPending}
        >
          Register
        </Button>
      </form>

      {/* Batch status list. aria-live is scoped to the terse
          summary line, NOT the per-item <ul> — announcing every
          row of a 10-NSID batch one at a time spams screen
          readers, and the running summary already conveys
          progress. The list stays visible until Dismiss because
          auto-clearing loses the per-item error detail that's the
          whole point of showing it. */}
      {batchItems.length > 0 && (
        <div className="rounded-lg border border-zinc-200/60 bg-white/60 p-3 text-sm">
          <div className="flex items-center justify-between pb-2 border-b border-zinc-100">
            <span className="text-zinc-600" aria-live="polite">
              Registration: {batchItems.filter((i) => i.status === "success").length} ok · {batchItems.filter((i) => i.status === "error").length} failed · {batchItems.filter((i) => i.status === "skipped").length} skipped
              {batchRunning && " · running…"}
            </span>
            {!batchRunning && (
              <button
                className="text-xs text-zinc-500 underline"
                onClick={() => setBatchItems([])}
              >
                Dismiss
              </button>
            )}
          </div>
          <ul className="divide-y divide-zinc-100">
            {batchItems.map((it) => (
              <li key={it.nsid} className="flex items-center justify-between py-1.5">
                <code className="font-mono text-xs text-zinc-700">{it.nsid}</code>
                <span
                  className={
                    it.status === "success"
                      ? "text-emerald-700 text-xs"
                      : it.status === "error"
                        ? "text-red-700 text-xs"
                        : it.status === "skipped"
                          ? "text-amber-700 text-xs"
                          : "text-zinc-500 text-xs"
                  }
                  title={it.message}
                >
                  {it.status === "success"
                    ? "✓ registered"
                    : it.status === "error"
                      ? `✕ ${it.message ?? "failed"}`
                      : it.status === "skipped"
                        ? `⊘ ${it.message ?? "skipped"}`
                        : "…pending"}
                </span>
              </li>
            ))}
          </ul>
        </div>
      )}

      {/* Search */}
      <div className="relative">
        <svg className="absolute left-3 top-1/2 -translate-y-1/2 h-3.5 w-3.5 text-zinc-300" fill="none" viewBox="0 0 24 24" strokeWidth={1.5} stroke="currentColor">
          <path strokeLinecap="round" strokeLinejoin="round" d="m21 21-5.197-5.197m0 0A7.5 7.5 0 1 0 5.196 5.196a7.5 7.5 0 0 0 10.607 10.607Z" />
        </svg>
        <input
          type="text"
          placeholder="Search..."
          value={searchQuery}
          onChange={(e) => setSearchQuery(e.target.value)}
          className="w-full pl-9 pr-3 py-1.5 text-sm bg-white/50 border border-zinc-200/60 rounded-lg
                     text-zinc-800 placeholder:text-zinc-300
                     focus:outline-none focus:border-emerald-400 focus:ring-1 focus:ring-emerald-100
                     transition-all"
        />
      </div>

      {/* Tree */}
      {isLoading ? (
        <div className="flex items-center gap-2 py-8 justify-center">
          <div className="w-3 h-3 border-2 border-zinc-300 border-t-emerald-400 rounded-full animate-spin" />
          <span className="text-xs text-zinc-400">Loading...</span>
        </div>
      ) : roots.length === 0 ? (
        <p className="text-sm text-zinc-400 py-4 text-center">
          {searchQuery ? `No lexicons match "${searchQuery}"` : "No lexicons registered yet."}
        </p>
      ) : (
        <div className="font-mono">
          {roots.map(([key, node]) => (
            <TreeBranch
              key={key}
              node={node}
              isRoot
              onDelete={(nsid) => deleteMutation.mutate(nsid)}
              deletingNsid={deletingNsid}
              expandedId={expandedId}
              onToggleExpand={(id) => setExpandedId(expandedId === id ? null : id)}
            />
          ))}
        </div>
      )}
    </div>
  );
}
