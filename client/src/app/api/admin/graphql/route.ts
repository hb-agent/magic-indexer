import { NextRequest, NextResponse } from "next/server";
import { getSession } from "@/lib/session";
import { env } from "@/lib/env";

export const dynamic = "force-dynamic";

/**
 * Proxy for admin GraphQL requests.
 * Checks session authentication and passes user DID to Hypergoat.
 */
export async function POST(request: NextRequest) {
  try {
    const session = await getSession();
    const body = await request.json();

    // Build headers for Hypergoat. The backend's admin handler trusts
    // X-User-DID only when the request is accompanied by a valid
    // Authorization: Bearer <ADMIN_API_KEY> header (constant-time
    // compared on the backend side). The operator's OAuth session
    // gives us the DID; the shared ADMIN_API_KEY tells the backend
    // that this server-side proxy is the trusted intermediary. The
    // final authorization check — "is this DID in admin_dids?" —
    // still happens on the backend.
    const headers: HeadersInit = {
      "Content-Type": "application/json",
    };

    if (env.ADMIN_API_KEY) {
      headers["Authorization"] = `Bearer ${env.ADMIN_API_KEY}`;
    }

    // If user is authenticated, pass their DID
    if (session.did) {
      headers["X-User-DID"] = session.did;
      console.log("[admin-graphql] Authenticated request", { did: session.did });
    } else {
      console.log("[admin-graphql] Unauthenticated request - no session DID");
    }

    // Proxy to Hypergoat
    const response = await fetch(`${env.HYPERGOAT_URL}/admin/graphql`, {
      method: "POST",
      headers,
      body: JSON.stringify(body),
    });

    const data = await response.json();

    // Log errors from Hypergoat
    if (data.errors) {
      console.log("[admin-graphql] GraphQL errors:", JSON.stringify(data.errors));
    }

    return NextResponse.json(data, { status: response.status });
  } catch (error) {
    console.error("Admin GraphQL proxy error:", error);
    return NextResponse.json(
      { errors: [{ message: "Internal server error" }] },
      { status: 500 }
    );
  }
}
