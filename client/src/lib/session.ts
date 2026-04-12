import { env } from "./env";
import { getIronSession, type SessionOptions } from "iron-session";
import { cookies } from "next/headers";

/**
 * Session data stored in the encrypted cookie.
 */
export interface Session {
  did?: string;
  handle?: string;
  displayName?: string;
  avatar?: string;
  returnTo?: string;
  // OAuth session data (serialized) - persisted across serverless invocations
  oauthSession?: string;
  // In-flight OAuth authorization states (PKCE verifier, nonce, etc.),
  // keyed by the `state` parameter the library generates in authorize().
  // Persisted to the cookie because Vercel Functions are serverless —
  // module-scope in-memory storage does NOT survive across the
  // authorize → callback roundtrip when the two requests land on
  // different function instances. Each entry is a JSON-serialized blob.
  oauthStates?: Record<string, string>;
}

const isProduction = process.env.NODE_ENV === "production";

const sessionOptions: SessionOptions = {
  cookieName: "hypergoat_sid",
  password: env.COOKIE_SECRET,
  cookieOptions: {
    secure: isProduction,
    maxAge: 60 * 60 * 24 * 30, // 30 days
  },
};

/**
 * Get the current user's session from their encrypted cookie.
 */
export async function getSession(): Promise<Session> {
  const cookieStore = await cookies();
  const session = await getIronSession<Session>(cookieStore, sessionOptions);
  return session;
}

/**
 * Get the raw iron-session object for direct manipulation (save/destroy).
 */
export async function getRawSession() {
  const cookieStore = await cookies();
  return await getIronSession<Session>(cookieStore, sessionOptions);
}

/**
 * Clear the current user's session.
 */
export async function clearSession(): Promise<void> {
  const session = await getRawSession();
  session.destroy();
}
