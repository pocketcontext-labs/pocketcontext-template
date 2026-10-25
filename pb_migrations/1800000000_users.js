migrate((app) => {
  // Configure PocketBase's built-in collection; never create another auth table.
  const users = app.findCollectionByNameOrId("users");
  users.fields.add(new BoolField({name: "disabled"}));
  users.fields.add(new TextField({name: "name", required: true, max: 200}));
  users.listRule = null;
  users.viewRule = "id = @request.auth.id && @request.auth.collectionName = 'users' && @request.auth.disabled = false";
  users.createRule = null;
  users.updateRule = null;
  users.deleteRule = null;
  users.manageRule = null;
  users.authRule = "disabled = false";
  users.passwordAuth = {enabled: true, identityFields: ["email"]};
  users.authToken.duration = 604800;
  users.authAlert.enabled = false;
  app.save(users);

  // users is an auth collection, which the SQL reader never exposes. This base
  // mirror carries only the display name (pb_hooks/user_directory.js keeps it in
  // step); server.json lets each account read its own row.
  app.save(new Collection({name: "user_directory", type: "base",
    listRule: "id = @request.auth.id", viewRule: "id = @request.auth.id",
    createRule: null, updateRule: null, deleteRule: null,
    fields: [{name: "name", type: "text", required: true, max: 200}]}));
}, () => {
  throw new Error("Account policy rollback requires a deliberate backup restore.");
});
