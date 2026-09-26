import { NextResponse } from "next/server";
import { goFetch } from "../../../../../lib/server-api";

function cleanCode(rawCode: string) {
  return rawCode
    .toUpperCase()
    .replace(/[^A-Z0-9]/g, "");
}

export async function GET(
  _request: Request,
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
    const upstream =
      await goFetch(
        `/rooms/${code}/status`,
        {
          cache: "no-store",
          headers: {
            Accept:
              "application/json",
          },
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
            "no-store, no-cache, must-revalidate",
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