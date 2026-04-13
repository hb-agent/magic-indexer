import { NextRequest, NextResponse } from "next/server";
import { env } from "@/lib/env";

export const dynamic = "force-dynamic";

// 1 MiB — matches the backend's maxGraphQLBodyBytes limit.
const MAX_BODY_BYTES = 1 << 20;

/**
 * Proxy for public GraphQL requests to Hypergoat.
 */
export async function POST(request: NextRequest) {
  try {
    // Reject oversized requests at the proxy layer. Check content-length
    // first (fast path), then enforce the limit on the actual body bytes
    // to handle chunked transfer encoding where content-length is absent.
    const contentLength = request.headers.get("content-length");
    if (contentLength && parseInt(contentLength, 10) > MAX_BODY_BYTES) {
      return NextResponse.json(
        { errors: [{ message: "Request body too large" }] },
        { status: 413 }
      );
    }

    const rawBody = await request.text();
    if (rawBody.length > MAX_BODY_BYTES) {
      return NextResponse.json(
        { errors: [{ message: "Request body too large" }] },
        { status: 413 }
      );
    }

    const body = JSON.parse(rawBody);

    const response = await fetch(`${env.HYPERGOAT_URL}/graphql`, {
      method: "POST",
      headers: {
        "Content-Type": "application/json",
      },
      body: JSON.stringify(body),
    });

    const data = await response.json();

    return NextResponse.json(data, { status: response.status });
  } catch (error) {
    console.error("GraphQL proxy error:", error);
    return NextResponse.json(
      { errors: [{ message: "Internal server error" }] },
      { status: 500 }
    );
  }
}
