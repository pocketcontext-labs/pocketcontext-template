// Public mode needs to know whether the account password is strong, but only a
// hash exists at serve time. appconfig's record hooks set this hidden flag from
// the plain password at the moment it is saved; a password set by an earlier
// release leaves it false, so public mode refuses to start until it is changed.
migrate((app) => {
  const users = app.findCollectionByNameOrId("users");
  users.fields.add(new BoolField({name: "publicPasswordStrong", hidden: true}));
  app.save(users);
}, (app) => {
  const users = app.findCollectionByNameOrId("users");
  users.fields.removeByName("publicPasswordStrong");
  app.save(users);
});
