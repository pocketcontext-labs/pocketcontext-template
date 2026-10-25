// The hello-world collection. A new product replaces it with its own model.
migrate((app) => {
  const signedIn = "@request.auth.id != '' && @request.auth.collectionName = 'users' && @request.auth.disabled = false";
  const own = signedIn + " && owner = @request.auth.id";
  app.save(new Collection({
    name: "greetings", type: "base",
    // pb_hooks/greetings.js sets owner to the requester on create and keeps it immutable.
    listRule: own, viewRule: own, createRule: signedIn, updateRule: own, deleteRule: own,
    fields: [
      {name: "message", type: "text", required: true, max: 500},
      {name: "owner", type: "relation", collectionId: app.findCollectionByNameOrId("users").id, maxSelect: 1, required: true, cascadeDelete: false},
      {name: "created", type: "autodate", onCreate: true},
      {name: "updated", type: "autodate", onCreate: true, onUpdate: true},
    ],
    indexes: ["CREATE INDEX idx_greetings_owner_created ON greetings (owner, created)"],
  }));
}, (app) => {
  app.delete(app.findCollectionByNameOrId("greetings"));
});
