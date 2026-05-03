import { useEffect, useState } from "react";

const initialState = {
  loading: true,
  message: "",
  stack: "",
  error: "",
};

export default function App() {
  const [state, setState] = useState(initialState);

  useEffect(() => {
    let cancelled = false;

    fetch("/api/message")
      .then((response) => {
        if (!response.ok) {
          throw new Error(`request failed: ${response.status}`);
        }
        return response.json();
      })
      .then((payload) => {
        if (cancelled) {
          return;
        }
        setState({
          loading: false,
          message: payload.message,
          stack: payload.stack,
          error: "",
        });
      })
      .catch((error) => {
        if (cancelled) {
          return;
        }
        setState({
          loading: false,
          message: "",
          stack: "",
          error: error.message,
        });
      });

    return () => {
      cancelled = true;
    };
  }, []);

  return (
    <main className="app-shell">
      <section className="card">
        <p className="eyebrow">Devup Demo Stack</p>
        <h1>React Frontend + Python Backend</h1>
        <p className="lede">
          The frontend is served by Vite and reaches the backend through a local
          proxy inside the container runtime.
        </p>
        <div className="status-row">
          <span className="status-label">Status</span>
          <span className={`pill ${state.error ? "pill-error" : "pill-ok"}`}>
            {state.loading ? "Loading" : state.error ? "Error" : "Connected"}
          </span>
        </div>
        <div className="panel">
          <div>
            <strong>Message:</strong>{" "}
            {state.loading ? "Waiting for backend..." : state.message || "-"}
          </div>
          <div>
            <strong>Stack:</strong> {state.loading ? "-" : state.stack || "-"}
          </div>
          {state.error ? (
            <div className="error">
              <strong>Problem:</strong> {state.error}
            </div>
          ) : null}
        </div>
      </section>
    </main>
  );
}
