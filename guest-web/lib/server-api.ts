import "server-only";

const rawBaseUrl = process.env.GO_API_BASE_URL;

if (!rawBaseUrl) {
  throw new Error(
    "GO_API_BASE_URL environment variable is required"
  );
}

export const GO_API_BASE_URL = rawBaseUrl.replace(/\/$/, "");

const REQUEST_TIMEOUT_MS = 10_000;

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