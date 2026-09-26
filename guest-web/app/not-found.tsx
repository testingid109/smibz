export default function NotFound() {
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
            Smib not found
          </div>

          <h1>
            That link isn&apos;t active.
          </h1>

          <p className="subtitle">
            Check the shared link or ask the
            person who created the Smib for
            the latest link.
          </p>

          <a
            className="primary-button link-button"
            href="/"
          >
            Go to Smibz
          </a>
        </div>
      </div>
    </main>
  );
}