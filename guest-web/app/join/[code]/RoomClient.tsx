"use client";

import {
  useCallback,
  useEffect,
  useMemo,
  useState,
} from "react";

import type {
  JoinResponse,
  PreferenceLevel,
  StatusResponse,
} from "@/lib/types";

interface RoomClientProps {
  code: string;
  initialStatus: StatusResponse;
}

const preferences: Array<{
  level: PreferenceLevel;
  emoji: string;
  label: string;
  className: string;
}> = [
  { level: "WANT", emoji: "❤️", label: "Want", className: "want" },
  { level: "OKAY", emoji: "👍", label: "Okay", className: "okay" },
  { level: "NO", emoji: "❌", label: "No", className: "no" },
  { level: "NEVER", emoji: "🚫", label: "Never", className: "never" },
];

function participantStorageKey(code: string) {
  return `smibz:participant:${code}`;
}

function voteStorageKey(code: string) {
  return `smibz:votes:${code}`;
}

export default function RoomClient({
  code,
  initialStatus,
}: RoomClientProps) {
  const [status, setStatus] = useState(initialStatus);
  const [participantId, setParticipantId] = useState<string | null>(null);
  const [name, setName] = useState("");
  const [joinName, setJoinName] = useState("");
  const [votes, setVotes] = useState<Record<string, PreferenceLevel>>({});
  const [isJoining, setIsJoining] = useState(false);
  const [isSubmitting, setIsSubmitting] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [shareMessage, setShareMessage] = useState<string | null>(null);

  const refreshStatus = useCallback(async () => {
    const response = await fetch(`/api/rooms/${code}/status`, {
      cache: "no-store",
      headers: { Accept: "application/json" },
    });

    if (!response.ok) {
      throw new Error("Couldn't refresh this Smib.");
    }

    setStatus((await response.json()) as StatusResponse);
  }, [code]);

  useEffect(() => {
    try {
      const rawParticipant = localStorage.getItem(participantStorageKey(code));

      if (rawParticipant) {
        const saved = JSON.parse(rawParticipant) as {
          participantId?: string;
          name?: string;
        };

        if (saved.participantId && saved.name) {
          setParticipantId(saved.participantId);
          setName(saved.name);
        }
      }

      const rawVotes = localStorage.getItem(voteStorageKey(code));

      if (rawVotes) {
        setVotes(JSON.parse(rawVotes) as Record<string, PreferenceLevel>);
      }
    } catch {
      // Ignore stale or malformed browser storage.
    }
  }, [code]);

  useEffect(() => {
    if (!participantId || status.result) {
      return;
    }

    let stopped = false;

    async function poll() {
      try {
        await refreshStatus();
      } catch {
        if (!stopped) {
          setError("Connection interrupted. We'll keep trying…");
        }
      }
    }

    const timer = window.setInterval(poll, 2500);

    return () => {
      stopped = true;
      window.clearInterval(timer);
    };
  }, [participantId, status.result, refreshStatus]);

  const allRated = useMemo(() => {
    return (
      status.options.length > 0 &&
      status.options.every((option) => Boolean(votes[option.id]))
    );
  }, [status.options, votes]);

  const myParticipant = useMemo(() => {
    if (!participantId) {
      return null;
    }

    return status.participants.find(
      (participant) => participant.id === participantId
    );
  }, [participantId, status.participants]);

  const hasLockedVotes = Boolean(myParticipant?.hasVoted);

  async function joinRoom() {
    const cleanName = joinName.trim();

    if (!cleanName || status.result) {
      return;
    }

    setIsJoining(true);
    setError(null);

    try {
      const response = await fetch(`/api/rooms/${code}/join`, {
        method: "POST",
        headers: {
          "Content-Type": "application/json",
          Accept: "application/json",
        },
        body: JSON.stringify({ name: cleanName }),
      });

      const body = (await response.json()) as
        | JoinResponse
        | { error?: string };

      if (!response.ok || !("participantId" in body)) {
        throw new Error(
          "error" in body && body.error
            ? body.error
            : "Couldn't join this Smib."
        );
      }

      localStorage.setItem(
        participantStorageKey(code),
        JSON.stringify({
          participantId: body.participantId,
          name: cleanName,
        })
      );

      localStorage.removeItem(voteStorageKey(code));

      setParticipantId(body.participantId);
      setName(cleanName);
      setVotes({});

      await refreshStatus();
    } catch (joinError) {
      setError(
        joinError instanceof Error
          ? joinError.message
          : "Couldn't join this Smib."
      );
    } finally {
      setIsJoining(false);
    }
  }

  function selectVote(optionId: string, level: PreferenceLevel) {
    if (hasLockedVotes || status.result) {
      return;
    }

    const nextVotes = { ...votes, [optionId]: level };

    setVotes(nextVotes);
    localStorage.setItem(voteStorageKey(code), JSON.stringify(nextVotes));
  }

  async function submitVotes() {
    if (!participantId || !allRated || hasLockedVotes || status.result) {
      return;
    }

    setIsSubmitting(true);
    setError(null);

    try {
      const response = await fetch(`/api/rooms/${code}/vote`, {
        method: "POST",
        headers: {
          "Content-Type": "application/json",
          Accept: "application/json",
        },
        body: JSON.stringify({ participantId, preferences: votes }),
      });

      const body = (await response.json()) as { ok?: boolean; error?: string };

      if (!response.ok || !body.ok) {
        throw new Error(body.error || "Couldn't lock your votes.");
      }

      await refreshStatus();
    } catch (submitError) {
      setError(
        submitError instanceof Error
          ? submitError.message
          : "Couldn't lock your votes."
      );
    } finally {
      setIsSubmitting(false);
    }
  }

  async function copyRoomLink() {
    try {
      await navigator.clipboard.writeText(window.location.href);
      showShareMessage("Link copied.");
    } catch {
      showShareMessage("Copy failed. Use Share instead.");
    }
  }

  async function shareRoom() {
    const url = window.location.href;
    const text = `Join my Smibz decision: ${status.question}`;

    try {
      if (navigator.share) {
        await navigator.share({ title: "Join my Smibz", text, url });
        showShareMessage("Shared.");
        return;
      }

      await navigator.clipboard.writeText(url);
      showShareMessage("Link copied.");
    } catch {
      // User cancelled native share.
    }
  }

  function shareToWhatsApp() {
    const message =
      `Join my Smibz decision:\n\n` +
      `${status.question}\n\n` +
      `${window.location.href}`;

    const whatsappUrl = `https://wa.me/?text=${encodeURIComponent(message)}`;

    window.open(whatsappUrl, "_blank", "noopener,noreferrer");
  }

  function showShareMessage(message: string) {
    setShareMessage(message);
    window.setTimeout(() => setShareMessage(null), 2200);
  }

  const content = status.result ? (
    <ResultView status={status} name={name} onShare={shareRoom} />
  ) : !participantId ? (
    <JoinView
      name={joinName}
      onNameChange={setJoinName}
      onJoin={joinRoom}
      isJoining={isJoining}
      error={error}
      onShare={shareRoom}
    />
  ) : hasLockedVotes ? (
    <WaitingView
      status={status}
      name={name}
      onShare={shareRoom}
      onCopy={copyRoomLink}
    />
  ) : (
    <VoteView
      status={status}
      name={name}
      votes={votes}
      allRated={allRated}
      isSubmitting={isSubmitting}
      onSelect={selectVote}
      onSubmit={submitVotes}
      error={error}
      onShare={shareRoom}
      onCopy={copyRoomLink}
    />
  );

  return (
    <main className="page-shell">
      <div className="page-content">
        <div className="brand-row room-brand-row">
          <div className="brand" aria-label="Smibz">
            <span className="brand-mark" aria-hidden="true" />
            Smibz
          </div>

          <div className="top-share-actions">
            <button
              className="share-button compact-button"
              onClick={shareToWhatsApp}
              type="button"
            >
              WhatsApp
            </button>

            <button
              className="share-button compact-button"
              onClick={copyRoomLink}
              type="button"
            >
              Copy link
            </button>
          </div>
        </div>

        <div className="folk-strip" aria-hidden="true" />

        <section className="panel">
          <div className="eyebrow">Smib · {code}</div>

          <h1>{status.question}</h1>

          <div className="room-meta">
            <span className="badge">{status.category || "Anything"}</span>
            <span className="badge code">{code}</span>
            <span className="badge">
              {status.votedCount}/{status.totalParticipants} voted
            </span>
          </div>

          {content}

          {shareMessage ? (
            <div className="center-note share-toast" role="status">
              {shareMessage}
            </div>
          ) : null}
        </section>
      </div>
    </main>
  );
}

