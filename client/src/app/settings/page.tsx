"use client";

import { useState, useEffect } from "react";
import { useQuery, useMutation, useQueryClient } from "@tanstack/react-query";
import { graphqlClient } from "@/lib/graphql/client";
import { GET_SETTINGS, GET_OAUTH_CLIENTS } from "@/lib/graphql/queries";
import {
  UPDATE_SETTINGS,
  RESET_ALL,
  UPLOAD_LEXICONS,
  PREVIEW_PURGE_ACTOR,
  PURGE_ACTOR,
} from "@/lib/graphql/mutations";
import {
  Card,
  CardContent,
  CardHeader,
  CardTitle,
  CardDescription,
  Button,
  Input,
  Alert,
} from "@/components/ui";
import { useAuth } from "@/lib/auth";
import type {
  SettingsResponse,
  OAuthClientsResponse,
  PurgeActorPreview,
  PreviewPurgeActorResponse,
  PurgeActorResponse,
  PurgeActorResult,
} from "@/types";

export default function SettingsPage() {
  const queryClient = useQueryClient();
  const { session: authSession, isLoading: authLoading } = useAuth();

  // Fetch settings
  const { data: settingsData, isLoading } = useQuery({
    queryKey: ["settings"],
    queryFn: () => graphqlClient.request<SettingsResponse>(GET_SETTINGS),
  });

  // Fetch OAuth clients
  const { data: oauthData } = useQuery({
    queryKey: ["oauthClients"],
    queryFn: () => graphqlClient.request<OAuthClientsResponse>(GET_OAUTH_CLIENTS),
  });

  const settings = settingsData?.settings;
  const oauthClients = oauthData?.oauthClients ?? [];

  // Admin gate. The server enforces auth via ADMIN_API_KEY +
  // X-User-DID + admin_dids membership; this gate is UX only —
  // it stops non-admins from seeing destructive controls they
  // can't usefully operate. Single source of truth is the
  // server-known list returned by GET_SETTINGS, joined with
  // the iron-session DID surfaced via /api/status.
  const isAdmin =
    !!authSession && (settings?.adminDids ?? []).includes(authSession.did);
  const adminTooltip = authSession
    ? `Your DID (${authSession.did}) must appear in settings.adminDids to edit this.`
    : "Sign in with an admin DID to edit this.";

  // Form state
  const [domainAuthority, setDomainAuthority] = useState("");
  const [relayUrl, setRelayUrl] = useState("");
  const [plcDirectoryUrl, setPlcDirectoryUrl] = useState("");
  const [jetstreamUrl, setJetstreamUrl] = useState("");
  const [oauthScopes, setOauthScopes] = useState("");
  const [resetConfirmation, setResetConfirmation] = useState("");
  const [alert, setAlert] = useState<{ type: "success" | "error"; message: string } | null>(null);

  // --- Actor purge state. Three-stage flow: enter DID → preview
  // (server returns counts + a 5-minute HMAC token bound to this
  // (admin, target, count) triple) → retyped-DID confirm → execute.
  // The retype is the strongest "right target" check; the token's
  // freshness is enforced server-side, but we surface a countdown
  // so the operator isn't surprised by a stale-token rejection.
  const [purgeDidInput, setPurgeDidInput] = useState("");
  const [purgePreview, setPurgePreview] = useState<PurgeActorPreview | null>(null);
  const [purgeConfirmDid, setPurgeConfirmDid] = useState("");
  const [purgeResult, setPurgeResult] = useState<PurgeActorResult | null>(null);
  const [purgeError, setPurgeError] = useState<string | null>(null);
  const [purgeTokenSecondsLeft, setPurgeTokenSecondsLeft] = useState(0);

  // Countdown tick: when a preview is live, decrement every second
  // and clear the preview when it hits zero so the UI doesn't let
  // the operator click "Purge" with a token the server will reject.
  useEffect(() => {
    if (!purgePreview) {
      setPurgeTokenSecondsLeft(0);
      return;
    }
    const expiresAtMs = new Date(purgePreview.tokenExpiresAt).getTime();
    // A malformed timestamp from the server would yield NaN, which
    // collapses the countdown to 0 and shows a misleading "Token
    // expired" message. Detect once at effect set-up and bail
    // with a distinct error so the operator re-previews.
    if (Number.isNaN(expiresAtMs)) {
      setPurgePreview(null);
      setPurgeConfirmDid("");
      setPurgeError("Invalid token response from server. Preview the purge again.");
      return;
    }
    const tick = () => {
      const remaining = Math.max(0, Math.floor((expiresAtMs - Date.now()) / 1000));
      setPurgeTokenSecondsLeft(remaining);
      if (remaining <= 0) {
        setPurgePreview(null);
        setPurgeConfirmDid("");
        setPurgeError("Confirmation token expired. Preview the purge again.");
      }
    };
    tick();
    const id = setInterval(tick, 1000);
    return () => clearInterval(id);
  }, [purgePreview]);

  // Update form when settings load
  useState(() => {
    if (settings) {
      setDomainAuthority(settings.domainAuthority);
      setRelayUrl(settings.relayUrl);
      setPlcDirectoryUrl(settings.plcDirectoryUrl);
      setJetstreamUrl(settings.jetstreamUrl);
      setOauthScopes(settings.oauthSupportedScopes);
    }
  });

  // Update settings mutation
  const updateMutation = useMutation({
    mutationFn: (variables: Record<string, unknown>) =>
      graphqlClient.request(UPDATE_SETTINGS, variables),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ["settings"] });
      setAlert({ type: "success", message: "Settings updated successfully" });
    },
    onError: (error: Error) => {
      setAlert({ type: "error", message: error.message });
    },
  });

  // Reset mutation
  const resetMutation = useMutation({
    mutationFn: (confirm: string) =>
      graphqlClient.request(RESET_ALL, { confirm }),
    onSuccess: () => {
      queryClient.invalidateQueries();
      setResetConfirmation("");
      setAlert({ type: "success", message: "All data has been reset" });
    },
    onError: (error: Error) => {
      setAlert({ type: "error", message: error.message });
    },
  });

  // Purge preview / execute mutations.
  const previewPurgeMutation = useMutation({
    mutationFn: (did: string) =>
      graphqlClient.request<PreviewPurgeActorResponse>(PREVIEW_PURGE_ACTOR, { did }),
    onSuccess: (data) => {
      setPurgePreview(data.previewPurgeActor);
      setPurgeConfirmDid("");
      setPurgeResult(null);
      setPurgeError(null);
    },
    onError: (error: Error) => {
      setPurgePreview(null);
      setPurgeError(error.message);
    },
  });

  const purgeMutation = useMutation({
    mutationFn: ({ did, confirmToken }: { did: string; confirmToken: string }) =>
      graphqlClient.request<PurgeActorResponse>(PURGE_ACTOR, { did, confirmToken }),
    onSuccess: (data) => {
      setPurgeResult(data.purgeActor);
      setPurgePreview(null);
      setPurgeConfirmDid("");
      setPurgeDidInput("");
      setPurgeError(null);
      queryClient.invalidateQueries();
    },
    onError: (error: Error) => {
      // Map the three token-rejection sentinels to operator-readable
      // copy. Anything else surfaces verbatim. All three clear the
      // preview so the operator can re-arm via "preview again".
      let msg = error.message;
      if (error.message.includes("purge_token_expired")) {
        msg = "Confirmation token expired. Preview the purge again.";
        setPurgePreview(null);
      } else if (error.message.includes("purge_token_already_used")) {
        msg = "Confirmation token has already been used. Preview again.";
        setPurgePreview(null);
      } else if (error.message.includes("purge_token_invalid")) {
        msg =
          "Confirmation token is no longer valid (record count may have changed). Preview the purge again.";
        setPurgePreview(null);
      }
      setPurgeError(msg);
    },
  });

  const handleSaveSettings = () => {
    updateMutation.mutate({
      domainAuthority: domainAuthority || undefined,
      relayUrl: relayUrl || undefined,
      plcDirectoryUrl: plcDirectoryUrl || undefined,
      jetstreamUrl: jetstreamUrl || undefined,
      oauthSupportedScopes: oauthScopes || undefined,
      adminDids: settings?.adminDids,
    });
  };

  const handleReset = () => {
    if (resetConfirmation === "RESET") {
      resetMutation.mutate("RESET");
    }
  };

  if (isLoading) {
    return (
      <div className="pt-8 sm:pt-12 space-y-6">
        {[...Array(3)].map((_, i) => (
          <div key={i} className="h-48 animate-pulse rounded-xl bg-zinc-100" />
        ))}
      </div>
    );
  }

  return (
    <div className="pt-8 sm:pt-12 space-y-10">
      {/* Hero Section */}
      <div className="max-w-md">
        <h2 className="font-[family-name:var(--font-garamond)] text-3xl sm:text-4xl text-zinc-900 leading-tight">
          Settings
        </h2>
        <p className="text-zinc-500 mt-3 leading-relaxed">
          Configure your Hyperindex AppView instance
        </p>
      </div>

      {alert && (
        <Alert variant={alert.type === "success" ? "success" : "error"}>
          {alert.message}
        </Alert>
      )}

      {/* Gate the "Read-only view" banner on auth being resolved.
          During the first paint useAuth().isLoading is true and
          authSession is null, so isAdmin is false — without this
          guard, a legitimate admin sees the read-only banner flash
          for a frame before /api/status resolves. Destructive UI
          was already hidden behind isAdmin and unaffected. */}
      {!authLoading && !isAdmin && (
        <div
          id="admin-gate-hint"
          className="rounded-xl border border-amber-200/60 bg-amber-50/40 p-4 text-sm text-amber-900"
        >
          <p className="font-medium">Read-only view</p>
          <p className="mt-1 text-amber-800/90">{adminTooltip}</p>
        </div>
      )}

      {/* Basic Settings */}
      <div className="space-y-4">
        <h3 className="font-[family-name:var(--font-garamond)] text-xl text-zinc-900">
          Basic Settings
        </h3>
        <div className="rounded-xl border border-zinc-200/60 bg-white p-6 space-y-4">
          <Input
            label="Domain Authority"
            placeholder="your-domain.com"
            value={domainAuthority}
            onChange={(e) => setDomainAuthority(e.target.value)}
            hint="The domain that owns this AppView instance"
            disabled={!isAdmin}
            aria-describedby={!isAdmin ? "admin-gate-hint" : undefined}
          />
          <div className="flex justify-end pt-2">
            <Button
              variant="primary"
              onClick={handleSaveSettings}
              loading={updateMutation.isPending}
              disabled={!isAdmin}
              title={!isAdmin ? adminTooltip : undefined}
            >
              Save Settings
            </Button>
          </div>
        </div>
      </div>

      {/* External Services */}
      <div className="space-y-4">
        <h3 className="font-[family-name:var(--font-garamond)] text-xl text-zinc-900">
          External Services
        </h3>
        <div className="rounded-xl border border-zinc-200/60 bg-white p-6 space-y-4">
          <Input
            label="Relay URL"
            placeholder="https://relay1.us-west.bsky.network"
            value={relayUrl}
            onChange={(e) => setRelayUrl(e.target.value)}
            disabled={!isAdmin}
            aria-describedby={!isAdmin ? "admin-gate-hint" : undefined}
          />
          <Input
            label="PLC Directory URL"
            placeholder="https://plc.directory"
            value={plcDirectoryUrl}
            onChange={(e) => setPlcDirectoryUrl(e.target.value)}
            disabled={!isAdmin}
            aria-describedby={!isAdmin ? "admin-gate-hint" : undefined}
          />
          <Input
            label="Jetstream URL"
            placeholder="wss://jetstream2.us-west.bsky.network/subscribe"
            value={jetstreamUrl}
            onChange={(e) => setJetstreamUrl(e.target.value)}
            disabled={!isAdmin}
            aria-describedby={!isAdmin ? "admin-gate-hint" : undefined}
          />
          <Input
            label="OAuth Supported Scopes"
            placeholder="atproto transition:generic"
            value={oauthScopes}
            onChange={(e) => setOauthScopes(e.target.value)}
            disabled={!isAdmin}
            aria-describedby={!isAdmin ? "admin-gate-hint" : undefined}
          />
          <div className="flex justify-end pt-2">
            <Button
              variant="primary"
              onClick={handleSaveSettings}
              loading={updateMutation.isPending}
              disabled={!isAdmin}
              title={!isAdmin ? adminTooltip : undefined}
            >
              Save Settings
            </Button>
          </div>
        </div>
      </div>

      {/* Admin DIDs */}
      <div className="space-y-4">
        <h3 className="font-[family-name:var(--font-garamond)] text-xl text-zinc-900">
          Administrators
        </h3>
        <div className="rounded-xl border border-zinc-200/60 bg-white p-6">
          {settings?.adminDids.length === 0 ? (
            <p className="text-sm text-zinc-400">
              No administrators configured
            </p>
          ) : (
            <ul className="divide-y divide-zinc-100">
              {settings?.adminDids.map((did) => (
                <li key={did} className="flex items-center justify-between py-3 first:pt-0 last:pb-0">
                  <code className="text-sm text-zinc-600 font-mono">{did}</code>
                </li>
              ))}
            </ul>
          )}
        </div>
      </div>

      {/* OAuth Clients */}
      <div className="space-y-4">
        <h3 className="font-[family-name:var(--font-garamond)] text-xl text-zinc-900">
          OAuth Clients
        </h3>
        <div className="rounded-xl border border-zinc-200/60 bg-white p-6">
          {oauthClients.length === 0 ? (
            <p className="text-sm text-zinc-400">
              No OAuth clients registered
            </p>
          ) : (
            <ul className="divide-y divide-zinc-100">
              {oauthClients.map((client) => (
                <li key={client.clientId} className="py-3 first:pt-0 last:pb-0">
                  <div className="flex items-center justify-between">
                    <div>
                      <p className="font-medium text-zinc-800">
                        {client.clientName}
                      </p>
                      <code className="text-xs text-zinc-400 font-mono">{client.clientId}</code>
                    </div>
                    <span className="rounded-full bg-zinc-100 px-2 py-1 text-xs text-zinc-600">
                      {client.clientType}
                    </span>
                  </div>
                </li>
              ))}
            </ul>
          )}
        </div>
      </div>

      {/* Danger Zone — hidden entirely for non-admins so the
          destructive surface area shrinks. Reset and Purge
          mutations are also server-gated; this only removes the
          UI. */}
      {isAdmin && (
        <div className="space-y-4">
          <h3 className="font-[family-name:var(--font-garamond)] text-xl text-red-600">
            Danger Zone
          </h3>

          {/* Reset all data */}
          <div className="rounded-xl border border-red-200/60 bg-red-50/30 p-6 space-y-4">
            <p className="text-sm text-zinc-600">
              Reset all data including records, actors, and activity. This action cannot be undone.
            </p>
            <div className="flex flex-col sm:flex-row items-start sm:items-end gap-4">
              <div className="w-full sm:w-auto">
                <Input
                  label="Type RESET to confirm"
                  placeholder="RESET"
                  value={resetConfirmation}
                  onChange={(e) => setResetConfirmation(e.target.value)}
                />
              </div>
              <Button
                variant="destructive"
                onClick={handleReset}
                disabled={resetConfirmation !== "RESET"}
                loading={resetMutation.isPending}
              >
                Reset All Data
              </Button>
            </div>
          </div>

          {/* Actor purge — scoped destructive op for takedowns /
              GDPR / test cleanup. Three stages, server-bound: enter
              DID → preview (counts + countdown token) → retyped-DID
              confirm → execute. The retype is the strongest "right
              target" check; the token's binding to (admin, target,
              count) is what defeats replay and racing-ingest. */}
          <div className="rounded-xl border border-red-200/60 bg-red-50/30 p-6 space-y-4">
            <div>
              <h4 className="font-medium text-red-700">Purge actor</h4>
              <p className="text-sm text-zinc-600 mt-1">
                Permanently delete every record and the actor row for a single DID. Best-effort Tap cleanup runs after the SQL commit. Use for GDPR takedowns and test-data cleanup.
              </p>
            </div>

            {!purgePreview && !purgeResult && (
              <div className="flex flex-col sm:flex-row items-start sm:items-end gap-4">
                <div className="w-full sm:flex-1">
                  <Input
                    label="DID to purge"
                    placeholder="did:plc:..."
                    value={purgeDidInput}
                    onChange={(e) => setPurgeDidInput(e.target.value)}
                  />
                </div>
                <Button
                  variant="primary"
                  onClick={() => previewPurgeMutation.mutate(purgeDidInput.trim())}
                  disabled={!purgeDidInput.trim()}
                  loading={previewPurgeMutation.isPending}
                >
                  Preview
                </Button>
              </div>
            )}

            {purgePreview && (
              <div className="space-y-3" id="purge-confirm-panel">
                <div className="rounded border border-red-300/40 bg-white p-4 space-y-1 text-sm">
                  <div className="flex items-center justify-between">
                    <span className="text-zinc-500">DID</span>
                    <code className="text-zinc-800 font-mono">{purgePreview.did}</code>
                  </div>
                  <div className="flex items-center justify-between">
                    <span className="text-zinc-500">Records to delete</span>
                    <span className="text-zinc-800 font-medium">{purgePreview.recordCount}</span>
                  </div>
                  <div className="flex items-center justify-between">
                    <span className="text-zinc-500">Actor row present</span>
                    <span className="text-zinc-800">{purgePreview.actorExists ? "yes" : "no"}</span>
                  </div>
                  {purgePreview.actorExists && (
                    <>
                      <div className="flex items-center justify-between">
                        <span className="text-zinc-500">Handle</span>
                        <span className="text-zinc-800">{purgePreview.handle || "—"}</span>
                      </div>
                      <div className="flex items-center justify-between">
                        <span className="text-zinc-500">Last indexed</span>
                        <span className="text-zinc-800">{purgePreview.latestIndexedAt || "—"}</span>
                      </div>
                    </>
                  )}
                  {/* No aria-live on the per-second countdown — it
                      would spam screen readers every second. The
                      preview card carries the same information, and
                      the Purge button's disabled state collapses to
                      false when the token expires (clearing the
                      preview), which IS announced via the
                      preview-cleared branch. */}
                  <div className="flex items-center justify-between pt-2 border-t border-red-200/40 mt-2">
                    <span className="text-zinc-500">Token expires in</span>
                    <span
                      className={
                        purgeTokenSecondsLeft < 30
                          ? "text-red-600 font-mono"
                          : "text-zinc-800 font-mono"
                      }
                    >
                      {Math.floor(purgeTokenSecondsLeft / 60)}:
                      {String(purgeTokenSecondsLeft % 60).padStart(2, "0")}
                    </span>
                  </div>
                </div>

                <div className="flex flex-col sm:flex-row items-start sm:items-end gap-4">
                  <div className="w-full sm:flex-1">
                    <Input
                      label="Re-type the DID to confirm"
                      placeholder={purgePreview.did}
                      value={purgeConfirmDid}
                      onChange={(e) => setPurgeConfirmDid(e.target.value)}
                      aria-describedby="purge-confirm-help"
                    />
                    <p id="purge-confirm-help" className="text-xs text-zinc-500 mt-1">
                      Must match the previewed DID exactly.
                    </p>
                  </div>
                  <div className="flex gap-2">
                    <Button
                      variant="outline"
                      onClick={() => {
                        setPurgePreview(null);
                        setPurgeConfirmDid("");
                      }}
                    >
                      Cancel
                    </Button>
                    <Button
                      variant="destructive"
                      onClick={() =>
                        purgeMutation.mutate({
                          did: purgePreview.did,
                          confirmToken: purgePreview.confirmToken,
                        })
                      }
                      disabled={
                        purgeConfirmDid !== purgePreview.did ||
                        purgeTokenSecondsLeft <= 0
                      }
                      loading={purgeMutation.isPending}
                    >
                      Purge
                    </Button>
                  </div>
                </div>
              </div>
            )}

            {purgeResult && (
              <div className="rounded border border-emerald-300/40 bg-emerald-50/40 p-4 text-sm space-y-1">
                <p className="font-medium text-emerald-800">Purge complete</p>
                <p className="text-emerald-900/80">
                  <code className="font-mono">{purgeResult.did}</code> — {purgeResult.recordsDeleted} record(s) deleted,{" "}
                  {purgeResult.actorRowsDeleted} actor row(s) removed, Tap: {purgeResult.tapStatus}.
                </p>
                <button
                  className="mt-2 text-emerald-700 underline text-xs"
                  onClick={() => setPurgeResult(null)}
                >
                  Dismiss
                </button>
              </div>
            )}

            {purgeError && (
              <Alert variant="error">
                {purgeError}
                {!purgePreview && (
                  <button
                    className="ml-2 underline text-xs"
                    onClick={() => setPurgeError(null)}
                  >
                    Dismiss
                  </button>
                )}
              </Alert>
            )}
          </div>
        </div>
      )}
    </div>
  );
}
