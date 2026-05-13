/**
 * Environment variables for the client.
 * Uses process.env directly with defaults for development.
 */

function getEnv(key: string, defaultValue: string = ""): string {
  return process.env[key] || defaultValue;
}

function getPort(): number {
  const port = process.env.PORT;
  return port ? parseInt(port, 10) : 3000;
}

export const env = {
  // Secret for encrypting session cookies (must be at least 32 chars).
  // CRITICAL: No default — a missing secret must fail loudly rather than
  // silently using a publicly-known value that lets anyone forge sessions.
  COOKIE_SECRET: (() => {
    const secret = process.env.COOKIE_SECRET;
    if (!secret || secret.length < 32) {
      throw new Error(
        "COOKIE_SECRET must be set to a random string of at least 32 characters. " +
        "Generate one with: openssl rand -base64 32"
      );
    }
    return secret;
  })(),
  
  // Public URL for OAuth callbacks (empty = use localhost)
  PUBLIC_URL: getEnv("PUBLIC_URL", ""),
  
  // Port for the Next.js server
  PORT: getPort(),
  
  // Private JWK for confidential OAuth client (optional, for production)
  ATPROTO_JWK_PRIVATE: getEnv("ATPROTO_JWK_PRIVATE", ""),
  
  // Hypergoat backend URL
  HYPERGOAT_URL: getEnv("HYPERGOAT_URL", "http://127.0.0.1:8080"),

  // Admin API key (backend-issued shared secret). Required when the
  // backend has ADMIN_API_KEY set: the backend only trusts the
  // X-User-DID header forwarded by this proxy if the request also
  // carries a matching Authorization: Bearer <key> header. The actual
  // authorization decision still happens on the backend — we verify
  // the session DID via iron-session, then the backend verifies that
  // DID is in admin_dids. This proxy is the "trusted intermediary"
  // between OAuth sign-in and the shared-secret admin API.
  ADMIN_API_KEY: getEnv("ADMIN_API_KEY", ""),
};

// Fail-fast on a self-referential backend URL.
//
// A common misconfiguration is pointing the client at itself —
// e.g. setting HYPERGOAT_URL=https://magic-indexer-admin.vercel.app
// when that's the *client's* origin, not the backend's. Requests
// then loop, the GraphQL queries 404, and the failure mode reads
// like "the API is broken" rather than "the env is wrong."
//
// Compare origins (not the full URL — paths can legitimately
// differ). Only fire when both halves of the comparison resolve;
// preview deployments where PUBLIC_URL is unset and Vercel hands
// out a dynamic branch URL fall through unscathed.
//
// Dev: throw at module load — the loop closes before code ships.
// Production: console.error and continue — don't hard-brick a
// live deploy over a config typo when the alternative is "serve
// traffic with a noisy log line."
function originOf(url: string): string | null {
  if (!url) return null;
  try {
    return new URL(url).origin;
  } catch {
    return null;
  }
}

const clientOrigin =
  originOf(env.PUBLIC_URL) ??
  originOf(getEnv("NEXT_PUBLIC_VERCEL_BRANCH_URL")) ??
  originOf(getEnv("VERCEL_BRANCH_URL"));
const backendOrigin = originOf(env.HYPERGOAT_URL);

if (clientOrigin && backendOrigin && clientOrigin === backendOrigin) {
  const msg =
    `[fatal-config] HYPERGOAT_URL points at the client's own origin (${clientOrigin}). ` +
    `Requests will loop. Set HYPERGOAT_URL to the backend's URL (the Railway / Go server), ` +
    `not the client's URL (Vercel / Next.js).`;
  if (process.env.NODE_ENV === "production") {
    console.error(msg);
  } else {
    throw new Error(msg);
  }
}
