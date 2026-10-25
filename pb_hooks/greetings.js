// Ownership of greetings: the server, not the client, decides who owns a record.
function isUser(auth) {
  return auth && auth.collection().name === "users";
}

// REST create: the owner is always the signed-in account, whatever was sent.
function create(e) {
  if (!isUser(e.auth)) {
    throw new ForbiddenError("Sign in with a users account to add greetings.");
  }
  e.record.set("owner", e.auth.id);
  return e.next();
}

// Every update path (REST, batch, internal): the owner never changes.
function update(e) {
  if (e.record.getString("owner") !== e.record.original().getString("owner")) {
    throw new BadRequestError("The owner of a greeting cannot change.");
  }
  return e.next();
}

module.exports = {create, update};