function JoinView({
  name,
  onNameChange,
  onJoin,
  isJoining,
  error,
  onShare,
}: {
  name: string;
  onNameChange: (value: string) => void;
  onJoin: () => void;
  isJoining: boolean;
  error: string | null;
  onShare: () => void;
}) {
  return (
    <div>
      <div className="join-hero">
        <div className="join-emoji" aria-hidden="true">🤝</div>
        <h2>You&apos;re invited.</h2>
        <p className="subtitle">
          Enter your name and privately rate every option. Your individual
          choices won&apos;t be revealed before the group is done.
        </p>
      </div>

      <div className="form-stack">
        <div>
          <label className="label" htmlFor="guest-name">Your name</label>
          <input
            id="guest-name"
            className="input"
            value={name}
            onChange={(event) => onNameChange(event.target.value)}
            placeholder="e.g. Aman"
            maxLength={40}
            autoComplete="name"
            autoFocus
          />
        </div>

        <button
          className="primary-button"
          onClick={onJoin}
          disabled={!name.trim() || isJoining}
          type="button"
        >
          {isJoining ? "Joining…" : "Join & choose →"}
        </button>

        <button className="secondary-button" onClick={onShare} type="button">
          Invite someone else
        </button>
      </div>

      {error ? <div className="error">{error}</div> : null}
    </div>
  );
}

