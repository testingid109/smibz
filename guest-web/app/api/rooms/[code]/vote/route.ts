import { NextResponse } from "next/server";
import { goFetch } from "../../../../../lib/server-api";

function cleanCode(rawCode: string) {
  return rawCode
    .toUpperCase()
    .replace(/[^A-Z0-9]/g, "");
}

const allowedLevels =
  new Set([
    "WANT",
    "OKAY",
    "NO",
    "NEVER",
  ]);

export async function POST(
  request: Request,
  context: {
    params: Promise<{ code: string }>;
  }
) {
  const { code: rawCode } =
    await context.params;

  const code = cleanCode(rawCode);

  if (!/^[A-Z0-9]{6}$/.test(code)) {
    return NextResponse.json(
      {
        error:
          "Invalid room code",
      },
      {
        status: 400,
      }
    );
  }

  try {
    const payload =
      await request.json();

    const participantId =
      typeof payload?.participantId ===
      "string"
        ? payload.participantId.trim()
        : "";

    const rawPreferences =
      payload?.preferences;

    if (
      !participantId ||
      !rawPreferences ||
      typeof rawPreferences !==
        "object" ||
      Array.isArray(
        rawPreferences
      )
    ) {
      return NextResponse.json(
        {
          error:
            "Participant and preferences are required",
        },
        {
          status: 400,
        }
      );
    }

    const preferences: Record<
      string,
      string
    > = {};

    for (const [
      optionId,
      level,
    ] of Object.entries(
      rawPreferences
    )) {
      if (
        typeof level === "string" &&
        allowedLevels.has(level)
      ) {
        preferences[
          optionId
        ] = level;
      }
    }

    if (
      Object.keys(
        preferences
      ).length === 0
    ) {
      return NextResponse.json(
        {
          error:
            "At least one preference is required",
        },
        {
          status: 400,
        }
      );
    }

    const upstream =
      await goFetch(
        `/rooms/${code}/vote`,
        {
          method: "POST",

          headers: {
            "Content-Type":
              "application/json",
            Accept:
              "application/json",
          },

          body: JSON.stringify({
            participantId,
            preferences,
          }),
        }
      );

    const body =
      await upstream.text();

    return new NextResponse(
      body,
      {
        status:
          upstream.status,

        headers: {
          "Content-Type":
            upstream.headers.get(
              "content-type"
            ) ||
            "application/json",

          "Cache-Control":
            "no-store",
        },
      }
    );
  } catch {
    return NextResponse.json(
      {
        error:
          "Backend unavailable",
      },
      {
        status: 502,
      }
    );
  }
}