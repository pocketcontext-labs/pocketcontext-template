import {
  useCallback,
  useEffect,
  useRef,
  useState,
  type FormEvent,
} from "react";
import { api, MESSAGE_LIMIT, type Greeting } from "./api";
import { message } from "./errors";

const dateFormat = new Intl.DateTimeFormat(undefined, {
  dateStyle: "medium",
  timeStyle: "short",
});
export function formatDate(value: string): string {
  // PocketBase stores "YYYY-MM-DD HH:MM:SS.sssZ" in UTC.
  const date = new Date(value.replace(" ", "T"));
  return Number.isNaN(date.getTime()) ? value : dateFormat.format(date);
}

/** The signed-in account's greetings, newest first, and a form to add one. */
export default function Greetings() {
  const [greetings, setGreetings] = useState<Greeting[] | null>(null);
  const [error, setError] = useState("");
  const [draft, setDraft] = useState("");
  const [busy, setBusy] = useState(false);
  const [status, setStatus] = useState("");
  const mounted = useRef(true);
  const reads = useRef(0);
  const load = useCallback(async () => {
    const read = ++reads.current;
    try {
      const rows = await api.listGreetings();
      if (mounted.current && read === reads.current) {
        setGreetings(rows);
        setError("");
      }
    } catch (e) {
      if (mounted.current && read === reads.current) setError(message(e));
    }
  }, []);
  useEffect(() => {
    mounted.current = true;
    void load();
    let stop: (() => void) | undefined;
    // Changes from the CLI or another tab appear without a reload.
    api
      .watchGreetings(() => void load())
      .then((unsubscribe) => {
        if (mounted.current) stop = unsubscribe;
        else unsubscribe();
      })
      .catch(() => {});
    return () => {
      mounted.current = false;
      stop?.();
    };
  }, [load]);
  const submit = async (event: FormEvent<HTMLFormElement>) => {
    event.preventDefault();
    setBusy(true);
    setError("");
    setStatus("");
    try {
      await api.addGreeting(draft);
      if (!mounted.current) return;
      setDraft("");
      setStatus("Greeting added.");
      await load();
    } catch (e) {
      if (mounted.current) setError(message(e));
    } finally {
      if (mounted.current) setBusy(false);
    }
  };
  const remaining = MESSAGE_LIMIT - draft.trim().length;
  return (
    <section className="panel" aria-labelledby="greetings-title">
      <h2 id="greetings-title">Your greetings</h2>
      <form className="add" onSubmit={submit}>
        <label htmlFor="new-greeting">New greeting</label>
        <div className="row">
          <input
            id="new-greeting"
            name="message"
            value={draft}
            maxLength={MESSAGE_LIMIT}
            required
            aria-describedby="greeting-remaining"
            onChange={(event) => setDraft(event.target.value)}
            placeholder="Hello, world"
          />
          <button className="primary" disabled={busy || !draft.trim()}>
            Add
          </button>
        </div>
        <p id="greeting-remaining" className="quiet">
          {remaining} characters left
        </p>
      </form>
      <p role="status" className="quiet">
        {status}
      </p>
      {error && (
        <p role="alert" className="error">
          {error}
        </p>
      )}
      {greetings === null ? (
        !error && <p className="quiet">Loading greetings…</p>
      ) : greetings.length === 0 ? (
        <p className="empty">No greetings yet. Add the first one above.</p>
      ) : (
        <ol className="greetings" aria-label="Greetings, newest first">
          {greetings.map((greeting) => (
            <li key={greeting.id}>
              <p>{greeting.message}</p>
              <time dateTime={greeting.created.replace(" ", "T")}>
                {formatDate(greeting.created)}
              </time>
            </li>
          ))}
        </ol>
      )}
    </section>
  );
}