function VoteView({
  status,
  name,
  votes,
  allRated,
  isSubmitting,
  onSelect,
  onSubmit,
  error,
  onShare,
  onCopy,
}: {
  status: StatusResponse;
  name: string;
  votes: Record<string, PreferenceLevel>;
  allRated: boolean;
  isSubmitting: boolean;
  onSelect: (optionId: string, level: PreferenceLevel) => void;
  onSubmit: () => void;
  error: string | null;
  onShare: () => void;
  onCopy: () => void;
}) {
  return (
    <div>
      <p className="subtitle">
        Hi {name}. Rate every option honestly. Your individual choices stay
        private until everyone locks their picks.
      </p>

      <div className="options-grid">
        {status.options.map((option) => (
          <div className="option-card" key={option.id}>
            <div className="option-title">{option.text}</div>

            <div className="preference-grid">
              {preferences.map((preference) => {
                const selected = votes[option.id] === preference.level;

                return (
                  <button
                    key={preference.level}
                    type="button"
                    className={`preference ${preference.className} ${
                      selected ? "selected" : ""
                    }`}
                    onClick={() => onSelect(option.id, preference.level)}
                    aria-pressed={selected}
                  >
                    <span className="emoji">{preference.emoji}</span>
                    <span className="label-small">{preference.label}</span>
                  </button>
                );
              })}
            </div>
          </div>
        ))}
      </div>

      <div className="privacy-note">
        <span className="privacy-icon" aria-hidden="true">🔒</span>
        <span>Your picks stay private until the group result is ready.</span>
      </div>

      <div className="status-card">
        <strong>
          {status.votedCount} of {status.totalParticipants}
        </strong>{" "}
        people have locked their picks.
        <ParticipantList participants={status.participants} />
      </div>

      {error ? <div className="error">{error}</div> : null}

      <div className="actions">
        <button className="share-button" onClick={onCopy} type="button">
          Copy link
        </button>

        <button className="share-button" onClick={onShare} type="button">
          Share
        </button>

        <button
          className="primary-button span-two-mobile"
          onClick={onSubmit}
          disabled={!allRated || isSubmitting}
          type="button"
        >
          {isSubmitting ? "Locking…" : "Lock My Votes"}
        </button>
      </div>

      {!allRated ? (
        <p className="center-note" style={{ marginTop: 10 }}>
          Rate every option before locking.
        </p>
      ) : null}
    </div>
  );
}

