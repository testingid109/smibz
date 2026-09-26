import type { Metadata } from "next";
import { notFound } from "next/navigation";
import { fetchRoomStatus } from "../../../lib/server-api";
import type { StatusResponse } from "../../../lib/types";
import RoomClient from "./RoomClient";

export const dynamic = "force-dynamic";

function cleanCode(rawCode: string) {
  return rawCode
    .toUpperCase()
    .replace(/[^A-Z0-9]/g, "");
}

export async function generateMetadata(
  props: {
    params: Promise<{ code: string }>;
  }
): Promise<Metadata> {
  const { code: rawCode } = await props.params;

  const code = cleanCode(rawCode);

  if (!/^[A-Z0-9]{6}$/.test(code)) {
    return {
      title: "Smibz — Invalid link",
    };
  }

  const room =
    (await fetchRoomStatus(code)) as
      | StatusResponse
      | null;

  if (!room) {
    return {
      title: "Smibz — Smib not found",
      description:
        "This Smib link is no longer active.",
    };
  }

  const title = `${room.question} — Smibz`;

  const description =
    `Join this Smibz decision in ` +
    `${room.category || "Smibz"}. ` +
    `Make your picks privately and find ` +
    `common ground together.`;

  const siteUrl =
    process.env.NEXT_PUBLIC_SITE_URL ||
    "https://smibz.free";

  const url =
    `${siteUrl.replace(/\/$/, "")}/join/${code}`;

  return {
    title,
    description,

    alternates: {
      canonical: url,
    },

    openGraph: {
      type: "website",
      url,
      title,
      description,
      siteName: "Smibz",
    },

    twitter: {
      card: "summary",
      title,
      description,
    },

    robots: {
      index: false,
      follow: false,
    },
  };
}

export default async function JoinRoomPage(
  props: {
    params: Promise<{ code: string }>;
  }
) {
  const { code: rawCode } = await props.params;

  const code = cleanCode(rawCode);

  if (!/^[A-Z0-9]{6}$/.test(code)) {
    notFound();
  }

  const room =
    (await fetchRoomStatus(code)) as
      | StatusResponse
      | null;

  if (!room) {
    notFound();
  }

  return (
    <RoomClient
      code={code}
      initialStatus={room}
    />
  );
}