import { NextResponse } from "next/server";
import { goFetch } from "../../../../../lib/server-api";

function cleanCode(rawCode: string) {
  return rawCode
    .toUpperCase()
    .replace(/[^A-Z0-9]/g, "");
}

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
        error: "Invalid room code",
      },
      {
        status: 400,
      }
    );
  }

  try {
    const payload =
      await request.json();

    const name =
      typeof payload?.name === "string"
        ? payload.name.trim()
        : "";

    if (!name) {
      return NextResponse.json(
        {
          error: "Name is required",
        },
        {
          status: 400,
        }
      );
    }

    if (name.length > 40) {
      return NextResponse.json(
        {
          error: "Name is too long",
        },
        {
          status: 400,
        }
      );
    }

    const upstream =
      await goFetch(
        `/rooms/${code}/join`,
        {
          method: "POST",

          headers: {
            "Content-Type":
              "application/json",
            Accept:
              "application/json",
          },

          body: JSON.stringify({
            name,
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