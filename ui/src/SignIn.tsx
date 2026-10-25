import { useEffect, useState, type FormEvent } from "react";
import { api } from "./api";
import Brand from "./Brand";
import { message } from "./errors";

/**
 * Password sign-in, or a one-time code from `templatecontext login-code`
 * (for a person on another computer). `templatecontext open` signs in with a
 * single-use link that the server redeems on /bundle/login.
 */
export default function SignIn() {
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [mode, setMode] = useState<"password" | "code">("password");
  useEffect(() => () => api.cancelLogin(), []);
  const attempt = async (action: () => Promise<unknown>) => {
    setBusy(true);
    setError("");
    try {
      await action();
    } catch (e) {
      setError(message(e));
    } finally {
      setBusy(false);
    }
  };
  const submit = (event: FormEvent<HTMLFormElement>) => {
    event.preventDefault();
    const form = new FormData(event.currentTarget);
    void attempt(() =>
      mode === "password"
        ? api.loginPassword(
            String(form.get("email")),
            String(form.get("password")),
          )
        : api.loginCode(String(form.get("code"))),
    );
  };
  return (
    <main className="sign-in">
      <section className="card" aria-labelledby="sign-in-title">
        <Brand />
        <h1 id="sign-in-title">Sign in</h1>
        <form className="stack" onSubmit={submit} key={mode}>
          {mode === "password" ? (
            <>
              <label>
                Email
                <input
                  required
                  name="email"
                  type="email"
                  autoComplete="username"
                />
              </label>
              <label>
                Password
                <input
                  required
                  name="password"
                  type="password"
                  autoComplete="current-password"
                />
              </label>
            </>
          ) : (
            <label>
              Sign-in code
              <input
                required
                name="code"
                autoComplete="one-time-code"
                spellCheck={false}
                autoCapitalize="off"
              />
            </label>
          )}
          <button className="primary" disabled={busy}>
            Sign in
          </button>
        </form>
        <button
          type="button"
          className="link"
          onClick={() => {
            api.cancelLogin();
            setError("");
            setMode(mode === "password" ? "code" : "password");
          }}
        >
          {mode === "password"
            ? "Use a sign-in code instead"
            : "Use a password instead"}
        </button>
        <p className="hint">
          {mode === "password" ? (
            <>
              Or run <code>templatecontext open</code> to sign in with a
              single-use link.
            </>
          ) : (
            <>
              On the agent computer,{" "}
              <code>templatecontext login-code --email EMAIL</code> issues a
              code that works once.
            </>
          )}
        </p>
        {error && (
          <p role="alert" className="error">
            {error}
          </p>
        )}
      </section>
    </main>
  );
}
