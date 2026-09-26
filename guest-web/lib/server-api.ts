import "server-only";

const rawBaseUrl = process.env.GO_API_BASE_URL;

if (!rawBaseUrl) {
  throw new Error(
    "GO_API_BASE_URL environment variable is required"
  );
}

export const GO_API_BASE_URL = rawBaseUrl.replace(/\/$/, "");

// Matches the Android client: Render's free tier can take 30-50s to wake
// from a cold start, and 10s was firing false failures during that window.
const REQUEST_TIMEOUT_MS = 40_000;

export async function goFetch(
  path: string,
  init: RequestInit = {}
) {
  return fetch(`${GO_API_BASE_URL}${path}`, {
    ...init,
    signal:
      init.signal ??
      AbortSignal.timeout(REQUEST_TIMEOUT_MS),
  });
}

export async function fetchRoomStatus(code: string) {
  try {
    const response = await goFetch(
      `/rooms/${encodeURIComponent(code)}/status`,
      {
        cache: "no-store",
        headers: {
          Accept: "application/json",
        },
      }
    );

    if (!response.ok) {
      return null;
    }

    return response.json();
  } catch {
    return null;
  }
}
