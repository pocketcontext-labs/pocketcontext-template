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
    ? pb.authStore.record.id
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
  return (
    <div className="app">
      <header className="topbar">
        <Brand />
        <div className="account">
          <button
            type="button"
            className="secondary"
            onClick={() => api.logout()}
          >
            Sign out
          </button>
        </div>
      </header>
      {/* Remount per session so no state survives a change of identity. */}
      <main className="content" key={signedIn}>
        <Greetings />
        <Search />
      </main>
    </div>
  );
}
