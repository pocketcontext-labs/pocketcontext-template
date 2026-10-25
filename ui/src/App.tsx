import { useEffect, useSyncExternalStore } from "react";
import { api, pb } from "./api";
import Brand from "./Brand";
import Greetings from "./Greetings";
import Search from "./Search";
import SignIn from "./SignIn";

function subscribe(change: () => void) {
  return pb.authStore.onChange(change);
}
function identity() {
  return pb.authStore.isValid && pb.authStore.record?.collectionName === "users"
    ? `${pb.authStore.record.id}\n${pb.authStore.record.email ?? ""}`
    : "";
}

export default function App() {
  const signedIn = useSyncExternalStore(subscribe, identity, () => "");
  useEffect(() => {
    if (!signedIn) return;
    const renew = () => void api.refreshSession().catch(() => {});
    renew();
    window.addEventListener("focus", renew);
    return () => window.removeEventListener("focus", renew);
  }, [signedIn]);
  if (!signedIn) return <SignIn />;
  const [account, email] = signedIn.split("\n");
  return (
    <div className="app">
      <header className="topbar">
        <Brand />
        <div className="account">
          {email && <span className="quiet">{email}</span>}
          <button
            type="button"
            className="secondary"
            onClick={() => api.logout()}
          >
            Sign out
          </button>
        </div>
      </header>
      {/* Remount per account so no state survives a change of identity. */}
      <main className="content" key={account}>
        <Greetings />
        <Search />
      </main>
    </div>
  );
}
