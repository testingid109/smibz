import type { Metadata, Viewport } from "next";
import type { ReactNode } from "react";
import "./globals.css";

const siteUrl =
  process.env.NEXT_PUBLIC_SITE_URL || "https://smibz.freedev.app";

export const metadata: Metadata = {
  metadataBase: new URL(siteUrl),

  title: "Smibz — Decide together",

  description:
    "Join a Smibz decision, make your private picks, and find common ground together.",

  applicationName: "Smibz",

  openGraph: {
    type: "website",
    siteName: "Smibz",
    title: "Smibz — Decide together",
    description:
      "Join a Smibz decision and make your picks privately.",
  },

  twitter: {
    card: "summary",
    title: "Smibz — Decide together",
    description:
      "Join a Smibz decision and make your picks privately.",
  },

  robots: {
    index: false,
    follow: false,
  },
};

export const viewport: Viewport = {
  width: "device-width",
  initialScale: 1,
  viewportFit: "cover",
  themeColor: "#fff9ea",
};

export default function RootLayout({
  children,
}: {
  children: ReactNode;
}) {
  return (
    <html lang="en">
      <body>{children}</body>
    </html>
  );
}