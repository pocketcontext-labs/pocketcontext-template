// The hello-world collection. A new product replaces it with its own model.
migrate((app) => {
  // The project's one account may read and write every record; nobody else can.
  const signedIn = "@request.auth.id != '' && @request.auth.collectionName = 'users'";
  app.save(new Collection({
    name: "greetings", type: "base",
    listRule: signedIn, viewRule: signedIn, createRule: signedIn, updateRule: signedIn, deleteRule: signedIn,
    fields: [
      {name: "message", type: "text", required: true, max: 500},
      {name: "created", type: "autodate", onCreate: true},
      {name: "updated", type: "autodate", onCreate: true, onUpdate: true},
    ],
    indexes: ["CREATE INDEX idx_greetings_created ON greetings (created)"],
  }));
}, (app) => {
  app.delete(app.findCollectionByNameOrId("greetings"));
});