function WaitingView({
  status,
  name,
  onShare,
  onCopy,
}: {
  status: StatusResponse;
  name: string;
  onShare: () => void;
  onCopy: () => void;
}) {
  const progress =
    status.totalParticipants > 0
      ? Math.min(100, (status.votedCount / status.totalParticipants) * 100)
      : 0;

  return (
    <div>
      <div className="waiting-hero">
        <div className="waiting-emoji" aria-hidden="true">✅</div>
        <h2>Your votes are locked.</h2>
        <p className="subtitle">
          Nice work, {name}. We&apos;re waiting for everyone else to finish.
        </p>
      </div>

      <div className="status-card waiting-card">
        <div className="waiting-count">
          {status.votedCount} of {status.totalParticipants} voted
        </div>

        <div className="progress-track" aria-hidden="true">
          <div className="progress-fill" style={{ width: `${progress}%` }} />
        </div>

        <ParticipantList participants={status.participants} />
      </div>

      <div className="room-footer">
        <button className="primary-button" onClick={onShare} type="button">
          Share the Smib ↗
        </button>

        <button className="secondary-button" onClick={onCopy} type="button">
          Copy link
        </button>
      </div>
    </div>
  );
}

function ParticipantList({
  participants,
}: {
  participants: StatusResponse["participants"];
}) {
  return (
    <div className="participant-list">
      {participants.map((participant) => (
        <div className="participant-row" key={participant.id}>
          <span className="participant-name">{participant.name}</span>
          <span className="participant-state">
            {participant.hasVoted ? "✓ Voted" : "Waiting…"}
          </span>
        </div>
      ))}
    </div>
  );
}

function ResultView({
  status,
  name,
  onShare,
}: {
  status: StatusResponse;
  name: string;
  onShare: () => void;
}) {
  const result = status.result ?? undefined;
  const headline = matchTypeLabel(result?.matchType);
  const topOptionId = result?.topOptionId;
  const top = result?.options.find((option) => option.optionId === topOptionId);

  return (
    <div>
      <div className="result-card result-card-hero">
        <div className="result-confetti" aria-hidden="true">🎉</div>
        <div className="eyebrow">Decision ready</div>
        <h2>
          {headline.emoji} {headline.text}
        </h2>
        <p className="subtitle">
          {name ? `Nice work, ${name}. ` : ""}
          Everyone has locked their picks.
        </p>

        {top ? (
          <div className="result-option top">
            <div className="winner-label">COMMON GROUND</div>
            <div className="result-option-title">{top.text}</div>
            <div className="result-option-votes">
              ❤️ {top.want}   👍 {top.okay}   ❌ {top.no}   🚫 {top.never}
            </div>
          </div>
        ) : null}
      </div>

      <div className="result-list">
        {result?.options
          .filter((option) => option.optionId !== topOptionId)
          .map((option) => (
            <div className="result-option" key={option.optionId}>
              <div className="result-option-title">{option.text}</div>
              <div className="result-option-votes">
                ❤️ {option.want}   👍 {option.okay}   ❌ {option.no}   🚫 {option.never}
              </div>
            </div>
          ))}
      </div>

      <div className="room-footer">
        <button className="primary-button" onClick={onShare} type="button">
          Share this Smib ↗
        </button>
      </div>
    </div>
  );
}

function matchTypeLabel(matchType?: string) {
  switch (matchType) {
    case "FULL_CONSENSUS":
      return { emoji: "🎉", text: "Everyone agrees!" };
    case "STRONG_CONSENSUS":
      return { emoji: "🎉", text: "Strong match found" };
    case "ACCEPTABLE_CONSENSUS":
      return { emoji: "🤝", text: "Everyone's on board" };
    case "PARTIAL_CONSENSUS":
      return { emoji: "🤝", text: "Common ground found" };
    case "CONFLICT":
      return { emoji: "😅", text: "Mixed feelings — closest fit" };
    default:
      return { emoji: "😬", text: "No option worked for everyone" };
  }
}