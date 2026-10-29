// A project has exactly one account, created by `templatecontext init` and
// hidden from the reader and the CLI. It signs in with its password, the local
// key or a one-time code; every API still requires its token. Databases whose
// schema came from other migrations are refused before any migration runs
// (internal/bundle/schema.go).
migrate((app) => {
  // Configure PocketBase's built-in collection; never create another auth table.
  // pb_hooks/account_access.js refuses a second account and any deletion.
  const users = app.findCollectionByNameOrId("users");
  users.listRule = null;
  users.viewRule = "id = @request.auth.id";
  users.createRule = null;
  users.updateRule = null;
  users.deleteRule = null;
  users.manageRule = null;
  users.authRule = "";
  users.passwordAuth = {enabled: true, identityFields: ["email"]};
  users.oauth2.enabled = false;
  users.otp.enabled = false;
  users.mfa.enabled = false;
  users.authToken.duration = 604800;
  users.authAlert.enabled = false;
  app.save(users);
}, () => {
  throw new Error("Account policy rollback requires a deliberate backup restore.");
});
