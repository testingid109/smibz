"use client";

import { FormEvent, useState } from "react";
import { useRouter } from "next/navigation";

export default function HomePage() {
  const router = useRouter();
  const [code, setCode] = useState("");

  function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();

    const clean = code
      .toUpperCase()
      .replace(/[^A-Z0-9]/g, "")
      .slice(0, 6);

    if (clean.length === 6) {
      router.push(`/join/${clean}`);
    }
  }

  return (
    <main className="page-shell">
      <div className="page-content">
        <div className="panel compact">
          <div className="brand-row">
            <div className="brand">
              <span
                className="brand-mark"
                aria-hidden="true"
              />
              Smibz
            </div>
          </div>

          <div
            className="folk-strip"
            aria-hidden="true"
          />

          <div className="eyebrow">
            Decide together
          </div>

          <h1>Join a Smib.</h1>

          <p className="subtitle">
            Open the link your friend sent on WhatsApp
            or enter the 6-character Smib code below.
            Your choices stay private until everyone has
            voted.
          </p>

          <form
            className="form-stack"
            onSubmit={submit}
          >
            <div>
              <label
                className="label"
                htmlFor="room-code"
              >
                Smib code
              </label>

              <input
                id="room-code"
                className="input code-input"
                value={code}
                onChange={(event) =>
                  setCode(event.target.value)
                }
                placeholder="ABC123"
                maxLength={6}
                autoCapitalize="characters"
                autoCorrect="off"
                spellCheck={false}
                inputMode="text"
                aria-describedby="room-code-help"
              />

              <div
                id="room-code-help"
                className="field-hint"
              >
                You can also open a direct link like
                smibz.free/join/ABC123.
              </div>
            </div>

            <button
              className="primary-button"
              disabled={
                code.replace(
                  /[^A-Za-z0-9]/g,
                  ""
                ).length !== 6
              }
              type="submit"
            >
              Open Smib →
            </button>
          </form>
        </div>
      </div>
    </main>
  );
}