import { NextRequest, NextResponse } from "next/server";
import { getGlobalOAuthClient } from "@/lib/auth/client";
import { isValidHandle } from "@atproto/syntax";
import { OAuthResolverError } from "@atproto/oauth-client-node";
import { getRawSession } from "@/lib/session";
import { env } from "@/lib/env";

export const dynamic = "force-dynamic";

// isSameOriginPath returns true iff `returnTo` resolves to a path that
// lives within our own origin when relative to `env.PUBLIC_URL`. This
// is stricter than a `startsWith("/")` check because the latter
// permits things like `//evil.com`, `/\evil.com`, `/%2f%2fevil.com`,
// and `   /admin` (leading whitespace), each of which can be
// interpreted by a browser or by Response.redirect as a cross-origin
// destination.
//
// The check returns the *normalised* path (or null) so the caller can
// store the canonicalised value rather than the attacker's input.
function safeReturnPath(returnTo: unknown): string | null {
  if (typeof returnTo !== "string") return null;
  const base = env.PUBLIC_URL || "http://localhost";
  let baseOrigin: string;
  try {
    baseOrigin = new URL(base).origin;
  } catch {
    return null;
  }
  let parsed: URL;
  try {
    // Parse against the base so a relative path stays inside our
    // origin and an absolute attacker URL surfaces a different origin.
    parsed = new URL(returnTo, base);
  } catch {
    return null;
  }
  if (parsed.origin !== baseOrigin) return null;
  // Return the path-plus-search; drop the host portion so the value
  // we store is canonicalised.
  return parsed.pathname + parsed.search;
}

export async function POST(request: NextRequest) {
  try {
    const client = await getGlobalOAuthClient();
    const body = await request.json();
    const handle = body?.handle;
    const returnTo = body?.returnTo;

    if (typeof handle !== "string" || !isValidHandle(handle)) {
      return NextResponse.json({ error: "Invalid handle" }, { status: 400 });
    }

    // Store returnTo in session before redirecting, but only after
    // canonicalising it through safeReturnPath — the value flows
    // through to the OAuth callback's Response.redirect and a
    // permissive check there would let a `//evil.com`-shaped value
    // scheme-relative-redirect to an attacker origin.
    if (returnTo !== undefined && returnTo !== null) {
      const safe = safeReturnPath(returnTo);
      if (safe === null) {
        return NextResponse.json(
          { error: "Invalid returnTo: must be a same-origin path" },
          { status: 400 }
        );
      }
      const session = await getRawSession();
      session.returnTo = safe;
      await session.save();
    }

    const url = await client.authorize(handle, {
      scope: "atproto transition:generic",
    });

    return NextResponse.json({ redirectUrl: url.toString() });
  } catch (error) {
    console.error("OAuth authorize failed:", error);
    let errorMessage = "Couldn't initiate login";

    if (error instanceof OAuthResolverError) {
      errorMessage = error.message;
    }

    return NextResponse.json({ error: errorMessage }, { status: 500 });
  }
}
